package llm

import (
	"sync"
	"time"
)

var modelHealthCooldowns = [...]time.Duration{
	30 * time.Second,
	time.Minute,
	2 * time.Minute,
	5 * time.Minute,
}

type ModelHealthOptions struct {
	Now func() time.Time
}

type ModelHealthOutcome uint8

const (
	ModelHealthSuccess ModelHealthOutcome = iota + 1
	ModelHealthEligibleFailure
	ModelHealthNeutral
)

type ModelAttemptTicket struct {
	Ref   string
	Probe bool

	generation uint64
	probeToken uint64
}

type ModelHealthSkip struct {
	Ref               string
	Reason            string
	CooldownRemaining time.Duration
}

type ModelSelection struct {
	Index   int
	Ticket  ModelAttemptTicket
	Skipped []ModelHealthSkip
}

type ModelHealthTransition struct {
	Applied       bool
	Stale         bool
	Cooldown      time.Duration
	CooldownUntil time.Time
}

type modelHealthState struct {
	generation    uint64
	failures      int
	cooldownUntil time.Time
	lastReason    string
	probeInFlight bool
	probeToken    uint64
}

type ModelHealth struct {
	mu        sync.Mutex
	now       func() time.Time
	states    map[string]*modelHealthState
	nextToken uint64
}

func NewModelHealth(opts ModelHealthOptions) *ModelHealth {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &ModelHealth{
		now:    now,
		states: map[string]*modelHealthState{},
	}
}

func (h *ModelHealth) Acquire(chain []string, attempted map[string]struct{}) (ModelSelection, bool) {
	if h == nil {
		return ModelSelection{}, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	now := h.now()
	selection := ModelSelection{Index: -1}
	for index, ref := range chain {
		if ref == "" {
			continue
		}
		if _, seen := attempted[ref]; seen {
			continue
		}
		state := h.states[ref]
		if state == nil || state.failures == 0 {
			generation := uint64(0)
			if state != nil {
				generation = state.generation
			}
			selection.Index = index
			selection.Ticket = ModelAttemptTicket{Ref: ref, generation: generation}
			return selection, true
		}
		if now.Before(state.cooldownUntil) {
			selection.Skipped = append(selection.Skipped, ModelHealthSkip{
				Ref:               ref,
				Reason:            state.lastReason,
				CooldownRemaining: state.cooldownUntil.Sub(now),
			})
			continue
		}
		if state.probeInFlight {
			selection.Skipped = append(selection.Skipped, ModelHealthSkip{
				Ref:    ref,
				Reason: "probe_in_flight",
			})
			continue
		}
		h.nextToken++
		state.probeInFlight = true
		state.probeToken = h.nextToken
		selection.Index = index
		selection.Ticket = ModelAttemptTicket{
			Ref:        ref,
			Probe:      true,
			generation: state.generation,
			probeToken: state.probeToken,
		}
		return selection, true
	}
	return selection, false
}

func (h *ModelHealth) Complete(ticket ModelAttemptTicket, outcome ModelHealthOutcome, reason string) ModelHealthTransition {
	if h == nil || ticket.Ref == "" {
		return ModelHealthTransition{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	state := h.states[ticket.Ref]
	if state == nil {
		state = &modelHealthState{}
		h.states[ticket.Ref] = state
	}
	if state.generation != ticket.generation ||
		(ticket.Probe && (!state.probeInFlight || state.probeToken != ticket.probeToken)) {
		return ModelHealthTransition{Stale: true}
	}

	switch outcome {
	case ModelHealthNeutral:
		if ticket.Probe {
			state.probeInFlight = false
			state.probeToken = 0
		}
		return ModelHealthTransition{Applied: true}
	case ModelHealthSuccess:
		// Completing any successful attempt advances the generation so a
		// slower concurrent failure from the same generation cannot reopen a
		// circuit after the newer success.
		state.generation++
		state.failures = 0
		state.cooldownUntil = time.Time{}
		state.lastReason = ""
		state.probeInFlight = false
		state.probeToken = 0
		return ModelHealthTransition{Applied: true}
	case ModelHealthEligibleFailure:
		state.generation++
		state.failures++
		cooldown := modelHealthCooldowns[min(state.failures-1, len(modelHealthCooldowns)-1)]
		state.cooldownUntil = h.now().Add(cooldown)
		state.lastReason = reason
		state.probeInFlight = false
		state.probeToken = 0
		return ModelHealthTransition{
			Applied:       true,
			Cooldown:      cooldown,
			CooldownUntil: state.cooldownUntil,
		}
	default:
		return ModelHealthTransition{}
	}
}
