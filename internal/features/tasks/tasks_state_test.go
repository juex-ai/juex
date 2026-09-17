package tasks

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func createTask(t *testing.T, store *Store, title string, status Status, priority Priority) Task {
	t.Helper()
	task, err := store.Create(Create{Title: title, Description: title + " description", Acceptance: "verified", Status: status, Priority: priority})
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func TestStoreCRUDAndDurability(t *testing.T) {
	store := NewStore(t.TempDir(), Options{})
	a := createTask(t, store, "first", Todo, P1)
	b := createTask(t, store, "second", Pending, P0)
	if a.ID == "" || a.ID == b.ID {
		t.Fatal("task IDs must be distinct")
	}
	title := "updated"
	got, err := store.Update(a.ID, Update{Title: &title, Status: Doing, Priority: P2})
	if err != nil || got.Title != title || got.Status != Doing || got.Priority != P2 {
		t.Fatalf("update: %+v %v", got, err)
	}
	reloaded := NewStore(store.ThreadDir, Options{})
	snapshot, err := reloaded.Snapshot()
	if err != nil || len(snapshot.Tasks) != 2 || snapshot.Tasks[1] != b {
		t.Fatalf("snapshot: %+v %v", snapshot, err)
	}
	if err := store.Delete(a.ID); err != nil {
		t.Fatal(err)
	}
	snapshot, _ = store.Snapshot()
	if len(snapshot.Tasks) != 1 || snapshot.Tasks[0] != b {
		t.Fatalf("delete changed unrelated task: %+v", snapshot)
	}
	before, _ := os.ReadFile(store.Path)
	for _, update := range []Update{{Status: "invalid"}, {Priority: "p3"}, {Title: new(string)}} {
		if _, err := store.Update(b.ID, update); err == nil {
			t.Fatal("invalid update succeeded")
		}
	}
	if _, err := store.Update("missing", Update{Status: Done}); err == nil {
		t.Fatal("missing task update succeeded")
	}
	if err := store.Delete("missing"); err == nil {
		t.Fatal("missing task delete succeeded")
	}
	after, _ := os.ReadFile(store.Path)
	if string(before) != string(after) {
		t.Fatal("rejected mutation changed state")
	}
}

func TestContinuationSelectionAndStaleCommit(t *testing.T) {
	store := NewStore(t.TempDir(), Options{})
	createTask(t, store, "todo p0", Todo, P0)
	createTask(t, store, "doing p2", Doing, P2)
	winner := createTask(t, store, "doing p1 first", Doing, P1)
	createTask(t, store, "doing p1 second", Doing, P1)
	createTask(t, store, "pending", Pending, P0)
	createTask(t, store, "failed", Failed, P0)
	createTask(t, store, "done", Done, P0)
	decision, err := store.CompletionGateDecision()
	if err != nil || !decision.BlockStop || decision.Task.ID != winner.ID || !strings.Contains(decision.ContinuePrompt, winner.ID) {
		t.Fatalf("selection: %+v %v", decision, err)
	}
	if recorded, err := store.RecordContinuation(decision); err != nil || !recorded {
		t.Fatalf("commit: %v %v", recorded, err)
	}
	if recorded, err := store.RecordContinuation(decision); err != nil || recorded {
		t.Fatalf("stale commit: %v %v", recorded, err)
	}
	snapshot, _ := store.Snapshot()
	for _, task := range snapshot.Tasks {
		want := 0
		if task.ID == winner.ID {
			want = 1
		}
		if task.ContinuationCount != want {
			t.Fatalf("wrong count: %+v", task)
		}
	}
	decision, _ = store.CompletionGateDecision()
	createTask(t, store, "higher priority", Doing, P0)
	if recorded, err := store.RecordContinuation(decision); err != nil || recorded {
		t.Fatalf("superseded selection committed: %v %v", recorded, err)
	}
}

func TestGenerationPruningRollbackAndCommit(t *testing.T) {
	store := NewStore(t.TempDir(), Options{})
	for _, status := range []Status{Todo, Doing, Done, Pending, Failed} {
		createTask(t, store, string(status), status, P1)
	}
	before, _ := store.Snapshot()
	finalize, rollback, err := store.StagePruneDone("g000001")
	if err != nil {
		t.Fatal(err)
	}
	if err := rollback(); err != nil {
		t.Fatal(err)
	}
	restored, _ := store.Snapshot()
	if !reflect.DeepEqual(before, restored) {
		t.Fatal("rollback did not restore exact tasks")
	}
	finalize, _, err = store.StagePruneDone("g000001")
	if err != nil {
		t.Fatal(err)
	}
	if err := finalize(); err != nil {
		t.Fatal(err)
	}
	after, _ := store.Snapshot()
	if len(after.Tasks) != 4 {
		t.Fatalf("prune: %+v", after)
	}
	for i, task := range after.Tasks {
		j := i
		if i >= 2 {
			j++
		}
		if task != before.Tasks[j] {
			t.Fatal("unfinished task changed")
		}
	}
}

func TestStoreRejectsMalformedAuthority(t *testing.T) {
	for _, invalid := range []string{
		`{"version":2,"tasks":[]}`,
		`{"version":1,"tasks":[],"extra":true}`,
		`{"version":1,"tasks":[]} {}`,
		`{"version":1,"tasks":[{"id":"a","title":"a","description":"a","status":"unknown","priority":"p1","updated_at":"2026-09-17T00:00:00Z"}]}`,
		`{"version":1,"tasks":[{"id":"a","title":"a","description":"a","status":"todo","priority":"p3","updated_at":"2026-09-17T00:00:00Z"}]}`,
	} {
		store := NewStore(t.TempDir(), Options{})
		createTask(t, store, "seed", Todo, P1)
		if err := os.WriteFile(store.Path, []byte(invalid), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Snapshot(); err == nil {
			t.Fatalf("accepted invalid authority: %s", invalid)
		}
	}
}
