package thread

import (
	"errors"
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
	created := worker.Projection()
	if created.RetentionState != RetentionActive || created.ExecutionState != ExecutionIdle {
		t.Fatalf("created lifecycle = %s/%s, want active/idle", created.RetentionState, created.ExecutionState)
	}
	if _, err := worker.BeginNewGeneration(); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.appendFactsStoreLocked(Fact{Type: FactTurnFailed, TurnID: "turn-before-archive"}); err != nil {
		t.Fatal(err)
	}
	if state := worker.Projection().ExecutionState; state != ExecutionFailed {
		t.Fatalf("execution state before archive = %q, want failed", state)
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
	archivedProjection := archived.Projection()
	if archivedProjection.RetentionState != RetentionArchived || archivedProjection.ExecutionState != "" || archivedProjection.ArchivedAt == nil {
		t.Fatalf("archived lifecycle = %s/%s archived_at=%v, want archived/<empty>/timestamp", archivedProjection.RetentionState, archivedProjection.ExecutionState, archivedProjection.ArchivedAt)
	}
	entries, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.ThreadID == worker.ID && (entry.RetentionState != RetentionArchived || entry.ExecutionState != "") {
			t.Fatalf("archived index lifecycle = %s/%s, want archived/<empty>", entry.RetentionState, entry.ExecutionState)
		}
	}
	if _, err := archived.appendFactsStoreLocked(Fact{Type: FactTurnStarted, TurnID: "turn-after-archive"}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("archived execution fact error = %v, want invalid transition", err)
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
	restoredProjection := restored.Projection()
	if restoredProjection.RetentionState != RetentionActive || restoredProjection.ExecutionState != ExecutionIdle || restoredProjection.ArchivedAt != nil {
		t.Fatalf("restored lifecycle = %s/%s archived_at=%v, want active/idle/<nil>", restoredProjection.RetentionState, restoredProjection.ExecutionState, restoredProjection.ArchivedAt)
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

func TestArchiveParentRequiresChildrenToBeArchivedFirst(t *testing.T) {
	store := NewStore(t.TempDir())
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
	if err := store.Archive(parent); err == nil || !strings.Contains(err.Error(), child.ID) {
		t.Fatalf("archive parent error = %v, want active child %s", err, child.ID)
	}
	if err := store.Archive(child); err != nil {
		t.Fatal(err)
	}
	if err := store.Archive(parent); err != nil {
		t.Fatal(err)
	}
}

func TestRollbackWorkerCreationRemovesIdentityForRetry(t *testing.T) {
	store := NewStore(t.TempDir())
	store.random = &sequenceReader{next: 0}
	main, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = main.Close() }()
	worker, err := store.CreateWorker(MainID, "retry-me")
	if err != nil {
		t.Fatal(err)
	}
	workerID := worker.ID
	if err := worker.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.RollbackWorkerCreation(workerID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.OpenActive(workerID); !os.IsNotExist(err) {
		t.Fatalf("rolled-back Worker still opens: %v", err)
	}
	retried, err := store.CreateWorker(MainID, "retry-me")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = retried.Close() }()
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

func TestRecoverLayoutRebuildsProjectionBeforeRelocatingArchivedWorker(t *testing.T) {
	store := NewStore(t.TempDir())
	main, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = main.Close() }()
	worker, err := store.CreateWorker(MainID, "interrupted-archive")
	if err != nil {
		t.Fatal(err)
	}
	workerID := worker.ID
	if _, _, err := worker.journal.Append(Fact{Type: FactThreadArchived}); err != nil {
		t.Fatal(err)
	}
	if err := worker.Close(); err != nil {
		t.Fatal(err)
	}

	if err := store.RecoverLayout(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(store.ThreadsDir(), workerID)); !os.IsNotExist(err) {
		t.Fatalf("journal-archived Worker remains active: %v", err)
	}
	archived, err := store.OpenArchived(workerID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = archived.Close() }()
	if archived.Projection().RetentionState != RetentionArchived {
		t.Fatal("recovered archived Worker has active projection")
	}
	entries, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.ThreadID == workerID && entry.RetentionState != RetentionArchived {
			t.Fatalf("recovered index entry remains active: %+v", entry)
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
