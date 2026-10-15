package runtime

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/events"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/framework/provenance"
	"github.com/juex-ai/juex/internal/framework/thread"
)

func TestRecitationRecordedRequestsReuseRolloverAndColdRead(t *testing.T) {
	target, err := thread.NewStore(t.TempDir()).EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = target.Close() }()
	reader := &RecitationReader{}
	if snapshot, err := reader.Read(target.Dir); err != nil || snapshot != nil {
		t.Fatalf("no request: %+v, %v", snapshot, err)
	}
	tracker := provenance.NewTracker()
	record := func(id, purpose, text string) {
		t.Helper()
		input := llm.TextMessage(llm.RoleUser, "request")
		input.ID = "message-user"
		history := []llm.Message{input}
		if text != "" {
			message := llm.TextMessage(llm.RoleUser, text)
			message.ID = "runtime-notes"
			message.Kind = llm.MessageKindRuntimeContext
			history = append(history, message)
		}
		epoch, err := provenance.BuildRequestEpoch(provenance.RequestInput{Purpose: purpose, History: history})
		if err != nil {
			t.Fatal(err)
		}
		epoch.EpochID = id
		tracker.PrepareEpoch(&epoch)
		if err := target.AppendEvent(events.Event{ID: id, Type: provenance.RequestEpochType, TurnID: "turn-1", Payload: provenance.RequestEpochPayload{Epoch: epoch}}); err != nil {
			t.Fatal(err)
		}
		tracker.CommitEpoch(epoch)
	}
	record("epoch-1", "turn", "## Notes\nFirst")
	snapshot, err := reader.Read(target.Dir)
	if err != nil || snapshot.EpochID != "epoch-1" || len(snapshot.Fragments) != 1 || snapshot.Fragments[0].Text != "## Notes\nFirst" {
		t.Fatalf("inline: %+v, %v", snapshot, err)
	}
	if _, err := target.BeginCompactedGeneration(llm.TextMessage(llm.RoleUser, "summary"), false, nil); err != nil {
		t.Fatal(err)
	}
	record("epoch-2", "turn", "## Notes\nFirst")
	snapshot, err = reader.Read(target.Dir)
	if err != nil || snapshot.EpochID != "epoch-2" || snapshot.GenerationID == thread.InitialGeneration || snapshot.Fragments[0].Text != "## Notes\nFirst" {
		t.Fatalf("reused across generation: %+v, %v", snapshot, err)
	}
	record("epoch-3", "turn", "## Notes\nUpdated")
	record("epoch-compact", "compaction", "")
	if err := target.AppendEvent(events.Event{Type: "turn.completed", TurnID: "turn-1"}); err != nil {
		t.Fatal(err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(target.Dir, "generations", "g999999.jsonl")
	if err := os.WriteFile(staged, []byte("staged"), 0600); err != nil {
		t.Fatal(err)
	}
	reader = &RecitationReader{}
	snapshot, err = reader.Read(target.Dir)
	if err != nil || snapshot.EpochID != "epoch-3" || snapshot.Fragments[0].Text != "## Notes\nUpdated" {
		t.Fatalf("cold latest ordinary request: %+v, %v", snapshot, err)
	}
	if content, err := os.ReadFile(staged); err != nil || string(content) != "staged" {
		t.Fatalf("passive read changed staging: %v", err)
	}
}

func TestRecitationEmptyAndUnknownReference(t *testing.T) {
	for _, reused := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "missing reference"}[reused], func(t *testing.T) {
			target, err := thread.NewStore(t.TempDir()).EnsureMain()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = target.Close() }()
			message := llm.TextMessage(llm.RoleUser, "test")
			message.ID = "message"
			if reused {
				message.Kind = llm.MessageKindRuntimeContext
			}
			epoch, err := provenance.BuildRequestEpoch(provenance.RequestInput{History: []llm.Message{message}})
			if err != nil {
				t.Fatal(err)
			}
			epoch.EpochID = "epoch"
			if reused {
				epoch.Messages[0].Snapshot.Content = nil
				epoch.Messages[0].Snapshot.Reused = true
			}
			if err := target.AppendEvent(events.Event{Type: provenance.RequestEpochType, Payload: provenance.RequestEpochPayload{Epoch: epoch}}); err != nil {
				t.Fatal(err)
			}
			snapshot, err := (&RecitationReader{}).Read(target.Dir)
			if reused {
				if err == nil {
					t.Fatal("missing reused snapshot appeared empty")
				}
				return
			}
			if err != nil || snapshot == nil || snapshot.Fragments == nil || len(snapshot.Fragments) != 0 {
				t.Fatalf("empty request: %+v, %v", snapshot, err)
			}
		})
	}
}
