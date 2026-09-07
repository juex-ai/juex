package e2e

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/app/modulecatalog"
	"github.com/juex-ai/juex/internal/features/inputtracking"
	"github.com/juex-ai/juex/internal/foundation/llm"
	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
	"github.com/juex-ai/juex/internal/framework/runtime"
)

type inputTrackingProvider struct {
	complete func(context.Context, []llm.Message, []llm.ToolSpec) (llm.Response, error)
}

func (*inputTrackingProvider) Name() string { return "input-tracking-test" }
func (p *inputTrackingProvider) Complete(ctx context.Context, _ string, history []llm.Message, specs []llm.ToolSpec) (llm.Response, error) {
	return p.complete(ctx, history, specs)
}

func inputTrackingConfig(t *testing.T, enabled bool) config.Config {
	t.Helper()
	cfg := config.Config{ModuleInventory: modulecatalog.Inventory(), ProviderID: "openai", Model: "test", Preset: config.PresetMinimal,
		WorkDir: t.TempDir(), AgentStateDir: t.TempDir(), ContextWindow: 32000,
		Modules: config.ModulePolicy{inputtracking.ModuleID: {Enabled: enabled}},
	}
	cfg.Compaction = config.DefaultCompactionConfig()
	cfg.Compaction.KeepRecentTokens = 1
	return cfg
}

func inputTrackingApp(t *testing.T, cfg config.Config, provider llm.Provider) *app.App {
	t.Helper()
	a, err := app.New(app.Options{Config: cfg, Provider: provider, SummaryProvider: &moduleSummaryProvider{summary: "## Goal\nContinue active work.\n## Critical Context\nHistorical actions need not repeat.\n## Next Steps\nFollow remaining requirements."}, DisableMCP: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.CloseAndWait() })
	return a
}

func inputTrackingAnswer(text string) llm.Response {
	return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, text), StopReason: llm.StopEndTurn}
}

func inputTrackingCheck(ids ...string) llm.Response {
	return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
		{Type: llm.BlockText, Text: "The status question has been answered."},
		memoryCall("check-batch", inputtracking.ToolCheck, map[string]any{"input_ids": ids}),
	}}, StopReason: llm.StopToolUse}
}

func inputTrackingReminders(history []llm.Message) string {
	var result strings.Builder
	for _, message := range history {
		if strings.HasPrefix(message.FirstText(), "Unchecked input [") {
			result.WriteString(message.FirstText())
		}
	}
	return result.String()
}

func TestInputTrackingMidTurnChecklistAndAnswerCommit(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("enabled=%v", enabled), func(t *testing.T) {
			isolateModuleConfig(t)
			provider := &inputTrackingProvider{}
			a := inputTrackingApp(t, inputTrackingConfig(t, enabled), provider)
			call := 0
			var questionID string
			provider.complete = func(ctx context.Context, history []llm.Message, _ []llm.ToolSpec) (llm.Response, error) {
				call++
				reminders := inputTrackingReminders(history)
				if !enabled && reminders != "" {
					t.Fatal("disabled module injected reminders")
				}
				switch call {
				case 1:
					if enabled && !strings.Contains(reminders, "original implementation") {
						t.Fatal("initial input missing from checklist")
					}
					for _, text := range []string{"status question", "add invalid-input tests", "do not merge yet"} {
						accepted, err := a.Engine.ReceivePendingInput(ctx, runtime.PendingInputRequest{Message: llm.TextMessage(llm.RoleUser, text)})
						if err != nil || accepted.Disposition != runtime.PendingInputQueued {
							t.Fatalf("queue new input: %+v, %v", accepted, err)
						}
						if text == "status question" {
							questionID = accepted.RecordID
						}
					}
					if _, err := a.Engine.CheckInputs(ctx, []string{questionID}); err == nil {
						t.Fatal("checked undelivered input")
					}
					return inputTrackingAnswer("Working on the implementation."), nil
				case 2:
					if enabled {
						for _, text := range []string{"original implementation", "status question", "add invalid-input tests", "do not merge yet"} {
							if !strings.Contains(reminders, text) {
								t.Errorf("missing input %q", text)
							}
						}
						return inputTrackingCheck(questionID), nil
					}
					return inputTrackingAnswer("Waiting for requested information."), nil
				case 3:
					if strings.Contains(reminders, "status question") || !strings.Contains(reminders, "original implementation") || !strings.Contains(reminders, "do not merge yet") {
						t.Fatalf("independent check lost other work: %s", reminders)
					}
					return inputTrackingAnswer("Waiting for requested information."), nil
				default:
					return llm.Response{}, fmt.Errorf("unexpected continuation %d", call)
				}
			}
			if _, err := a.Engine.Turn(t.Context(), "original implementation"); err != nil {
				t.Fatal(err)
			}
			if status := a.Engine.PendingInputStatus(); status.PendingCount != 0 || status.TurnID != "" {
				t.Fatalf("checklist changed delivery settlement: %+v", status)
			}
			_, available := a.Engine.Tools.Get(inputtracking.ToolCheck)
			if available != enabled {
				t.Fatalf("tool availability=%v enabled=%v", available, enabled)
			}
			if !enabled {
				return
			}
			unchecked, err := a.Engine.UncheckedInputs(t.Context())
			if err != nil || len(unchecked) != 3 {
				t.Fatalf("remaining requirements=%+v, %v", unchecked, err)
			}
			events, err := a.Thread.ReadEvents()
			if err != nil {
				t.Fatal(err)
			}
			answered := false
			checked := false
			for _, event := range events {
				if event.Type == "llm.responded" && strings.Contains(fmt.Sprint(event.Payload), "status question has been answered") {
					answered = true
				}
				if event.Type == runtime.InputCheckedType {
					checked = true
					if !answered {
						t.Fatal("check committed before the answer")
					}
				}
			}
			if !checked {
				t.Fatal("no durable check fact")
			}
		})
	}
}

