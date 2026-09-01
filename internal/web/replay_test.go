package web

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/events"
	juexruntime "github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/thread"
	"github.com/juex-ai/juex/internal/toolevents"
)

func TestReplaySince_ReturnsEventsAfterID(t *testing.T) {
	var buf bytes.Buffer
	for _, e := range []events.Event{
		{ID: "1", Type: "turn.started"},
		{ID: "2", Type: toolevents.RequestedType},
		{ID: "3", Type: "turn.completed"},
	} {
		b, _ := json.Marshal(e)
		buf.Write(b)
		buf.WriteByte('\n')
	}
	got, err := replaySince(&buf, "1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Type != toolevents.RequestedType || got[1].Type != "turn.completed" {
		t.Errorf("unexpected slice: %+v", got)
	}
}

func TestReplaySince_EmptyWhenSinceIsLast(t *testing.T) {
	body := `{"id":"1","type":"x"}` + "\n"
	got, err := replaySince(strings.NewReader(body), "1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestReplaySince_EmptySinceReturnsAll(t *testing.T) {
	body := `{"id":"1","type":"x"}` + "\n" + `{"id":"2","type":"y"}` + "\n"
	got, err := replaySince(strings.NewReader(body), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d", len(got))
	}
}

func TestReplaySince_SkipsMalformedLines(t *testing.T) {
	body := `{"id":"1","type":"x"}` + "\n" +
		`not-json` + "\n" +
		`{"id":"2","type":"y"}` + "\n"
	got, err := replaySince(strings.NewReader(body), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d, want 2 (malformed line skipped): %+v", len(got), got)
	}
}

func TestCaptureCommittedEventReplayReadsBeforeLatestCheckpoint(t *testing.T) {
	server := newTestServer(t)
	store := thread.NewStore(server.opts.Cfg.RuntimePaths().StateDir)
	target, err := store.OpenActive(thread.MainID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []events.Event{
		{ID: "before-checkpoint", Type: "turn.started", TurnID: "turn-1", Payload: juexruntime.TurnStartedPayload{}},
		{ID: "checkpoint-terminal", Type: "turn.completed", TurnID: "turn-1", Payload: juexruntime.TurnCompletedPayload{}},
		{ID: "after-checkpoint", Type: "turn.started", TurnID: "turn-2", Payload: juexruntime.TurnStartedPayload{}},
	} {
		if err := target.AppendEvent(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}

	active, err := server.getThread(context.Background(), thread.MainID)
	if err != nil {
		t.Fatal(err)
	}
	if projected := active.app.Thread.ReplaySnapshot().Events; len(projected) != 2 {
		t.Fatalf("checkpoint projection retained %d events, want 2", len(projected))
	}
	replay, err := captureCommittedEventReplay(active.app, thread.MainID)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := replay.readJournal()
	if err != nil {
		t.Fatal(err)
	}
	if err := replay.Close(); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range journal {
		if event.ID == "before-checkpoint" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("complete durable replay omitted pre-checkpoint event: %#v", journal)
	}
	projected, err := projectBrowserEvents(replay.seed, journal, "before-checkpoint", replay.authoritative)
	if err != nil {
		t.Fatal(err)
	}
	if len(projected) != 2 || projected[0].ID != "checkpoint-terminal" || projected[1].ID != "after-checkpoint" {
		t.Fatalf("SSE resume projection = %#v", projected)
	}
}
