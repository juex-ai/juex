package runtime

import runtimemodule "github.com/juex-ai/juex/internal/runtime/module"

type GoalStateStoreProvider interface {
	GoalStateStore() *GoalStateStore
}

type NotesStoreProvider interface {
	NotesStore() *NotesStore
}

type GoalStatusProvider interface {
	GoalStatusSnapshot() (*GoalStatusSnapshot, error)
}

type NotesStatusProvider interface {
	NotesStatusSnapshot() (*NotesSnapshot, error)
}

type HookGoalStateProvider interface {
	HookGoalState() []byte
}

func SessionStateStoresFromModules(set *runtimemodule.Set) (*GoalStateStore, *NotesStore) {
	var goal *GoalStateStore
	var notes *NotesStore
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

func SessionStateStatusFromModules(set *runtimemodule.Set) (*GoalStatusSnapshot, *NotesSnapshot) {
	var goal *GoalStatusSnapshot
	var notes *NotesSnapshot
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

func (e *Engine) GoalStatusSnapshot() (*GoalStatusSnapshot, error) {
	if e == nil {
		return nil, nil
	}
	modules := e.SessionRuntimeSnapshot().Modules
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

func (e *Engine) NotesStatusSnapshot() (*NotesSnapshot, error) {
	if e == nil {
		return nil, nil
	}
	modules := e.SessionRuntimeSnapshot().Modules
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
