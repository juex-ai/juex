package goal

import (
	"context"

	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
)

func Inspection() *runtimemodule.Inspection {
	store := func(t runtimemodule.ThreadContext) *GoalStateStore {
		return NewGoalStateStore(t.Dir, GoalStateOptions{})
	}
	return runtimemodule.InspectState(1, []string{"goal.status"},
		func(t runtimemodule.ThreadContext) []string { return []string{store(t).Path} },
		func(_ context.Context, t runtimemodule.ThreadContext) (*GoalStatusSnapshot, error) {
			return store(t).StatusSnapshot()
		},
	)
}
