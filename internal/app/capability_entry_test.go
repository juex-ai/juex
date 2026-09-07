package app

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/app/modulecatalog"
	extensionsmodule "github.com/juex-ai/juex/internal/features/extensions"
	workerthreadsmodule "github.com/juex-ai/juex/internal/features/workerthreads"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/framework/agent"
	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
	"github.com/juex-ai/juex/internal/framework/runtime"
	"github.com/juex-ai/juex/internal/framework/thread"
)

type capabilityProvider struct{ calls atomic.Int32 }

type capabilityStartPolicy struct{ calls atomic.Int32 }

func (*capabilityStartPolicy) ID() runtimemodule.ID { return "startup-probe" }

func (p *capabilityStartPolicy) ApplyThreadStart(context.Context, runtimemodule.ThreadStartRequest) (runtimemodule.ThreadStartDecision, error) {
	p.calls.Add(1)
	return runtimemodule.ThreadStartDecision{}, nil
}

func (*capabilityProvider) Name() string { return "capability-test" }

func (p *capabilityProvider) Complete(context.Context, string, []llm.Message, []llm.ToolSpec) (llm.Response, error) {
	p.calls.Add(1)
	return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "completed"), StopReason: llm.StopEndTurn}, nil
}

func capabilityApp(t *testing.T, cfg config.Config, id string, provider *capabilityProvider) *App {
	t.Helper()
	a, err := New(Options{Config: cfg, ThreadID: id, Provider: provider})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.CloseAndWait() })
	return a
}

func TestDisabledGoalRejectsDirectAndAdmittedSlash(t *testing.T) {
	cfg := config.Config{ModuleInventory: modulecatalog.Inventory(), WorkDir: t.TempDir(), AgentStateDir: t.TempDir(), Preset: config.PresetMinimal}
	provider := &capabilityProvider{}
	a := capabilityApp(t, cfg, thread.MainID, provider)
	if result := a.AdmitTurn(t.Context(), agent.TurnAdmissionRequest{Prompt: "/goal finish this"}); result.Kind != agent.TurnAdmissionRejected || result.Error.Kind != "module_disabled" {
		t.Errorf("disabled goal admission = %+v", result)
	}
	if _, err := a.Run(t.Context(), "/goal finish this"); err == nil || !strings.Contains(err.Error(), "goal module is disabled") {
		t.Errorf("disabled goal Run = %v", err)
	}
	if provider.calls.Load() != 0 {
		t.Fatal("disabled goal reached Provider")
	}
}

func TestRuntimeExtensionEnablementDistinguishesEmptyCatalog(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		cfg := config.Config{ModuleInventory: modulecatalog.Inventory(), WorkDir: t.TempDir(), AgentStateDir: t.TempDir(), Preset: config.PresetMinimal,
			Modules: config.ModulePolicy{extensionsmodule.ModuleID: {Enabled: enabled}},
		}
		status, err := snapshotRuntimeStatus(t, cfg, RuntimeStatusOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if status.Extensions.Enabled != enabled || status.Extensions.Count != 0 {
			t.Fatalf("Extension enabled=%t: %+v", enabled, status.Extensions)
		}
	}
}

