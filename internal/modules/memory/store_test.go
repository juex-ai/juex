package memory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/homestore"
)

func storeDirectory(store *Store) string {
	return filepath.Join(store.agentDir, "modules", "memory")
}

func TestStoreEntryLifecycleAndIndex(t *testing.T) {
	agent := t.TempDir()
	store := NewStore(agent)
	if _, err := os.Stat(filepath.Join(agent, "modules")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("constructor initialized files: %v", err)
	}
	entry := Entry{Name: "writing-style", Description: `User said "be concise"; cite the source.`, Type: "user", Body: "Keep responses concise.\n中文 preference. Σ [literal]."}
	first, err := store.Write(t.Context(), entry)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"WRITING-STYLE", "BE CONCISE", "USER", "中文", "ς", "[literal]", ""} {
		hits, err := store.Search(t.Context(), query)
		if err != nil || len(hits) != 1 || hits[0].Body != entry.Body {
			t.Fatalf("search %q=%+v, %v", query, hits, err)
		}
	}
	entry.Body = "Updated durable preference."
	updated, err := NewStore(agent).Write(t.Context(), entry)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.CreatedAt.Equal(first.CreatedAt) || updated.UpdatedAt.Before(first.UpdatedAt) {
		t.Fatalf("timestamps first=%+v updated=%+v", first, updated)
	}
	index := filepath.Join(agent, "modules", "memory", "MEMORY.md")
	if err := os.WriteFile(index, []byte("corrupt index"), 0600); err != nil {
		t.Fatal(err)
	}
	hits, err := NewStore(agent).Search(t.Context(), "updated durable")
	if err != nil || len(hits) != 1 {
		t.Fatalf("restart reads authority: %+v %v", hits, err)
	}
	if err := store.RebuildIndex(t.Context()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(index)
	if err != nil || !strings.Contains(string(data), "[writing-style](writing-style.md)") || strings.Contains(string(data), entry.Body) {
		t.Fatalf("index=%q %v", data, err)
	}
	for range 2 {
		if err := store.Delete(t.Context(), entry.Name); err != nil {
			t.Fatal(err)
		}
	}
	hits, err = store.Search(t.Context(), "")
	if err != nil || len(hits) != 0 {
		t.Fatalf("deleted entries=%+v %v", hits, err)
	}
}