func TestInputTrackingFailureCompactionAndToggleRecovery(t *testing.T) {
	isolateModuleConfig(t)
	cfg := inputTrackingConfig(t, true)
	provider := &inputTrackingProvider{complete: func(context.Context, []llm.Message, []llm.ToolSpec) (llm.Response, error) {
		return llm.Response{}, errors.New("injected API failure")
	}}
	a := inputTrackingApp(t, cfg, provider)
	if _, err := a.Engine.Turn(t.Context(), "recover this unfinished requirement"); err == nil {
		t.Fatal("expected API failure")
	}
	open, err := a.Engine.UncheckedInputs(t.Context())
	if err != nil || len(open) != 1 {
		t.Fatalf("failure lost input: %+v, %v", open, err)
	}
	oldID := open[0].ID
	if err := a.Engine.RequestContextTransition(runtimemodule.ContextTransitionRequest{Kind: runtimemodule.ContextTransitionNew}); err == nil {
		t.Fatal("model context_new discarded unfinished input")
	}
	if _, err := a.Engine.Compact(t.Context(), "", "", "manual", false); err != nil {
		t.Fatal(err)
	}
	if err := a.CloseAndWait(); err != nil {
		t.Fatal(err)
	}
	quiet := &inputTrackingProvider{complete: func(context.Context, []llm.Message, []llm.ToolSpec) (llm.Response, error) {
		return inputTrackingAnswer("waiting"), nil
	}}
	cfg.Modules[inputtracking.ModuleID] = config.ModuleSettings{Enabled: false}
	disabled := inputTrackingApp(t, cfg, quiet)
	if reminders, err := disabled.Engine.UncheckedInputs(t.Context()); err != nil || len(reminders) != 0 {
		t.Fatalf("disabled state exposed: %+v, %v", reminders, err)
	}
	if _, err := disabled.Engine.Turn(t.Context(), "disabled period input"); err != nil {
		t.Fatal(err)
	}
	if err := disabled.CloseAndWait(); err != nil {
		t.Fatal(err)
	}
	cfg.Modules[inputtracking.ModuleID] = config.ModuleSettings{Enabled: true}
	reopened := inputTrackingApp(t, cfg, quiet)
	open, err = reopened.Engine.UncheckedInputs(t.Context())
	if err != nil || len(open) != 1 || open[0].ID != oldID || !strings.Contains(open[0].Content, "unfinished requirement") {
		t.Fatalf("compact/restart/toggle lost or backfilled input: %+v, %v", open, err)
	}
	if err := reopened.Engine.NewContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if open, err = reopened.Engine.UncheckedInputs(t.Context()); err != nil || len(open) != 0 {
		t.Fatalf("host /new did not end scope: %+v, %v", open, err)
	}
}

func TestInputTrackingLargeChecklistKeepsEveryIDAndContentReference(t *testing.T) {
	isolateModuleConfig(t)
	cfg := inputTrackingConfig(t, true)
	cfg.ContextWindow = 1000000
	cfg.Compaction.UserInputInlineMaxBytes = 1024
	cfg.Compaction.UserInputPreviewHeadBytes = 256
	cfg.Compaction.UserInputPreviewTailBytes = 256
	provider := &inputTrackingProvider{}
	a := inputTrackingApp(t, cfg, provider)
	a.Engine.MaxPendingInputs = 128
	var ids []string
	call := 0
	provider.complete = func(ctx context.Context, history []llm.Message, _ []llm.ToolSpec) (llm.Response, error) {
		call++
		if call == 1 {
			for i := 0; i < 100; i++ {
				text := fmt.Sprintf("Requirement %d: ", i) + strings.Repeat("preserve this detail ", 300)
				accepted, err := a.Engine.ReceivePendingInput(ctx, runtime.PendingInputRequest{Message: llm.TextMessage(llm.RoleUser, text)})
				if err != nil {
					t.Fatal(err)
				}
				ids = append(ids, accepted.RecordID)
			}
			return inputTrackingAnswer("Continue."), nil
		}
		allText := ""
		for _, message := range history {
			allText += message.FirstText()
		}
		for _, id := range ids {
			if !strings.Contains(allText, id) {
				t.Errorf("provider did not receive input ID %s", id)
			}
		}
		records, err := a.Engine.PendingInputQueue.Records()
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range ids {
			record := records[id]
			if record.ModelMessage == nil || len(record.Message.FirstText()) < 6000 {
				t.Fatalf("large input lost original content or artifact: %s", id)
			}
		}
		reminders, err := a.Engine.UncheckedInputs(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, reminder := range reminders {
			if reminder.ID != "" && strings.Contains(reminder.Content, "Requirement ") && !strings.Contains(reminder.Content, `"artifact"`) {
				t.Fatalf("large input has no content reference: %s", reminder.ID)
			}
		}
		return inputTrackingAnswer("Waiting on the user."), nil
	}
	if _, err := a.Engine.Turn(t.Context(), "Review incoming requirements."); err != nil {
		t.Fatal(err)
	}
	if call != 2 {
		t.Fatalf("calls = %d, want 2", call)
	}
}
