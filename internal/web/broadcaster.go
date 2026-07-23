package web

import (
	"sync"
	"time"
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

// broadcaster fans an event stream out to N subscribers. A slow
// subscriber is dropped instead of stalling everyone else.
type broadcaster struct {
	mu     sync.Mutex
	cond   *sync.Cond
	subs   map[*subscriber]struct{}
	queue  []BrowserEvent
	done   chan struct{}
	closed bool
}

func newBroadcaster() *broadcaster {
	b := &broadcaster{
		subs: map[*subscriber]struct{}{},
		done: make(chan struct{}),
	}
	b.cond = sync.NewCond(&b.mu)
	go b.run()
	return b
}

func (b *broadcaster) subscribe() *subscriber {
	s := &subscriber{
		ch:     make(chan BrowserEvent, broadcasterBufferSize),
		done:   make(chan struct{}),
		parent: b,
		live:   true,
	}
	b.mu.Lock()
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
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.queue = append(b.queue, event)
	b.cond.Signal()
}

func (b *broadcaster) run() {
	defer close(b.done)
	for {
		b.mu.Lock()
		for len(b.queue) == 0 && !b.closed {
			b.cond.Wait()
		}
		if b.closed {
			b.mu.Unlock()
			return
		}
		event := b.queue[0]
		b.queue[0] = BrowserEvent{}
		b.queue = b.queue[1:]
		if len(b.queue) == 0 {
			b.queue = nil
		}
		b.mu.Unlock()
		b.publish(event)
	}
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
		if !b.deliver(s, e) {
			b.unsubscribe(s)
		}
	}
}

// deliver tries to push e to s.ch with a short timeout. Returns false
// if the subscriber is too slow.
func (b *broadcaster) deliver(s *subscriber, e BrowserEvent) bool {
	select {
	case s.ch <- e:
		return true
	case <-s.done:
		return false
	default:
	}
	t := time.NewTimer(slowClientTimeout)
	defer t.Stop()
	select {
	case s.ch <- e:
		return true
	case <-s.done:
		return false
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
	b.queue = nil
	subs := b.subs
	b.subs = map[*subscriber]struct{}{}
	b.cond.Signal()
	b.mu.Unlock()
	for s := range subs {
		s.drop()
	}
	<-b.done
}
