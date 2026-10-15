package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRetirementPersistsWholeScopeIntentBeforeDeleting(t *testing.T) {
	agent := t.TempDir()
	dirs := []string{filepath.Join(agent, "threads", "main"), filepath.Join(agent, "archive", "threads", "worker")}
	owner := Owner{Module: "removed-implementation", Scope: ScopeThread}
	for _, dir := range dirs {
		resource, err := Prepare(dir, owner, DiscardOnRemoval)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(resource, "opaque-state"), []byte("invalid body"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	err := reconcile(agent, dirs, nil, func(path string) error {
		calls++
		if calls == 2 {
			return os.ErrPermission
		}
		return os.RemoveAll(path)
	})
	if err == nil {
		t.Fatal("cleanup failure was hidden")
	}
	if _, err := os.Stat(filepath.Join(agent, intentFile)); err != nil {
		t.Fatal(err)
	}
	// Re-enabling cannot cancel an accepted retirement, including untouched Threads.
	if err := Reconcile(agent, dirs, []Owner{owner}); err != nil {
		t.Fatal(err)
	}
	for _, dir := range dirs {
		if _, err := os.Stat(Directory(dir, owner)); !os.IsNotExist(err) {
			t.Fatalf("state revived: %s: %v", dir, err)
		}
	}
	if err := Reconcile(agent, dirs, nil); err != nil {
		t.Fatal(err)
	}
}

func TestOwnershipBoundsAndRetention(t *testing.T) {
	agent, dir := t.TempDir(), t.TempDir()
	discard := Owner{Module: "disposable", Scope: ScopeThread}
	retained := Owner{Module: "knowledge", Scope: ScopeThread}
	for _, item := range []struct {
		owner     Owner
		retention Retention
	}{{discard, DiscardOnRemoval}, {retained, Retain}} {
		root, err := Prepare(dir, item.owner, item.retention)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "state"), []byte("body"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	sentinel := filepath.Join(dir, "journal.jsonl")
	if err := os.WriteFile(sentinel, []byte("history"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(dir, Owner{Module: "../knowledge", Scope: ScopeThread}, DiscardOnRemoval); err == nil {
		t.Fatal("accepted escaping owner")
	}
	if err := Reconcile(agent, []string{dir}, nil); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{sentinel, filepath.Join(Directory(dir, retained), "state")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOwnershipRejectsUnrecordedDirectoryAndSymlinks(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		t.Run(map[bool]string{false: "unrecorded", true: "symlink"}[symlink], func(t *testing.T) {
			dir := t.TempDir()
			owner := Owner{Module: "example", Scope: ScopeThread}
			if err := os.MkdirAll(filepath.Dir(Directory(dir, owner)), 0700); err != nil {
				t.Fatal(err)
			}
			if symlink {
				if err := os.Symlink(t.TempDir(), Directory(dir, owner)); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(Directory(dir, owner), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Prepare(dir, owner, DiscardOnRemoval); err == nil {
				t.Fatal("adopted unowned or linked state")
			}
		})
	}
}

func TestLeaseWaitsForEveryWriterAndDoesNotRescanWorkers(t *testing.T) {
	agent, dir := t.TempDir(), t.TempDir()
	owner := Owner{Module: "example", Scope: ScopeThread}
	path, err := Prepare(dir, owner, DiscardOnRemoval)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "state"), []byte("current"), 0600); err != nil {
		t.Fatal(err)
	}
	scans := 0
	enumerate := func() ([]string, error) { scans++; return []string{dir}, nil }
	main, err := Acquire(agent, []Owner{owner}, enumerate)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := Acquire(agent, []Owner{owner}, enumerate)
	if err != nil {
		t.Fatal(err)
	}
	if scans != 1 {
		t.Fatalf("Worker scanned stored Threads %d times", scans)
	}
	if err := main.Close(); err != nil {
		t.Fatal(err)
	}
	if lease, err := Acquire(agent, nil, enumerate); err == nil {
		_ = lease.Close()
		t.Fatal("retired with Worker writer alive")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if err := worker.Close(); err != nil {
		t.Fatal(err)
	}
	applied, err := Acquire(agent, nil, enumerate)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = applied.Close() }()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("state not retired: %v", err)
	}
}

func TestRegistrationRecoveryAndMalformedOwnership(t *testing.T) {
	for _, test := range []string{"missing resource", "escaping metadata", "cross owner alias", "linked parent"} {
		t.Run(test, func(t *testing.T) {
			agent, dir := t.TempDir(), t.TempDir()
			owner := Owner{Module: "example", Scope: ScopeThread}
			path, err := Prepare(dir, owner, DiscardOnRemoval)
			if err != nil {
				t.Fatal(err)
			}
			switch test {
			case "missing resource":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				// Metadata was durable but no resource write completed before the crash.
				if err := Reconcile(agent, []string{dir}, nil); err != nil {
					t.Fatal(err)
				}
			case "escaping metadata", "cross owner alias":
				target := "../../journal.jsonl"
				if test == "cross owner alias" {
					target = "modules/other"
				}
				if err := writeJSON(filepath.Join(dir, ledgerFile), ledger{Version: 1, Resources: []resource{{Owner: owner, Path: target, Retention: DiscardOnRemoval}}}); err != nil {
					t.Fatal(err)
				}
				if err := Reconcile(agent, []string{dir}, nil); err == nil {
					t.Fatal("accepted invalid resource path")
				}
				if _, err := os.Stat(path); err != nil {
					t.Fatal(err)
				}
			case "linked parent":
				outside := t.TempDir()
				sentinel := filepath.Join(outside, "keep")
				if err := os.WriteFile(sentinel, []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.RemoveAll(filepath.Join(dir, "modules")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(dir, "modules")); err != nil {
					t.Fatal(err)
				}
				if err := Reconcile(agent, []string{dir}, nil); err == nil {
					t.Fatal("accepted linked resource parent")
				}
				if _, err := os.Stat(sentinel); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
