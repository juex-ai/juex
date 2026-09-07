package goal

import (
	"context"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/runtime/workmem"
)

func Inspection() *runtimemodule.Inspection {
	store := func(t runtimemodule.ThreadContext) *workmem.GoalStateStore {
		return workmem.NewGoalStateStore(t.Dir, workmem.GoalStateOptions{})
	}
	return runtimemodule.InspectState(1, []string{"goal.status"},
		func(t runtimemodule.ThreadContext) []string { return []string{store(t).Path} },
		func(_ context.Context, t runtimemodule.ThreadContext) (*workmem.GoalStatusSnapshot, error) {
			return store(t).StatusSnapshot()
		},
	)
}
