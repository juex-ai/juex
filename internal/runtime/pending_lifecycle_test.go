package runtime

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/llm"
)

func TestReceivePendingInputStartsIdleTurnWithFrameworkIdentity(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)

	result, err := eng.ReceivePendingInput(context.Background(), PendingInputRequest{
		Message: llm.TextMessage(llm.RoleUser, "hello"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Disposition != PendingInputStarted {
		t.Fatalf("disposition = %q, want %q", result.Disposition, PendingInputStarted)
	}
	if result.TurnID == "" || result.Message.ID == "" || result.Message.FirstText() != "hello" {
		t.Fatalf("started input = %+v", result)
	}
	if status := eng.PendingInputStatus(); status.TurnID != result.TurnID {
		t.Fatalf("active turn = %+v, want %q", status, result.TurnID)
	}
	record := pendingLifecycleTestRecord(t, eng, result.RecordID)
	if record.State != PendingInputStateAdmitted || record.TurnID != result.TurnID || record.MessageID != result.Message.ID {
		t.Fatalf("durable record = %+v", record)
	}
}

func TestResolvePendingInputTreatsTranscriptAsDeliveredAfterTerminalFailure(t *testing.T) {
	eng, _ := newEngine(t, errorProvider{}, false)
	opts := PendingInputOptions{ID: "mcp-event-1", TTL: time.Hour}
	started, err := eng.ReceivePendingInput(context.Background(), PendingInputRequest{
		Message: llm.Message{Role: llm.RoleUser, Kind: llm.MessageKindMCPEvent, Blocks: []llm.Block{{Type: llm.BlockText, Text: "event"}}},
		Options: &opts,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, runErr := eng.TurnMessageWithID(context.Background(), started.Message, started.TurnID)
	if runErr == nil {
		t.Fatal("TurnMessageWithID() error = nil, want provider failure")
	}

	resolved, err := eng.ResolvePendingInput(started.RecordID, runErr)
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("ResolvePendingInput() error = %v, want provider failure", err)
	}
	if resolved.Disposition != PendingInputProcessed || resolved.Retry != PendingInputNoRetry || pendingLifecycleTestRecord(t, eng, resolved.RecordID).State != PendingInputStateProcessed {
		t.Fatalf("resolved input = %+v", resolved)
	}
}

func TestReceivePendingInputDeduplicatesStableExternalIdentity(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	opts := PendingInputOptions{ID: "observation-1", TTL: time.Hour}

	started, err := eng.ReceivePendingInput(context.Background(), PendingInputRequest{
		Message: llm.Message{Role: llm.RoleUser, Kind: llm.MessageKindObservation, Blocks: []llm.Block{{Type: llm.BlockText, Text: "changed"}}},
		Options: &opts,
	})
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := eng.ReceivePendingInput(context.Background(), PendingInputRequest{RecordID: "observation-1"})
	if err != nil {
		t.Fatal(err)
	}
	if started.Disposition != PendingInputStarted || duplicate.Disposition != PendingInputQueued {
		t.Fatalf("started = %+v; duplicate = %+v", started, duplicate)
	}
	if duplicate.RecordID != started.RecordID || duplicate.Status.PendingCount != 0 {
		t.Fatalf("duplicate attached a second live input: %+v", duplicate)
	}
	record := pendingLifecycleTestRecord(t, eng, duplicate.RecordID)
	if record.Message.Kind != llm.MessageKindObservation || record.Origin != PendingInputOriginTurn || !record.ExpiresAt.IsZero() {
		t.Fatalf("stable source metadata changed: %+v", record)
	}
}

func TestReceivePendingInputDefersAcceptedExternalInputUntilRecoveryBarrier(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	opts := PendingInputOptions{ID: "side-result-1", TTL: time.Hour}

	accepted, err := eng.ReceivePendingInput(context.Background(), PendingInputRequest{
		Message:       llm.Message{Role: llm.RoleUser, Kind: llm.MessageKindSideSession, Blocks: []llm.Block{{Type: llm.BlockText, Text: "child result"}}},
		Options:       &opts,
		DeferDelivery: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Disposition != PendingInputQueued || accepted.Retry != PendingInputRetryAfterRecovery || pendingLifecycleTestRecord(t, eng, accepted.RecordID).State != PendingInputStatePending {
		t.Fatalf("accepted input = %+v", accepted)
	}
	if status := eng.PendingInputStatus(); status.TurnID != "" || status.PendingCount != 0 {
		t.Fatalf("deferred input became live before barrier: %+v", status)
	}

	started, err := eng.ReceivePendingInput(context.Background(), PendingInputRequest{RecordID: accepted.RecordID})
	if err != nil {
		t.Fatal(err)
	}
	if started.Disposition != PendingInputStarted || started.RecordID != accepted.RecordID || started.Message.Kind != llm.MessageKindSideSession {
		t.Fatalf("resumed input = %+v", started)
	}
}

func TestReceivePendingInputQueuesBehindActiveTurn(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	started, err := eng.ReceivePendingInput(context.Background(), PendingInputRequest{
		Message: llm.TextMessage(llm.RoleUser, "first"),
	})
	if err != nil {
		t.Fatal(err)
	}

	queued, err := eng.ReceivePendingInput(context.Background(), PendingInputRequest{
		Message: llm.TextMessage(llm.RoleUser, "second"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if queued.Disposition != PendingInputQueued || queued.Status.TurnID != started.TurnID || queued.Status.PendingCount != 1 {
		t.Fatalf("queued input = %+v", queued)
	}
	record := pendingLifecycleTestRecord(t, eng, queued.RecordID)
	if record.ID == "" || record.MessageID == "" || record.Message.FirstText() != "second" || record.State != PendingInputStatePending {
		t.Fatalf("durable queued record = %+v", record)
	}
}

func TestTurnRejectsSynchronousInputBeforeQueueingWhenTurnIsReserved(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	if err := eng.ReserveTurnID("external-turn"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.finishActiveTurn("external-turn") })

	if _, err := eng.Turn(context.Background(), "synchronous input"); !errors.Is(err, ErrActiveTurnExists) {
		t.Fatalf("Turn() error = %v, want %v", err, ErrActiveTurnExists)
	}
	if status := eng.PendingInputStatus(); status.PendingCount != 0 {
		t.Fatalf("pending status = %+v, want rejected synchronous input to remain unqueued", status)
	}
	records, err := eng.currentPendingInputQueue().Records()
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.Message.FirstText() == "synchronous input" {
			t.Fatalf("synchronous input was durably accepted before Turn() returned an error: %+v", record)
		}
	}
}

func TestDiscardPendingInputRemovesAttachedLiveQueueEntry(t *testing.T) {
	eng, bus := newEngine(t, &mockProvider{}, false)
	eng.MaxPendingInputs = 1
	statusStore := NewStatusStore(StatusSeed{SessionID: "discard-session", MaxPendingInputs: 1})
	unsubscribe := bus.Subscribe("*", statusStore.Publish)
	defer unsubscribe()
	if err := eng.ReserveTurnID("active-turn"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.finishActiveTurn("active-turn") })
	opts := PendingInputOptions{ID: "discard-attached", TTL: time.Hour}
	queued, err := eng.ReceivePendingInput(context.Background(), PendingInputRequest{
		Message: llm.TextMessage(llm.RoleUser, "discard me"),
		Options: &opts,
	})
	if err != nil {
		t.Fatal(err)
	}
	if queued.Disposition != PendingInputQueued || queued.Status.PendingCount != 1 {
		t.Fatalf("queued input = %+v", queued)
	}
	if snapshot := statusStore.Snapshot(); snapshot.Session.PendingCount != 1 || snapshot.Session.CanAcceptInput {
		t.Fatalf("queued status projection = %+v, want full queue", snapshot.Session)
	}

	discarded, err := eng.DiscardPendingInput(queued.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	if discarded.Disposition != PendingInputDropped || discarded.Status.PendingCount != 0 {
		t.Fatalf("discarded input = %+v, want inert record removed from live queue", discarded)
	}
	if record := pendingLifecycleTestRecord(t, eng, queued.RecordID); record.State != PendingInputStateDropped {
		t.Fatalf("durable record = %+v, want dropped", record)
	}
	if snapshot := statusStore.Snapshot(); snapshot.Session.PendingCount != 0 || !snapshot.Session.CanAcceptInput {
		t.Fatalf("discarded status projection = %+v, want available queue", snapshot.Session)
	}
}

func TestReceivePendingInputReturnsStorageRetryWhenPersistedRecordCannotBeRead(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	record, err := eng.PersistPendingMessageWithOptions(
		context.Background(),
		llm.TextMessage(llm.RoleUser, "retry journal read"),
		PendingInputOptions{ID: "journal-read-retry", TTL: time.Hour},
	)
	if err != nil {
		t.Fatal(err)
	}
	queue := eng.currentPendingInputQueue()
	pendingPath := queue.path
	backupPath := pendingPath + ".before-read-failure"
	if err := os.Rename(pendingPath, backupPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(pendingPath, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Remove(pendingPath)
		_ = os.Rename(backupPath, pendingPath)
	})

	result, err := eng.ReceivePendingInput(context.Background(), PendingInputRequest{RecordID: record.ID})
	if err == nil {
		t.Fatal("ReceivePendingInput() error = nil, want journal read failure")
	}
	if result.RecordID != record.ID || result.Retry != PendingInputRetryAfterStorage {
		t.Fatalf("ReceivePendingInput() result = %+v, want record %q with storage retry", result, record.ID)
	}
}

func TestReceivePendingInputReturnsStorageRetryWhenPersistedEnqueueWriteFails(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	record, err := eng.PersistPendingMessageWithOptions(
		context.Background(),
		llm.TextMessage(llm.RoleUser, "retry enqueue write"),
		PendingInputOptions{ID: "enqueue-write-retry", TTL: time.Hour},
	)
	if err != nil {
		t.Fatal(err)
	}
	queue := eng.currentPendingInputQueue()
	queue.now = func() time.Time { return record.ExpiresAt.Add(time.Second) }
	wantErr := errors.New("journal append failed")
	queue.fileOps.write = func(*os.File, []byte) (int, error) { return 0, wantErr }

	result, err := eng.ReceivePendingInput(context.Background(), PendingInputRequest{RecordID: record.ID})
	if !errors.Is(err, wantErr) {
		t.Fatalf("ReceivePendingInput() error = %v, want %v", err, wantErr)
	}
	if result.RecordID != record.ID || result.Retry != PendingInputRetryAfterStorage {
		t.Fatalf("ReceivePendingInput() result = %+v, want record %q with storage retry", result, record.ID)
	}
}

func TestReceivePendingInputDoesNotRetryCanceledPersistedEnqueue(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	record, err := eng.PersistPendingMessageWithOptions(
		context.Background(),
		llm.TextMessage(llm.RoleUser, "cancel enqueue"),
		PendingInputOptions{ID: "cancel-enqueue", TTL: time.Hour},
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx := &cancelOnSecondErrContext{Context: context.Background()}

	result, err := eng.ReceivePendingInput(ctx, PendingInputRequest{RecordID: record.ID})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ReceivePendingInput() error = %v, want %v", err, context.Canceled)
	}
	if result.RecordID != record.ID || result.Retry != PendingInputNoRetry {
		t.Fatalf("ReceivePendingInput() result = %+v, want canceled record without retry", result)
	}
}

func TestRestorePendingInputSerializesWithLifecycleDiscard(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	eng.pendingLifecycleMu.Lock()
	locked := true
	t.Cleanup(func() {
		if locked {
			eng.pendingLifecycleMu.Unlock()
		}
	})
	done := make(chan error, 1)
	go func() { done <- eng.restorePendingInput(context.Background(), "restore-turn", "") }()

	select {
	case err := <-done:
		t.Fatalf("restorePendingInput() completed outside lifecycle serialization: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	eng.pendingLifecycleMu.Unlock()
	locked = false
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("restorePendingInput() did not continue after lifecycle serialization released")
	}
}

type cancelOnSecondErrContext struct {
	context.Context
	calls int
}

func (c *cancelOnSecondErrContext) Err() error {
	c.calls++
	if c.calls > 1 {
		return context.Canceled
	}
	return nil
}

func pendingLifecycleTestRecord(t *testing.T, eng *Engine, recordID string) PendingInputRecord {
	t.Helper()
	record, ok, err := eng.PersistedPendingMessage(recordID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("pending input %q not found", recordID)
	}
	return record
}
