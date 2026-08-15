package events_test

import (
	"testing"

	"github.com/juex-ai/juex/internal/eventcatalog"
	"github.com/juex-ai/juex/internal/events"
)

func TestDurableSinkRejectsMalformedCatalogEventBeforeJournal(t *testing.T) {
	journal := &catalogJournal{}
	sink := events.NewDurableSink(journal)
	t.Cleanup(func() { _ = sink.Close() })
	sink.SetCatalog(eventcatalog.Default())

	if _, err := sink.Commit(events.Event{
		Type:    "turn.started",
		Payload: map[string]any{"input": 42},
	}); err == nil {
		t.Fatal("Commit() error = nil")
	}
	if len(journal.events) != 0 {
		t.Fatalf("journal events = %+v, want none", journal.events)
	}
}

type catalogJournal struct {
	events []events.Event
}

func (j *catalogJournal) AppendEvent(event events.Event) error {
	j.events = append(j.events, event)
	return nil
}
