//go:build postgres

package e2e

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/app/managed"
	"github.com/juex-ai/juex/internal/calendar"
	calendarpg "github.com/juex-ai/juex/internal/calendar/postgres"
	"github.com/juex-ai/juex/internal/calendar/recurrence"
	"github.com/juex-ai/juex/internal/foundation/application"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func calendarFixture(t *testing.T) (memoryFixture, *calendar.Service, *calendarpg.Store) {
	t.Helper()
	f := managedMemory(t)
	if err := calendarpg.Migrate(context.Background(), f.pool); err != nil {
		t.Fatal(err)
	}
	if err := calendarpg.Migrate(context.Background(), f.pool); err != nil {
		t.Fatal(err)
	}
	store := calendarpg.New(f.pool)
	return f, &calendar.Service{Repository: store, Authority: managed.RuntimeAuthority{Directory: f.directory}}, store
}

func calendarChange(agent string) calendar.Change {
	return calendar.Change{ID: uuid.NewString(), Action: "save", Definition: &calendar.Definition{Name: "Check project", Content: "Read the project status", Mode: "agent", AgentID: agent, Rule: recurrence.Rule{Frequency: recurrence.FrequencyInterval, EverySeconds: 60}}}
}

func TestManagedCalendarAtomicAdmissionAndIsolation(t *testing.T) {
	f, service, store := calendarFixture(t)
	ctx := context.Background()
	q := calendarChange(f.scope.AgentID)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			r, err := service.Change(ctx, f.human, nil, "shared-command", q)
			if err != nil || r.Version != 1 {
				t.Error(r, err)
			}
		})
	}
	wg.Wait()
	page, err := service.Schedules(ctx, f.scope.Access, 0, 50)
	if err != nil || len(page.Schedules) != 1 {
		t.Fatal(page, err)
	}
	if _, err := service.Change(ctx, f.scope.Access, &f.scope, "other", calendar.Change{ID: q.ID, Version: 0, Action: "pause"}); !errors.Is(err, application.ErrConflict) {
		t.Fatal(err)
	}
	foreign := f.human
	foreign.UserID = uuid.NewString()
	if _, err := service.Schedules(ctx, foreign, 0, 50); !errors.Is(err, application.ErrDenied) {
		t.Fatal("cross-owner schedules", err)
	}
	if err := service.CancelCommand(ctx, f.scope, "cancel-first"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Change(ctx, f.scope.Access, &f.scope, "cancel-first", calendar.Change{ID: q.ID, Version: 1, Action: "pause"}); !errors.Is(err, application.ErrDenied) {
		t.Fatal(err)
	}
	when := time.Now().UTC().Add(4 * time.Hour)
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			if err := store.Update(ctx, f.scope, func(state *calendar.State) error { return state.Advance(q.ID, when) }); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	service.Repository = calendarpg.New(f.pool)
	occ, err := service.Occurrences(ctx, f.human, q.ID, 0, 50)
	if err != nil || len(occ.Occurrences) != 1 {
		t.Fatal(occ, err)
	}
	if _, err := service.Configure(ctx, f.human, 1, false); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Schedules(ctx, f.scope.Access, 0, 50); !errors.Is(err, application.ErrDisabled) {
		t.Fatal("disabled Agent read", err)
	}
	if page, err := service.Schedules(ctx, f.human, 0, 50); err != nil || len(page.Schedules) != 1 {
		t.Fatal("disabled human read", page, err)
	}
	if _, err := service.Change(ctx, f.human, nil, "disabled-write", calendar.Change{ID: q.ID, Version: 2, Action: "pause"}); !errors.Is(err, application.ErrDisabled) {
		t.Fatal(err)
	}
}

type calendarWorkers struct {
	mu        sync.Mutex
	states    map[string]calendar.WorkerState
	admitted  int
	loseReply bool
}

