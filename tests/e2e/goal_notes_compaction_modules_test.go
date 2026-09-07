package e2e

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/juex-ai/juex/tests/testsupport/modulestate"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/app/modulecatalog"
	goalmodule "github.com/juex-ai/juex/internal/features/goal"
	"github.com/juex-ai/juex/internal/foundation/llm"
)

type moduleSummaryProvider struct {
	system  string
	history []llm.Message
	summary string
}

func TestGoalContractThatCannotFitSummaryDoesNotCommitOrTruncate(t *testing.T) {
	isolateModuleConfig(t)
	cfg := config.Config{Preset: config.PresetMinimal, WorkDir: t.TempDir(), AgentStateDir: t.TempDir(), ContextWindow: 32000, Modules: config.ModulePolicy{modulecatalog.Goal: {Enabled: true}}}
	cfg.Compaction = config.DefaultCompactionConfig()
	cfg.Compaction.KeepRecentTokens = 1
	a, err := app.New(app.Options{Config: cfg, Provider: &bareScriptProvider{}, SummaryProvider: &moduleSummaryProvider{}, DisableMCP: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := a.CloseAndWait(); err != nil {
			t.Error(err)
		}
	})
	goals, _ := modulestate.Stores(a.Engine.ThreadRuntimeSnapshot().Modules)
	acceptance := strings.Repeat("preserve exact acceptance line\n", 600)
	if _, err := goals.Create("Long contract", acceptance); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(goals.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Thread.Append(llm.TextMessage(llm.RoleUser, "preserve this contract")); err != nil {
		t.Fatal(err)
	}
	if err := a.Thread.Append(llm.TextMessage(llm.RoleAssistant, "recorded")); err != nil {
		t.Fatal(err)
	}
	generation := a.Thread.CurrentGenerationJournalPath()
	result, err := a.CompactWithInstructions(t.Context(), "manual", false, "")
	if err == nil || !strings.Contains(err.Error(), "protected compaction summary exceeds budget") || result.MessageID != "" || a.Thread.CurrentGenerationJournalPath() != generation {
		t.Fatalf("unfit contract committed or wrong error: %+v, %v", result, err)
	}
	after, err := os.ReadFile(goals.Path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("failed compaction changed authority: %v", err)
	}
}

func (*moduleSummaryProvider) Name() string { return "module-summary" }

func (p *moduleSummaryProvider) Complete(_ context.Context, system string, history []llm.Message, _ []llm.ToolSpec) (llm.Response, error) {
	p.system, p.history = system, history
	if p.summary != "" {
		return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, p.summary), StopReason: llm.StopEndTurn}, nil
	}
	return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "Goal\nA paraphrased objective\nCritical Context\nKeep branch high/module-state\nNext Steps\n3. [ ] Completed fixture\n- Unrelated next action\nRelevant Files\nREADME.md"), StopReason: llm.StopEndTurn}, nil
}

