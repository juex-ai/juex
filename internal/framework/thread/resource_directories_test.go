package thread

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResourceDirectoriesDoesNotOpenOrRecoverThreadMetadata(t *testing.T) {
	store := NewStore(t.TempDir())
	for _, root := range []string{store.ThreadsDir(), store.ArchiveDir()} {
		dir := filepath.Join(root, MainID)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "thread.json"), []byte("broken metadata"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	dirs, err := store.ResourceDirectories()
	if err != nil || len(dirs) != 2 {
		t.Fatalf("scopes = %v %v", dirs, err)
	}
	if _, err := os.Stat(store.IndexPath()); !os.IsNotExist(err) {
		t.Fatalf("enumeration created index: %v", err)
	}
}

func TestResourceDirectoriesRejectsLinkedThreadRoots(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := os.Mkdir(store.ThreadsDir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(store.ThreadsDir(), MainID)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResourceDirectories(); err == nil {
		t.Fatal("accepted linked Thread scope")
	}
}
