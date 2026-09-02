package thread

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
)

func TestCheckpointColdOpenRestoresContextAndReplaysSuffix(t *testing.T) {
	stateDir := t.TempDir()
	store := NewStore(stateDir)
	target, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	user := llm.TextMessage(llm.RoleUser, "first")
	assistant := llm.TextMessage(llm.RoleAssistant, "answer")
	if _, err := target.AppendFacts(
		Fact{Type: FactInputAccepted, InputID: "input-1"},
		Fact{Type: FactInputAttemptStart, InputID: "input-1", AttemptID: "attempt-1", GenerationID: InitialGeneration, TurnID: "turn-1"},
	); err != nil {
		t.Fatal(err)
	}
	if err := target.AppendBatch([]llm.Message{user, assistant}); err != nil {
		t.Fatal(err)
	}
	if err := target.AppendEvent(events.Event{Type: "turn.completed", TurnID: "turn-1"}); err != nil {
		t.Fatal(err)
	}
	checkpointProjection := target.Projection()
	if checkpointProjection.Journal.LastCheckpointSeq == 0 ||
		checkpointProjection.Journal.LastCheckpointSeq != checkpointProjection.Revision {
		t.Fatalf("terminal Turn did not append checkpoint: %+v", checkpointProjection.Journal)
	}
	if err := target.Append(llm.TextMessage(llm.RoleUser, "suffix")); err != nil {
		t.Fatal(err)
	}
	wantProjection, wantHistory := target.Snapshot()
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "threads", MainID, projectionFile), []byte("corrupt projection\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.OpenActive(MainID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	gotProjection, gotHistory := reopened.Snapshot()
	if gotProjection.Revision != wantProjection.Revision ||
		gotProjection.Journal.ProjectedOffset != wantProjection.Journal.ProjectedOffset ||
		gotProjection.Counts != wantProjection.Counts ||
		gotProjection.CurrentGeneration != wantProjection.CurrentGeneration {
		t.Fatalf("cold-open projection = %+v, want %+v", gotProjection, wantProjection)
	}
	if len(gotHistory) != len(wantHistory) {
		t.Fatalf("cold-open provider history = %d, want %d: %+v", len(gotHistory), len(wantHistory), gotHistory)
	}
	for index := range wantHistory {
		if gotHistory[index].FirstText() != wantHistory[index].FirstText() {
			t.Fatalf("history[%d] = %q, want %q", index, gotHistory[index].FirstText(), wantHistory[index].FirstText())
		}
	}
	var replayedEvents []events.Event
	reopened.ReplayEvents(func(event events.Event) { replayedEvents = append(replayedEvents, event) })
	if len(replayedEvents) != 1 || replayedEvents[0].Type != "turn.completed" || replayedEvents[0].TurnID != "turn-1" {
		t.Fatalf("checkpoint status events = %+v, want terminal turn.completed", replayedEvents)
	}
}