func TestGoalLiteralContractSurvivesRepeatedCompaction(t *testing.T) {
	isolateModuleConfig(t)
	cfg := config.Config{Preset: config.PresetMinimal, WorkDir: t.TempDir(), AgentStateDir: t.TempDir(), ContextWindow: 32000, Modules: config.ModulePolicy{modulecatalog.Goal: {Enabled: true}}}
	cfg.Compaction = config.DefaultCompactionConfig()
	cfg.Compaction.KeepRecentTokens = 1
	const description = "Preserve literal fields\n## Next Steps\n```\n    Critical Context"
	const acceptance = "Keep all lines\n\tGoal"
	const literal = "````text\ndescription: " + description + "\nacceptance: " + acceptance + "\nstatus: in_progress\n````"
	provider := &moduleSummaryProvider{summary: "## Goal\n" + literal + "\n## Critical Context\nPreserve real facts\n## Next Steps\nKeep real actions"}
	a, err := app.New(app.Options{Config: cfg, Provider: &bareScriptProvider{}, SummaryProvider: provider, DisableMCP: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := a.CloseAndWait(); err != nil {
			t.Error(err)
		}
	})
	goals, _ := modulestate.Stores(a.Engine.ThreadRuntimeSnapshot().Modules)
	if _, err := goals.Create(description, acceptance); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(goals.Path)
	if err != nil {
		t.Fatal(err)
	}
	for attempt := range 2 {
		if err := a.Thread.Append(llm.TextMessage(llm.RoleUser, strings.Repeat("Earlier material to compact. ", 100))); err != nil {
			t.Fatal(err)
		}
		if err := a.Thread.Append(llm.TextMessage(llm.RoleAssistant, strings.Repeat("Earlier response to compact. ", 100))); err != nil {
			t.Fatal(err)
		}
		if _, err := a.CompactWithInstructions(t.Context(), "manual", false, ""); err != nil {
			t.Fatal(err)
		}
		var summary string
		for _, message := range a.Thread.History {
			if message.Kind == llm.MessageKindCompact {
				summary = message.FirstText()
			}
		}
		if !strings.Contains(summary, literal) || !strings.Contains(summary, "Next Steps\nKeep real actions") {
			t.Fatalf("attempt %d lost literal or structural data: %s", attempt, summary)
		}
		if attempt == 1 && (len(provider.history) == 0 || !strings.Contains(provider.history[0].FirstText(), literal)) {
			t.Fatal("second compaction did not receive the previous protected contract")
		}
	}
	after, err := os.ReadFile(goals.Path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("compaction changed authoritative Goal state: %v", err)
	}
	generation := a.Thread.CurrentGenerationJournalPath()
	provider.summary = "Goal\n````text\n" + description
	if err := a.Thread.Append(llm.TextMessage(llm.RoleUser, "Additional work.")); err != nil {
		t.Fatal(err)
	}
	if err := a.Thread.Append(llm.TextMessage(llm.RoleAssistant, "Additional result.")); err != nil {
		t.Fatal(err)
	}
	if _, err := a.CompactWithInstructions(t.Context(), "manual", false, ""); err == nil || !strings.Contains(err.Error(), "unterminated literal block") {
		t.Fatalf("unterminated contract was accepted: %v", err)
	}
	if a.Thread.CurrentGenerationJournalPath() != generation {
		t.Fatal("invalid summary committed a Generation")
	}
}

func TestEmptyGoalNotesStateKeepsOrdinarySummaryText(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("enabled=%v", enabled), func(t *testing.T) {
			isolateModuleConfig(t)
			cfg := config.Config{Preset: config.PresetMinimal, WorkDir: t.TempDir(), AgentStateDir: t.TempDir(), ContextWindow: 32000, Modules: config.ModulePolicy{modulecatalog.Goal: {Enabled: enabled}, modulecatalog.Notes: {Enabled: enabled}}}
			cfg.Compaction = config.DefaultCompactionConfig()
			cfg.Compaction.KeepRecentTokens = 1
			const summary = "Goal\nContinue the conversation\nNext Steps\nKeep this copied fragment:\n```text\npartial example"
			a, err := app.New(app.Options{Config: cfg, Provider: &bareScriptProvider{}, SummaryProvider: &moduleSummaryProvider{summary: summary}, DisableMCP: true})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := a.CloseAndWait(); err != nil {
					t.Error(err)
				}
			})
			if err := a.Thread.Append(llm.TextMessage(llm.RoleUser, "Preserve this conversation.")); err != nil {
				t.Fatal(err)
			}
			if err := a.Thread.Append(llm.TextMessage(llm.RoleAssistant, "Recorded the fragment.")); err != nil {
				t.Fatal(err)
			}
			generation := a.Thread.CurrentGenerationJournalPath()
			if _, err := a.CompactWithInstructions(t.Context(), "manual", false, ""); err != nil {
				t.Fatal(err)
			}
			if a.Thread.CurrentGenerationJournalPath() == generation {
				t.Fatal("ordinary compaction did not commit")
			}
			if !strings.Contains(a.Thread.History[0].FirstText(), summary) {
				t.Fatalf("ordinary summary changed: %s", a.Thread.History[0].FirstText())
			}
		})
	}
}