func TestStoreValidationAndBadEntries(t *testing.T) {
	store := NewStore(t.TempDir())
	valid := Entry{Name: "good", Description: "Stable fact", Type: "project", Body: "Original"}
	if _, err := store.Write(t.Context(), valid); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", "../escape", "a/b", "a\\b", ".hidden", "MEMORY", "memory", strings.Repeat("a", 129)} {
		entry := valid
		entry.Name = name
		if _, err := store.Write(t.Context(), entry); err == nil {
			t.Errorf("accepted slug %q", name)
		}
		if err := store.Delete(t.Context(), name); err == nil {
			t.Errorf("delete accepted slug %q", name)
		}
	}
	for _, description := range []string{" ", "line\nnext", "line\rnext", "line\u0085next", "line\u2028next", "line\u2029next", string([]byte{0xff})} {
		entry := valid
		entry.Description = description
		if _, err := store.Write(t.Context(), entry); err == nil {
			t.Errorf("accepted description %q", description)
		}
	}
	for _, kind := range []string{"", "progress", "USER"} {
		entry := valid
		entry.Type = kind
		if _, err := store.Write(t.Context(), entry); err == nil {
			t.Errorf("accepted type %q", kind)
		}
	}
	entry := valid
	entry.Body = string([]byte{0xff})
	if _, err := store.Write(t.Context(), entry); err == nil {
		t.Fatal("accepted invalid UTF-8 body")
	}
	for name, body := range map[string]string{"unterminated.md": "---\nname: bad\n", "mismatch.md": "---\nname: other\ndescription: bad\ntype: project\n---\nwrong", "invalid-type.md": "---\nname: invalid-type\ndescription: bad\ntype: progress\n---\nwrong", "plain.md": "not a memory entry"} {
		if err := os.WriteFile(filepath.Join(storeDirectory(store), name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.RebuildIndex(t.Context()); err != nil {
		t.Fatal(err)
	}
	hits, err := store.Search(t.Context(), "")
	if err != nil || len(hits) != 1 || hits[0].Name != "good" {
		t.Fatalf("bad entries affected valid data: %+v %v", hits, err)
	}
}

func TestStoreRejectsSymlinkBoundaries(t *testing.T) {
	outside := t.TempDir()
	target := filepath.Join(outside, "target.md")
	if err := os.WriteFile(target, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	store := NewStore(t.TempDir())
	if err := store.RebuildIndex(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(storeDirectory(store), "linked.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := store.Write(t.Context(), Entry{Name: "linked", Description: "link", Type: "reference", Body: "replacement"}); err == nil {
		t.Fatal("wrote through symlink entry")
	}
	if err := store.Delete(t.Context(), "linked"); err == nil {
		t.Fatal("accepted symlink entry deletion")
	}
	hits, err := store.Search(t.Context(), "")
	if err != nil || len(hits) != 0 {
		t.Fatalf("read symlink: %+v %v", hits, err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "outside" {
		t.Fatalf("outside modified: %q %v", data, err)
	}
	for _, component := range []string{"modules", "modules/memory"} {
		agent := t.TempDir()
		path := filepath.Join(agent, component)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, path); err != nil {
			t.Fatal(err)
		}
		if err := NewStore(agent).RebuildIndex(t.Context()); err == nil {
			t.Fatalf("accepted symlink directory %s", component)
		}
	}
}

func TestStoreIndexPublicationStaysInOpenedDirectory(t *testing.T) {
	store := NewStore(t.TempDir())
	entry := Entry{Name: "durable", Description: "Original directory", Type: "project", Body: "Retain this knowledge"}
	if _, err := store.Write(t.Context(), entry); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	moved := storeDirectory(store) + "-moved"
	err := store.withLock(t.Context(), true, func(root *os.Root) error {
		if err := os.Rename(storeDirectory(store), moved); err != nil {
			return err
		}
		if err := os.Symlink(outside, storeDirectory(store)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if err := root.Remove(indexFile); err != nil {
			return err
		}
		return store.rebuildIndex(t.Context(), root)
	})
	if err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("publication escaped opened directory: %v, %v", entries, err)
	}
	if data, err := os.ReadFile(filepath.Join(moved, indexFile)); err != nil || !strings.Contains(string(data), "[durable](durable.md)") {
		t.Fatalf("original directory index=%q, %v", data, err)
	}
}

func TestStoreConcurrentViewsKeepCompleteIndex(t *testing.T) {
	agent := t.TempDir()
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			store := NewStore(agent)
			for revision := range 2 {
				_, err := store.Write(t.Context(), Entry{Name: fmt.Sprintf("entry-%02d", i), Description: "Shared Agent knowledge", Type: "reference", Body: fmt.Sprintf("revision-%d", revision)})
				if err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	store := NewStore(agent)
	hits, err := store.Search(t.Context(), "revision-1")
	if err != nil || len(hits) != 16 {
		t.Fatalf("concurrent entries=%d %v", len(hits), err)
	}
	index, err := os.ReadFile(filepath.Join(storeDirectory(store), "MEMORY.md"))
	if err != nil || strings.Count(string(index), "](") != 16 {
		t.Fatalf("index lost writes: %s %v", index, err)
	}
	isolated, err := NewStore(t.TempDir()).Search(t.Context(), "")
	if err != nil || len(isolated) != 0 {
		t.Fatalf("Agent leaked knowledge: %+v %v", isolated, err)
	}
}

func TestStoreLockWaitCancelsAndIndexFailurePreservesEntry(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.RebuildIndex(t.Context()); err != nil {
		t.Fatal(err)
	}
	lock, err := homestore.AcquireLock(filepath.Join(storeDirectory(store), ".lock"), homestore.LockTry)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	err = store.RebuildIndex(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked maintenance=%v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(storeDirectory(store), "MEMORY.md")
	if err := os.Remove(index); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(index, 0700); err != nil {
		t.Fatal(err)
	}
	_, err = store.Write(t.Context(), Entry{Name: "durable", Description: "Stored before index failure", Type: "feedback", Body: "Retain this knowledge"})
	if err == nil || !strings.Contains(err.Error(), "saved") {
		t.Fatalf("index failure did not identify committed entry: %v", err)
	}
	hits, err := store.Search(t.Context(), "retain")
	if err != nil || len(hits) != 1 {
		t.Fatalf("index failure lost authority: %+v %v", hits, err)
	}
	if err := os.Remove(index); err != nil {
		t.Fatal(err)
	}
	if err := store.RebuildIndex(t.Context()); err != nil {
		t.Fatal(err)
	}
}
