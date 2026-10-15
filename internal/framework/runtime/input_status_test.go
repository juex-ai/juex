package runtime

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/framework/thread"
)

func TestInputStatusSurvivesPruningCompactionNewScopeAndColdRead(t *testing.T) {
	q, target := trackedQueue(t)
	deliver := func(text string) PendingInputRecord {
		record, err := q.AdmitTurnInput("turn-"+text, llm.TextMessage(llm.RoleUser, text), false)
		if err != nil {
			t.Fatal(err)
		}
		associated, err := q.inputAssociation(record.Message)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := target.AppendAssigned(record.Message, associated); err != nil {
			t.Fatal(err)
		}
		if err := q.MarkMessageProcessed(record.Message); err != nil {
			t.Fatal(err)
		}
		settleTracked(t, q, record)
		return record
	}
	first, second := deliver("first"), deliver("second")
	reader := &InputStatusReader{}
	page, err := reader.Read(target.Dir, []string{first.MessageID})
	if err != nil || len(page.Messages) != 1 || page.Messages[first.MessageID].CheckedAt != nil {
		t.Fatalf("initial page: %+v, %v", page, err)
	}
	if _, _, err := q.checkInputs(t.Context(), []string{first.ID}, "check"); err != nil {
		t.Fatal(err)
	}
	records, err := q.Records()
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := records[first.ID]; exists {
		t.Fatal("checked input was not pruned")
	}
	if _, err := target.BeginCompactedGeneration(llm.TextMessage(llm.RoleUser, "summary"), false, nil); err != nil {
		t.Fatal(err)
	}
	page, err = reader.Read(target.Dir, []string{first.MessageID, second.MessageID})
	if err != nil || page.Messages[first.MessageID].CheckedAt == nil || page.Messages[second.MessageID].CheckedAt != nil || page.ScopeID != first.ScopeID {
		t.Fatalf("compacted page: %+v, %v", page, err)
	}
	if _, err := target.BeginNewGeneration(); err != nil {
		t.Fatal(err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
	// An observer must leave an unregistered staged rollover alone.
	staged := filepath.Join(target.Dir, "generations", "g999999.jsonl")
	if err := os.WriteFile(staged, []byte("staged"), 0600); err != nil {
		t.Fatal(err)
	}
	reader = &InputStatusReader{}
	page, err = reader.Read(target.Dir, []string{second.MessageID})
	if err != nil || len(page.Messages) != 1 || page.ScopeID == second.ScopeID || page.Messages[second.MessageID].CheckedAt != nil {
		t.Fatalf("cold old page: %+v, %v", page, err)
	}
	if _, err := os.Stat(staged); err != nil {
		t.Fatalf("observation changed staging: %v", err)
	}
	page, err = reader.Read(target.Dir, []string{first.MessageID})
	if err != nil || page.Messages[first.MessageID].CheckedAt == nil {
		t.Fatalf("cold checked page: %+v, %v", page, err)
	}
}

func TestInputAssociationCommitContainsOriginalMessage(t *testing.T) {
	q, target := trackedQueue(t)
	record, err := q.AdmitTurnInput("turn", llm.TextMessage(llm.RoleUser, "request"), false)
	if err != nil {
		t.Fatal(err)
	}
	associated, err := q.inputAssociation(record.Message)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := target.AppendAssigned(record.Message, associated); err != nil {
		t.Fatal(err)
	}
	snapshot, err := target.CaptureEventStore()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = snapshot.Close() }()
	found := false
	_, err = snapshot.VisitAfter(thread.EventCursor{}, func(commit thread.Commit) error {
		original, association := false, false
		for _, fact := range commit.Facts {
			if fact.Message != nil && fact.Message.ID == record.MessageID {
				original = true
			}
			if fact.Event != nil && fact.Event.Type == InputTrackedType {
				association = true
			}
		}
		if association {
			found = true
			if !original {
				t.Fatal("association has a separate crash boundary")
			}
		}
		return nil
	})
	if err != nil || !found {
		t.Fatalf("association commit: %v, %v", found, err)
	}
}

func TestTrackedAcceptanceKeepsIdentityWhenDeliveredWithTrackingDisabled(t *testing.T) {
	q, target := trackedQueue(t)
	accepted, err := q.AdmitTurnInput("turn", llm.TextMessage(llm.RoleUser, "accepted while enabled"), false)
	if err != nil {
		t.Fatal(err)
	}
	disabled := NewPendingInputQueue(target.Dir, PendingInputQueueOptions{Thread: target, TrackUserInputs: false})
	engine := &Engine{Thread: target, PendingInputQueue: disabled, TrackUserInputs: false}
	if _, err := engine.appendInputMessage(accepted.Message); err != nil {
		t.Fatal(err)
	}
	newer, err := disabled.AdmitTurnInput("disabled", llm.TextMessage(llm.RoleUser, "accepted while disabled"), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.appendInputMessage(newer.Message); err != nil {
		t.Fatal(err)
	}
	reader := &InputStatusReader{}
	page, err := reader.Read(target.Dir, []string{accepted.MessageID, newer.MessageID})
	if err != nil || len(page.Messages) != 1 || page.Messages[accepted.MessageID].InputID != accepted.ID {
		t.Fatalf("toggle delivery: %+v %v", page, err)
	}
}
