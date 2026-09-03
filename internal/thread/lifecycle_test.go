package thread

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	beforeLifecycle, err := os.Stat(filepath.Join(worker.Dir, journalFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.ApplyAlias("renamed-worker"); err != nil {
		t.Fatal(err)
	}
	beforeArchive := worker.Projection()
	scratchFile := filepath.Join(worker.ScratchpadDir(), "draft.md")
	if err := os.WriteFile(scratchFile, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Archive(worker); err != nil {
		t.Fatal(err)
	}
	archivedJournal, err := os.Stat(filepath.Join(store.ArchiveDir(), worker.ID, journalFile))
	if err != nil {
		t.Fatal(err)
	}
	if archivedJournal.Size() != beforeLifecycle.Size() {
		t.Fatalf("rename/archive changed Journal size from %d to %d", beforeLifecycle.Size(), archivedJournal.Size())
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
	if archivedProjection.Revision != beforeArchive.Revision+1 || len(archivedProjection.Generations) != len(beforeArchive.Generations) {
		t.Fatalf("archived metadata = %+v, before = %+v", archivedProjection, beforeArchive)
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
	if _, err := archived.BeginNewGeneration(); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("archived Generation append error = %v, want invalid transition", err)
	}
	_ = archived.Close()
	restored, err := store.Unarchive(worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.Close() }()
	restoredJournal, err := os.Stat(filepath.Join(restored.Dir, journalFile))
	if err != nil {
		t.Fatal(err)
	}
	if restoredJournal.Size() != beforeLifecycle.Size() {
		t.Fatalf("unarchive changed Journal size from %d to %d", beforeLifecycle.Size(), restoredJournal.Size())
	}
	if generation := restored.Projection().CurrentGeneration.ID; generation != "g000002" {
		t.Fatalf("restored generation = %s", generation)
	}
	restoredProjection := restored.Projection()
	if restoredProjection.RetentionState != RetentionActive || restoredProjection.ExecutionState != ExecutionIdle || restoredProjection.ArchivedAt != nil {
		t.Fatalf("restored lifecycle = %s/%s archived_at=%v, want active/idle/<nil>", restoredProjection.RetentionState, restoredProjection.ExecutionState, restoredProjection.ArchivedAt)
	}
	if restoredProjection.Revision != archivedProjection.Revision+1 || len(restoredProjection.Generations) != len(archivedProjection.Generations) {
		t.Fatalf("restored metadata = %+v, archived = %+v", restoredProjection, archivedProjection)
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

func TestRecoverLayoutRemovesInterruptedCreationDirectories(t *testing.T) {
	stateDir := t.TempDir()
	store := NewStore(stateDir)
	main, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	if err := main.Close(); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{MainID, "000000"} {
		dir, err := os.MkdirTemp(store.ThreadsDir(), creationDirPattern(id))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, journalFile), []byte("complete but unpublished\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	unrelated := filepath.Join(store.ThreadsDir(), ".keep")
	if err := os.Mkdir(unrelated, 0o755); err != nil {
		t.Fatal(err)
	}

	restarted := NewStore(stateDir)
	injected := errors.New("injected recovery sync failure")
	threadsDirSyncs := 0
	restarted.syncDir = func(path string) error {
		if path == restarted.ThreadsDir() {
			threadsDirSyncs++
			if threadsDirSyncs == 1 {
				return injected
			}
		}
		return nil
	}
	if err := restarted.RecoverLayout(); !errors.Is(err, injected) {
		t.Fatalf("first recovery error = %v, want injected sync failure", err)
	}
	if err := restarted.RecoverLayout(); err != nil {
		t.Fatal(err)
	}
	if threadsDirSyncs != 2 {
		t.Fatalf("active Threads directory syncs = %d, want retry after failure", threadsDirSyncs)
	}
	entries, err := os.ReadDir(restarted.ThreadsDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if isInterruptedCreationDir(entry.Name()) {
			t.Fatalf("interrupted creation remains after restart recovery: %s", entry.Name())
		}
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatalf("unrelated hidden directory was removed: %v", err)
	}
	reopened, err := restarted.OpenActive(MainID)
	if err != nil {
		t.Fatalf("published Main was damaged by recovery: %v", err)
	}
	defer func() { _ = reopened.Close() }()
}

func TestRecoverLayoutUsesMetadataWithoutOpeningJournal(t *testing.T) {
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
	metadataPath := filepath.Join(worker.Dir, projectionFile)
	metadata := mustReadProjection(t, metadataPath)
	archivedAt := NewTimestamp(metadata.UpdatedAt.Add(time.Millisecond))
	metadata.RetentionState = RetentionArchived
	metadata.ExecutionState = ""
	metadata.ArchivedAt = &archivedAt
	metadata.UpdatedAt = archivedAt
	metadata.Revision++
	mustWriteJSON(t, metadataPath, metadata)
	if err := worker.Close(); err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(store.ThreadsDir(), workerID, journalFile)
	if err := os.Rename(journalPath, journalPath+".unavailable"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.OpenActive(workerID); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("OpenActive namespace mismatch error = %v, want invalid metadata", err)
	}

	if err := store.RecoverLayout(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(store.ThreadsDir(), workerID)); !os.IsNotExist(err) {
		t.Fatalf("journal-archived Worker remains active: %v", err)
	}
	archivedJournalPath := filepath.Join(store.ArchiveDir(), workerID, journalFile)
	if err := os.Rename(archivedJournalPath+".unavailable", archivedJournalPath); err != nil {
		t.Fatal(err)
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
