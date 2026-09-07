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
				mod.part.Section = "Goal\nNext Steps"
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
