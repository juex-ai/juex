package runtime

import (
	"context"
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
