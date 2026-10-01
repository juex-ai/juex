//go:build postgres

package e2e

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/calendar"
	"github.com/juex-ai/juex/internal/foundation/application"
)

func TestManagedCalendarCompletedWorkerRetainsBackgroundCancellation(t *testing.T) {
	f, s, store := calendarFixture(t)
	ctx := context.Background()
	q := calendarChange(f.scope.AgentID)
	if _, err := s.Change(ctx, f.human, nil, "create", q); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := store.Update(ctx, f.scope, func(state *calendar.State) error {
		if err := state.Advance(q.ID, time.Now().Add(2*time.Minute)); err != nil {
			return err
		}
		for key := range state.Deliveries {
			id = key
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	w := &calendarWorkers{states: map[string]calendar.WorkerState{id: {ID: "original-worker", State: "completed", Operations: []string{"original-process"}}}}
	s.Workers = w
	if err := s.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.View(ctx, f.scope, func(state *calendar.State) error {
		d := state.Deliveries[id]
		if !d.Finished || d.Settled || !d.ExternalPending {
			t.Error("lost live operation after model completion", d)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Configure(ctx, f.human, 1, false); err != nil {
		t.Fatal(err)
	}
	if err := s.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.View(ctx, f.scope, func(state *calendar.State) error {
		d := state.Deliveries[id]
		if !d.CancelRequested || d.Settled || !d.ExternalPending {
			t.Error("accepted cancellation claimed external settlement", d)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	w.states[id] = calendar.WorkerState{ID: "original-worker", State: "cancelled"}
	if err := store.Update(ctx, f.scope, func(state *calendar.State) error { state.Deliveries[id].AttemptedAt = time.Time{}; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.View(ctx, f.scope, func(state *calendar.State) error {
		if d := state.Deliveries[id]; !d.Settled || d.ExternalPending || d.State != "cancelled" {
			t.Error(d)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if w.admitted != 0 {
		t.Fatal("finished Worker was admitted again", w.admitted)
	}
}

type reorderedCalendarWorkers struct {
	calendarWorkers
	entered, release chan struct{}
	mu               sync.Mutex
	calls            int
}

func (w *reorderedCalendarWorkers) State(ctx context.Context, d calendar.Delivery) (calendar.WorkerState, error) {
	w.mu.Lock()
	w.calls++
	first := w.calls == 1
	w.mu.Unlock()
	if first {
		close(w.entered)
		select {
		case <-w.release:
		case <-ctx.Done():
			return calendar.WorkerState{}, ctx.Err()
		}
		return calendar.WorkerState{ID: "original", State: "active"}, nil
	}
	return calendar.WorkerState{ID: "original", State: "completed"}, nil
}

func TestManagedCalendarParallelOldReceiptCannotReplaceCompletion(t *testing.T) {
	f, s, store := calendarFixture(t)
	ctx := context.Background()
	q := calendarChange(f.scope.AgentID)
	if _, err := s.Change(ctx, f.human, nil, "create", q); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, f.scope, func(state *calendar.State) error { return state.Advance(q.ID, time.Now().Add(2*time.Minute)) }); err != nil {
		t.Fatal(err)
	}
	w := &reorderedCalendarWorkers{entered: make(chan struct{}), release: make(chan struct{})}
	s.Workers = w
	done := make(chan error, 1)
	go func() { done <- s.Step(ctx) }()
	select {
	case <-w.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first state query did not start")
	}
	if err := s.Step(ctx); err != nil {
		close(w.release)
		t.Fatal(err)
	}
	close(w.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := store.View(ctx, f.scope, func(state *calendar.State) error {
		for _, d := range state.Deliveries {
			if d.State != "completed" || !d.Settled {
				t.Error("stale receipt replaced completion", d)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestManagedCalendarFutureSchedulesPauseWhenTargetArchived(t *testing.T) {
	for _, saveFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "scheduler", true: "save_before_scheduler"}[saveFirst], func(t *testing.T) {
			f, s, _ := calendarFixture(t)
			ctx := context.Background()
			q := calendarChange(f.scope.AgentID)
			q.Definition.Rule.EverySeconds = 86400
			if _, err := s.Change(ctx, f.human, nil, "create", q); err != nil {
				t.Fatal(err)
			}
			agent, err := f.directory.SetAgentArchived(ctx, f.human.ActorID, f.human.TenantID, f.scope.AgentID, 1, true)
			if err != nil {
				t.Fatal(err)
			}
			s.Workers = &calendarWorkers{states: map[string]calendar.WorkerState{}}
			if saveFirst {
				q.Version = 1
				// Switching to a reminder must not silently reactivate an invalid target.
				q.Definition.Mode = "reminder"
				q.Definition.AgentID = ""
				if _, err := s.Change(ctx, f.human, nil, "retarget", q); !errors.Is(err, application.ErrConflict) {
					t.Fatal(err)
				}
			} else if err := s.Step(ctx); err != nil {
				t.Fatal(err)
			}
			page, err := s.Schedules(ctx, f.human, 0, 50)
			if err != nil || len(page.Schedules) != 1 || page.Schedules[0].Status != "paused" || page.Schedules[0].PauseReason != "target_unavailable" {
				t.Fatal(page, err)
			}
			if _, err := f.directory.SetAgentArchived(ctx, f.human.ActorID, f.human.TenantID, f.scope.AgentID, agent.Version, false); err != nil {
				t.Fatal(err)
			}
			q.Version = page.Schedules[0].Version
			saved, err := s.Change(ctx, f.human, nil, "save-paused", q)
			if err != nil || saved.State != "paused" {
				t.Fatal("editing reactivated schedule", saved, err)
			}
			if _, err := s.Change(ctx, f.human, nil, "explicit-resume", calendar.Change{ID: q.ID, Version: saved.Version, Action: "resume"}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
