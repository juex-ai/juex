package tasks

import (
	"context"
	"encoding/json"
	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
	"strings"
)

func (m *Module) CompactionContribution(ctx context.Context) (runtimemodule.CompactionContribution, error) {
	if err := ctx.Err(); err != nil {
		return runtimemodule.CompactionContribution{}, err
	}
	if m == nil || m.store == nil {
		return runtimemodule.CompactionContribution{}, nil
	}
	state, err := m.store.Snapshot()
	if err != nil {
		return runtimemodule.CompactionContribution{}, err
	}
	if len(state.Tasks) == 0 {
		return runtimemodule.CompactionContribution{}, nil
	}
	unfinished := state.unfinished()
	data, err := json.Marshal(TasksSnapshot{Tasks: unfinished.Tasks})
	if err != nil {
		return runtimemodule.CompactionContribution{}, err
	}
	canonical := string(data)
	longest, run := 0, 0
	for _, r := range canonical {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	fence := strings.Repeat("`", max(3, longest+1))
	canonical = fence + "json\n" + canonical + "\n" + fence
	contextText, _ := unfinished.RenderProviderContext()
	return runtimemodule.CompactionContribution{State: string(data), Section: "Tasks", Guidance: "Copy the authoritative Tasks JSON exactly into the Tasks section, preserving every unfinished task and all its fields. Enclose it in this fence: " + fence,
		Reconcile:           func(ctx context.Context, _ string) (string, error) { return canonical, ctx.Err() },
		ContextReplacements: map[string]string{"thread_tasks": contextText},
	}, nil
}
