package thread

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkerDepthLimitUsesMetadataBeforeIndexWrites(t *testing.T) {
	store := NewStore(t.TempDir())
	main, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	_ = main.Close()
	parent, err := store.CreateWorker(MainID, "parent", 1)
	if err != nil {
		t.Fatal(err)
	}
	_ = parent.Close()
	if depth, err := store.Depth(parent.ID); err != nil || depth != 1 {
		t.Fatalf("depth = %d, %v", depth, err)
	}
	// A denied creation must not even repair a missing index.
	if err := os.Remove(store.IndexPath()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateWorker(parent.ID, "denied", 1); err == nil || !strings.Contains(err.Error(), "max_depth=1") {
		t.Fatalf("error = %v", err)
	}
	if _, err := os.Stat(store.IndexPath()); !os.IsNotExist(err) {
		t.Fatalf("denied creation rewrote index: %v", err)
	}
	child, err := store.CreateWorker(parent.ID, "child", 2)
	if err != nil {
		t.Fatal(err)
	}
	_ = child.Close()
	if _, err := store.CreateWorker(child.ID, "denied", 2); err == nil || !strings.Contains(err.Error(), "max_depth=2") {
		t.Fatalf("error = %v", err)
	}
	entries, err := store.List()
	if err != nil || len(entries) != 3 {
		t.Fatalf("entries = %v, %v", entries, err)
	}
}

func TestWorkerDepthIncludesArchivedAncestors(t *testing.T) {
	store := NewStore(t.TempDir())
	main, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	_ = main.Close()
	parent, err := store.CreateWorker(MainID, "parent", 2)
	if err != nil {
		t.Fatal(err)
	}
	child, err := store.CreateWorker(parent.ID, "child", 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Archive(child); err != nil {
		t.Fatal(err)
	}
	if err := store.Archive(parent); err != nil {
		t.Fatal(err)
	}
	child, err = store.Unarchive(child.ID)
	if err != nil {
		t.Fatal(err)
	}
	_ = child.Close()
	if depth, err := NewStore(store.AgentStateDir()).Depth(child.ID); err != nil || depth != 2 {
		t.Fatalf("depth = %d, %v", depth, err)
	}
	if _, err := store.CreateWorker(child.ID, "denied", 2); err == nil || !strings.Contains(err.Error(), "max_depth=2") {
		t.Fatalf("error = %v", err)
	}
}

func TestWorkerDepthRejectsBrokenChainWithoutRepair(t *testing.T) {
	for _, mode := range []string{"missing", "cycle"} {
		t.Run(mode, func(t *testing.T) {
			store := NewStore(t.TempDir())
			main, err := store.EnsureMain()
			if err != nil {
				t.Fatal(err)
			}
			_ = main.Close()
			parent, err := store.CreateWorker(MainID, "parent", 2)
			if err != nil {
				t.Fatal(err)
			}
			child, err := store.CreateWorker(parent.ID, "child", 2)
			if err != nil {
				t.Fatal(err)
			}
			projection := parent.Projection()
			projection.ParentThreadID = "abcdef"
			if mode == "cycle" {
				projection.ParentThreadID = child.ID
			}
			_ = parent.Close()
			_ = child.Close()
			data, err := json.Marshal(projection)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(parent.Dir, projectionFile), data, 0600); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(store.IndexPath())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.CreateWorker(child.ID, "denied", 2); err == nil {
				t.Fatal("accepted broken chain")
			}
			after, err := os.ReadFile(store.IndexPath())
			if err != nil || string(before) != string(after) {
				t.Fatal("rejection changed index")
			}
		})
	}
}
