// Package events implements in-process pub/sub and durable commit helpers for
// runtime events.
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
	ID            string       `json:"id"`
	Type          string       `json:"type"`
	SchemaVersion int          `json:"schema_version,omitempty"`
	ReplayPolicy  ReplayPolicy `json:"replay_policy,omitempty"`
	Timestamp     time.Time    `json:"ts"`
	TurnID        string       `json:"turn_id,omitempty"`
	Payload       any          `json:"payload,omitempty"`
	Transient     bool         `json:"-"` // bypasses journals while remaining eligible for live delivery
	Opaque        bool         `json:"-"` // replay retained the fact but its schema is intentionally not projected
}

type Handler func(Event)

type Committer interface {
	Commit(Event) (Event, error)
}

type subscription struct {
	id      uint64
	pattern string
	fn      Handler
}

type Bus struct {
	mu        sync.RWMutex
	nextID    uint64
	subs      []subscription
	committer Committer
}

func NewBus() *Bus { return &Bus{} }

func (b *Bus) SetCommitter(committer Committer) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.committer = committer
	b.mu.Unlock()
}

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
func (b *Bus) Emit(e Event) error {
	if b == nil {
		return nil
	}
	committed, err := b.Commit(e)
	if err != nil {
		return err
	}
	b.PublishCommitted(committed)
	return nil
}

// Commit crosses the configured durable boundary without notifying live
// subscribers. Callers that need an atomic state transition can publish the
// committed fact after releasing their transition lock.
func (b *Bus) Commit(e Event) (Event, error) {
	if b == nil {
		return Normalize(e), nil
	}
	b.mu.RLock()
	committer := b.committer
	b.mu.RUnlock()
	if committer != nil {
		committed, err := committer.Commit(e)
		if err != nil {
			return Event{}, err
		}
		return committed, nil
	}
	return Normalize(e), nil
}

// PublishCommitted synchronously notifies subscribers about an event that has
// already crossed its configured commit boundary.
func (b *Bus) PublishCommitted(e Event) {
	if b == nil {
		return
	}
	b.publish(e)
}

func (b *Bus) publish(e Event) {
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
