package web

import (
	"sync"
)

// broadcasterBufferSize bounds how far behind a single SSE client can
// fall before we drop them. 64 events is enough for a typical turn
// without burdening memory.
const broadcasterBufferSize = 64

// subscriber is one connected SSE consumer.
type subscriber struct {
	ch     chan BrowserEvent
	done   chan struct{}
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

func (s *subscriber) drop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.live {
		return
	}
	s.live = false
	close(s.done)
}

func (s *subscriber) deliver(event BrowserEvent) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.live {
		return false
	}
	select {
	case s.ch <- event:
		return true
	default:
		return false
	}
}

// broadcaster fans an event stream out to N subscribers. A slow
// subscriber is dropped instead of stalling everyone else.
type broadcaster struct {
	mu sync.Mutex

	subs   map[*subscriber]struct{}
	closed bool
}

func newBroadcaster() *broadcaster {
	return &broadcaster{
		subs: map[*subscriber]struct{}{},
	}
}

func (b *broadcaster) subscribe() *subscriber {
	s := &subscriber{
		ch:     make(chan BrowserEvent, broadcasterBufferSize),
		done:   make(chan struct{}),
		parent: b,
		live:   true,
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		s.drop()
		return s
	}
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	return s
}

// unsubscribe removes s from the broadcaster. The data channel is
// intentionally never closed, so publish goroutines cannot panic on a
// send-to-closed race. Consumers observe s.done or their request context.
func (b *broadcaster) unsubscribe(s *subscriber) {
	b.mu.Lock()
	delete(b.subs, s)
	b.mu.Unlock()
	s.drop()
}

func (b *broadcaster) enqueue(event BrowserEvent) {
	b.publish(event)
}

func (b *broadcaster) publish(e BrowserEvent) {
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
		if !s.deliver(e) {
			b.unsubscribe(s)
		}
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
		s.drop()
	}
}
