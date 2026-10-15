package notes

import runtimemodule "github.com/juex-ai/juex/internal/framework/module"

// StoreFromModules returns the enabled owner's state without constructing resources.
func StoreFromModules(set *runtimemodule.Set) *NotesStore {
	if set == nil {
		return nil
	}
	for _, module := range set.Modules() {
		if provider, ok := module.(interface{ NotesStore() *NotesStore }); ok {
			if store := provider.NotesStore(); store != nil {
				return store
			}
		}
	}
	return nil
}
func StatusFromModules(set *runtimemodule.Set) (*NotesSnapshot, error) {
	if set == nil {
		return nil, nil
	}
	for _, module := range set.Modules() {
		if provider, ok := module.(interface {
			NotesStatusSnapshot() (*NotesSnapshot, error)
		}); ok {
			return provider.NotesStatusSnapshot()
		}
	}
	return nil, nil
}
