package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/session"
)

func TestPendingInputQueue_AppendFailureLeavesNoLiveRecordAndRequiresValidPrefixRepair(t *testing.T) {
	dir := t.TempDir()
	store := NewPendingInputQueue(dir, PendingInputQueueOptions{})
	first, err := store.Enqueue(
		llm.TextMessage(llm.RoleUser, "committed prefix"),
		PendingInputOptions{ID: "prefix", TTL: time.Hour},
		"turn-1",
	)
	if err != nil {
		t.Fatal(err)
	}
	prefix, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}

	want := errors.New("injected partial append failure")
	store.fileOps.write = func(file *os.File, body []byte) (int, error) {
		partial := len(body) / 2
		n, writeErr := file.Write(body[:partial])
		return n, errors.Join(want, writeErr)
	}
	if _, err := store.Enqueue(
		llm.TextMessage(llm.RoleUser, "must not become live"),
		PendingInputOptions{ID: "failed", TTL: time.Hour},
		"turn-1",
	); !errors.Is(err, want) {
		t.Fatalf("Enqueue() error = %v, want %v", err, want)
	}
	if _, ok := store.records["failed"]; ok {
		t.Fatalf("failed append entered live index: %+v", store.records["failed"])
	}
	if _, err := NewPendingInputQueue(dir, PendingInputQueueOptions{}).Records(); err == nil || !strings.Contains(err.Error(), "parse") {
		t.Fatalf("reload error = %v, want invalid-tail parse failure", err)
	}

	if err := os.Truncate(store.path, int64(len(prefix))); err != nil {
		t.Fatal(err)
	}
	repaired := NewPendingInputQueue(dir, PendingInputQueueOptions{})
	records, err := repaired.Records()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[first.ID].Message.FirstText() != "committed prefix" {
		t.Fatalf("records after prefix repair = %+v, want only committed prefix", records)
	}
	retried, err := repaired.Enqueue(
		llm.TextMessage(llm.RoleUser, "retry exactly once"),
		PendingInputOptions{ID: "failed", TTL: time.Hour},
		"turn-2",
	)
	if err != nil {
		t.Fatal(err)
	}
	if replayable, err := repaired.Replayable("turn-2", 0); err != nil {
		t.Fatal(err)
	} else if len(replayable) != 2 || replayable[0].ID != first.ID || replayable[1].ID != retried.ID {
		t.Fatalf("replayable after retry = %+v, want prefix then one retry", replayable)
	}
}

