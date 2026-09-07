package web

import (
	"sync"
	"testing"
	"time"
)

func TestBroadcaster_FansEventsToAllSubscribers(t *testing.T) {
	b := newBroadcaster()
	defer b.close()
	a := b.subscribe()
	defer a.unsubscribe()
	c := b.subscribe()
	defer c.unsubscribe()

	b.publish(BrowserEvent{Type: "turn.started"})
	b.publish(BrowserEvent{Type: "turn.completed"})

	for _, ch := range []*subscriber{a, c} {
		got := []string{}
		for i := 0; i < 2; i++ {
			select {
			case e := <-ch.ch:
				got = append(got, e.Type)
			case <-time.After(time.Second):
				t.Fatalf("timeout on subscriber after %d events", i)
			}
		}
		if got[0] != "turn.started" || got[1] != "turn.completed" {
			t.Errorf("got %v", got)
		}
	}
}

func TestBroadcaster_AssignsOrderedSequence(t *testing.T) {
	b := newBroadcaster()
	defer b.close()
	s := b.subscribe()
	defer s.unsubscribe()

	b.publish(BrowserEvent{Type: "turn.started"})
	b.publish(BrowserEvent{Type: "turn.completed"})

	first := <-s.ch
	second := <-s.ch
	if first.sequence != 1 || second.sequence != 2 {
		t.Fatalf("event sequences = %d, %d; want 1, 2", first.sequence, second.sequence)
	}
}

func TestBroadcaster_ReplayBoundaryIncludesOnlyQueuedReplayEvents(t *testing.T) {
	b := newBroadcaster()
	defer b.close()

	b.publish(BrowserEvent{ID: "before-subscribe", Type: "turn.started"})
	s := b.subscribe()
	defer s.unsubscribe()
	b.publish(BrowserEvent{
		ID:        "queued-transient",
		Type:      "llm.output_delta",
		transient: true,
	})
	b.publish(BrowserEvent{ID: "queued-replay", Type: "turn.completed"})
	b.publish(BrowserEvent{
		ID:        "fresh-transient",
		Type:      "llm.output_delta",
		transient: true,
	})

	boundary := b.replayBoundary(s, []BrowserEvent{
		{ID: "before-subscribe", Type: "turn.started"},
		{ID: "queued-replay", Type: "turn.completed"},
	})
	if boundary != 3 {
		t.Fatalf("replay boundary = %d; want queued replay sequence 3", boundary)
	}

	queuedTransient := <-s.ch
	queuedReplay := <-s.ch
	freshTransient := <-s.ch
	deduper := newBrowserReplayDeduplicator(
		[]BrowserEvent{{ID: "queued-replay", Type: "turn.completed"}},
		boundary,
	)
	if !deduper.skip(queuedTransient) {
		t.Fatal("transient before queued replay duplicate was delivered")
	}
	if !deduper.skip(queuedReplay) {
		t.Fatal("queued replay duplicate was delivered")
	}
	if deduper.skip(freshTransient) {
		t.Fatal("transient outside the replay snapshot was skipped")
	}
}

func TestBroadcaster_SlowSubscriberIsDropped(t *testing.T) {
	b := newBroadcaster()
	defer b.close()
	slow := b.subscribe()
	// never read from slow.ch
	for i := 0; i < broadcasterBufferSize+10; i++ {
		b.publish(BrowserEvent{Type: "x"})
	}
	if slow.isLive() {
		t.Fatal("slow subscriber was not dropped after overflow")
	}
}

func TestBroadcaster_UnsubscribeStopsDelivery(t *testing.T) {
	b := newBroadcaster()
	defer b.close()
	s := b.subscribe()
	s.unsubscribe()
	b.publish(BrowserEvent{Type: "after-unsub"})
	select {
	case e := <-s.ch:
		t.Errorf("received after unsubscribe: %+v", e)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestBroadcaster_CloseDropsAllSubscribers(t *testing.T) {
	b := newBroadcaster()
	s := b.subscribe()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		select {
		case <-s.done:
		case <-time.After(time.Second):
			t.Errorf("expected subscriber done signal")
		}
	}()
	b.close()
	wg.Wait()
	select {
	case _, ok := <-s.ch:
		if !ok {
			t.Fatal("event channel was closed; concurrent publishers can panic on send")
		}
	default:
	}
}

func TestBroadcaster_CloseDoesNotPanicDuringSlowDelivery(t *testing.T) {
	b := newBroadcaster()
	s := b.subscribe()
	for i := 0; i < broadcasterBufferSize; i++ {
		s.ch <- BrowserEvent{Type: "buffered"}
	}
	panicCh := make(chan any, 1)
	go func() {
		defer func() { panicCh <- recover() }()
		b.publish(BrowserEvent{Type: "after-full"})
	}()
	time.Sleep(10 * time.Millisecond)
	b.close()
	select {
	case p := <-panicCh:
		if p != nil {
			t.Fatalf("publish panicked during close: %v", p)
		}
	case <-time.After(time.Second):
		t.Fatal("publish did not return after close")
	}
}

func TestBroadcaster_SubscribeAfterCloseIsAlreadyDropped(t *testing.T) {
	b := newBroadcaster()
	b.close()

	s := b.subscribe()
	if s.isLive() {
		t.Fatal("subscriber created after close is live")
	}
	select {
	case <-s.done:
	default:
		t.Fatal("subscriber created after close was not notified")
	}
}
