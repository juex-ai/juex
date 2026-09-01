package thread

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/llm"
)

func TestStoreCreatesAndReplaysMainAndWorker(t *testing.T) {
	t.Parallel()
	store := NewStore(t.TempDir())
	store.now = fixedNow()
	store.random = zeroReader{}
	main, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	if main.ID != MainID || main.Alias != MainAlias || main.ParentThreadID != "" {
		t.Fatalf("Main = %#v", main)
	}
	worker, err := store.CreateWorker(MainID, "reviewer")
	if err != nil {
		t.Fatal(err)
	}
	if worker.ID != "000000" || worker.Alias != "reviewer" || worker.ParentThreadID != MainID {
		t.Fatalf("Worker = %#v", worker)
	}
	message, err := worker.AppendAssigned(llm.TextMessage(llm.RoleUser, "review this"))
	if err != nil {
		t.Fatal(err)
	}
	if message.ID == "" {
		t.Fatal("message id is empty")
	}
	if err := worker.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.OpenActive("000000")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if len(reopened.History) != 1 || reopened.History[0].FirstText() != "review this" {
		t.Fatalf("History = %#v", reopened.History)
	}
	projection := reopened.Projection()
	if projection.CurrentGeneration.ID != InitialGeneration || projection.Counts.GenerationCount != 1 {
		t.Fatalf("Projection = %#v", projection)
	}
	if _, err := os.Stat(reopened.ScratchpadDir()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(reopened.SpoolDir()); err != nil {
		t.Fatal(err)
	}
}

func TestInputLifecycleAndGenerationProjection(t *testing.T) {
	t.Parallel()
	store := NewStore(t.TempDir())
	store.now = fixedNow()
	main, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	defer main.Close()
	notes := "keep across compact"
	goal := json.RawMessage(`{"status":"working"}`)
	if _, err := main.AppendFacts(
		Fact{Type: FactGoalUpdated, Goal: goal},
		Fact{Type: FactNotesUpdated, Notes: &notes},
		Fact{Type: FactInputAccepted, InputID: "in_1"},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := main.AppendFacts(Fact{
		Type: FactInputAttemptStart, InputID: "in_1", AttemptID: "ia_1",
		GenerationID: InitialGeneration, TurnID: "turn_1",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := main.AppendFacts(
		Fact{Type: FactInputAttemptDone, InputID: "in_1", AttemptID: "ia_1"},
		Fact{Type: FactInputCompleted, InputID: "in_1"},
		Fact{Type: FactThreadSettled},
	); err != nil {
		t.Fatal(err)
	}
	summary := llm.TextMessage(llm.RoleUser, "compact bootstrap")
	if _, err := main.BeginCompactedGeneration(summary, false); err != nil {
		t.Fatal(err)
	}
	afterCompact := main.ReplaySnapshot()
	if string(afterCompact.Projection.Goal) != string(goal) || afterCompact.Projection.Notes != notes {
		t.Fatalf("Compact lost Thread state: %#v", afterCompact.Projection)
	}
	if len(afterCompact.Messages) != 0 || len(afterCompact.Activities) != 1 || afterCompact.Activities[0].Summary == nil {
		t.Fatalf("Compact projection = %#v", afterCompact)
	}
	if _, err := main.BeginNewGeneration(); err != nil {
		t.Fatal(err)
	}
	afterNew := main.ReplaySnapshot()
	if len(afterNew.Projection.Goal) != 0 || afterNew.Projection.Notes != "" {
		t.Fatalf("New retained Goal/Notes: %#v", afterNew.Projection)
	}
	if afterNew.Projection.CurrentGeneration.ID != "g000003" || afterNew.Projection.Counts.GenerationCount != 3 {
		t.Fatalf("Generation = %#v", afterNew.Projection.CurrentGeneration)
	}
	if afterNew.Inputs["in_1"].State != InputCompleted || afterNew.Projection.Counts.PendingInputCount != 0 {
		t.Fatalf("Input projection = %#v", afterNew.Inputs["in_1"])
	}
}

func TestInvalidInputTransitionIsRejectedBeforeJournalCommit(t *testing.T) {
	t.Parallel()
	store := NewStore(t.TempDir())
	store.now = fixedNow()
	main, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	defer main.Close()
	before := main.Projection().Revision
	_, err = main.AppendFacts(Fact{Type: FactInputCompleted, InputID: "missing"})
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("error = %v", err)
	}
	if after := main.Projection().Revision; after != before {
		t.Fatalf("revision changed from %d to %d", before, after)
	}
}

func TestListUsesIndexWithoutOpeningJournal(t *testing.T) {
	t.Parallel()
	store := NewStore(t.TempDir())
	store.now = fixedNow()
	main, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	if err := main.Close(); err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(store.ThreadsDir(), MainID, journalFile)
	if err := os.Chmod(journalPath, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(journalPath, 0o600) })
	entries, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ThreadID != MainID {
		t.Fatalf("entries = %#v", entries)
	}
}

type zeroReader struct{}

func (zeroReader) Read(data []byte) (int, error) {
	for i := range data {
		data[i] = 0
	}
	return len(data), nil
}

func TestUsageIncludesCachedInputTokens(t *testing.T) {
	t.Parallel()
	store := NewStore(t.TempDir())
	store.now = func() time.Time { return time.Date(2026, 9, 1, 0, 0, 0, 456000000, time.UTC) }
	main, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	defer main.Close()
	main.RecordResponseUsage(llm.Usage{InputTokens: 10, CachedInputTokens: 7, OutputTokens: 3}, &llm.ContextUsage{ContextWindow: 100, TotalTokens: 40})
	projection := main.Projection()
	if projection.TokenUsage.CachedInputTokens != 7 || projection.ContextUsage.CurrentTokens != 40 || projection.ContextUsage.Percentage != 40 {
		t.Fatalf("usage projection = %#v", projection)
	}
}

func TestIndependentStoreHandlesSerializeIndexUpdates(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	first := NewStore(dir)
	second := NewStore(dir)
	main, err := first.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	defer main.Close()

	const workers = 16
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			store := first
			if i%2 == 1 {
				store = second
			}
			worker, createErr := store.CreateWorker(MainID, fmt.Sprintf("worker-%02d", i))
			if createErr == nil {
				createErr = worker.Close()
			}
			errs <- createErr
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	entries, err := NewStore(dir).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != workers+1 {
		t.Fatalf("entries = %d, want %d", len(entries), workers+1)
	}
}

func TestWorkerAliasMustBeUniqueAcrossActiveAndArchivedThreads(t *testing.T) {
	t.Parallel()
	store := NewStore(t.TempDir())
	main, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	defer main.Close()
	worker, err := store.CreateWorker(MainID, "reviewer")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateWorker(MainID, "reviewer"); err == nil {
		t.Fatal("duplicate active alias was accepted")
	}
	if err := store.Archive(worker); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateWorker(MainID, "reviewer"); err == nil {
		t.Fatal("duplicate archived alias was accepted")
	}
}

func TestWorkerAliasUniquenessMatchesCaseInsensitiveClientResolution(t *testing.T) {
	store := NewStore(t.TempDir())
	main, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	defer main.Close()
	worker, err := store.CreateWorker(MainID, "Reviewer")
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	if _, err := store.CreateWorker(MainID, "reviewer"); err == nil {
		t.Fatal("case-insensitive duplicate alias was accepted")
	}
	if _, err := store.CreateWorker(MainID, "MAIN"); err == nil {
		t.Fatal("case-insensitive reserved Main alias was accepted")
	}
}