func TestPendingInputQueue_DeduplicatesByID(t *testing.T) {
	now := time.Date(2026, 6, 14, 8, 0, 0, 0, time.UTC)
	store := NewPendingInputQueue(t.TempDir(), PendingInputQueueOptions{
		Now: func() time.Time { return now },
	})

	first, err := store.Enqueue(llm.TextMessage(llm.RoleUser, "one"), PendingInputOptions{ID: "event-1", TTL: time.Minute}, "turn-1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Enqueue(llm.TextMessage(llm.RoleUser, "two"), PendingInputOptions{ID: "event-1", TTL: time.Minute}, "turn-1")
	if err != nil {
		t.Fatal(err)
	}

	if second.ID != first.ID || second.Message.FirstText() != "one" {
		t.Fatalf("duplicate enqueue replaced record: first=%+v second=%+v", first, second)
	}
	records, err := store.Replayable("turn-2", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].ID != "event-1" {
		t.Fatalf("records = %+v", records)
	}
}

func TestPendingInputQueue_ExpiresReplayableRecords(t *testing.T) {
	now := time.Date(2026, 6, 14, 8, 0, 0, 0, time.UTC)
	store := NewPendingInputQueue(t.TempDir(), PendingInputQueueOptions{
		Now: func() time.Time { return now },
	})
	record, err := store.Enqueue(llm.TextMessage(llm.RoleUser, "stale"), PendingInputOptions{ID: "event-1", TTL: time.Second}, "turn-1")
	if err != nil {
		t.Fatal(err)
	}

	now = now.Add(2 * time.Second)
	replayable, err := store.Replayable("turn-2", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayable) != 0 {
		t.Fatalf("replayable = %+v, want none", replayable)
	}
	records, err := store.Records()
	if err != nil {
		t.Fatal(err)
	}
	if records[record.ID].State != PendingInputStateExpired {
		t.Fatalf("state = %q, want expired", records[record.ID].State)
	}
}

func TestPendingInputQueue_ProcessedRecordsDoNotReplay(t *testing.T) {
	now := time.Date(2026, 6, 14, 8, 0, 0, 0, time.UTC)
	store := NewPendingInputQueue(t.TempDir(), PendingInputQueueOptions{
		Now: func() time.Time { return now },
	})
	record, err := store.Enqueue(llm.TextMessage(llm.RoleUser, "done"), PendingInputOptions{ID: "event-1", TTL: time.Minute}, "turn-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkProcessed([]string{record.ID}); err != nil {
		t.Fatal(err)
	}

	replayable, err := store.Replayable("turn-2", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayable) != 0 {
		t.Fatalf("replayable = %+v, want none", replayable)
	}
}

func TestPendingInputQueue_PersistsStableMessageID(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 14, 8, 0, 0, 0, time.UTC)
	store := NewPendingInputQueue(dir, PendingInputQueueOptions{
		Now: func() time.Time { return now },
	})
	record, err := store.Enqueue(llm.TextMessage(llm.RoleUser, "recover"), PendingInputOptions{ID: "event-1", TTL: time.Minute}, "turn-1")
	if err != nil {
		t.Fatal(err)
	}
	reloaded := NewPendingInputQueue(dir, PendingInputQueueOptions{
		Now: func() time.Time { return now },
	})
	replayable, err := reloaded.Replayable("turn-2", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayable) != 1 {
		t.Fatalf("replayable = %+v", replayable)
	}
	if replayable[0].Message.ID == "" || replayable[0].Message.ID != record.Message.ID {
		t.Fatalf("message id = %q, want stable %q", replayable[0].Message.ID, record.Message.ID)
	}
	createdAt, ok := session.MessageCreatedAt(replayable[0].Message.ID)
	if !ok {
		t.Fatalf("MessageCreatedAt(%q) = false", replayable[0].Message.ID)
	}
	if want := now.Truncate(time.Second); !createdAt.Equal(want) {
		t.Fatalf("message created at = %s, want %s", createdAt, want)
	}
}

func TestPendingInputQueue_ReplayablePreservesJournalOrderWhenCreatedAtTies(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 14, 8, 0, 0, 0, time.UTC)
	store := NewPendingInputQueue(dir, PendingInputQueueOptions{
		Now: func() time.Time { return now },
	})
	first, err := store.Enqueue(
		llm.TextMessage(llm.RoleUser, "first"),
		PendingInputOptions{ID: "z-first", TTL: time.Minute},
		"turn-1",
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Enqueue(
		llm.TextMessage(llm.RoleUser, "second"),
		PendingInputOptions{ID: "a-second", TTL: time.Minute},
		"turn-1",
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkAdmitted([]string{first.ID}, "turn-2"); err != nil {
		t.Fatal(err)
	}

	reloaded := NewPendingInputQueue(dir, PendingInputQueueOptions{
		Now: func() time.Time { return now },
	})
	replayable, err := reloaded.Replayable("turn-2", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayable) != 2 || replayable[0].ID != first.ID || replayable[1].ID != second.ID {
		t.Fatalf("replayable order = %+v, want %q then %q", replayable, first.ID, second.ID)
	}
}

func TestPendingInputQueue_ReplayablePreservesJournalOrderWhenClockMovesBackward(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 14, 8, 0, 0, 0, time.UTC)
	store := NewPendingInputQueue(dir, PendingInputQueueOptions{
		Now: func() time.Time { return now },
	})
	first, err := store.Enqueue(
		llm.TextMessage(llm.RoleUser, "first"),
		PendingInputOptions{ID: "first", TTL: time.Minute},
		"turn-1",
	)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(-time.Hour)
	second, err := store.Enqueue(
		llm.TextMessage(llm.RoleUser, "second"),
		PendingInputOptions{ID: "second", TTL: time.Minute},
		"turn-1",
	)
	if err != nil {
		t.Fatal(err)
	}

	reloaded := NewPendingInputQueue(dir, PendingInputQueueOptions{
		Now: func() time.Time { return now },
	})
	replayable, err := reloaded.Replayable("turn-2", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayable) != 2 || replayable[0].ID != first.ID || replayable[1].ID != second.ID {
		t.Fatalf("replayable order = %+v, want journal order [%q %q]", replayable, first.ID, second.ID)
	}
}

