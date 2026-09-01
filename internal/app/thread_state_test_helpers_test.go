package app

import (
	"testing"

	"github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/runtime/workmem"
)

func appThreadStateStores(t testing.TB, app *App) (*workmem.GoalStateStore, *workmem.NotesStore) {
	t.Helper()
	if app == nil || app.Engine == nil {
		t.Fatal("Thread state stores require an initialized app")
	}
	goalState, notes := runtime.ThreadStateStoresFromModules(app.Engine.ThreadRuntimeSnapshot().Modules)
	if goalState == nil || notes == nil {
		t.Fatalf("Thread state modules returned goal=%p notes=%p", goalState, notes)
	}
	return goalState, notes
}

func appGoalStateStore(t testing.TB, app *App) *workmem.GoalStateStore {
	t.Helper()
	goalState, _ := appThreadStateStores(t, app)
	return goalState
}
