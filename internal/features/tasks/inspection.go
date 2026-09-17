package tasks

import (
	"context"

	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
)

func Inspection() *runtimemodule.Inspection {
	store := func(t runtimemodule.ThreadContext) *Store {
		return NewStore(t.Dir, Options{})
	}
	return runtimemodule.InspectState(1, []string{"tasks.status"},
		func(t runtimemodule.ThreadContext) []string { return []string{store(t).Path} },
		func(_ context.Context, t runtimemodule.ThreadContext) (*TasksSnapshot, error) {
			return store(t).StatusSnapshot()
		},
	)
}
