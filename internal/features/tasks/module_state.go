package tasks

import runtimemodule "github.com/juex-ai/juex/internal/framework/module"

// StoreFromModules returns the enabled owner's state without constructing resources.
func StoreFromModules(set *runtimemodule.Set) *Store {
	if set == nil {
		return nil
	}
	for _, module := range set.Modules() {
		if provider, ok := module.(interface{ Store() *Store }); ok {
			if store := provider.Store(); store != nil {
				return store
			}
		}
	}
	return nil
}
func StatusFromModules(set *runtimemodule.Set) (*TasksSnapshot, error) {
	if set == nil {
		return nil, nil
	}
	for _, module := range set.Modules() {
		if provider, ok := module.(interface {
			Snapshot() (*TasksSnapshot, error)
		}); ok {
			return provider.Snapshot()
		}
	}
	return nil, nil
}

func HookStateFromModules(set *runtimemodule.Set) []byte {
	if set == nil {
		return nil
	}
	for _, module := range set.Modules() {
		if provider, ok := module.(interface{ HookTasksState() []byte }); ok {
			return provider.HookTasksState()
		}
	}
	return nil
}
