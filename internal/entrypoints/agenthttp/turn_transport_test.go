package agenthttp

import (
	"context"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/app/modulecatalog"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/framework/agent"
	"github.com/juex-ai/juex/internal/framework/runtime"
)

func TestWebTurnTransportInterruptIsIdempotent(t *testing.T) {
	prov := newPendingProvider(llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn})
	_, as := newTurnTransportTestThread(t, prov)

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
	status := as.agent.Status.Snapshot()
	if status.Turn == nil || status.Turn.ID != "turn-1" ||
		status.Turn.State != runtime.TurnLifecycleCancelled {
		t.Fatalf("interrupted canonical status = %+v", status)
	}
}

func TestWebTurnTransportInterruptPreservesQueuedInput(t *testing.T) {
	prov := newPendingProvider(llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "unused"), StopReason: llm.StopEndTurn})
	_, as := newTurnTransportTestThread(t, prov)

	as.turns.start("turn-1", llm.TextMessage(llm.RoleUser, "active"))
	waitPendingProviderStarted(t, prov, "provider did not start")
	if _, err := as.agent.Engine.ReceivePendingInput(context.Background(), runtime.PendingInputRequest{
		Message: llm.TextMessage(llm.RoleUser, "preserve me"),
		Options: &runtime.PendingInputOptions{ID: "queued-before-interrupt", TTL: time.Hour},
	}); err != nil {
		t.Fatal(err)
	}
	if !as.turns.interrupt() {
		t.Fatal("interrupt returned false")
	}
	as.turns.wait()

	_, history := as.agent.Thread.Snapshot()
	if got := len(history); got != 2 {
		t.Fatalf("history len = %d, want active and preserved pending input: %+v", got, history)
	}
	if got := history[1].FirstText(); got != "preserve me" {
		t.Fatalf("preserved message = %q", got)
	}
	records, err := as.agent.Engine.PendingInputQueue.Records()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := records["queued-before-interrupt"]; ok {
		t.Fatalf("cancelled input was retained: %+v", records["queued-before-interrupt"])
	}
}

func TestWebTurnTransportStartCancelsExistingTurn(t *testing.T) {
	prov := newPendingProvider(
		llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "first"), StopReason: llm.StopEndTurn},
		llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "second"), StopReason: llm.StopEndTurn},
	)
	_, as := newTurnTransportTestThread(t, prov)

	as.turns.start("turn-1", llm.TextMessage(llm.RoleUser, "first"))
	waitPendingProviderStarted(t, prov, "provider did not start")
	as.turns.start("turn-2", llm.TextMessage(llm.RoleUser, "second"))
	as.turns.wait()

	status := as.agent.Status.Snapshot()
	if status.Turn == nil || status.Turn.ID != "turn-2" ||
		status.Turn.State != runtime.TurnLifecycleCompleted {
		t.Fatalf("final canonical status = %+v", status)
	}
}

func newTurnTransportTestThread(t *testing.T, provider llm.Provider) (*Server, *activeThread) {
	t.Helper()
	work := t.TempDir()
	srv := NewServer(Options{
		Cfg:      config.Config{ModuleInventory: modulecatalog.Inventory(), ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: work, Compaction: config.DefaultCompactionConfig()},
		Provider: provider,
	})
	t.Cleanup(srv.Close)
	if err := agent.EnsureMainThread(srv.opts.Cfg.RuntimePaths().StateDir); err != nil {
		t.Fatal(err)
	}
	as, err := srv.openThread(context.Background(), "0")
	if err != nil {
		t.Fatal(err)
	}
	return srv, as
}
