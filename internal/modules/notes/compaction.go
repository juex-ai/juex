package notes

import (
	"context"
	"encoding/json"
	"strings"

	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
)

func (m *Module) CompactionContribution(ctx context.Context) (runtimemodule.CompactionContribution, error) {
	if err := ctx.Err(); err != nil {
		return runtimemodule.CompactionContribution{}, err
	}
	store := m.NotesStore()
	if store == nil {
		return runtimemodule.CompactionContribution{}, nil
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		m.recordNotesContextError(store, err)
		return runtimemodule.CompactionContribution{}, nil
	}
	m.clearNotesContextError()
	if strings.TrimSpace(snapshot.Content) == "" {
		return runtimemodule.CompactionContribution{}, nil
	}
	data, err := json.Marshal(snapshot.Content)
	if err != nil {
		return runtimemodule.CompactionContribution{}, err
	}
	// Capture immutable content once; retries and reconciliation must not observe
	// a different revision of the authoritative file.
	content := snapshot.Content
	return runtimemodule.CompactionContribution{
		State: string(data), Section: "Next Steps",
		Guidance: `Keep Next Steps consistent with unfinished Notes items: copy every unfinished - [ ] checklist item's text verbatim into Next Steps and do not omit one. Do not present completed Notes items as pending. Preserve unrelated next steps.`,
		Reconcile: func(ctx context.Context, candidate string) (string, error) {
			return reconcileNextSteps(candidate, content), ctx.Err()
		},
	}, nil
}

func reconcileNextSteps(candidate, notes string) string {
	pending := map[string]string{}
	completed := map[string]bool{}
	var order []string
	for _, line := range strings.Split(notes, "\n") {
		line = strings.TrimSpace(line)
		key := nextStepKey(line)
		if key == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "- [ ] "), strings.HasPrefix(line, "- [ ]\t"):
			if _, exists := pending[key]; !exists {
				order = append(order, key)
				pending[key] = line
			}
		case strings.HasPrefix(strings.ToLower(line), "- [x] "), strings.HasPrefix(strings.ToLower(line), "- [x]\t"):
			completed[key] = true
		}
	}
	seen := map[string]bool{}
	var lines []string
	for _, line := range strings.Split(candidate, "\n") {
		key := nextStepKey(line)
		if original, ok := pending[key]; ok {
			if !seen[key] {
				lines = append(lines, original)
				seen[key] = true
			}
		} else if !completed[key] {
			lines = append(lines, line)
		}
	}
	for _, key := range order {
		if !seen[key] {
			lines = append(lines, pending[key])
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func nextStepKey(line string) string {
	line = strings.ToLower(strings.TrimSpace(line))
	for _, prefix := range []string{"- [ ]", "- [x]", "* [ ]", "* [x]", "- ", "* "} {
		if strings.HasPrefix(line, prefix) {
			line = strings.TrimSpace(strings.TrimPrefix(line, prefix))
			break
		}
	}
	return strings.Join(strings.Fields(line), " ")
}
