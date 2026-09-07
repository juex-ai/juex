package goal

import runtimemodule "github.com/juex-ai/juex/internal/framework/module"

// StoreFromModules returns the enabled owner's state without constructing resources.
func StoreFromModules(set *runtimemodule.Set) *GoalStateStore {
	if set == nil {
		return nil
	}
	for _, module := range set.Modules() {
		if provider, ok := module.(interface{ GoalStateStore() *GoalStateStore }); ok {
			if store := provider.GoalStateStore(); store != nil {
				return store
			}
		}
	}
	return nil
}
func StatusFromModules(set *runtimemodule.Set) (*GoalStatusSnapshot, error) {
	if set == nil {
		return nil, nil
	}
	for _, module := range set.Modules() {
		if provider, ok := module.(interface {
			GoalStatusSnapshot() (*GoalStatusSnapshot, error)
		}); ok {
			return provider.GoalStatusSnapshot()
		}
	}
	return nil, nil
}

func HookStateFromModules(set *runtimemodule.Set) []byte {
	if set == nil {
		return nil
	}
	for _, module := range set.Modules() {
		if provider, ok := module.(interface{ HookGoalState() []byte }); ok {
			return provider.HookGoalState()
		}
	}
	return nil
}
