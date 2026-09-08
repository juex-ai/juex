package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/app/modulecatalog"
	"github.com/juex-ai/juex/internal/foundation/llm"

	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
)

type checkpointModule struct {
	part        runtimemodule.CompactionContribution
	reads       int
	runtimeText string
}

func (*checkpointModule) ID() runtimemodule.ID { return "checkpoint-fixture" }

func (m *checkpointModule) CompactionContribution(context.Context) (runtimemodule.CompactionContribution, error) {
	m.reads++
	return m.part, nil
}

func (m *checkpointModule) Context(context.Context, runtimemodule.ContextRequest) ([]runtimemodule.ContextSection, error) {
	if m.runtimeText == "" {
		return nil, nil
	}
	return []runtimemodule.ContextSection{{Key: "checkpoint", Source: "fixture", Text: m.runtimeText, Projection: runtimemodule.ContextProjectionRuntimeMessage, MessageID: "runtime-checkpoint", Budget: runtimemodule.UnboundedContextBudget()}}, nil
}

func TestCompactionModuleProtectsOnlyItsSectionBeforeGenerationCommit(t *testing.T) {
	for _, name := range []string{"retry-and-complete", "correction-error", "over-summary-budget", "over-context-budget", "invalid-output", "duplicate-section", "cancel-during-correction"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			mod := &checkpointModule{part: runtimemodule.CompactionContribution{State: `{"checkpoint":"immutable-checkpoint"}`, Guidance: "Keep the checkpoint exactly.", Section: "Checkpoint"}}
			mod.part.Reconcile = func(_ context.Context, candidate string) (string, error) {
				if candidate != "model checkpoint" {
					t.Errorf("callback saw another section: %q", candidate)
				}
				switch name {
				case "correction-error":
					return "", errors.New("checkpoint invalid")
				case "over-summary-budget":
					return strings.Repeat("protected value ", 3000), nil
				case "invalid-output":
					return string([]byte{0xff}), nil
				case "cancel-during-correction":
					cancel()
				}
				return "immutable-checkpoint\nNext Steps\n  literal multiline state", nil
			}
			if name == "over-context-budget" {
				mod.runtimeText = strings.Repeat("large runtime context ", 3000)
			}
			summary := "Goal\nConversation objective\nCritical Context\nUnrelated fact\nCheckpoint\nmodel checkpoint"
			if name == "duplicate-section" {
				summary += "\nCheckpoint\nduplicate"
			}
			provider := &stubProvider{replies: []llm.Response{
				{Message: llm.TextMessage(llm.RoleAssistant, ""), StopReason: llm.StopMaxTokens},
				{Message: llm.TextMessage(llm.RoleAssistant, summary), StopReason: llm.StopEndTurn},
			}}
			cfg := config.Config{ModuleInventory: modulecatalog.Inventory(), Preset: config.PresetMinimal, WorkDir: t.TempDir(), AgentStateDir: t.TempDir(), ContextWindow: 32000}
			cfg.Compaction = config.DefaultCompactionConfig()
			cfg.Compaction.KeepRecentTokens = 1
			if name == "over-context-budget" {
				cfg.Compaction.ReserveTokens = 30000
			}
			a, err := New(Options{Config: cfg, Provider: provider, DisableMCP: true, threadModuleFactories: []runtimemodule.ThreadFactorySpec{{ID: mod.ID(), Enabled: true, New: func(context.Context, runtimemodule.ThreadContext) (runtimemodule.Module, error) { return mod, nil }}}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := a.CloseAndWait(); err != nil {
					t.Error(err)
				}
			})
			for i := 0; i < 4; i++ {
				if err := a.Thread.Append(llm.TextMessage(llm.RoleUser, strings.Repeat("old input ", 30))); err != nil {
					t.Fatal(err)
				}
				if err := a.Thread.Append(llm.TextMessage(llm.RoleAssistant, "stored")); err != nil {
					t.Fatal(err)
				}
			}
			before := a.Thread.CurrentGenerationJournalPath()
			result, err := a.CompactWithInstructions(ctx, "manual", false, "")
			if mod.reads != 1 {
				t.Errorf("state read %d times across retries", mod.reads)
			}
			if name != "retry-and-complete" {
				if err == nil || result.MessageID != "" || a.Thread.CurrentGenerationJournalPath() != before {
					t.Fatalf("invalid candidate committed: %+v, %v", result, err)
				}
				return
			}
			if err != nil || result.MessageID == "" || a.Thread.CurrentGenerationJournalPath() == before {
				t.Fatalf("protected candidate not committed: %+v, %v", result, err)
			}
			if provider.calls != 2 {
				t.Fatalf("summary attempts=%d", provider.calls)
			}
			for i, history := range provider.histories {
				if len(history) != 1 || !strings.Contains(history[0].FirstText(), "immutable-checkpoint") || !strings.Contains(provider.systems[i], "Keep the checkpoint exactly.") {
					t.Errorf("attempt %d lost owned contribution", i)
				}
			}
			var committed string
			for _, message := range a.Thread.History {
				if message.Kind == llm.MessageKindCompact {
					committed = message.FirstText()
				}
			}
			for _, want := range []string{"Unrelated fact", "Checkpoint\nimmutable-checkpoint\nNext Steps\n  literal multiline state"} {
				if !strings.Contains(committed, want) {
					t.Errorf("committed summary lost %q: %s", want, committed)
				}
			}
		})
	}
}