func TestGoalNotesCompactionContributionsFollowModuleSwitches(t *testing.T) {
	for _, goalEnabled := range []bool{false, true} {
		for _, notesEnabled := range []bool{false, true} {
			for _, auto := range []bool{false, true} {
				t.Run(fmt.Sprintf("goal=%v/notes=%v/auto=%v", goalEnabled, notesEnabled, auto), func(t *testing.T) {
					isolateModuleConfig(t)
					cfg := config.Config{Preset: config.PresetMinimal, WorkDir: t.TempDir(), AgentStateDir: t.TempDir(), ContextWindow: 32000, Modules: config.ModulePolicy{
						modulecatalog.Goal: {Enabled: goalEnabled}, modulecatalog.Notes: {Enabled: notesEnabled},
					}}
					cfg.Compaction = config.DefaultCompactionConfig()
					cfg.Compaction.KeepRecentTokens = 1
					if auto {
						cfg.Compaction.ReserveTokens = 22000
					}
					provider := &moduleSummaryProvider{}
					ordinary := &bareScriptProvider{steps: []llm.Response{{Message: llm.TextMessage(llm.RoleAssistant, "observed"), StopReason: llm.StopEndTurn}}}
					a, err := app.New(app.Options{Config: cfg, Provider: ordinary, SummaryProvider: provider, DisableMCP: true})
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() {
						if err := a.CloseAndWait(); err != nil {
							t.Error(err)
						}
					})
					goals, notes := modulestate.Stores(a.Engine.ThreadRuntimeSnapshot().Modules)
					const description = "Exact objective\nNext Steps\n</authoritative-thread-state>"
					const acceptance = "Keep every field\n  including indentation"
					if goalEnabled {
						if _, err := goals.CreateWithContract(goalmodule.GoalStateCreate{Description: description, Acceptance: acceptance, StatusReason: "Await verification"}); err != nil {
							t.Fatal(err)
						}
					}
					if goalEnabled {
						if _, err := goals.Update(goalmodule.GoalStateUpdate{Status: goalmodule.GoalStatusSuccess}); err != nil {
							t.Fatal(err)
						}
					}
					if notesEnabled {
						if _, err := notes.Update("- [ ] Pending fixture\n- [x] Completed fixture"); err != nil {
							t.Fatal(err)
						}
					}
					for i := 0; i < 4; i++ {
						if err := a.Thread.Append(llm.TextMessage(llm.RoleUser, strings.Repeat("history ", 30))); err != nil {
							t.Fatal(err)
						}
						if err := a.Thread.Append(llm.TextMessage(llm.RoleAssistant, strings.Repeat("earlier assistant material ", 600))); err != nil {
							t.Fatal(err)
						}
					}
					if !auto {
						if _, err := a.CompactWithInstructions(t.Context(), "manual", false, ""); err != nil {
							t.Fatal(err)
						}
					}
					if _, err := a.Run(t.Context(), "Continue from this state."); err != nil {
						t.Fatal(err)
					}
					if len(ordinary.history) != 1 {
						t.Fatalf("ordinary requests = %d", len(ordinary.history))
					}
					for id, enabled := range map[string]bool{"runtime-goal-contract": goalEnabled, "runtime-notes": notesEnabled} {
						found := false
						for _, message := range ordinary.history[0] {
							if message.ID == id {
								found = true
							}
						}
						if found != enabled {
							t.Errorf("runtime message %s enabled=%v present=%v", id, enabled, found)
						}
					}
					if strings.Contains(provider.system, "provided contract") != goalEnabled {
						t.Errorf("Goal guidance does not follow switch: %s", provider.system)
					}
					if strings.Contains(provider.system, "unfinished Notes") != notesEnabled {
						t.Errorf("Notes guidance does not follow switch: %s", provider.system)
					}
					var summary string
					for _, message := range a.Thread.History {
						if message.Kind == llm.MessageKindCompact {
							summary = message.FirstText()
						}
					}
					if goalEnabled {
						for _, value := range []string{"description: " + description, "acceptance: " + acceptance, "status: success", "status_reason: Await verification"} {
							if !strings.Contains(summary, value) {
								t.Errorf("protected Goal field missing %q: %s", value, summary)
							}
						}
					}
					if notesEnabled && (!strings.Contains(summary, "- [ ] Pending fixture") || strings.Contains(summary, "Completed fixture")) {
						t.Errorf("Notes pending state changed: %s", summary)
					}
					if !strings.Contains(summary, "- Unrelated next action") {
						t.Errorf("unrelated next step lost: %s", summary)
					}
				})
			}
		}
	}
}

