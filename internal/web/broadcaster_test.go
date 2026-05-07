package web

import (
	"sync"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/events"
)

func TestBroadcaster_FansEventsToAllSubscribers(t *testing.T) {
	b := newBroadcaster()
	defer b.close()
	a := b.subscribe()
	defer a.unsubscribe()
	c := b.subscribe()
	defer c.unsubscribe()

	b.publish(events.Event{Type: "turn.started"})
	b.publish(events.Event{Type: "turn.completed"})

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

func TestBroadcaster_SlowSubscriberIsDropped(t *testing.T) {
	b := newBroadcaster()
	defer b.close()
	slow := b.subscribe()
	// never read from slow.ch
	for i := 0; i < broadcasterBufferSize+10; i++ {
		b.publish(events.Event{Type: "x"})
	}
	// Give the broadcaster time to drop the slow subscriber.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if !slow.isLive() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("slow subscriber was not dropped after overflow")
}

func TestBroadcaster_UnsubscribeStopsDelivery(t *testing.T) {
	b := newBroadcaster()
	defer b.close()
	s := b.subscribe()
	s.unsubscribe()
	b.publish(events.Event{Type: "after-unsub"})
	select {
	case e := <-s.ch:
		t.Errorf("received after unsubscribe: %+v", e)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestBroadcaster_CloseUnblocksAllSubscribers(t *testing.T) {
	b := newBroadcaster()
	s := b.subscribe()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, ok := <-s.ch
		if ok {
			t.Errorf("expected channel closed, got value")
		}
	}()
	b.close()
	wg.Wait()
}
