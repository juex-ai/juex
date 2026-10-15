package module

import (
	"context"
	"strings"
	"testing"
)

type compactionContributor struct {
	id    ID
	part  CompactionContribution
	calls int
}

func (m *compactionContributor) ID() ID { return m.id }
func (m *compactionContributor) CompactionContribution(context.Context) (CompactionContribution, error) {
	m.calls++
	return m.part, nil
}

func TestCompactionContributionsAreOwnedFrozenAndBounded(t *testing.T) {
	valid := CompactionContribution{State: `{"checkpoint":"exact"}`, Guidance: "retain checkpoint", Section: "Checkpoint"}
	for _, name := range []string{"valid", "invalid-json", "duplicate-section", "invalid-section", "over-budget", "canceled"} {
		t.Run(name, func(t *testing.T) {
			mod := &compactionContributor{id: "checkpoint", part: valid}
			if name == "invalid-json" {
				mod.part.State = "broken"
			}
			if name == "invalid-section" {
				mod.part.Section = "Tasks\nNext Steps"
			}
			if name == "over-budget" {
				mod.part.Guidance = strings.Repeat("large", 100)
			}
			registry := NewRegistry()
			if err := registry.Register(mod); err != nil {
				t.Fatal(err)
			}
			if name == "duplicate-section" {
				if err := registry.Register(&compactionContributor{id: "second", part: valid}); err != nil {
					t.Fatal(err)
				}
			}
			set, err := registry.Seal(t.Context(), ToolContext{})
			if err != nil {
				t.Fatal(err)
			}
			mod.id = "forged-after-registration"
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if name == "canceled" {
				cancel()
			}
			parts, err := CollectCompactionContributions(ctx, CompactionBudget{MaxBytes: 128, MaxTokens: 128, EstimateTokens: func(s string) int { return len(s) }}, set)
			if name != "valid" {
				if err == nil {
					t.Fatalf("invalid contribution accepted: %+v", parts)
				}
				return
			}
			if err != nil || len(parts) != 1 || parts[0].ModuleID != "checkpoint" {
				t.Fatalf("wrong ownership: %+v, %v", parts, err)
			}
			mod.part.State = `"changed"`
			if parts[0].State != valid.State || mod.calls != 1 {
				t.Fatalf("state was not frozen: %+v, calls=%d", parts, mod.calls)
			}
		})
	}
}

func TestCompactionContextReplacementsAreFrozenAndOwned(t *testing.T) {
	mod := &compactionContributor{id: "tasks", part: CompactionContribution{State: `{}`, Section: "Tasks", ContextReplacements: map[string]string{"tasks": "unfinished"}}}
	registry := NewRegistry()
	if err := registry.Register(mod); err != nil {
		t.Fatal(err)
	}
	set, err := registry.Seal(t.Context(), ToolContext{})
	if err != nil {
		t.Fatal(err)
	}
	parts, err := CollectCompactionContributions(t.Context(), CompactionBudget{MaxBytes: 128, MaxTokens: 128, EstimateTokens: func(s string) int { return len(s) }}, set)
	if err != nil {
		t.Fatal(err)
	}
	mod.part.ContextReplacements["tasks"] = "changed"
	sections := []ContextSection{
		{ModuleID: "tasks", Scope: parts[0].Scope, Key: "tasks", Text: "done and unfinished", Projection: ContextProjectionRuntimeMessage, MessageID: "shared-id", Budget: BoundedContextBudget(32)},
		{ModuleID: "notes", Scope: parts[0].Scope, Key: "notes", Text: "retained notes", Projection: ContextProjectionRuntimeMessage, MessageID: "shared-id", Budget: BoundedContextBudget(32)},
	}
	projected, err := ProjectCompactionContext(sections, parts)
	if err != nil || len(projected) != 2 || projected[0].Text != "unfinished" || projected[1] != sections[1] || sections[0].Text != "done and unfinished" {
		t.Fatalf("projection modified frozen state or another owner: %+v %v", projected, err)
	}
	parts[0].ContextReplacements["tasks"] = ""
	projected, err = ProjectCompactionContext(sections, parts)
	if err != nil || len(projected) != 1 || projected[0] != sections[1] {
		t.Fatalf("empty replacement: %+v %v", projected, err)
	}
	for _, replacement := range []map[string]string{{"notes": "forged"}, {"missing": "forged"}, {"tasks": strings.Repeat("x", 33)}, {"tasks": string([]byte{0xff})}} {
		parts[0].ContextReplacements = replacement
		if _, err := ProjectCompactionContext(sections, parts); err == nil {
			t.Fatalf("invalid replacement accepted: %+v", replacement)
		}
	}
}
