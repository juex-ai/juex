package web

import (
	"sync"
	"time"

	"github.com/juex-ai/juex/internal/events"
)

// broadcasterBufferSize bounds how far behind a single SSE client can
// fall before we drop them. 64 events is enough for a typical turn
// without burdening memory.
const broadcasterBufferSize = 64

// slowClientTimeout is the per-publish deadline for delivering an event
// to a single subscriber. If the subscriber's buffer is full and stays
// full past this deadline, the broadcaster gives up and drops them.
const slowClientTimeout = 5 * time.Second

// subscriber is one connected SSE consumer.
type subscriber struct {
	ch     chan events.Event
	parent *broadcaster
	mu     sync.Mutex
	live   bool
}

func (s *subscriber) unsubscribe() {
	s.parent.unsubscribe(s)
}

func (s *subscriber) isLive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.live
}

// broadcaster fans an event stream out to N subscribers. A slow
// subscriber is dropped instead of stalling everyone else.
type broadcaster struct {
	mu     sync.Mutex
	subs   map[*subscriber]struct{}
	closed bool
}

func newBroadcaster() *broadcaster {
	return &broadcaster{subs: map[*subscriber]struct{}{}}
}

func (b *broadcaster) Publish(e events.Event) {
	b.publish(e)
}

func (b *broadcaster) subscribe() *subscriber {
	s := &subscriber{
		ch:     make(chan events.Event, broadcasterBufferSize),
		parent: b,
		live:   true,
	}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	return s
}

// unsubscribe removes s from the broadcaster. The subscriber's channel
// is intentionally NOT closed here — only (*broadcaster).close closes
// channels, so publish goroutines never panic on send-to-closed.
// Consumers must observe s.isLive() or their request ctx.Done() to
// detect they've been dropped (e.g. by the slow-client path).
func (b *broadcaster) unsubscribe(s *subscriber) {
	b.mu.Lock()
	delete(b.subs, s)
	b.mu.Unlock()
	s.mu.Lock()
	s.live = false
	s.mu.Unlock()
}

func (b *broadcaster) publish(e events.Event) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	subs := make([]*subscriber, 0, len(b.subs))
	for s := range b.subs {
		subs = append(subs, s)
	}
	b.mu.Unlock()

	for _, s := range subs {
		if !b.deliver(s, e) {
			b.unsubscribe(s)
		}
	}
}

// deliver tries to push e to s.ch with a short timeout. Returns false
// if the subscriber is too slow.
func (b *broadcaster) deliver(s *subscriber, e events.Event) bool {
	select {
	case s.ch <- e:
		return true
	default:
	}
	t := time.NewTimer(slowClientTimeout)
	defer t.Stop()
	select {
	case s.ch <- e:
		return true
	case <-t.C:
		return false
	}
}

func (b *broadcaster) close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	subs := b.subs
	b.subs = map[*subscriber]struct{}{}
	b.mu.Unlock()
	for s := range subs {
		s.mu.Lock()
		if s.live {
			s.live = false
			close(s.ch)
		}
		s.mu.Unlock()
	}
}
