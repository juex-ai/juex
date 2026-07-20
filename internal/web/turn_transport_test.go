package web

import (
	"context"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/runtime"
)

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
	status := as.app.Status.Snapshot()
	if status.Turn == nil || status.Turn.ID != "turn-1" ||
		status.Turn.State != runtime.TurnLifecycleCancelled {
		t.Fatalf("interrupted canonical status = %+v", status)
	}
}

func TestWebTurnTransportInterruptPreservesQueuedInput(t *testing.T) {
	prov := newPendingProvider(llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "unused"), StopReason: llm.StopEndTurn})
	_, as := newTurnTransportTestSession(t, prov)

	as.turns.start("turn-1", llm.TextMessage(llm.RoleUser, "active"))
	waitPendingProviderStarted(t, prov, "provider did not start")
	if _, err := as.app.Engine.EnqueuePendingMessageWithOptions(context.Background(), llm.TextMessage(llm.RoleUser, "preserve me"), runtime.PendingInputOptions{
		ID:  "queued-before-interrupt",
		TTL: time.Hour,
	}); err != nil {
		t.Fatal(err)
	}
	if !as.turns.interrupt() {
		t.Fatal("interrupt returned false")
	}
	as.turns.wait()

	if got := len(as.app.Session.History); got != 2 {
		t.Fatalf("history len = %d, want active and preserved pending input: %+v", got, as.app.Session.History)
	}
	if got := as.app.Session.History[1].FirstText(); got != "preserve me" {
		t.Fatalf("preserved message = %q", got)
	}
	records, err := as.app.Engine.PendingInputQueue.Records()
	if err != nil {
		t.Fatal(err)
	}
	if got := records["queued-before-interrupt"].State; got != runtime.PendingInputStateProcessed {
		t.Fatalf("pending state = %q, want %q", got, runtime.PendingInputStateProcessed)
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

	status := as.app.Status.Snapshot()
	if status.Turn == nil || status.Turn.ID != "turn-2" ||
		status.Turn.State != runtime.TurnLifecycleCompleted {
		t.Fatalf("final canonical status = %+v", status)
	}
}

func TestWebTurnTransportResetClearsAdmissionBookkeeping(t *testing.T) {
	_, as := newTurnTransportTestSession(t, stubProvider{})

	as.turns.start("turn-1", llm.TextMessage(llm.RoleUser, "hi"))
	as.turns.wait()
	as.turns.admissionsMu.Lock()
	if completed := as.turns.admissions["turn-1"]; !completed {
		as.turns.admissionsMu.Unlock()
		t.Fatal("admission was not completed before reset")
	}
	as.turns.admissionsMu.Unlock()

	as.turns.reset()
	as.turns.admissionsMu.Lock()
	defer as.turns.admissionsMu.Unlock()
	if len(as.turns.admissions) != 0 {
		t.Fatalf("admissions after reset = %+v", as.turns.admissions)
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
