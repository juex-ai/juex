package thread

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/llm"
)

func TestUsageProjectionRecoversCommittedTailExactlyOnce(t *testing.T) {
	store := NewStore(t.TempDir())
	main, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected usage projection failure")
	main.writeProjection = func(string, []byte) error { return injected }
	_, err = main.RecordProviderUsage("turn-1", "openai:gpt-test", llm.Usage{InputTokens: 11, CachedInputTokens: 4, OutputTokens: 3}, nil)
	var persistErr *ProjectionPersistError
	if !errors.As(err, &persistErr) || !errors.Is(err, injected) {
		t.Fatalf("record Usage error = %v, want committed projection failure", err)
	}
	main.writeProjection = nil
	if err := main.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.OpenActive(MainID)
	if err != nil {
		t.Fatalf("recover committed Usage tail: %v", err)
	}
	projection := reopened.Projection()
	if projection.TokenUsage.Total != (llm.Usage{InputTokens: 11, CachedInputTokens: 4, OutputTokens: 3}) {
		t.Fatalf("recovered total = %+v", projection.TokenUsage.Total)
	}
	if projection.TokenUsage.ByModel["openai:gpt-test"] != projection.TokenUsage.Total {
		t.Fatalf("recovered by-model Usage = %+v", projection.TokenUsage.ByModel)
	}
	if projection.UsageAggregatedThrough != projection.EventCursor {
		t.Fatalf("recovered cursors = usage %+v event %+v", projection.UsageAggregatedThrough, projection.EventCursor)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err = store.OpenActive(MainID)
	if err != nil {
		t.Fatalf("second reopen: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	if got := reopened.Projection().TokenUsage.Total; got != projection.TokenUsage.Total {
		t.Fatalf("second reopen double-counted Usage: got %+v want %+v", got, projection.TokenUsage.Total)
	}
}

func TestUsageProjectionRecoversOnlyAfterCursorAcrossGenerations(t *testing.T) {
	store := NewStore(t.TempDir())
	main, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := main.RecordProviderUsage("turn-1", "openai:gpt-a", llm.Usage{InputTokens: 5, OutputTokens: 2}, nil); err != nil {
		t.Fatal(err)
	}
	throughFirst := main.Projection()
	accountedGenerationPath := main.CurrentGenerationJournalPath()
	if _, err := main.BeginNewGeneration(); err != nil {
		t.Fatal(err)
	}
	if _, err := main.RecordProviderUsage("turn-2", "anthropic:claude-b", llm.Usage{InputTokens: 7, CachedInputTokens: 3, OutputTokens: 4}, nil); err != nil {
		t.Fatal(err)
	}
	final := main.Projection()
	if err := main.Close(); err != nil {
		t.Fatal(err)
	}

	stale := final
	stale.TokenUsage = throughFirst.TokenUsage.Clone()
	stale.UsageAggregatedThrough = throughFirst.UsageAggregatedThrough
	writeProjectionForTest(t, main.Dir, stale)
	accountedData, err := os.ReadFile(accountedGenerationPath)
	if err != nil {
		t.Fatal(err)
	}
	accountedData = []byte(strings.Replace(string(accountedData), "thread.created", "thread.brokenx", 1))
	if err := os.WriteFile(accountedGenerationPath, accountedData, 0o600); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.OpenActive(MainID)
	if err != nil {
		t.Fatalf("recover stale aggregate: %v", err)
	}
	projection := reopened.Projection()
	if projection.TokenUsage.Total != (llm.Usage{InputTokens: 12, CachedInputTokens: 3, OutputTokens: 6}) {
		t.Fatalf("recovered total = %+v", projection.TokenUsage.Total)
	}
	if projection.TokenUsage.ByModel["openai:gpt-a"] != (llm.Usage{InputTokens: 5, OutputTokens: 2}) ||
		projection.TokenUsage.ByModel["anthropic:claude-b"] != (llm.Usage{InputTokens: 7, CachedInputTokens: 3, OutputTokens: 4}) {
		t.Fatalf("recovered by-model Usage = %+v", projection.TokenUsage.ByModel)
	}
	if projection.UsageAggregatedThrough != projection.EventCursor {
		t.Fatalf("recovered cursors = usage %+v event %+v", projection.UsageAggregatedThrough, projection.EventCursor)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err = store.OpenActive(MainID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	if got := reopened.Projection().TokenUsage.Total; got != projection.TokenUsage.Total {
		t.Fatalf("second reopen double-counted Usage: got %+v want %+v", got, projection.TokenUsage.Total)
	}
}

func TestUsageProjectionRecoversStaleCursorInCurrentGeneration(t *testing.T) {
	store := NewStore(t.TempDir())
	main, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := main.RecordProviderUsage("turn-1", "openai:gpt-a", llm.Usage{InputTokens: 3, OutputTokens: 1}, nil); err != nil {
		t.Fatal(err)
	}
	first := main.Projection()
	if _, err := main.RecordProviderUsage("turn-2", "openai:gpt-a", llm.Usage{InputTokens: 4, CachedInputTokens: 2, OutputTokens: 2}, nil); err != nil {
		t.Fatal(err)
	}
	final := main.Projection()
	if err := main.Close(); err != nil {
		t.Fatal(err)
	}
	final.TokenUsage = first.TokenUsage.Clone()
	final.UsageAggregatedThrough = first.UsageAggregatedThrough
	writeProjectionForTest(t, main.Dir, final)

	reopened, err := store.OpenActive(MainID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	want := llm.Usage{InputTokens: 7, CachedInputTokens: 2, OutputTokens: 3}
	projection := reopened.Projection()
	if projection.TokenUsage.Total != want || projection.TokenUsage.ByModel["openai:gpt-a"] != want {
		t.Fatalf("recovered Usage = %+v", projection.TokenUsage)
	}
	if projection.UsageAggregatedThrough != projection.EventCursor {
		t.Fatalf("recovered cursors = usage %+v event %+v", projection.UsageAggregatedThrough, projection.EventCursor)
	}
}

func TestEventStoreRejectsNonUsageUnpublishedTail(t *testing.T) {
	store := NewStore(t.TempDir())
	main, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected message projection failure")
	main.writeProjection = func(string, []byte) error { return injected }
	if err := main.Append(llm.TextMessage(llm.RoleUser, "unpublished")); !errors.Is(err, injected) {
		t.Fatalf("append error = %v, want injected projection failure", err)
	}
	main.writeProjection = nil
	if err := main.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.OpenActive(MainID); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("reopen error = %v, want invalid metadata", err)
	}
}

func TestUsageRecoveryRejectsCursorThatIsNotARecordBoundary(t *testing.T) {
	store := NewStore(t.TempDir())
	main, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := main.RecordProviderUsage("turn-1", "openai:gpt-test", llm.Usage{InputTokens: 4, OutputTokens: 1}, nil); err != nil {
		t.Fatal(err)
	}
	usageProjection := main.Projection()
	if err := main.Append(llm.TextMessage(llm.RoleUser, "after Usage")); err != nil {
		t.Fatal(err)
	}
	final := main.Projection()
	if err := main.Close(); err != nil {
		t.Fatal(err)
	}
	final.UsageAggregatedThrough = usageProjection.UsageAggregatedThrough
	final.UsageAggregatedThrough.Offset--
	writeProjectionForTest(t, main.Dir, final)
	if _, err := store.OpenActive(MainID); !errors.Is(err, ErrCorruptJournal) {
		t.Fatalf("reopen error = %v, want corrupt Journal", err)
	}
}

func writeProjectionForTest(t *testing.T, dir string, projection Projection) {
	t.Helper()
	data, err := json.MarshalIndent(projection, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(dir, projectionFile), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestThreadPersistsCommitsInGenerationJournals(t *testing.T) {
	t.Parallel()
	store := NewStore(t.TempDir())
	main, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = main.Close() }()

	paths := main.GenerationJournalPaths()
	want := []string{filepath.Join(main.Dir, "generations", "g000001.jsonl")}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("Generation Journal paths = %v, want %v", paths, want)
	}
	if _, err := os.Stat(paths[0]); err != nil {
		t.Fatal(err)
	}
}

func TestCompactedGenerationReopensFromCurrentJournalAlone(t *testing.T) {
	t.Parallel()
	store := NewStore(t.TempDir())
	main, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	first, err := main.AppendAssigned(llm.TextMessage(llm.RoleUser, "summarized away"))
	if err != nil {
		t.Fatal(err)
	}
	retained, err := main.AppendAssigned(llm.TextMessage(llm.RoleAssistant, "retained tail"))
	if err != nil {
		t.Fatal(err)
	}
	summary := llm.TextMessage(llm.RoleUser, "compact summary")
	summary.Kind = llm.MessageKindCompact
	summary.Compaction = &llm.CompactionMetadata{RetainedMessageIDs: []string{retained.ID}}
	if _, err := main.BeginCompactedGeneration(summary, false, nil); err != nil {
		t.Fatal(err)
	}
	paths := main.GenerationJournalPaths()
	if len(paths) != 2 || main.CurrentGenerationJournalPath() != paths[1] {
		t.Fatalf("Generation paths = %v, current = %q", paths, main.CurrentGenerationJournalPath())
	}
	if err := main.Close(); err != nil {
		t.Fatal(err)
	}
	// A current-context reopen must not inspect an older Generation's content.
	if err := os.WriteFile(paths[0], []byte("complete but corrupt historical data\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.OpenActive(MainID)
	if err != nil {
		t.Fatalf("reopen current Generation with unread historical content: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	if got := messageTexts(reopened.History); !reflect.DeepEqual(got, []string{"compact summary", "retained tail"}) {
		t.Fatalf("Provider context = %v", got)
	}
	if reopened.HasMessageID(first.ID) {
		t.Fatal("summarized message remained in current Provider context")
	}
}

func TestEventStoreRecoversUnregisteredGenerationOrphan(t *testing.T) {
	t.Parallel()
	store := NewStore(t.TempDir())
	main, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := main.BeginNewGeneration(); err != nil {
		t.Fatal(err)
	}
	if err := main.Close(); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(store.ThreadsDir(), MainID, "generations", "g000003.jsonl")
	if err := os.WriteFile(orphan, []byte("orphan"), 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.OpenActive(MainID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	if _, err := os.Stat(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan stat error = %v, want not-exist", err)
	}
}

func TestGenerationRolloverStopsBeforeFileCreationWhenTargetExists(t *testing.T) {
	store := NewStore(t.TempDir())
	main, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = main.Close() }()
	before := main.Projection()
	target := filepath.Join(main.Dir, generationsDirectory, "g000002.jsonl")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}

	if _, err := main.BeginNewGeneration(); err == nil {
		t.Fatal("rollover accepted an occupied Generation path")
	}
	if got := main.Projection(); !reflect.DeepEqual(got, before) {
		t.Fatalf("failed rollover changed projection: got %+v want %+v", got, before)
	}
	if paths := main.GenerationJournalPaths(); len(paths) != 1 {
		t.Fatalf("failed rollover registered paths = %v", paths)
	}
}

func TestGenerationRolloverRemovesDurableSeedWhenMetadataWriteFails(t *testing.T) {
	store := NewStore(t.TempDir())
	main, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = main.Close() }()
	before := main.Projection()
	target := filepath.Join(main.Dir, generationsDirectory, "g000002.jsonl")
	injected := errors.New("injected metadata write failure")
	main.writeProjection = func(string, []byte) error { return injected }

	if _, err := main.BeginNewGeneration(); !errors.Is(err, injected) {
		t.Fatalf("rollover error = %v, want injected failure", err)
	}
	main.writeProjection = nil
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staged Generation remains after metadata failure: %v", err)
	}
	if got := main.Projection(); !reflect.DeepEqual(got, before) {
		t.Fatalf("metadata failure changed projection: got %+v want %+v", got, before)
	}
	if err := main.Append(llm.TextMessage(llm.RoleUser, "still on initial Generation")); err != nil {
		t.Fatalf("append after rolled-back seed: %v", err)
	}
}

func TestGenerationRolloverIsCommittedWhenIndexRefreshFails(t *testing.T) {
	store := NewStore(t.TempDir())
	main, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected index write failure")
	store.writeIndex = func(string, []byte) error { return injected }
	commit, err := main.BeginNewGeneration()
	var persistErr *ProjectionPersistError
	if !errors.As(err, &persistErr) || !errors.Is(err, injected) {
		t.Fatalf("rollover error = %v, want committed projection failure", err)
	}
	if got := main.Projection(); got.CurrentGeneration.ID != "g000002" || got.EventCursor.Seq != commit.Seq {
		t.Fatalf("committed projection = %+v, commit = %+v", got, commit)
	}
	if _, err := os.Stat(main.CurrentGenerationJournalPath()); err != nil {
		t.Fatal(err)
	}
	store.writeIndex = nil
	if err := main.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.OpenActive(MainID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	if got := reopened.Projection().CurrentGeneration.ID; got != "g000002" {
		t.Fatalf("reopened Generation = %q, want g000002", got)
	}
}

func TestEventStoreSequenceContinuesAcrossGenerationFiles(t *testing.T) {
	store := NewStore(t.TempDir())
	main, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = main.Close() }()
	if err := main.Append(llm.TextMessage(llm.RoleUser, "first")); err != nil {
		t.Fatal(err)
	}
	newCommit, err := main.BeginNewGeneration()
	if err != nil {
		t.Fatal(err)
	}
	if err := main.Append(llm.TextMessage(llm.RoleUser, "second")); err != nil {
		t.Fatal(err)
	}
	compactCommit, err := main.BeginCompactedGeneration(llm.TextMessage(llm.RoleUser, "summary"), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	projection := main.Projection()
	if newCommit.Seq != 3 || compactCommit.Seq != 5 || projection.EventCursor.Seq != 5 {
		t.Fatalf("sequence new=%d compact=%d cursor=%d, want 3/5/5", newCommit.Seq, compactCommit.Seq, projection.EventCursor.Seq)
	}
	wantBoundaries := []uint64{1, 3, 5}
	for index, generation := range projection.Generations {
		if generation.BoundarySeq != wantBoundaries[index] {
			t.Fatalf("Generation %s boundary = %d, want %d", generation.ID, generation.BoundarySeq, wantBoundaries[index])
		}
	}
}

func TestEventStoreRejectsUnexpectedGenerationEntries(t *testing.T) {
	for _, test := range []struct {
		name  string
		write func(*testing.T, string)
	}{
		{name: "unknown-file", write: func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, "unexpected.txt"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "generation-symlink", write: func(t *testing.T, dir string) {
			if err := os.Symlink(filepath.Join(dir, "g000001.jsonl"), filepath.Join(dir, "g000002.jsonl")); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := NewStore(t.TempDir())
			main, err := store.EnsureMain()
			if err != nil {
				t.Fatal(err)
			}
			generationDir := filepath.Dir(main.CurrentGenerationJournalPath())
			if err := main.Close(); err != nil {
				t.Fatal(err)
			}
			test.write(t, generationDir)
			if _, err := store.OpenActive(MainID); !errors.Is(err, ErrCorruptJournal) {
				t.Fatalf("OpenActive error = %v, want corrupt Journal", err)
			}
		})
	}
}

func TestTimelineCrossesCompactedGenerationWithoutRepeatingRetainedMessages(t *testing.T) {
	t.Parallel()
	store := NewStore(t.TempDir())
	main, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = main.Close() }()
	if err := main.Append(llm.TextMessage(llm.RoleUser, "old message")); err != nil {
		t.Fatal(err)
	}
	retained, err := main.AppendAssigned(llm.TextMessage(llm.RoleAssistant, "retained once"))
	if err != nil {
		t.Fatal(err)
	}
	summary := llm.TextMessage(llm.RoleUser, "summary seed")
	summary.Kind = llm.MessageKindCompact
	summary.Compaction = &llm.CompactionMetadata{RetainedMessageIDs: []string{retained.ID}}
	if _, err := main.BeginCompactedGeneration(summary, false, nil); err != nil {
		t.Fatal(err)
	}
	if err := main.Append(llm.TextMessage(llm.RoleUser, "new message")); err != nil {
		t.Fatal(err)
	}

	page, err := main.Timeline("", 20)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, item := range page.Items {
		switch {
		case item.Message != nil:
			got = append(got, "message:"+item.Message.FirstText())
		case item.Activity != nil:
			got = append(got, "activity:"+item.Activity.Type)
		}
	}
	want := []string{
		"message:old message",
		"message:retained once",
		"activity:context.compacted",
		"message:new message",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("timeline = %v, want %v", got, want)
	}
	if page.HasMoreBefore || page.PreviousCursor != "" {
		t.Fatalf("complete timeline page = %#v", page)
	}
}

func messageTexts(messages []llm.Message) []string {
	result := make([]string, len(messages))
	for index := range messages {
		result[index] = messages[index].FirstText()
	}
	return result
}