func TestPendingInputQueue_TurnInputDoesNotExpireAndUsesOneAdmissionCheckpoint(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 14, 8, 0, 0, 0, time.UTC)
	store := NewPendingInputQueue(dir, PendingInputQueueOptions{Now: func() time.Time { return now }})
	record, err := store.AdmitTurnInput("turn-1", llm.TextMessage(llm.RoleUser, "accepted"), false)
	if err != nil {
		t.Fatal(err)
	}
	if record.Origin != PendingInputOriginTurn || record.State != PendingInputStateAdmitted || !record.ExpiresAt.IsZero() || record.Attempts != 1 {
		t.Fatalf("turn input record = %+v", record)
	}
	createdAt, ok := session.MessageCreatedAt(record.Message.ID)
	if !ok || !createdAt.Equal(now) {
		t.Fatalf("turn input message created at = %s, %v, want %s, true", createdAt, ok, now)
	}
	data, err := os.ReadFile(filepath.Join(dir, pendingInputFile))
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(strings.TrimSpace(string(data)), "\n") + 1; lines != 1 {
		t.Fatalf("admission journal lines = %d, want one checkpoint", lines)
	}

	now = now.Add(24 * time.Hour)
	reloaded := NewPendingInputQueue(dir, PendingInputQueueOptions{Now: func() time.Time { return now }})
	replayable, err := reloaded.Replayable("turn-2", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayable) != 1 || replayable[0].ID != record.ID {
		t.Fatalf("replayable turn inputs = %+v", replayable)
	}
	if err := reloaded.MarkMessageProcessed(record.MessageID); err != nil {
		t.Fatal(err)
	}
	if !reloaded.loaded || len(reloaded.records) != 1 || len(reloaded.replayable) != 0 {
		t.Fatalf("queue index = loaded:%v records:%d replayable:%d", reloaded.loaded, len(reloaded.records), len(reloaded.replayable))
	}
}

func TestPendingInputQueue_PromotedTurnInputClearsQueuedExpiry(t *testing.T) {
	now := time.Date(2026, 6, 14, 8, 0, 0, 0, time.UTC)
	store := NewPendingInputQueue(t.TempDir(), PendingInputQueueOptions{Now: func() time.Time { return now }})
	record, err := store.Enqueue(llm.TextMessage(llm.RoleUser, "promote me"), PendingInputOptions{TTL: time.Second}, "compact-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PromoteToTurnInput([]string{record.ID}, "turn-1"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	replayable, err := store.Replayable("turn-2", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayable) != 1 || replayable[0].Origin != PendingInputOriginTurn || !replayable[0].ExpiresAt.IsZero() {
		t.Fatalf("promoted turn input = %+v", replayable)
	}
}

func TestPendingInputQueue_StagePersistedInputKeepsItReplayableUntilCommit(t *testing.T) {
	dir := t.TempDir()
	store := NewPendingInputQueue(dir, PendingInputQueueOptions{})
	pending, err := store.Enqueue(
		llm.TextMessage(llm.RoleUser, "retry me"),
		PendingInputOptions{ID: "persisted", TTL: time.Hour},
		"source-turn",
	)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := store.StageTurnInput("turn-1", pending.Message, false)
	if err != nil {
		t.Fatal(err)
	}
	if staged.State != PendingInputStatePending || staged.Origin != PendingInputOriginQueued || staged.TurnID != "source-turn" {
		t.Fatalf("staged persisted input = %+v, want original replayable record", staged)
	}

	reloaded := NewPendingInputQueue(dir, PendingInputQueueOptions{})
	replayable, err := reloaded.Replayable("recovery-turn", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayable) != 1 || replayable[0].ID != pending.ID {
		t.Fatalf("replayable staged input = %+v, want %q", replayable, pending.ID)
	}
	if err := reloaded.CommitTurnInput(pending.ID, "turn-1"); err != nil {
		t.Fatal(err)
	}
	records, err := reloaded.Records()
	if err != nil {
		t.Fatal(err)
	}
	committed := records[pending.ID]
	if committed.State != PendingInputStateAdmitted || committed.Origin != PendingInputOriginTurn || committed.TurnID != "turn-1" || !committed.ExpiresAt.IsZero() {
		t.Fatalf("committed persisted input = %+v", committed)
	}
}