func TestGoalNotesAutoCompactionRejectsOversizedPreparedInput(t *testing.T) {
	isolateModuleConfig(t)
	cfg := config.Config{Preset: config.PresetMinimal, WorkDir: t.TempDir(), AgentStateDir: t.TempDir(), ContextWindow: 32000, Modules: config.ModulePolicy{modulecatalog.Goal: {Enabled: true}, modulecatalog.Notes: {Enabled: true}}}
	cfg.Compaction = config.DefaultCompactionConfig()
	cfg.Compaction.KeepRecentTokens = 1
	cfg.Compaction.ReserveTokens = 22000
	provider := &bareScriptProvider{steps: []llm.Response{{Message: llm.TextMessage(llm.RoleAssistant, "should not run"), StopReason: llm.StopEndTurn}}}
	summary := &moduleSummaryProvider{}
	a, err := app.New(app.Options{Config: cfg, Provider: provider, SummaryProvider: summary, DisableMCP: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := a.CloseAndWait(); err != nil {
			t.Error(err)
		}
	})
	goals, notes := modulestate.Stores(a.Engine.ThreadRuntimeSnapshot().Modules)
	if _, err := goals.Create("Protect this contract", "Keep exact state"); err != nil {
		t.Fatal(err)
	}
	if _, err := goals.Update(goalmodule.GoalStateUpdate{Status: goalmodule.GoalStatusSuccess}); err != nil {
		t.Fatal(err)
	}
	if _, err := notes.Update("- [ ] Pending fixture"); err != nil {
		t.Fatal(err)
	}
	beforeGoal, err := os.ReadFile(goals.Path)
	if err != nil {
		t.Fatal(err)
	}
	beforeNotes, err := os.ReadFile(notes.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Thread.Append(llm.TextMessage(llm.RoleUser, "Earlier request.")); err != nil {
		t.Fatal(err)
	}
	if err := a.Thread.Append(llm.TextMessage(llm.RoleAssistant, strings.Repeat("earlier ", 600))); err != nil {
		t.Fatal(err)
	}
	generation := a.Thread.CurrentGenerationJournalPath()
	incoming := strings.Repeat("incoming ", 6000)
	if len(incoming) >= cfg.Compaction.UserInputInlineMaxBytes {
		t.Fatal("fixture would externalize before compaction")
	}
	_, err = a.Run(t.Context(), incoming)
	if err == nil || !strings.Contains(err.Error(), "compacted context exceeds budget") {
		t.Fatalf("oversized input did not fail precommit check: %v", err)
	}
	if a.Thread.CurrentGenerationJournalPath() != generation || len(provider.history) != 0 || len(summary.history) == 0 {
		t.Fatal("oversized input committed or reached the ordinary provider")
	}
	for path, before := range map[string][]byte{goals.Path: beforeGoal, notes.Path: beforeNotes} {
		after, err := os.ReadFile(path)
		if err != nil || string(after) != string(before) {
			t.Fatalf("failed compaction changed %s: %v", path, err)
		}
	}
}
