// Package statusstream distributes replaceable snapshots with optional
// cursor-based replay.
package statusstream

import (
	"context"
	"strings"
	"sync"
)

const subscriberBuffer = 16

// Options defines value semantics and replay retention for a Store.
type Options[T any] struct {
	Clone        func(T) T
	Cursor       func(T) string
	Equal        func(T, T) bool
	HistoryLimit int
}

// OpenOptions controls where a stream resumes and whether it follows updates.
type OpenOptions struct {
	After  string
	Follow bool
}

type entry[T any] struct {
	value    T
	cursor   string
	sequence uint64
}

// Store owns one current snapshot, optional replay history, and live
// subscribers. Values returned to consumers are cloned.
type Store[T any] struct {
	publishMu sync.Mutex
	mu        sync.RWMutex

	clone        func(T) T
	cursor       func(T) string
	equal        func(T, T) bool
	historyLimit int

	current     entry[T]
	history     []entry[T]
	subscribers map[uint64]chan entry[T]
	nextID      uint64
	sequence    uint64
}

// Stream presents replay and live updates as one sequential source. It has one
// consumer; Close may be called concurrently to wake a blocked Next.
type Stream[T any] struct {
	replay  []T
	updates <-chan entry[T]
	clone   func(T) T
	follow  bool
	index   int
	done    chan struct{}
	close   func()
	once    sync.Once
}

// New creates a snapshot store. A zero HistoryLimit keeps only the current
// value and never attempts cursor replay.
func New[T any](initial T, options Options[T]) *Store[T] {
	if options.Clone == nil {
		panic("statusstream: Clone is required")
	}
	if options.Cursor == nil {
		panic("statusstream: Cursor is required")
	}
	if options.Equal == nil {
		panic("statusstream: Equal is required")
	}
	if options.HistoryLimit < 0 {
		panic("statusstream: HistoryLimit cannot be negative")
	}

	current := options.Clone(initial)
	initialEntry := entry[T]{
		value:    current,
		cursor:   options.Cursor(current),
		sequence: 1,
	}
	store := &Store[T]{
		clone:        options.Clone,
		cursor:       options.Cursor,
		equal:        options.Equal,
		historyLimit: options.HistoryLimit,
		current:      initialEntry,
		subscribers:  map[uint64]chan entry[T]{},
		sequence:     1,
	}
	if options.HistoryLimit > 0 {
		store.history = []entry[T]{initialEntry}
	}
	return store
}

// Snapshot returns an isolated copy of the current value.
func (s *Store[T]) Snapshot() T {
	if s == nil {
		var zero T
		return zero
	}
	s.mu.RLock()
	current := s.current.value
	s.mu.RUnlock()
	return s.clone(current)
}

// Values returns isolated copies of the current value and recorded history.
func (s *Store[T]) Values() (T, []T) {
	if s == nil {
		var zero T
		return zero, nil
	}
	s.mu.RLock()
	current := s.current.value
	history := make([]T, len(s.history))
	for index := range s.history {
		history[index] = s.history[index].value
	}
	s.mu.RUnlock()

	current = s.clone(current)
	for index := range history {
		history[index] = s.clone(history[index])
	}
	return current, history
}

// Publish replaces the current value, optionally records it for cursor replay,
// and offers it to every live subscriber.
func (s *Store[T]) Publish(next T, record bool) {
	if s == nil {
		return
	}
	s.publishMu.Lock()
	defer s.publishMu.Unlock()

	stored := s.clone(next)
	cursor := s.cursor(stored)

	s.mu.Lock()
	s.sequence++
	nextEntry := entry[T]{
		value:    stored,
		cursor:   cursor,
		sequence: s.sequence,
	}
	s.current = nextEntry
	if record && s.historyLimit > 0 {
		s.history = append(s.history, nextEntry)
		if len(s.history) > s.historyLimit {
			s.history = append([]entry[T](nil), s.history[len(s.history)-s.historyLimit:]...)
		}
	}
	subscribers := make([]chan entry[T], 0, len(s.subscribers))
	for _, subscriber := range s.subscribers {
		subscribers = append(subscribers, subscriber)
	}
	s.mu.Unlock()

	for _, subscriber := range subscribers {
		publishLatest(subscriber, nextEntry)
	}
}

