package modulestate

import (
	"github.com/juex-ai/juex/internal/features/goal"
	"github.com/juex-ai/juex/internal/features/notes"
	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
)

func Stores(set *runtimemodule.Set) (*goal.GoalStateStore, *notes.NotesStore) {
	return goal.StoreFromModules(set), notes.StoreFromModules(set)
}
func Status(set *runtimemodule.Set) (*goal.GoalStatusSnapshot, *notes.NotesSnapshot) {
	g, _ := goal.StatusFromModules(set)
	n, _ := notes.StatusFromModules(set)
	return g, n
}
