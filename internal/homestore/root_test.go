package homestore

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestRootOperationsStayInOpenedDirectory(t *testing.T) {
	parent := t.TempDir()
	original, moved, outside := filepath.Join(parent, "original"), filepath.Join(parent, "moved"), t.TempDir()
	if err := os.Mkdir(original, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(original)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if err := os.Rename(original, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, original); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	lock, err := AcquireLockAt(root, ".lock", LockTry)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	other, err := AcquireLock(filepath.Join(moved, ".lock"), LockTry)
	if other != nil {
		_ = other.Close()
	}
	if !errors.Is(err, ErrLockBusy) {
		t.Fatalf("lock did not cover original directory: %v", err)
	}
	for _, body := range []string{"first", "replacement"} {
		if err := WriteFileAtomicAt(root, "entry.md", []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := SyncRoot(root); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(moved, "entry.md")); err != nil || string(data) != "replacement" {
		t.Fatalf("published entry=%q, %v", data, err)
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("root operations escaped: %v, %v", entries, err)
	}
	if entries, err := os.ReadDir(moved); err != nil || len(entries) != 2 {
		t.Fatalf("temporary files remain: %v, %v", entries, err)
	}
}

func TestAcquireLockAtConcurrentFirstCreation(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 32 {
		wg.Go(func() {
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Error(err)
				return
			}
			defer func() { _ = root.Close() }()
			<-start
			lock, err := AcquireLockAt(root, ".lock", LockTry)
			if errors.Is(err, ErrLockBusy) {
				return
			}
			if err != nil {
				t.Error(err)
				return
			}
			if err := lock.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	close(start)
	wg.Wait()
}

func TestWriteFileAtomicAtFailureOutcomes(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if err := root.Mkdir("target", 0o700); err != nil {
		t.Fatal(err)
	}
	err = WriteFileAtomicAt(root, "target", []byte("unpublished"), 0o600)
	if err == nil || ReplacementOccurred(err) {
		t.Fatalf("directory replacement=%v", err)
	}
	if info, err := root.Stat("target"); err != nil || !info.IsDir() {
		t.Fatalf("failed replacement changed destination: %v", err)
	}
	want := errors.New("directory sync failed")
	err = writeFileAtomicAtWith(root, "entry.md", []byte("committed"), 0o600, func(*os.Root) error { return want })
	if !errors.Is(err, want) || !ReplacementOccurred(err) {
		t.Fatalf("publication outcome=%v, replaced=%t", err, ReplacementOccurred(err))
	}
	if data, err := root.ReadFile("entry.md"); err != nil || string(data) != "committed" {
		t.Fatalf("post-publication failure lost entry: %q, %v", data, err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 2 {
		t.Fatalf("failure cleanup=%v, %v", entries, err)
	}
}

func TestRootOperationsRequireBasenames(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	for _, name := range []string{"", ".", "..", "../escape", "nested/file", `nested\file`} {
		if lock, err := AcquireLockAt(root, name, LockTry); err == nil || lock != nil {
			t.Errorf("accepted lock basename %q", name)
		}
		if err := WriteFileAtomicAt(root, name, nil, 0o600); err == nil {
			t.Errorf("accepted publication basename %q", name)
		}
	}
	if _, err := AcquireLockAt(root, ".lock", LockMode(99)); err == nil {
		t.Fatal("accepted invalid lock mode")
	}
	if _, err := AcquireLockAt(nil, ".lock", LockTry); err == nil {
		t.Fatal("accepted missing root")
	}
}
