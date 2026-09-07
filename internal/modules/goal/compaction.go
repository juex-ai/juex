package goal

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
)

type summaryContract struct {
	Description  string `json:"description,omitempty"`
	Acceptance   string `json:"acceptance,omitempty"`
	Status       string `json:"status,omitempty"`
	StatusReason string `json:"status_reason,omitempty"`
}

func (m *Module) CompactionContribution(ctx context.Context) (runtimemodule.CompactionContribution, error) {
	if err := ctx.Err(); err != nil {
		return runtimemodule.CompactionContribution{}, err
	}
	store := m.GoalStateStore()
	if store == nil {
		return runtimemodule.CompactionContribution{}, nil
	}
	state, err := store.Snapshot()
	if err != nil {
		return runtimemodule.CompactionContribution{}, fmt.Errorf("goal state: %w", err)
	}
	snapshot := state.StatusSnapshot()
	if snapshot == nil {
		return runtimemodule.CompactionContribution{}, nil
	}
	contract := summaryContract{Description: snapshot.Description, Acceptance: snapshot.Acceptance, Status: string(snapshot.Status), StatusReason: snapshot.StatusReason}
	data, err := json.Marshal(contract)
	if err != nil {
		return runtimemodule.CompactionContribution{}, err
	}
	var entries []string
	for _, field := range []struct{ key, value string }{{"description", contract.Description}, {"acceptance", contract.Acceptance}, {"status", contract.Status}, {"status_reason", contract.StatusReason}} {
		if field.value != "" {
			entries = append(entries, field.key+": "+field.value)
		}
	}
	canonical := strings.Join(entries, "\n")
	return runtimemodule.CompactionContribution{
		State: string(data), Section: "Goal",
		Guidance:  `Copy the Goal section from the provided contract instead of re-deriving it from history. Preserve its description, acceptance, status, and status_reason exactly when present, including multiline text. Use separate description:, acceptance:, status:, and status_reason: entries; omit only absent fields.`,
		Reconcile: func(ctx context.Context, _ string) (string, error) { return canonical, ctx.Err() },
	}, nil
}
