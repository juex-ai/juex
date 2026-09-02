package runtime

import (
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/runtime/workmem"
)

type GoalStateStoreProvider interface {
	GoalStateStore() *workmem.GoalStateStore
}

type NotesStoreProvider interface {
	NotesStore() *workmem.NotesStore
}

type GoalStatusProvider interface {
	GoalStatusSnapshot() (*workmem.GoalStatusSnapshot, error)
}

type NotesStatusProvider interface {
	NotesStatusSnapshot() (*workmem.NotesSnapshot, error)
}

type HookGoalStateProvider interface {
	HookGoalState() []byte
}

func ThreadStateStoresFromModules(set *runtimemodule.Set) (*workmem.GoalStateStore, *workmem.NotesStore) {
	var goal *workmem.GoalStateStore
	var notes *workmem.NotesStore
	if set == nil {
		return nil, nil
	}
	for _, module := range set.Modules() {
		if goal == nil {
			if provider, ok := module.(GoalStateStoreProvider); ok {
				goal = provider.GoalStateStore()
			}
		}
		if notes == nil {
			if provider, ok := module.(NotesStoreProvider); ok {
				notes = provider.NotesStore()
			}
		}
	}
	return goal, notes
}

func ThreadStateStatusFromModules(set *runtimemodule.Set) (*workmem.GoalStatusSnapshot, *workmem.NotesSnapshot) {
	var goal *workmem.GoalStatusSnapshot
	var notes *workmem.NotesSnapshot
	if set == nil {
		return nil, nil
	}
	for _, module := range set.Modules() {
		if goal == nil {
			if provider, ok := module.(GoalStatusProvider); ok {
				goal, _ = provider.GoalStatusSnapshot()
			}
		}
		if notes == nil {
			if provider, ok := module.(NotesStatusProvider); ok {
				notes, _ = provider.NotesStatusSnapshot()
			}
		}
	}
	return goal, notes
}

func HookGoalStateFromModules(set *runtimemodule.Set) []byte {
	if set == nil {
		return nil
	}
	for _, module := range set.Modules() {
		if provider, ok := module.(HookGoalStateProvider); ok {
			return provider.HookGoalState()
		}
	}
	return nil
}

func (e *Engine) GoalStatusSnapshot() (*workmem.GoalStatusSnapshot, error) {
	if e == nil {
		return nil, nil
	}
	modules := e.ThreadRuntimeSnapshot().Modules
	if modules == nil {
		return nil, nil
	}
	for _, module := range modules.Modules() {
		if provider, ok := module.(GoalStatusProvider); ok {
			return provider.GoalStatusSnapshot()
		}
	}
	return nil, nil
}

func (e *Engine) NotesStatusSnapshot() (*workmem.NotesSnapshot, error) {
	if e == nil {
		return nil, nil
	}
	modules := e.ThreadRuntimeSnapshot().Modules
	if modules == nil {
		return nil, nil
	}
	for _, module := range modules.Modules() {
		if provider, ok := module.(NotesStatusProvider); ok {
			return provider.NotesStatusSnapshot()
		}
	}
	return nil, nil
}
