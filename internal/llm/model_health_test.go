package llm

import (
	"sync"
	"testing"
	"time"
)

type modelHealthClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *modelHealthClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *modelHealthClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func TestModelHealthCooldownEscalatesCapsAndResets(t *testing.T) {
	clock := &modelHealthClock{now: time.Unix(1_000, 0)}
	health := NewModelHealth(ModelHealthOptions{Now: clock.Now})
	chain := []string{"primary", "fallback"}

	for i, want := range []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute, 5 * time.Minute} {
		selection, ok := health.Acquire(chain, nil)
		if !ok || selection.Ticket.Ref != "primary" {
			t.Fatalf("failure %d selection = %+v, %v", i, selection, ok)
		}
		transition := health.Complete(selection.Ticket, ModelHealthEligibleFailure, "timeout")
		if !transition.Applied || transition.Cooldown != want {
			t.Fatalf("failure %d transition = %+v, want cooldown %s", i, transition, want)
		}
		fallback, ok := health.Acquire(chain, nil)
		if !ok || fallback.Ticket.Ref != "fallback" || len(fallback.Skipped) != 1 {
			t.Fatalf("failure %d fallback = %+v, %v", i, fallback, ok)
		}
		clock.Advance(want)
	}

	probe, ok := health.Acquire(chain, nil)
	if !ok || !probe.Ticket.Probe || probe.Ticket.Ref != "primary" {
		t.Fatalf("probe = %+v, %v", probe, ok)
	}
	if got := health.Complete(probe.Ticket, ModelHealthSuccess, ""); !got.Applied {
		t.Fatalf("probe success = %+v", got)
	}

	health.Complete(mustAcquireModel(t, health, chain).Ticket, ModelHealthEligibleFailure, "timeout")
	clock.Advance(30 * time.Second)
	probe = mustAcquireModel(t, health, chain)
	got := health.Complete(probe.Ticket, ModelHealthEligibleFailure, "timeout")
	if got.Cooldown != time.Minute {
		t.Fatalf("cooldown after reset = %s, want 1m", got.Cooldown)
	}
}

func TestModelHealthAllowsOnlyOneHalfOpenProbeAndNeutralReleasesIt(t *testing.T) {
	clock := &modelHealthClock{now: time.Unix(2_000, 0)}
	health := NewModelHealth(ModelHealthOptions{Now: clock.Now})
	chain := []string{"primary", "fallback"}

	first := mustAcquireModel(t, health, chain)
	health.Complete(first.Ticket, ModelHealthEligibleFailure, "rate_limit")
	clock.Advance(30 * time.Second)

	probe := mustAcquireModel(t, health, chain)
	if !probe.Ticket.Probe || probe.Ticket.Ref != "primary" {
		t.Fatalf("probe = %+v", probe)
	}
	concurrent := mustAcquireModel(t, health, chain)
	if concurrent.Ticket.Ref != "fallback" || len(concurrent.Skipped) != 1 || concurrent.Skipped[0].Reason != "probe_in_flight" {
		t.Fatalf("concurrent = %+v", concurrent)
	}
	if got := health.Complete(probe.Ticket, ModelHealthNeutral, ""); !got.Applied {
		t.Fatalf("neutral = %+v", got)
	}

	retry := mustAcquireModel(t, health, chain)
	if !retry.Ticket.Probe || retry.Ticket.Ref != "primary" {
		t.Fatalf("retry probe = %+v", retry)
	}
}

func TestModelHealthRejectsStaleSuccessAndFailure(t *testing.T) {
	clock := &modelHealthClock{now: time.Unix(3_000, 0)}
	health := NewModelHealth(ModelHealthOptions{Now: clock.Now})
	chain := []string{"primary"}

	staleSuccess := mustAcquireModel(t, health, chain).Ticket
	winningFailure := mustAcquireModel(t, health, chain).Ticket
	if got := health.Complete(winningFailure, ModelHealthEligibleFailure, "timeout"); !got.Applied {
		t.Fatalf("winning failure = %+v", got)
	}
	if got := health.Complete(staleSuccess, ModelHealthSuccess, ""); !got.Stale {
		t.Fatalf("stale success = %+v", got)
	}

	clock.Advance(30 * time.Second)
	probe := mustAcquireModel(t, health, chain).Ticket
	if got := health.Complete(probe, ModelHealthEligibleFailure, "timeout"); got.Cooldown != time.Minute {
		t.Fatalf("probe failure = %+v", got)
	}
	if got := health.Complete(probe, ModelHealthEligibleFailure, "timeout"); !got.Stale {
		t.Fatalf("stale failure = %+v", got)
	}
}

func TestModelHealthRejectsConcurrentFailureAfterSuccess(t *testing.T) {
	health := NewModelHealth(ModelHealthOptions{})
	chain := []string{"primary"}

	winner := mustAcquireModel(t, health, chain).Ticket
	staleFailure := mustAcquireModel(t, health, chain).Ticket
	if got := health.Complete(winner, ModelHealthSuccess, ""); !got.Applied {
		t.Fatalf("success = %+v", got)
	}
	if got := health.Complete(staleFailure, ModelHealthEligibleFailure, "timeout"); !got.Stale {
		t.Fatalf("stale failure = %+v", got)
	}

	selection, ok := health.Acquire(chain, nil)
	if !ok || selection.Ticket.Probe || len(selection.Skipped) != 0 {
		t.Fatalf("selection after stale failure = %+v, %v", selection, ok)
	}
}

func TestModelHealthConcurrentAcquireReservesSingleProbe(t *testing.T) {
	clock := &modelHealthClock{now: time.Unix(4_000, 0)}
	health := NewModelHealth(ModelHealthOptions{Now: clock.Now})
	chain := []string{"primary", "fallback"}
	health.Complete(mustAcquireModel(t, health, chain).Ticket, ModelHealthEligibleFailure, "timeout")
	clock.Advance(30 * time.Second)

	const workers = 32
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan ModelSelection, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			selection, ok := health.Acquire(chain, nil)
			if ok {
				results <- selection
			}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	probes := 0
	for selection := range results {
		if selection.Ticket.Ref == "primary" && selection.Ticket.Probe {
			probes++
		}
	}
	if probes != 1 {
		t.Fatalf("half-open probes = %d, want 1", probes)
	}
}

func TestModelHealthAcquireHonorsAttemptedSet(t *testing.T) {
	health := NewModelHealth(ModelHealthOptions{})
	selection, ok := health.Acquire([]string{"primary", "fallback"}, map[string]struct{}{"primary": {}})
	if !ok || selection.Ticket.Ref != "fallback" {
		t.Fatalf("selection = %+v, %v", selection, ok)
	}
}

func mustAcquireModel(t *testing.T, health *ModelHealth, chain []string) ModelSelection {
	t.Helper()
	selection, ok := health.Acquire(chain, nil)
	if !ok {
		t.Fatal("expected model selection")
	}
	return selection
}
