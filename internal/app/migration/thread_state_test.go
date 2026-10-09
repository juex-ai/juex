package migration

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/migration/legacy"
)

func TestRuntimeImportUsesCurrentNotesTasksWithoutRunningCompletion(t *testing.T) {
	scope, source, bindings := runtimeFixture()
	at := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	task := managedruntime.ThreadTask{ID: "old-task", Title: "Old complete task", Description: "Keep the actual record", Acceptance: "Verified", Status: "done", Priority: "p1", ContinuationCount: 1, UpdatedAt: at}
	encoded, _ := json.Marshal(map[string]any{"version": 1, "tasks": []managedruntime.ThreadTask{task}})
	source.Files = []legacy.SourceFile{
		{Path: "threads/0/modules/notes/notes.md", Data: []byte("Current Notes must survive import"), ModifiedAt: at},
		{Path: "threads/0/modules/tasks/tasks.json", Data: encoded},
		{Path: "archive/threads/abcdef/modules/notes/notes.md", Data: []byte("Archived Worker notes")},
	}
	converted, err := ConvertRuntime(scope, source, bindings)
	if err != nil {
		t.Fatal(err)
	}
	state := converted.Import.Threads[0].State
	if !reflect.DeepEqual(state.Tasks, []managedruntime.ThreadTask{task}) || state.Notes.Content != string(source.Files[0].Data) || !state.Notes.UpdatedAt.Equal(at) || state.Revision != 0 {
		t.Fatal("import changed live state", state)
	}
	if converted.Import.Threads[1].State.Notes.Content != "Archived Worker notes" || !converted.Import.Threads[1].State.Notes.UpdatedAt.IsZero() {
		t.Fatal("invented timestamp or lost archived state")
	}
	source.Files[1].Data = append(encoded, []byte(" trailing")...)
	if _, err := ConvertRuntime(scope, source, bindings); err == nil {
		t.Fatal("accepted damaged tasks file")
	}
	source.Files = nil
	converted, err = ConvertRuntime(scope, source, bindings)
	if err != nil || converted.Import.Threads[0].State.Notes.Content != "" || len(converted.Import.Threads[0].State.Tasks) != 0 {
		t.Fatal("missing current state reconstructed from history", err)
	}
}