func TestCheckpointPreservesCumulativeCompactionCount(t *testing.T) {
	store := NewStore(t.TempDir())
	target, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"summary one", "summary two"} {
		if _, err := target.BeginCompactedGeneration(llm.TextMessage(llm.RoleUser, text), false); err != nil {
			t.Fatal(err)
		}
	}
	if got := target.ReplaySnapshot().CompactionCount; got != 2 {
		t.Fatalf("live compaction count = %d, want 2", got)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.OpenActive(MainID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	replay := reopened.ReplaySnapshot()
	if replay.CompactionCount != 2 {
		t.Fatalf("reopened compaction count = %d, want 2", replay.CompactionCount)
	}
	if len(replay.Activities) != 1 || replay.Activities[0].Summary == nil || replay.Activities[0].Summary.FirstText() != "summary two" {
		t.Fatalf("bounded latest activity = %+v", replay.Activities)
	}
}

func TestCheckpointPayloadIsBoundedToActiveContextAndOpenInputs(t *testing.T) {
	store := NewStore(t.TempDir())
	target, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = target.Close() }()
	if _, err := target.AppendFacts(
		Fact{Type: FactInputAccepted, InputID: "input-1"},
		Fact{Type: FactInputAttemptStart, InputID: "input-1", AttemptID: "attempt-1", GenerationID: InitialGeneration, TurnID: "turn-1"},
	); err != nil {
		t.Fatal(err)
	}
	if err := target.Append(llm.TextMessage(llm.RoleUser, "current context")); err != nil {
		t.Fatal(err)
	}
	if err := target.AppendEvent(events.Event{Type: "turn.completed", TurnID: "turn-1"}); err != nil {
		t.Fatal(err)
	}

	lines := readJournalLines(t, filepath.Join(target.Dir, journalFile))
	var checkpoint *ReplayCheckpoint
	for _, line := range lines {
		var commit Commit
		if err := json.Unmarshal(line, &commit); err != nil {
			t.Fatal(err)
		}
		for _, fact := range commit.Facts {
			if fact.Type == FactProjectionCheck {
				checkpoint = fact.Checkpoint
			}
		}
	}
	if checkpoint == nil {
		t.Fatal("checkpoint fact missing")
	}
	if len(checkpoint.ProviderMessages) != 1 || checkpoint.ProviderMessages[0].FirstText() != "current context" {
		t.Fatalf("checkpoint context = %+v", checkpoint.ProviderMessages)
	}
	if len(checkpoint.Inputs) != 0 || len(checkpoint.InputRecords) != 0 {
		t.Fatalf("checkpoint retained terminal Input state: inputs=%+v records=%+v", checkpoint.Inputs, checkpoint.InputRecords)
	}
	if len(checkpoint.StatusEvents) != 1 || checkpoint.StatusEvents[0].Type != "turn.completed" || checkpoint.StatusEvents[0].TurnID != "turn-1" {
		t.Fatalf("checkpoint status events = %+v, want one terminal event", checkpoint.StatusEvents)
	}
}

func TestLoadInfoReplaysFullTranscriptBeforeCheckpoint(t *testing.T) {
	target, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"before compact", "answer"} {
		role := llm.RoleUser
		if text == "answer" {
			role = llm.RoleAssistant
		}
		if err := target.Append(llm.TextMessage(role, text)); err != nil {
			t.Fatal(err)
		}
	}
	if err := target.AppendEvent(events.Event{Type: "turn.completed", TurnID: "turn-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := target.BeginCompactedGeneration(llm.TextMessage(llm.RoleUser, "summary"), false); err != nil {
		t.Fatal(err)
	}
	dir := target.Dir
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}

	_, messages, err := LoadInfo(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].FirstText() != "before compact" || messages[1].FirstText() != "answer" {
		t.Fatalf("full transcript = %+v", messages)
	}
}

func BenchmarkOpenLargeCheckpointedJournal(b *testing.B) {
	store := NewStore(b.TempDir())
	target, err := store.EnsureMain()
	if err != nil {
		b.Fatal(err)
	}
	for batch := 0; batch < 32; batch++ {
		messages := make([]llm.Message, 64)
		for index := range messages {
			messages[index] = llm.TextMessage(llm.RoleUser, "historical message")
		}
		if err := target.AppendBatch(messages); err != nil {
			b.Fatal(err)
		}
	}
	if err := target.AppendEvent(events.Event{Type: "turn.completed", TurnID: "turn-history"}); err != nil {
		b.Fatal(err)
	}
	if err := target.Append(llm.TextMessage(llm.RoleUser, "small suffix")); err != nil {
		b.Fatal(err)
	}
	journalPath := filepath.Join(target.Dir, journalFile)
	if err := target.Close(); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		journal, _, err := openJournalForReplay(journalPath, MainID, nil)
		if err != nil {
			b.Fatal(err)
		}
		if err := journal.Close(); err != nil {
			b.Fatal(err)
		}
	}
}

func readJournalLines(t *testing.T, path string) [][]byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var lines [][]byte
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(line) > 0 {
			lines = append(lines, line)
		}
	}
	return lines
}
