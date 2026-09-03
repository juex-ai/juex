package runtime

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/homestore"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/thread"
)

func TestPendingInputQueuePersistsBoundedCurrentStateDocument(t *testing.T) {
	now := time.Date(2026, 6, 14, 8, 0, 0, 0, time.UTC)
	queue, target := pendingQueueFixture(t, func() time.Time { return now })
	record, err := queue.AdmitTurnInput("turn-1", llm.TextMessage(llm.RoleUser, "do it"), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.MarkProcessed([]string{record.ID}); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(target.Dir, "pending_inputs.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Version int                  `json:"v"`
		Records []PendingInputRecord `json:"records"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if document.Version != 1 || len(document.Records) != 1 || document.Records[0].ID != record.ID {
		t.Fatalf("pending document = %+v", document)
	}

	if err := target.AppendEvent(events.Event{Type: "turn.completed", TurnID: "turn-1", Payload: TurnCompletedPayload{
		InputIDs: []string{record.ID},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := queue.ApplyTerminalEvent(events.Event{Type: "turn.completed", TurnID: "turn-1", Payload: TurnCompletedPayload{
		InputIDs: []string{record.ID},
	}}); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if len(document.Records) != 0 {
		t.Fatalf("completed pending document retained history: %+v", document.Records)
	}
	if events, err := target.ReadEvents(); err != nil {
		t.Fatal(err)
	} else if len(events) != 1 || events[0].Type != "turn.completed" {
		t.Fatalf("Generation events = %+v, want terminal event only", events)
	}
}

func pendingQueueFixture(t *testing.T, now func() time.Time) (*PendingInputQueue, *thread.Thread) {
	t.Helper()
	store := thread.NewStore(t.TempDir())
	target, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = target.Close() })
	return NewPendingInputQueue(target.Dir, PendingInputQueueOptions{Now: now, Thread: target}), target
}

func TestPendingInputQueueDeduplicatesAndReplaysInAcceptanceOrder(t *testing.T) {
	now := time.Date(2026, 6, 14, 8, 0, 0, 0, time.UTC)
	queue, _ := pendingQueueFixture(t, func() time.Time { return now })
	first, err := queue.Enqueue(llm.TextMessage(llm.RoleUser, "one"), PendingInputOptions{ID: "event-1", TTL: time.Minute}, "turn-1")
	if err != nil {
		t.Fatal(err)
	}
	if duplicate, err := queue.Enqueue(llm.TextMessage(llm.RoleUser, "replacement"), PendingInputOptions{ID: "event-1"}, "turn-1"); err != nil {
		t.Fatal(err)
	} else if duplicate.Message.FirstText() != first.Message.FirstText() {
		t.Fatalf("duplicate replaced accepted input: %+v", duplicate)
	}
	now = now.Add(-time.Hour)
	second, err := queue.Enqueue(llm.TextMessage(llm.RoleUser, "two"), PendingInputOptions{ID: "event-2", TTL: time.Minute}, "turn-1")
	if err != nil {
		t.Fatal(err)
	}
	replayable, err := queue.Replayable("turn-2", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayable) != 2 || replayable[0].ID != first.ID || replayable[1].ID != second.ID {
		t.Fatalf("replay order = %+v", replayable)
	}
}

func TestPendingInputQueueExpiryRemovesTerminalState(t *testing.T) {
	now := time.Date(2026, 6, 14, 8, 0, 0, 0, time.UTC)
	queue, target := pendingQueueFixture(t, func() time.Time { return now })
	record, err := queue.Enqueue(llm.TextMessage(llm.RoleUser, "stale"), PendingInputOptions{ID: "event-1", TTL: time.Second}, "turn-1")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	if replayable, err := queue.Replayable("turn-2", 0); err != nil || len(replayable) != 0 {
		t.Fatalf("expired replay = %+v, err=%v", replayable, err)
	}
	reloaded := NewPendingInputQueue(target.Dir, PendingInputQueueOptions{Now: func() time.Time { return now }, Thread: target})
	records, err := reloaded.Records()
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := records[record.ID]; exists {
		t.Fatalf("expired record remains in bounded state: %+v", records[record.ID])
	}
}

func TestPendingInputQueueTurnCompletionRemovesStateAfterTerminalEvent(t *testing.T) {
	now := time.Date(2026, 6, 14, 8, 0, 0, 123456789, time.UTC)
	queue, target := pendingQueueFixture(t, func() time.Time { return now })
	record, err := queue.AdmitTurnInput("turn-1", llm.TextMessage(llm.RoleUser, "do it"), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.MarkProcessed([]string{record.ID}); err != nil {
		t.Fatal(err)
	}
	records, err := queue.Records()
	if err != nil {
		t.Fatal(err)
	}
	if current := records[record.ID]; current.State != PendingInputStateProcessed || current.Attempts != 1 {
		t.Fatalf("pending attempt before terminal event = %+v", current)
	}
	terminal := events.Event{Type: "turn.completed", TurnID: "turn-1", Payload: TurnCompletedPayload{InputIDs: []string{record.ID}}}
	if err := target.AppendEvent(terminal); err != nil {
		t.Fatal(err)
	}
	if records, err := queue.Records(); err != nil || len(records) != 1 {
		t.Fatalf("pending state changed before post-commit removal: %+v, err=%v", records, err)
	}
	if err := queue.ApplyTerminalEvent(terminal); err != nil {
		t.Fatal(err)
	}
	if records, err := queue.Records(); err != nil || len(records) != 0 {
		t.Fatalf("completed pending state = %+v, err=%v", records, err)
	}
}

func TestPendingInputQueueTurnFailureDeadLettersAttempt(t *testing.T) {
	queue, target := pendingQueueFixture(t, time.Now)
	record, err := queue.AdmitTurnInput("turn-failed", llm.TextMessage(llm.RoleUser, "do it"), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.MarkProcessed([]string{record.ID}); err != nil {
		t.Fatal(err)
	}
	terminal := events.Event{Type: "turn.errored", TurnID: "turn-failed", Payload: TurnErroredPayload{
		Error: "provider failed", ErrorKind: "error", InputIDs: []string{record.ID},
	}}
	if err := target.AppendEvent(terminal); err != nil {
		t.Fatal(err)
	}
	if err := queue.ApplyTerminalEvent(terminal); err != nil {
		t.Fatal(err)
	}
	records, err := queue.Records()
	if err != nil {
		t.Fatal(err)
	}
	if input := records[record.ID]; input.State != PendingInputStateDeadLettered || input.Attempts != 1 || input.LastError != "provider failed" {
		t.Fatalf("failed pending input = %+v", input)
	}
}

func TestPendingInputQueueAtomicWriteFailurePreservesPreviousDocument(t *testing.T) {
	now := time.Date(2026, 6, 14, 8, 0, 0, 0, time.UTC)
	queue, target := pendingQueueFixture(t, func() time.Time { return now })
	first, err := queue.Enqueue(llm.TextMessage(llm.RoleUser, "first"), PendingInputOptions{ID: "first", TTL: time.Hour}, "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(target.Dir, pendingInputFile)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("write stopped before replacement")
	failing := NewPendingInputQueue(target.Dir, PendingInputQueueOptions{
		Now:    func() time.Time { return now },
		Thread: target,
		WriteFile: func(string, []byte, os.FileMode, os.FileMode) error {
			return wantErr
		},
	})
	if _, err := failing.Enqueue(llm.TextMessage(llm.RoleUser, "second"), PendingInputOptions{ID: "second", TTL: time.Hour}, ""); !errors.Is(err, wantErr) {
		t.Fatalf("Enqueue() error = %v, want %v", err, wantErr)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("failed atomic write changed destination:\nbefore=%s\nafter=%s", before, after)
	}
	reloaded := NewPendingInputQueue(target.Dir, PendingInputQueueOptions{Thread: target})
	records, err := reloaded.Records()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[first.ID].ID != first.ID {
		t.Fatalf("reloaded records = %+v, want only %q", records, first.ID)
	}
}

func TestPendingInputQueueReloadsDocumentAfterPostReplacementError(t *testing.T) {
	now := time.Date(2026, 6, 14, 8, 0, 0, 0, time.UTC)
	_, target := pendingQueueFixture(t, func() time.Time { return now })
	wantErr := errors.New("directory sync failed")
	queue := NewPendingInputQueue(target.Dir, PendingInputQueueOptions{
		Now:    func() time.Time { return now },
		Thread: target,
		WriteFile: func(path string, data []byte, fileMode, parentMode os.FileMode) error {
			if err := homestore.WriteFileAtomic(path, data, fileMode, parentMode); err != nil {
				return err
			}
			return &homestore.AtomicWriteError{Operation: "sync parent directory", Path: filepath.Dir(path), Replaced: true, Err: wantErr}
		},
	})
	if _, err := queue.Enqueue(llm.TextMessage(llm.RoleUser, "committed"), PendingInputOptions{ID: "post-replace", TTL: time.Hour}, ""); !errors.Is(err, wantErr) || !homestore.ReplacementOccurred(err) {
		t.Fatalf("Enqueue() error = %v, replaced=%t", err, homestore.ReplacementOccurred(err))
	}
	records, err := queue.Records()
	if err != nil {
		t.Fatal(err)
	}
	if record := records["post-replace"]; record.ID != "post-replace" || record.Message.FirstText() != "committed" {
		t.Fatalf("reloaded committed record = %+v", record)
	}
}

func TestPendingInputQueueRejectsCorruptDocuments(t *testing.T) {
	now := time.Date(2026, 6, 14, 8, 0, 0, 0, time.UTC)
	record := func(id, messageID string) PendingInputRecord {
		msg := llm.TextMessage(llm.RoleUser, "record "+id)
		msg.ID = messageID
		return PendingInputRecord{ID: id, MessageID: messageID, Message: msg, Origin: PendingInputOriginQueued, State: PendingInputStatePending, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	}
	document := func(t *testing.T, version int, records ...PendingInputRecord) []byte {
		t.Helper()
		data, err := json.Marshal(pendingInputDocument{Version: version, Records: records})
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	for _, tc := range []struct {
		name string
		data func(t *testing.T) []byte
	}{
		{name: "unknown field", data: func(*testing.T) []byte {
			return []byte(`{"v":1,"records":[],"legacy":[]}`)
		}},
		{name: "unsupported version", data: func(t *testing.T) []byte {
			return document(t, 2)
		}},
		{name: "duplicate id", data: func(t *testing.T) []byte {
			duplicate := record("duplicate", "message-1")
			return document(t, pendingInputDocumentVersion, duplicate, duplicate)
		}},
		{name: "duplicate message id", data: func(t *testing.T) []byte {
			return document(t, pendingInputDocumentVersion, record("first", "message-1"), record("second", "message-1"))
		}},
		{name: "invalid state", data: func(t *testing.T) []byte {
			invalid := record("invalid", "message-1")
			invalid.State = "completed"
			return document(t, pendingInputDocumentVersion, invalid)
		}},
		{name: "invalid shape", data: func(t *testing.T) []byte {
			invalid := record("invalid", "message-1")
			invalid.Origin = "legacy"
			return document(t, pendingInputDocumentVersion, invalid)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, pendingInputFile), tc.data(t), 0o600); err != nil {
				t.Fatal(err)
			}
			queue := NewPendingInputQueue(dir, PendingInputQueueOptions{Now: func() time.Time { return now }})
			if _, err := queue.Records(); !errors.Is(err, ErrCorruptPendingInputs) {
				t.Fatalf("Records() error = %v, want ErrCorruptPendingInputs", err)
			}
		})
	}
}

func TestPendingInputQueueRetryAndCancelDeadLetter(t *testing.T) {
	queue, _ := pendingQueueFixture(t, time.Now)
	record, err := queue.AdmitTurnInput("turn-1", llm.TextMessage(llm.RoleUser, "retry me"), false)
	if err != nil {
		t.Fatal(err)
	}
	failure := events.Event{Type: "turn.errored", TurnID: "turn-1", Payload: TurnErroredPayload{
		Error: "provider failed", ErrorKind: "error", InputIDs: []string{record.ID},
	}}
	if err := queue.ApplyTerminalEvent(failure); err != nil {
		t.Fatal(err)
	}
	if err := queue.Retry(record.ID); err != nil {
		t.Fatal(err)
	}
	records, err := queue.Records()
	if err != nil {
		t.Fatal(err)
	}
	if current := records[record.ID]; current.State != PendingInputStatePending || current.TurnID != "" || current.LastError != "" || current.Attempts != 1 {
		t.Fatalf("retried record = %+v", current)
	}
	if err := queue.MarkAdmitted([]string{record.ID}, "turn-2"); err != nil {
		t.Fatal(err)
	}
	if err := queue.ApplyTerminalEvent(events.Event{Type: "turn.errored", TurnID: "turn-2", Payload: TurnErroredPayload{
		Error: "failed again", ErrorKind: "error", InputIDs: []string{record.ID},
	}}); err != nil {
		t.Fatal(err)
	}
	records, err = queue.Records()
	if err != nil {
		t.Fatal(err)
	}
	if current := records[record.ID]; current.State != PendingInputStateDeadLettered || current.Attempts != 2 {
		t.Fatalf("second failed attempt = %+v", current)
	}
	if err := queue.Cancel(record.ID); err != nil {
		t.Fatal(err)
	}
	if records, err := queue.Records(); err != nil || len(records) != 0 {
		t.Fatalf("cancelled dead letter = %+v, err=%v", records, err)
	}
}

func TestPendingInputQueueReconcilesCommittedTerminalBeforeRemoval(t *testing.T) {
	queue, target := pendingQueueFixture(t, time.Now)
	record, err := queue.AdmitTurnInput("turn-1", llm.TextMessage(llm.RoleUser, "once"), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.MarkProcessed([]string{record.ID}); err != nil {
		t.Fatal(err)
	}
	terminal := events.Event{Type: "turn.completed", TurnID: "turn-1", Payload: TurnCompletedPayload{InputIDs: []string{record.ID}}}
	if err := target.AppendEvent(terminal); err != nil {
		t.Fatal(err)
	}

	restarted := NewPendingInputQueue(target.Dir, PendingInputQueueOptions{Thread: target})
	records, err := restarted.Records()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("restart retained terminally consumed input: %+v", records)
	}
	if completed, err := restarted.Completed(record.ID); err != nil || !completed {
		t.Fatalf("Completed(%q) = %t, %v", record.ID, completed, err)
	}
	if _, err := restarted.Enqueue(llm.TextMessage(llm.RoleUser, "duplicate"), PendingInputOptions{ID: record.ID, TTL: time.Hour}, ""); !errors.Is(err, ErrPendingInputHandled) {
		t.Fatalf("duplicate completed Enqueue() error = %v, want ErrPendingInputHandled", err)
	}
}

func TestPendingInputQueueRecoversProcessedAttemptWithoutTerminal(t *testing.T) {
	queue, _ := pendingQueueFixture(t, time.Now)
	record, err := queue.AdmitTurnInput("turn-1", llm.TextMessage(llm.RoleUser, "recover me"), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.MarkProcessed([]string{record.ID}); err != nil {
		t.Fatal(err)
	}
	if err := queue.ReconcileRecoveryFacts(PendingInputRecoveryFacts{
		TranscriptMessageIDs: map[string]struct{}{record.MessageID: {}},
	}); err != nil {
		t.Fatal(err)
	}
	replayable, err := queue.Replayable("", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayable) != 1 || replayable[0].ID != record.ID || replayable[0].State != PendingInputStateProcessed {
		t.Fatalf("replayable crash-window input = %+v", replayable)
	}
	if _, err := queue.AdmitTurnInput("turn-2", replayable[0].Message, false); err != nil {
		t.Fatal(err)
	}
	records, err := queue.Records()
	if err != nil {
		t.Fatal(err)
	}
	if current := records[record.ID]; current.State != PendingInputStateAdmitted || current.TurnID != "turn-2" || current.Attempts != 2 || current.ProcessedAt != nil {
		t.Fatalf("recovered attempt = %+v", current)
	}
}

func TestPendingInputQueueTerminalDispositionControlsRecovery(t *testing.T) {
	for _, tc := range []struct {
		name      string
		eventType string
		errorKind string
		wantState PendingInputState
		wantCount int
	}{
		{name: "runtime restart", eventType: "turn.errored", errorKind: "runtime_restart", wantState: PendingInputStateRetryable, wantCount: 1},
		{name: "cancellation", eventType: "turn.cancelled", errorKind: "cancelled", wantCount: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			queue, _ := pendingQueueFixture(t, time.Now)
			record, err := queue.AdmitTurnInput("turn-1", llm.TextMessage(llm.RoleUser, tc.name), false)
			if err != nil {
				t.Fatal(err)
			}
			if err := queue.MarkProcessed([]string{record.ID}); err != nil {
				t.Fatal(err)
			}
			terminal := events.Event{Type: tc.eventType, TurnID: "turn-1", Payload: TurnErroredPayload{
				Error: tc.name, ErrorKind: tc.errorKind, InputIDs: []string{record.ID},
			}}
			if err := queue.ApplyTerminalEvent(terminal); err != nil {
				t.Fatal(err)
			}
			records, err := queue.Records()
			if err != nil {
				t.Fatal(err)
			}
			if len(records) != tc.wantCount {
				t.Fatalf("records = %+v, want count %d", records, tc.wantCount)
			}
			if tc.wantCount == 1 {
				if current := records[record.ID]; current.State != tc.wantState || current.TurnID != "" || current.LastError != tc.name {
					t.Fatalf("recoverable record = %+v", current)
				}
				replayable, err := queue.Replayable("", 0)
				if err != nil {
					t.Fatal(err)
				}
				if len(replayable) != 0 {
					t.Fatalf("terminally interrupted record replayed without explicit retry: %+v", replayable)
				}
				if err := queue.Retry(record.ID); err != nil {
					t.Fatal(err)
				}
				replayable, err = queue.Replayable("", 0)
				if err != nil || len(replayable) != 1 || replayable[0].ID != record.ID {
					t.Fatalf("explicitly retried records = %+v, err=%v", replayable, err)
				}
			}
		})
	}
}

func TestPendingInputQueueMaterializesAndRepairsThreadCount(t *testing.T) {
	queue, target := pendingQueueFixture(t, time.Now)
	record, err := queue.Enqueue(llm.TextMessage(llm.RoleUser, "queued"), PendingInputOptions{ID: "queued", TTL: time.Hour}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := target.Info().PendingInputs; got != 1 {
		t.Fatalf("pending count after enqueue = %d, want 1", got)
	}
	assertThreadIndexPendingCount(t, target, 1)
	if err := target.SetPendingInputCount(7); err != nil {
		t.Fatal(err)
	}
	restarted := NewPendingInputQueue(target.Dir, PendingInputQueueOptions{Thread: target})
	if _, err := restarted.Records(); err != nil {
		t.Fatal(err)
	}
	if got := target.Info().PendingInputs; got != 1 {
		t.Fatalf("repaired pending count = %d, want 1", got)
	}
	assertThreadIndexPendingCount(t, target, 1)
	if err := restarted.Cancel(record.ID); err != nil {
		t.Fatal(err)
	}
	if got := target.Info().PendingInputs; got != 0 {
		t.Fatalf("pending count after cancellation = %d, want 0", got)
	}
	assertThreadIndexPendingCount(t, target, 0)
}

func assertThreadIndexPendingCount(t *testing.T, target *thread.Thread, want int) {
	t.Helper()
	store := thread.NewStore(filepath.Dir(filepath.Dir(target.Dir)))
	entries, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.ThreadID == target.ID {
			if entry.PendingInputCount != want {
				t.Fatalf("index pending count = %d, want %d", entry.PendingInputCount, want)
			}
			return
		}
	}
	t.Fatalf("Thread %q missing from index", target.ID)
}

func TestPendingInputQueueBulkTransitionsRemainRecoverable(t *testing.T) {
	queue, target := pendingQueueFixture(t, time.Now)
	ids := make([]string, 0, 90)
	for i := 0; i < 90; i++ {
		record, err := queue.Enqueue(llm.TextMessage(llm.RoleUser, "queued"), PendingInputOptions{TTL: time.Hour}, "turn-1")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, record.ID)
	}
	if err := queue.MarkDropped(ids); err != nil {
		t.Fatal(err)
	}
	reloaded := NewPendingInputQueue(target.Dir, PendingInputQueueOptions{Thread: target})
	records, err := reloaded.Records()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("discarded records remain in bounded state: %+v", records)
	}
}
