package homestore

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestStoreLockUsesCanonicalLayout(t *testing.T) {
	home := t.TempDir()
	for _, scope := range []LockScope{AgentLocks, EndpointLocks, FleetLocks} {
		t.Run(string(scope), func(t *testing.T) {
			guard, err := New(home).Lock(scope, "agent-id", LockTry)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := guard.Close(); err != nil {
					t.Errorf("close lock: %v", err)
				}
			}()

			path := filepath.Join(home, ".locks", string(scope), "agent-id.lock")
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("lock path %s: %v", path, err)
			}
		})
	}
}

func TestStoreLockRejectsInvalidInputs(t *testing.T) {
	tests := []struct {
		name  string
		scope LockScope
		id    string
		mode  LockMode
	}{
		{name: "scope", scope: LockScope("other"), id: "agent-id", mode: LockTry},
		{name: "empty id", scope: AgentLocks, mode: LockTry},
		{name: "escaping id", scope: AgentLocks, id: "../agent-id", mode: LockTry},
		{name: "mode", scope: AgentLocks, id: "agent-id", mode: LockMode(99)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			guard, err := New(t.TempDir()).Lock(test.scope, test.id, test.mode)
			if guard != nil || err == nil {
				t.Fatalf("Lock(%q, %q, %d) = %v, %v; want validation error", test.scope, test.id, test.mode, guard, err)
			}
		})
	}
}

func TestTryLockReportsBusy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "guard.lock")
	first, err := AcquireLock(path, LockWait)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := first.Close(); err != nil {
			t.Errorf("close first lock: %v", err)
		}
	}()

	second, err := AcquireLock(path, LockTry)
	if second != nil || !errors.Is(err, ErrLockBusy) {
		t.Fatalf("second lock = %v, %v; want ErrLockBusy", second, err)
	}
}

func TestWriteFileAtomicCreatesAndReplaces(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested")
	path := filepath.Join(dir, "state.json")
	if err := WriteFileAtomic(path, []byte("first\n"), 0o640, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(path, []byte("second\n"), 0o600, 0o750); err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "second\n" {
		t.Fatalf("body = %q, want replacement", body)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %o, want 600", info.Mode().Perm())
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "state.json" {
		t.Fatalf("directory entries = %v, want only state.json", entries)
	}
}

func TestWriteFileAtomicExistingDoesNotRecreateParent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "runtime.json")
	err := WriteFileAtomicExisting(path, []byte("state\n"), 0o600)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("WriteFileAtomicExisting() error = %v, want os.ErrNotExist", err)
	}
	if _, statErr := os.Stat(filepath.Dir(path)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("missing parent was recreated: %v", statErr)
	}
}