func TestDisabledWorkerPausesPendingRecoveryAndPreservesMaintenance(t *testing.T) {
	cfg := config.Config{ModuleInventory: modulecatalog.Inventory(), WorkDir: t.TempDir(), AgentStateDir: t.TempDir(), Preset: config.PresetMinimal,
		Modules: config.ModulePolicy{workerthreadsmodule.ModuleID: {Enabled: true}},
	}
	if err := EnsureMainThread(cfg); err != nil {
		t.Fatal(err)
	}
	worker, err := thread.NewStore(cfg.AgentStateDir).CreateWorker(thread.MainID, "retained")
	if err != nil {
		t.Fatal(err)
	}
	id := worker.ID
	if err := worker.Close(); err != nil {
		t.Fatal(err)
	}
	first := capabilityApp(t, cfg, id, &capabilityProvider{})
	accepted, err := first.Engine.ReceivePendingInput(t.Context(), runtime.PendingInputRequest{Message: llm.TextMessage(llm.RoleUser, "retained input"), DeferDelivery: true})
	if err != nil || accepted.RecordID == "" {
		t.Fatalf("defer input = %+v %v", accepted, err)
	}
	if err := first.CloseAndWait(); err != nil {
		t.Fatal(err)
	}
	cfg.Modules[workerthreadsmodule.ModuleID] = config.ModuleSettings{Enabled: false}
	pausedProvider := &capabilityProvider{}
	paused := capabilityApp(t, cfg, id, pausedProvider)
	if err := paused.waitPendingInputRecoveryContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if pausedProvider.calls.Load() != 0 {
		t.Fatal("opening disabled Worker executed retained input")
	}
	if result := paused.AdmitTurn(t.Context(), agent.TurnAdmissionRequest{Prompt: "run hidden API"}); result.Kind != agent.TurnAdmissionRejected || result.Error.Kind != "module_disabled" {
		t.Errorf("disabled Worker admission = %+v", result)
	}
	if _, err := paused.Run(t.Context(), "run directly"); err == nil || !strings.Contains(err.Error(), "worker-threads module is disabled") {
		t.Errorf("disabled Worker direct execution = %v", err)
	}
	if result := paused.AdmitTurn(t.Context(), agent.TurnAdmissionRequest{Prompt: "retry notice", Kind: llm.MessageKindSystemNotice}); result.Kind != agent.TurnAdmissionRejected {
		t.Errorf("disabled Worker system notice = %+v", result)
	}
	message := llm.TextMessage(llm.RoleUser, "external input")
	if _, err := paused.RunAdmittedTurn(t.Context(), "bypass", message); err == nil {
		t.Error("disabled Worker admitted execution bypassed capability check")
	}
	paused.threadMu.RLock()
	_, deliveryErr := paused.deliverExternalInputLocked(t.Context(), message, runtime.PendingInputOptions{}, nil, false, nil)
	paused.threadMu.RUnlock()
	if deliveryErr == nil {
		t.Error("disabled Worker external input was accepted")
	}
	for range 2 {
		result := paused.AdmitTurn(t.Context(), agent.TurnAdmissionRequest{Prompt: SlashCompact})
		if result.Kind != agent.TurnAdmissionCommandCompleted || result.Start != nil {
			t.Fatalf("paused Worker maintenance = %+v", result)
		}
	}
	if _, exists, err := paused.Engine.PersistedPendingMessage(accepted.RecordID); err != nil || !exists {
		t.Fatalf("maintenance lost retained input: exists=%t err=%v", exists, err)
	}
	if pausedProvider.calls.Load() != 0 {
		t.Fatal("maintenance resumed ordinary Worker input")
	}
	if err := paused.CloseAndWait(); err != nil {
		t.Fatal(err)
	}
	cfg.Modules[workerthreadsmodule.ModuleID] = config.ModuleSettings{Enabled: true}
	resumedProvider := &capabilityProvider{}
	resumed := capabilityApp(t, cfg, id, resumedProvider)
	if err := resumed.waitPendingInputRecoveryContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if resumedProvider.calls.Load() != 1 {
		t.Fatalf("reenabled recovery calls = %d", resumedProvider.calls.Load())
	}
}

func TestDisabledWorkerNewContextDoesNotGreet(t *testing.T) {
	cfg := config.Config{ModuleInventory: modulecatalog.Inventory(), WorkDir: t.TempDir(), AgentStateDir: t.TempDir(), Preset: config.PresetMinimal}
	if err := EnsureMainThread(cfg); err != nil {
		t.Fatal(err)
	}
	worker, err := thread.NewStore(cfg.AgentStateDir).CreateWorker(thread.MainID, "maintenance")
	if err != nil {
		t.Fatal(err)
	}
	id := worker.ID
	if err := worker.Close(); err != nil {
		t.Fatal(err)
	}
	provider := &capabilityProvider{}
	a := capabilityApp(t, cfg, id, provider)
	before := a.Engine.ThreadRuntimeSnapshot().Thread.CurrentGenerationJournalPath()
	if _, err := a.Run(t.Context(), SlashNew); err != nil {
		t.Fatal(err)
	}
	if after := a.Engine.ThreadRuntimeSnapshot().Thread.CurrentGenerationJournalPath(); after == before {
		t.Fatal("host /new did not renew Generation")
	}
	if provider.calls.Load() != 0 {
		t.Fatal("disabled Worker /new executed a greeting")
	}
}

func TestDisabledWorkerSkipsThreadStartPolicies(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		cfg := config.Config{ModuleInventory: modulecatalog.Inventory(), WorkDir: t.TempDir(), AgentStateDir: t.TempDir(), Preset: config.PresetMinimal,
			Modules: config.ModulePolicy{workerthreadsmodule.ModuleID: {Enabled: enabled}},
		}
		if err := EnsureMainThread(cfg); err != nil {
			t.Fatal(err)
		}
		worker, err := thread.NewStore(cfg.AgentStateDir).CreateWorker(thread.MainID, "startup")
		if err != nil {
			t.Fatal(err)
		}
		id := worker.ID
		if err := worker.Close(); err != nil {
			t.Fatal(err)
		}
		probe := &capabilityStartPolicy{}
		a, err := New(Options{Config: cfg, ThreadID: id, Provider: &capabilityProvider{},
			runtimeModuleFactories: []runtimemodule.RuntimeFactorySpec{{ID: probe.ID(), Enabled: true,
				New: func(context.Context, runtimemodule.RuntimeContext) (runtimemodule.Module, error) { return probe, nil },
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := a.CloseAndWait(); err != nil {
			t.Fatal(err)
		}
		if (probe.calls.Load() == 1) != enabled {
			t.Errorf("Worker enabled=%t ran ThreadStart policies %d times", enabled, probe.calls.Load())
		}
	}
}
