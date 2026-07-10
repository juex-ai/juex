package events

import (
	"errors"
	"reflect"
	"sync"
	"testing"
)

func TestDurableSink_CommitsBeforeDelivery(t *testing.T) {
	journal := &recordingJournal{}
	delivery := &recordingDelivery{}
	sink := NewDurableSink(journal)
	sink.AddDelivery(delivery)

	committed, err := sink.Commit(Event{Type: "turn.started"})
	if err != nil {
		t.Fatal(err)
	}
	if committed.ID == "" {
		t.Fatal("committed event ID is empty")
	}
	if committed.Timestamp.IsZero() {
		t.Fatal("committed event timestamp is zero")
	}
	if len(journal.events) != 1 {
		t.Fatalf("journal events = %d, want 1", len(journal.events))
	}
	if len(delivery.events) != 1 {
		t.Fatalf("delivery events = %d, want 1", len(delivery.events))
	}
	if !reflect.DeepEqual(journal.events[0], committed) {
		t.Fatalf("journal event = %+v, want committed %+v", journal.events[0], committed)
	}
	if !reflect.DeepEqual(delivery.events[0], committed) {
		t.Fatalf("delivery event = %+v, want committed %+v", delivery.events[0], committed)
	}
}

func TestDurableSink_DoesNotDeliverWhenJournalFails(t *testing.T) {
	journalErr := errors.New("disk full")
	journal := &recordingJournal{err: journalErr}
	delivery := &recordingDelivery{}
	sink := NewDurableSink(journal)
	sink.AddDelivery(delivery)

	_, err := sink.Commit(Event{Type: "turn.started"})
	if !errors.Is(err, journalErr) {
		t.Fatalf("err = %v, want %v", err, journalErr)
	}
	if len(delivery.events) != 0 {
		t.Fatalf("delivery events = %+v, want none", delivery.events)
	}
}

func TestDurableSink_RequiresJournal(t *testing.T) {
	delivery := &recordingDelivery{}
	sink := NewDurableSink(nil)
	sink.AddDelivery(delivery)

	_, err := sink.Commit(Event{Type: "turn.started"})
	if !errors.Is(err, ErrDurableJournalMissing) {
		t.Fatalf("err = %v, want ErrDurableJournalMissing", err)
	}
	if len(delivery.events) != 0 {
		t.Fatalf("delivery events = %+v, want none", delivery.events)
	}
}

func TestDurableSink_PreservesDeliveryOrderFromJournalOrder(t *testing.T) {
	journal := &recordingJournal{}
	delivery := &recordingDelivery{}
	sink := NewDurableSink(journal)
	sink.AddDelivery(delivery)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := sink.Commit(Event{Type: "tool.output_delta"}); err != nil {
				t.Errorf("commit: %v", err)
			}
		}()
	}
	wg.Wait()

	if len(journal.events) != 50 {
		t.Fatalf("journal events = %d, want 50", len(journal.events))
	}
	if len(delivery.events) != len(journal.events) {
		t.Fatalf("delivery events = %d, want %d", len(delivery.events), len(journal.events))
	}
	for i := range journal.events {
		if journal.events[i].ID != delivery.events[i].ID {
			t.Fatalf("delivery order diverged at %d: journal=%s delivery=%s", i, journal.events[i].ID, delivery.events[i].ID)
		}
	}
}

func TestDurableSink_UnsubscribeAndClose(t *testing.T) {
	journal := &recordingJournal{}
	delivery := &recordingDelivery{}
	sink := NewDurableSink(journal)
	unsubscribe := sink.AddDelivery(delivery)

	if _, err := sink.Commit(Event{Type: "one"}); err != nil {
		t.Fatal(err)
	}
	unsubscribe()
	if _, err := sink.Commit(Event{Type: "two"}); err != nil {
		t.Fatal(err)
	}
	if len(delivery.events) != 1 || delivery.events[0].Type != "one" {
		t.Fatalf("delivery events = %+v, want only first event", delivery.events)
	}

	sink.Close()
	if _, err := sink.Commit(Event{Type: "three"}); !errors.Is(err, ErrDurableSinkClosed) {
		t.Fatalf("err = %v, want ErrDurableSinkClosed", err)
	}
}

func TestDurableSink_SetJournal(t *testing.T) {
	first := &recordingJournal{}
	second := &recordingJournal{}
	sink := NewDurableSink(first)

	if _, err := sink.Commit(Event{Type: "one"}); err != nil {
		t.Fatal(err)
	}
	sink.SetJournal(second)
	if _, err := sink.Commit(Event{Type: "two"}); err != nil {
		t.Fatal(err)
	}
	if len(first.events) != 1 || first.events[0].Type != "one" {
		t.Fatalf("first journal = %+v", first.events)
	}
	if len(second.events) != 1 || second.events[0].Type != "two" {
		t.Fatalf("second journal = %+v", second.events)
	}
}

type recordingJournal struct {
	err    error
	events []Event
}

func (j *recordingJournal) AppendEvent(e Event) error {
	if j.err != nil {
		return j.err
	}
	j.events = append(j.events, e)
	return nil
}

type recordingDelivery struct {
	events []Event
}

func (d *recordingDelivery) Publish(e Event) {
	d.events = append(d.events, e)
}