func (w *calendarWorkers) Admit(_ context.Context, d calendar.Delivery) (calendar.WorkerState, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if prior, ok := w.states[d.ID]; ok {
		return prior, nil
	}
	w.admitted++
	v := calendar.WorkerState{ID: uuid.NewString(), State: "active"}
	w.states[d.ID] = v
	if w.loseReply {
		w.loseReply = false
		return calendar.WorkerState{}, errors.New("reply lost")
	}
	return v, nil
}
func (w *calendarWorkers) State(_ context.Context, d calendar.Delivery) (calendar.WorkerState, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	v, ok := w.states[d.ID]
	if !ok {
		return v, calendar.ErrWorkerMissing
	}
	return v, nil
}
func (w *calendarWorkers) Cancel(_ context.Context, d calendar.Delivery) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	v := w.states[d.ID]
	v.State = "cancelled"
	w.states[d.ID] = v
	return nil
}

func TestManagedCalendarLostAdmissionReplyKeepsOriginalOccurrence(t *testing.T) {
	f, service, store := calendarFixture(t)
	ctx := context.Background()
	q := calendarChange(f.scope.AgentID)
	if _, err := service.Change(ctx, f.human, nil, "create", q); err != nil {
		t.Fatal(err)
	}
	// The occurrence was persisted before a prolonged outage. Its original
	// identity remains eligible; the catch-up window must not discard it.
	if err := store.Update(ctx, f.scope, func(state *calendar.State) error {
		if err := state.Advance(q.ID, time.Now().Add(2*time.Minute)); err != nil {
			return err
		}
		for _, d := range state.Deliveries {
			d.ScheduledAt = time.Now().Add(-48 * time.Hour)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	w := &calendarWorkers{states: map[string]calendar.WorkerState{}, loseReply: true}
	service.Workers = w
	if err := service.Step(ctx); err == nil {
		t.Fatal("lost admission reply hidden")
	}
	if err := service.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if w.admitted != 1 {
		t.Fatal("duplicate admission", w.admitted)
	}
	for id, v := range w.states {
		v.State = "outcome_unknown"
		v.Operations = []string{uuid.NewString()}
		w.states[id] = v
	}
	if err := service.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if err := service.Step(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := service.Occurrences(ctx, f.human, q.ID, 0, 50)
	if err != nil || len(page.Occurrences) != 1 || page.Occurrences[0].State != "outcome_unknown" || len(page.Occurrences[0].Operations) != 1 || w.admitted != 1 {
		t.Fatal(page, err, w.admitted)
	}
	if err := store.View(ctx, f.scope, func(state *calendar.State) error {
		for _, d := range state.Deliveries {
			if d.Notices["unknown"] == nil || d.Notices["unknown"].Event.Kind != "attention" {
				t.Error("unknown result did not create attention notification", d)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestManagedRuntimeApplicationCancellationTracksExternalOutcome(t *testing.T) {
	_, store, scope, _ := runtimeDatabase(t)
	ctx := context.Background()
	job := applicationJob()
	job.Application = "calendar"
	receipt, err := store.AdmitApplication(ctx, scope, job)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.Claim(ctx, scope.AgentID, "fixture", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	work, err := store.BeginTurn(ctx, lease, scope, receipt.InputID, runtimeConfig())
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := store.BeginAttempt(ctx, lease, work.TurnID, managedruntime.ModelRequest{MaxOutputTokens: work.Config.Models[work.ModelIndex].MaxOutput, Messages: work.History})
	if err != nil {
		t.Fatal(err)
	}
	call := llm.Block{Type: llm.BlockToolUse, ToolUseID: "original-command", ToolName: "shell", Input: map[string]any{"command": "echo hello"}}
	if err := store.FinishAttempt(ctx, lease, attempt.ID, llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{call}}}, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.Release(ctx, lease); err != nil {
		t.Fatal(err)
	}
	tool, err := store.ClaimTool(ctx, "tool-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CancelApplication(ctx, scope, job.Application, job.ID); err != nil {
		t.Fatal(err)
	}
	if r, err := store.ApplicationReceipt(ctx, scope, job.Application, job.ID); err != nil || r.State != "cancel_requested" || len(r.Operations) != 1 || r.Operations[0] != tool.ID {
		t.Fatal(r, err)
	}
	if err := store.FinishTool(ctx, tool, managedruntime.ToolOutcome{State: "unknown"}); err != nil {
		t.Fatal(err)
	}
	if r, err := store.ApplicationReceipt(ctx, scope, job.Application, job.ID); err != nil || r.State != "outcome_unknown" || len(r.Operations) != 1 || r.Operations[0] != tool.ID {
		t.Fatal(r, err)
	}
}
