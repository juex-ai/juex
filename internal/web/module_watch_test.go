package web

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

func TestModuleWatcherCoversChildCreatedWhileSubscribingParent(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "modules")
	child := filepath.Join(parent, "notes")
	statePath := filepath.Join(child, "notes.md")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()
	if err := watcher.Add(root); err != nil {
		t.Fatal(err)
	}
	err = watchModuleParents(root, child, map[string]bool{root: true}, func(dir string) error {
		if dir == parent {
			// The child appears after discovery of the parent but before its
			// subscription, so the parent's stream cannot report its creation.
			if err := os.Mkdir(child, 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(statePath, []byte("fresh"), 0o600); err != nil {
				return err
			}
		}
		return watcher.Add(dir)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(statePath); err != nil {
		t.Fatal(err)
	}
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-watcher.Events:
			if event.Name == statePath && event.Has(fsnotify.Remove) {
				return
			}
		case err := <-watcher.Errors:
			t.Fatal(err)
		case <-timer.C:
			t.Fatalf("state removal not observed; watched=%v", watcher.WatchList())
		}
	}
}
