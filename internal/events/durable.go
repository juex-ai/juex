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
	cond     *sync.Cond
	journal  Journal

	projections map[uint64]Delivery
	deliveries  map[uint64]Delivery
	nextID      uint64
	closed      bool
	queue       []deliveryBatch
}

type deliveryBatch struct {
	event      Event
	deliveries []Delivery
}

func NewDurableSink(journal Journal) *DurableSink {
	s := &DurableSink{
		journal:     journal,
		projections: map[uint64]Delivery{},
		deliveries:  map[uint64]Delivery{},
	}
	s.cond = sync.NewCond(&s.mu)
	go s.runDeliveries()
	return s
}

func (s *DurableSink) runDeliveries() {
	for {
		s.mu.Lock()
		for len(s.queue) == 0 && !s.closed {
			s.cond.Wait()
		}
		if len(s.queue) == 0 && s.closed {
			s.mu.Unlock()
			return
		}
		batch := s.queue[0]
		s.queue[0] = deliveryBatch{}
		s.queue = s.queue[1:]
		if len(s.queue) == 0 {
			s.queue = nil
		}
		s.mu.Unlock()

		for _, delivery := range batch.deliveries {
			delivery.Publish(batch.event)
		}
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

// AddProjection registers a synchronous post-commit projection. Projections
// run only after a durable event is appended successfully and before
// asynchronous live deliveries are queued.
func (s *DurableSink) AddProjection(projection Delivery) func() {
	if s == nil || projection == nil {
		return func() {}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return func() {}
	}
	s.nextID++
	id := s.nextID
	s.projections[id] = projection
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.projections, id)
	}
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
	if journal == nil && !e.Transient {
		s.mu.Unlock()
		return Event{}, ErrDurableJournalMissing
	}
	s.mu.Unlock()

	if !e.Transient {
		if err := journal.AppendEvent(e); err != nil {
			return Event{}, err
		}
	}

	s.mu.Lock()
	projections := make([]Delivery, 0, len(s.projections))
	for _, projection := range s.projections {
		projections = append(projections, projection)
	}
	deliveries := make([]Delivery, 0, len(s.deliveries))
	for _, delivery := range s.deliveries {
		deliveries = append(deliveries, delivery)
	}
	s.mu.Unlock()

	for _, projection := range projections {
		projection.Publish(e)
	}

	s.mu.Lock()
	if len(deliveries) > 0 {
		s.queue = append(s.queue, deliveryBatch{event: e, deliveries: deliveries})
		s.cond.Signal()
	}
	s.mu.Unlock()

	return e, nil
}

func (s *DurableSink) Close() {
	if s == nil {
		return
	}
	s.commitMu.Lock()
	defer s.commitMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	s.projections = map[uint64]Delivery{}
	s.deliveries = map[uint64]Delivery{}
	s.cond.Signal()
}
