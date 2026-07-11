package events

import (
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestDurableSink_CommitsBeforeDelivery(t *testing.T) {
	journal := &recordingJournal{}
	delivery := &recordingDelivery{}
	sink := NewDurableSink(journal)
	t.Cleanup(sink.Close)
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
	if !reflect.DeepEqual(journal.events[0], committed) {
		t.Fatalf("journal event = %+v, want committed %+v", journal.events[0], committed)
	}
	delivery.waitLen(t, 1)
	if !reflect.DeepEqual(delivery.snapshot()[0], committed) {
		t.Fatalf("delivery event = %+v, want committed %+v", delivery.snapshot()[0], committed)
	}
}

func TestDurableSink_DoesNotDeliverWhenJournalFails(t *testing.T) {
	journalErr := errors.New("disk full")
	journal := &recordingJournal{err: journalErr}
	delivery := &recordingDelivery{}
	sink := NewDurableSink(journal)
	t.Cleanup(sink.Close)
	sink.AddDelivery(delivery)

	_, err := sink.Commit(Event{Type: "turn.started"})
	if !errors.Is(err, journalErr) {
		t.Fatalf("err = %v, want %v", err, journalErr)
	}
	if got := delivery.snapshot(); len(got) != 0 {
		t.Fatalf("delivery events = %+v, want none", got)
	}
}

func TestDurableSink_RequiresJournal(t *testing.T) {
	delivery := &recordingDelivery{}
	sink := NewDurableSink(nil)
	t.Cleanup(sink.Close)
	sink.AddDelivery(delivery)

	_, err := sink.Commit(Event{Type: "turn.started"})
	if !errors.Is(err, ErrDurableJournalMissing) {
		t.Fatalf("err = %v, want ErrDurableJournalMissing", err)
	}
	if got := delivery.snapshot(); len(got) != 0 {
		t.Fatalf("delivery events = %+v, want none", got)
	}
}

func TestDurableSink_PreservesDeliveryOrderFromJournalOrder(t *testing.T) {
	journal := &recordingJournal{}
	delivery := &recordingDelivery{}
	sink := NewDurableSink(journal)
	t.Cleanup(sink.Close)
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
	delivery.waitLen(t, len(journal.events))
	delivered := delivery.snapshot()
	if len(delivered) != len(journal.events) {
		t.Fatalf("delivery events = %d, want %d", len(delivered), len(journal.events))
	}
	for i := range journal.events {
		if journal.events[i].ID != delivered[i].ID {
			t.Fatalf("delivery order diverged at %d: journal=%s delivery=%s", i, journal.events[i].ID, delivered[i].ID)
		}
	}
}

func TestDurableSink_DoesNotBlockCommitsOrStateWhileDelivering(t *testing.T) {
	journal := &recordingJournal{}
	delivery := &blockingDelivery{started: make(chan struct{}), release: make(chan struct{})}
	sink := NewDurableSink(journal)
	t.Cleanup(sink.Close)
	sink.AddDelivery(delivery)

	errCh := make(chan error, 1)
	go func() {
		_, err := sink.Commit(Event{Type: "turn.started"})
		errCh <- err
	}()

	select {
	case <-delivery.started:
	case <-time.After(time.Second):
		t.Fatal("delivery did not start")
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(100 * time.Millisecond):
		close(delivery.release)
		t.Fatal("Commit blocked behind a slow delivery")
	}

	manyDone := make(chan error, 1)
	go func() {
		for i := 0; i < 1500; i++ {
			if _, err := sink.Commit(Event{Type: "tool.output_delta"}); err != nil {
				manyDone <- err
				return
			}
		}
		manyDone <- nil
	}()
	select {
	case err := <-manyDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		close(delivery.release)
		t.Fatal("later commits blocked behind queued slow delivery")
	}

	added := make(chan struct{})
	go func() {
		sink.AddDelivery(DeliveryFunc(func(Event) {}))
		close(added)
	}()

	select {
	case <-added:
	case <-time.After(100 * time.Millisecond):
		close(delivery.release)
		t.Fatal("AddDelivery blocked behind a slow delivery")
	}

	close(delivery.release)
}

func TestDurableSink_UnsubscribeAndClose(t *testing.T) {
	journal := &recordingJournal{}
	delivery := &recordingDelivery{}
	sink := NewDurableSink(journal)
	t.Cleanup(sink.Close)
	unsubscribe := sink.AddDelivery(delivery)

	if _, err := sink.Commit(Event{Type: "one"}); err != nil {
		t.Fatal(err)
	}
	delivery.waitLen(t, 1)
	unsubscribe()
	if _, err := sink.Commit(Event{Type: "two"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	delivered := delivery.snapshot()
	if len(delivered) != 1 || delivered[0].Type != "one" {
		t.Fatalf("delivery events = %+v, want only first event", delivered)
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
	t.Cleanup(sink.Close)

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
	mu     sync.Mutex
	events []Event
}

func (d *recordingDelivery) Publish(e Event) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.events = append(d.events, e)
}

func (d *recordingDelivery) snapshot() []Event {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]Event(nil), d.events...)
}

func (d *recordingDelivery) waitLen(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if got := len(d.snapshot()); got >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("delivery len = %d, want at least %d", len(d.snapshot()), want)
}

type blockingDelivery struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (d *blockingDelivery) Publish(Event) {
	d.once.Do(func() { close(d.started) })
	<-d.release
}
