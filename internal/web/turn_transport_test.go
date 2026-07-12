package web

import (
	"context"
	"testing"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/llm"
)

func TestWebTurnTransportStatusTracksRunningAndDone(t *testing.T) {
	prov := newPendingProvider(llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn})
	_, as := newTurnTransportTestSession(t, prov)

	as.turns.start("turn-1", llm.TextMessage(llm.RoleUser, "hi"))
	waitPendingProviderStarted(t, prov, "provider did not start")
	status, ok := as.turns.status("turn-1")
	if !ok || status.State != "running" {
		t.Fatalf("running status = %+v, ok=%v", status, ok)
	}

	close(prov.release)
	as.turns.wait()
	status, ok = as.turns.status("turn-1")
	if !ok || status.State != "done" || status.Err != "" {
		t.Fatalf("done status = %+v, ok=%v", status, ok)
	}
}

func TestWebTurnTransportStatusForwardsPendingCounts(t *testing.T) {
	prov := newPendingProvider(llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn})
	_, as := newTurnTransportTestSession(t, prov)

	as.turns.start("turn-1", llm.TextMessage(llm.RoleUser, "hi"))
	waitPendingProviderStarted(t, prov, "provider did not start")
	if _, err := as.app.Engine.EnqueuePendingInput(context.Background(), "queued"); err != nil {
		t.Fatal(err)
	}
	status, ok := as.turns.status("turn-1")
	if !ok || status.State != "running" || status.PendingCount == nil || *status.PendingCount != 1 {
		t.Fatalf("running status = %+v, ok=%v", status, ok)
	}
	if status.MaxPendingInputs == nil || *status.MaxPendingInputs == 0 {
		t.Fatalf("missing max pending inputs: %+v", status)
	}

	close(prov.release)
	as.turns.wait()
}

func TestWebTurnTransportActiveStatusOnlyWhileRunning(t *testing.T) {
	prov := newPendingProvider(llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn})
	_, as := newTurnTransportTestSession(t, prov)

	if turnID, status, ok := as.turns.activeStatus(); ok {
		t.Fatalf("idle active status = %q %+v", turnID, status)
	}

	as.turns.start("turn-1", llm.TextMessage(llm.RoleUser, "hi"))
	waitPendingProviderStarted(t, prov, "provider did not start")
	turnID, status, ok := as.turns.activeStatus()
	if !ok || turnID != "turn-1" || status.State != "running" {
		t.Fatalf("running active status = %q %+v ok=%v", turnID, status, ok)
	}

	close(prov.release)
	as.turns.wait()
	if turnID, status, ok := as.turns.activeStatus(); ok {
		t.Fatalf("completed active status = %q %+v", turnID, status)
	}
}

func TestWebTurnTransportInterruptIsIdempotent(t *testing.T) {
	prov := newPendingProvider(llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn})
	_, as := newTurnTransportTestSession(t, prov)

	if as.turns.interrupt() {
		t.Fatal("idle interrupt returned true")
	}
	as.turns.start("turn-1", llm.TextMessage(llm.RoleUser, "hi"))
	waitPendingProviderStarted(t, prov, "provider did not start")
	if !as.turns.interrupt() {
		t.Fatal("running interrupt returned false")
	}
	if as.turns.interrupt() {
		t.Fatal("second interrupt returned true")
	}
	as.turns.wait()
	status, ok := as.turns.status("turn-1")
	if !ok || status.State != "errored" {
		t.Fatalf("interrupted status = %+v, ok=%v", status, ok)
	}
}

func TestWebTurnTransportStartCancelsExistingTurn(t *testing.T) {
	prov := newPendingProvider(
		llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "first"), StopReason: llm.StopEndTurn},
		llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "second"), StopReason: llm.StopEndTurn},
	)
	_, as := newTurnTransportTestSession(t, prov)

	as.turns.start("turn-1", llm.TextMessage(llm.RoleUser, "first"))
	waitPendingProviderStarted(t, prov, "provider did not start")
	as.turns.start("turn-2", llm.TextMessage(llm.RoleUser, "second"))
	as.turns.wait()

	first, ok := as.turns.status("turn-1")
	if !ok || first.State != "errored" {
		t.Fatalf("first status = %+v, ok=%v", first, ok)
	}
	second, ok := as.turns.status("turn-2")
	if !ok || second.State != "done" || second.Err != "" {
		t.Fatalf("second status = %+v, ok=%v", second, ok)
	}
}

func TestWebTurnTransportResetClearsTurnStates(t *testing.T) {
	_, as := newTurnTransportTestSession(t, stubProvider{})

	as.turns.start("turn-1", llm.TextMessage(llm.RoleUser, "hi"))
	as.turns.wait()
	if _, ok := as.turns.status("turn-1"); !ok {
		t.Fatal("missing completed turn before reset")
	}
	as.turns.reset()
	if status, ok := as.turns.status("turn-1"); ok {
		t.Fatalf("status after reset = %+v", status)
	}
}

func newTurnTransportTestSession(t *testing.T, provider llm.Provider) (*Server, *activeSession) {
	t.Helper()
	work := t.TempDir()
	srv := NewServer(Options{
		Cfg:      config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: work, Compaction: config.DefaultCompactionConfig()},
		Provider: provider,
	})
	t.Cleanup(srv.Close)
	as, err := srv.openSession(context.Background(), "", app.SessionModeNewPrimary)
	if err != nil {
		t.Fatal(err)
	}
	return srv, as
}
