package events

import (
	"errors"
	"sync"
)

var ErrDurableSinkClosed = errors.New("events: durable sink closed")
var ErrDurableJournalMissing = errors.New("events: durable journal missing")

type Journal interface {
	AppendEvent(Event) error
}

type Delivery interface {
	Publish(Event)
}

type DeliveryFunc func(Event)

func (fn DeliveryFunc) Publish(e Event) {
	if fn != nil {
		fn(e)
	}
}

type DurableSink struct {
	commitMu sync.Mutex
	mu       sync.Mutex
	journal  Journal

	deliveries map[uint64]Delivery
	nextID     uint64
	closed     bool
}

func NewDurableSink(journal Journal) *DurableSink {
	return &DurableSink{
		journal:    journal,
		deliveries: map[uint64]Delivery{},
	}
}

func (s *DurableSink) SetJournal(journal Journal) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.journal = journal
}

func (s *DurableSink) AddDelivery(delivery Delivery) func() {
	if s == nil || delivery == nil {
		return func() {}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return func() {}
	}
	s.nextID++
	id := s.nextID
	s.deliveries[id] = delivery
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.deliveries, id)
	}
}

func (s *DurableSink) Handle(e Event) {
	if s == nil {
		return
	}
	_, _ = s.Commit(e)
}

func (s *DurableSink) Commit(e Event) (Event, error) {
	if s == nil {
		return Event{}, ErrDurableSinkClosed
	}
	e = Normalize(e)

	s.commitMu.Lock()
	defer s.commitMu.Unlock()

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return Event{}, ErrDurableSinkClosed
	}
	journal := s.journal
	if journal == nil {
		s.mu.Unlock()
		return Event{}, ErrDurableJournalMissing
	}
	s.mu.Unlock()

	if err := journal.AppendEvent(e); err != nil {
		return Event{}, err
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return Event{}, ErrDurableSinkClosed
	}
	deliveries := make([]Delivery, 0, len(s.deliveries))
	for _, delivery := range s.deliveries {
		deliveries = append(deliveries, delivery)
	}
	s.mu.Unlock()

	for _, delivery := range deliveries {
		delivery.Publish(e)
	}
	return e, nil
}

func (s *DurableSink) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.deliveries = map[uint64]Delivery{}
}
