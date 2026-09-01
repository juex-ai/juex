package thread

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchiveUnarchivePreservesGenerationAndScratchpad(t *testing.T) {
	t.Parallel()
	store := NewStore(t.TempDir())
	store.now = fixedNow()
	store.random = zeroReader{}
	main, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = main.Close() }()
	worker, err := store.CreateWorker(MainID, "worker")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worker.BeginNewGeneration(); err != nil {
		t.Fatal(err)
	}
	scratchFile := filepath.Join(worker.ScratchpadDir(), "draft.md")
	if err := os.WriteFile(scratchFile, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Archive(worker); err != nil {
		t.Fatal(err)
	}
	archived, err := store.OpenArchived(worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	if generation := archived.Projection().CurrentGeneration.ID; generation != "g000002" {
		t.Fatalf("archived generation = %s", generation)
	}
	_ = archived.Close()
	restored, err := store.Unarchive(worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.Close() }()
	if generation := restored.Projection().CurrentGeneration.ID; generation != "g000002" {
		t.Fatalf("restored generation = %s", generation)
	}
	data, err := os.ReadFile(filepath.Join(restored.ScratchpadDir(), "draft.md"))
	if err != nil || string(data) != "keep" {
		t.Fatalf("scratchpad = %q, %v", data, err)
	}
}

func TestDeleteArchivedRejectsParentAndRemovesEligibleWorker(t *testing.T) {
	t.Parallel()
	store := NewStore(t.TempDir())
	store.now = fixedNow()
	store.random = &sequenceReader{next: 0}
	main, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = main.Close() }()
	parent, err := store.CreateWorker(MainID, "parent")
	if err != nil {
		t.Fatal(err)
	}
	child, err := store.CreateWorker(parent.ID, "child")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Archive(child); err != nil {
		t.Fatal(err)
	}
	if err := store.Archive(parent); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteArchived(parent.ID); err == nil {
		t.Fatal("deleting referenced parent succeeded")
	}
	if err := store.DeleteArchived(child.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(store.ArchiveDir(), child.ID)); !os.IsNotExist(err) {
		t.Fatalf("archived child still exists: %v", err)
	}
	entries, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.ThreadID == child.ID {
			t.Fatalf("deleted child remains in index: %#v", entry)
		}
	}
}

func TestRecoverLayoutFinishesInterruptedTrashOperation(t *testing.T) {
	store := NewStore(t.TempDir())
	main, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = main.Close() }()
	worker, err := store.CreateWorker(MainID, "delete-me")
	if err != nil {
		t.Fatal(err)
	}
	workerID := worker.ID
	if err := store.Archive(worker); err != nil {
		t.Fatal(err)
	}
	trashName := workerID + ".delete_interrupted"
	if err := os.MkdirAll(store.TrashDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(store.ArchiveDir(), workerID), filepath.Join(store.TrashDir(), trashName)); err != nil {
		t.Fatal(err)
	}

	if err := store.RecoverLayout(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(store.TrashDir(), trashName)); !os.IsNotExist(err) {
		t.Fatalf("trash operation remains: %v", err)
	}
	entries, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.EqualFold(entry.ThreadID, workerID) {
			t.Fatalf("interrupted delete remains in index: %+v", entry)
		}
	}
}

type sequenceReader struct{ next byte }

func (r *sequenceReader) Read(data []byte) (int, error) {
	for i := range data {
		data[i] = r.next
	}
	r.next++
	return len(data), nil
}
