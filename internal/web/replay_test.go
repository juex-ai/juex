package web

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/events"
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
