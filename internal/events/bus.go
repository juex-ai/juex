// Package events implements an in-process pub/sub bus for runtime events.
//
// Events are emitted synchronously to all matching subscribers. Subscribers
// register interest via glob patterns (path.Match semantics), e.g. "tool.*".
package events

import (
	"crypto/rand"
	"encoding/hex"
	"path"
	"sync"
	"time"
)

type Event struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	Timestamp time.Time `json:"ts"`
	TurnID    string    `json:"turn_id,omitempty"`
	Payload   any       `json:"payload,omitempty"`
}

type Handler func(Event)

type subscription struct {
	id      uint64
	pattern string
	fn      Handler
}

type Bus struct {
	mu     sync.RWMutex
	nextID uint64
	subs   []subscription
}

func NewBus() *Bus { return &Bus{} }

// Subscribe registers fn for events whose Type matches pattern (path.Match
// semantics). A pattern of "*" matches everything.
func (b *Bus) Subscribe(pattern string, fn Handler) func() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextID++
	id := b.nextID
	b.subs = append(b.subs, subscription{id: id, pattern: pattern, fn: fn})
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		for i := range b.subs {
			if b.subs[i].id == id {
				b.subs = append(b.subs[:i], b.subs[i+1:]...)
				return
			}
		}
	}
}

// Normalize fills the stable default fields expected on persisted and emitted events.
func Normalize(e Event) Event {
	if e.ID == "" {
		e.ID = newID()
	}
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}
	return e
}

// Emit dispatches e synchronously to all matching subscribers.
// If e.ID is empty, a random one is generated.
// If e.Timestamp is zero, time.Now().UTC() is used.
func (b *Bus) Emit(e Event) {
	e = Normalize(e)

	b.mu.RLock()
	matched := make([]Handler, 0, len(b.subs))
	for _, s := range b.subs {
		if match(s.pattern, e.Type) {
			matched = append(matched, s.fn)
		}
	}
	b.mu.RUnlock()

	for _, fn := range matched {
		fn(e)
	}
}

func match(pattern, typ string) bool {
	if pattern == "*" || pattern == typ {
		return true
	}
	ok, err := path.Match(pattern, typ)
	if err != nil {
		return false
	}
	return ok
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