func TestPendingInputQueue_ReconcileRecoveryFactsPromotesCommittedAdmissionAndDeduplicatesTranscript(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 22, 1, 0, 0, 0, time.UTC)
	store := NewPendingInputQueue(dir, PendingInputQueueOptions{Now: func() time.Time { return now }})
	committed, err := store.StageTurnInput("turn-committed", llm.TextMessage(llm.RoleUser, "recover after admission crash"), false)
	if err != nil {
		t.Fatal(err)
	}
	uncommitted, err := store.StageTurnInput("turn-uncommitted", llm.TextMessage(llm.RoleUser, "never admitted"), false)
	if err != nil {
		t.Fatal(err)
	}
	transcribed, err := store.Enqueue(
		llm.TextMessage(llm.RoleUser, "already in transcript"),
		PendingInputOptions{ID: "transcribed", TTL: time.Hour},
		"turn-old",
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.ReconcileRecoveryFacts(PendingInputRecoveryFacts{
		AdmittedMessageIDs:   map[string]struct{}{committed.MessageID: {}},
		TranscriptMessageIDs: map[string]struct{}{transcribed.MessageID: {}},
	}); err != nil {
		t.Fatal(err)
	}
	records, err := store.Records()
	if err != nil {
		t.Fatal(err)
	}
	if got := records[committed.ID]; got.State != PendingInputStateAdmitted || got.Origin != PendingInputOriginTurn || got.TurnID != "turn-committed" {
		t.Fatalf("committed admission recovery = %+v", got)
	}
	if got := records[uncommitted.ID]; got.State != PendingInputStateAccepting {
		t.Fatalf("uncommitted admission state = %q, want accepting", got.State)
	}
	if got := records[transcribed.ID]; got.State != PendingInputStateProcessed || got.ProcessedAt == nil || !got.ProcessedAt.Equal(now) {
		t.Fatalf("transcript reconciliation = %+v", got)
	}
	replayable, err := store.Replayable("turn-recovery", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayable) != 1 || replayable[0].ID != committed.ID {
		t.Fatalf("replayable records = %+v, want only committed admission", replayable)
	}
}

func TestPendingInputQueue_ReconcileRecoveryFactsDoesNotMatchReusedTurnID(t *testing.T) {
	dir := t.TempDir()
	store := NewPendingInputQueue(dir, PendingInputQueueOptions{})
	current, err := store.StageTurnInput(
		"turn-1",
		llm.TextMessage(llm.RoleUser, "accepted after restart"),
		false,
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.ReconcileRecoveryFacts(PendingInputRecoveryFacts{
		AdmittedMessageIDs: map[string]struct{}{"pending-input-from-earlier-process": {}},
	}); err != nil {
		t.Fatal(err)
	}
	records, err := store.Records()
	if err != nil {
		t.Fatal(err)
	}
	if got := records[current.ID]; got.State != PendingInputStateAccepting {
		t.Fatalf("reused turn ID promoted unmatched input: %+v", got)
	}

	if err := store.ReconcileRecoveryFacts(PendingInputRecoveryFacts{
		AdmittedMessageIDs: map[string]struct{}{current.MessageID: {}},
	}); err != nil {
		t.Fatal(err)
	}
	records, err = store.Records()
	if err != nil {
		t.Fatal(err)
	}
	if got := records[current.ID]; got.State != PendingInputStateAdmitted {
		t.Fatalf("matching admission message ID did not promote input: %+v", got)
	}
}

func TestNextUniquePendingInputIDRetriesHistoricalCollision(t *testing.T) {
	records := map[string]PendingInputRecord{
		"pending-collision": {ID: "pending-collision"},
	}
	candidates := []string{"pending-collision", "pending-fresh"}
	attempt := 0
	got := nextUniquePendingInputID(records, func() string {
		id := candidates[attempt]
		attempt++
		return id
	})
	if got != "pending-fresh" || attempt != 2 {
		t.Fatalf("generated id = %q after %d attempts, want pending-fresh after 2", got, attempt)
	}
}
