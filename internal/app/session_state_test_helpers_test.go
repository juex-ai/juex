package app

import (
	"testing"

	"github.com/juex-ai/juex/internal/runtime"
)

func appSessionStateStores(t testing.TB, app *App) (*runtime.GoalStateStore, *runtime.NotesStore) {
	t.Helper()
	if app == nil || app.Engine == nil {
		t.Fatal("session state stores require an initialized app")
	}
	goalState, notes := runtime.SessionStateStoresFromModules(app.Engine.SessionRuntimeSnapshot().Modules)
	if goalState == nil || notes == nil {
		t.Fatalf("session state modules returned goal=%p notes=%p", goalState, notes)
	}
	return goalState, notes
}

func appGoalStateStore(t testing.TB, app *App) *runtime.GoalStateStore {
	t.Helper()
	goalState, _ := appSessionStateStores(t, app)
	return goalState
}

func appNotesStore(t testing.TB, app *App) *runtime.NotesStore {
	t.Helper()
	_, notes := appSessionStateStores(t, app)
	return notes
}
