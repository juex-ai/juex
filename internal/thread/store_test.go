package thread

import (
	"bytes"
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
	defer func() { _ = reopened.Close() }()
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
	defer func() { _ = main.Close() }()
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
	defer func() { _ = main.Close() }()
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

func TestListRebuildsMissingOrInvalidProjectionFromJournal(t *testing.T) {
	for _, test := range []struct {
		name            string
		breakProjection func(string) error
	}{
		{name: "missing", breakProjection: os.Remove},
		{name: "invalid", breakProjection: func(path string) error { return os.WriteFile(path, []byte("not-json\n"), 0o600) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := NewStore(t.TempDir())
			store.now = fixedNow()
			store.random = zeroReader{}
			main, err := store.EnsureMain()
			if err != nil {
				t.Fatal(err)
			}
			worker, err := store.CreateWorker(MainID, "recover-me")
			if err != nil {
				t.Fatal(err)
			}
			workerID := worker.ID
			workerDir := worker.Dir
			if err := worker.Close(); err != nil {
				t.Fatal(err)
			}
			if err := main.Close(); err != nil {
				t.Fatal(err)
			}
			if err := test.breakProjection(filepath.Join(workerDir, projectionFile)); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(store.IndexPath()); err != nil {
				t.Fatal(err)
			}

			entries, err := NewStore(store.AgentStateDir()).List()
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 2 || entries[1].ThreadID != workerID || entries[1].Alias != "recover-me" {
				t.Fatalf("rebuilt entries = %#v", entries)
			}
			data, err := os.ReadFile(filepath.Join(workerDir, projectionFile))
			if err != nil {
				t.Fatal(err)
			}
			var projection Projection
			if err := json.Unmarshal(data, &projection); err != nil || projection.ThreadID != workerID {
				t.Fatalf("regenerated projection = %#v, %v", projection, err)
			}
			if _, err := store.CreateWorker(MainID, "recover-me"); err == nil {
				t.Fatal("duplicate alias unexpectedly succeeded after index rebuild")
			}
		})
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
	defer func() { _ = main.Close() }()
	main.RecordResponseUsage(llm.Usage{InputTokens: 10, CachedInputTokens: 7, OutputTokens: 3}, &llm.ContextUsage{ContextWindow: 100, TotalTokens: 40})
	projection := main.Projection()
	if projection.TokenUsage.CachedInputTokens != 7 || projection.ContextUsage.CurrentTokens != 40 || projection.ContextUsage.Percentage != 40 {
		t.Fatalf("usage projection = %#v", projection)
	}
}

func TestNewGenerationClearsContextUsageAndPreservesCumulativeTokens(t *testing.T) {
	t.Parallel()
	store := NewStore(t.TempDir())
	main, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	main.RecordResponseUsage(
		llm.Usage{InputTokens: 10, CachedInputTokens: 7, OutputTokens: 3},
		&llm.ContextUsage{ContextWindow: 100, TotalTokens: 95},
	)
	if _, err := main.BeginNewGeneration(); err != nil {
		t.Fatal(err)
	}
	projection := main.Projection()
	if projection.ContextUsage != nil || main.ContextUsageSnapshot() != nil {
		t.Fatalf("renewed Generation retained Context Usage: projection=%+v runtime=%+v", projection.ContextUsage, main.ContextUsageSnapshot())
	}
	if projection.TokenUsage.InputTokens != 10 || projection.TokenUsage.CachedInputTokens != 7 || projection.TokenUsage.OutputTokens != 3 {
		t.Fatalf("renewed Generation lost cumulative Token Usage: %+v", projection.TokenUsage)
	}
	if err := main.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.OpenActive(MainID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	if reopened.Projection().ContextUsage != nil || reopened.ContextUsageSnapshot() != nil {
		t.Fatalf("replay restored stale Context Usage: projection=%+v runtime=%+v", reopened.Projection().ContextUsage, reopened.ContextUsageSnapshot())
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
	defer func() { _ = main.Close() }()

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
	defer func() { _ = main.Close() }()
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
	defer func() { _ = main.Close() }()
	worker, err := store.CreateWorker(MainID, "Reviewer")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = worker.Close() }()
	if _, err := store.CreateWorker(MainID, "reviewer"); err == nil {
		t.Fatal("case-insensitive duplicate alias was accepted")
	}
	if _, err := store.CreateWorker(MainID, "MAIN"); err == nil {
		t.Fatal("case-insensitive reserved Main alias was accepted")
	}
	if _, err := store.CreateWorker(MainID, MainID); err == nil {
		t.Fatal("Main Thread ID was accepted as a Worker alias")
	}
	if _, err := store.CreateWorker(MainID, worker.ID); err == nil {
		t.Fatal("Worker Thread ID was accepted as another Worker alias")
	}
	if err := worker.ApplyAlias(worker.ID); err == nil {
		t.Fatal("Worker was renamed to its own Thread ID")
	}
}

func TestCreateWorkerDoesNotReuseArchivedThreadID(t *testing.T) {
	t.Parallel()
	store := NewStore(t.TempDir())
	main, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = main.Close() }()
	store.random = bytes.NewReader(bytes.Repeat([]byte{0}, 6))
	archived, err := store.CreateWorker(MainID, "archived-worker")
	if err != nil {
		t.Fatal(err)
	}
	if archived.ID != "000000" {
		t.Fatalf("archived ID = %q", archived.ID)
	}
	if err := store.Archive(archived); err != nil {
		t.Fatal(err)
	}

	store.random = bytes.NewReader(append(bytes.Repeat([]byte{0}, 6), bytes.Repeat([]byte{1}, 6)...))
	created, err := store.CreateWorker(MainID, "new-worker")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = created.Close() }()
	if created.ID != "111111" {
		t.Fatalf("created ID = %q, want retry result 111111", created.ID)
	}
	if archivedCopy, err := store.OpenArchived("000000"); err != nil {
		t.Fatalf("open archived Worker after collision retry: %v", err)
	} else if err := archivedCopy.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateWorkerRetriesGeneratedAliasCollision(t *testing.T) {
	t.Parallel()
	store := NewStore(t.TempDir())
	main, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = main.Close() }()
	store.random = bytes.NewReader(bytes.Repeat([]byte{2}, 6))
	existing, err := store.CreateWorker(MainID, DefaultWorkerAlias("000000"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = existing.Close() }()

	store.random = bytes.NewReader(append(bytes.Repeat([]byte{0}, 6), bytes.Repeat([]byte{1}, 6)...))
	created, err := store.CreateWorker(MainID, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = created.Close() }()
	if created.ID != "111111" || created.Alias != DefaultWorkerAlias("111111") {
		t.Fatalf("created Worker = id %q alias %q", created.ID, created.Alias)
	}
}

func TestCreateWorkerDoesNotGenerateIDReservedByAlias(t *testing.T) {
	t.Parallel()
	store := NewStore(t.TempDir())
	main, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = main.Close() }()
	store.random = bytes.NewReader(bytes.Repeat([]byte{2}, 6))
	existing, err := store.CreateWorker(MainID, "000000")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = existing.Close() }()

	store.random = bytes.NewReader(append(bytes.Repeat([]byte{0}, 6), bytes.Repeat([]byte{1}, 6)...))
	created, err := store.CreateWorker(MainID, "new-worker")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = created.Close() }()
	if created.ID != "111111" {
		t.Fatalf("created ID = %q, want retry result 111111", created.ID)
	}
}