// Replace swaps the complete replay state while preserving existing
// subscribers. It is used when a long-lived projection changes Sessions.
func (s *Store[T]) Replace(current T, history []T) {
	if s == nil {
		return
	}
	s.publishMu.Lock()
	defer s.publishMu.Unlock()

	entries := make([]entry[T], 0, len(history))
	var sequence uint64
	for _, value := range history {
		sequence++
		cloned := s.clone(value)
		entries = append(entries, entry[T]{
			value:    cloned,
			cursor:   s.cursor(cloned),
			sequence: sequence,
		})
	}
	if s.historyLimit == 0 {
		entries = nil
	} else if len(entries) > s.historyLimit {
		entries = append([]entry[T](nil), entries[len(entries)-s.historyLimit:]...)
	}

	clonedCurrent := s.clone(current)
	var currentEntry entry[T]
	if len(entries) > 0 && s.equal(entries[len(entries)-1].value, clonedCurrent) {
		currentEntry = entries[len(entries)-1]
	} else {
		sequence++
		currentEntry = entry[T]{
			value:    clonedCurrent,
			cursor:   s.cursor(clonedCurrent),
			sequence: sequence,
		}
	}
	if sequence == 0 {
		sequence = currentEntry.sequence
	}

	s.mu.Lock()
	s.current = currentEntry
	s.history = entries
	s.sequence = sequence
	subscribers := make([]chan entry[T], 0, len(s.subscribers))
	for _, subscriber := range s.subscribers {
		subscribers = append(subscribers, subscriber)
	}
	s.mu.Unlock()

	for _, subscriber := range subscribers {
		publishLatest(subscriber, currentEntry)
	}
}

// Open creates a single-consumer stream. Replay and subscriber registration
// are captured under one lock so a publication cannot fall between them.
func (s *Store[T]) Open(options OpenOptions) *Stream[T] {
	if s == nil {
		return &Stream[T]{done: make(chan struct{})}
	}

	after := strings.TrimSpace(options.After)
	s.mu.Lock()
	replayEntries := s.replayAfterLocked(after)
	var (
		id      uint64
		updates chan entry[T]
	)
	if options.Follow {
		s.nextID++
		id = s.nextID
		updates = make(chan entry[T], subscriberBuffer)
		s.subscribers[id] = updates
	}
	s.mu.Unlock()

	replay := make([]T, len(replayEntries))
	for index := range replayEntries {
		replay[index] = s.clone(replayEntries[index].value)
	}
	stream := &Stream[T]{
		replay:  replay,
		updates: updates,
		clone:   s.clone,
		follow:  options.Follow,
		done:    make(chan struct{}),
	}
	stream.close = func() {
		if !options.Follow {
			return
		}
		s.mu.Lock()
		delete(s.subscribers, id)
		s.mu.Unlock()
	}
	return stream
}

func (s *Store[T]) replayAfterLocked(after string) []entry[T] {
	if after == "" || s.historyLimit == 0 {
		return []entry[T]{s.current}
	}
	index := -1
	for candidate := len(s.history) - 1; candidate >= 0; candidate-- {
		if s.history[candidate].cursor == after {
			index = candidate
			break
		}
	}
	if index < 0 {
		return []entry[T]{s.current}
	}
	replay := append([]entry[T](nil), s.history[index+1:]...)
	if len(replay) == 0 || replay[len(replay)-1].sequence != s.current.sequence {
		replay = append(replay, s.current)
	}
	return replay
}

// Next returns the next replay or live value. It closes the stream when ctx is
// canceled or when a non-following replay is exhausted.
func (s *Stream[T]) Next(ctx context.Context) (T, bool) {
	var zero T
	if s == nil {
		return zero, false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-s.done:
		return zero, false
	case <-ctx.Done():
		s.Close()
		return zero, false
	default:
	}
	if s.index < len(s.replay) {
		value := s.replay[s.index]
		s.index++
		return value, true
	}
	if !s.follow {
		s.Close()
		return zero, false
	}
	select {
	case update := <-s.updates:
		return s.clone(update.value), true
	case <-ctx.Done():
		s.Close()
		return zero, false
	case <-s.done:
		return zero, false
	}
}

// Close releases the live subscription and wakes a blocked Next.
func (s *Stream[T]) Close() {
	if s == nil {
		return
	}
	s.once.Do(func() {
		close(s.done)
		if s.close != nil {
			s.close()
		}
	})
}

func publishLatest[T any](channel chan entry[T], update entry[T]) {
	select {
	case channel <- update:
		return
	default:
	}
	select {
	case <-channel:
	default:
	}
	select {
	case channel <- update:
	default:
	}
}
