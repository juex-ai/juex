package web

import (
	"context"
	"sync"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/llm"
)

type webTurnTransport struct {
	app *app.App

	lifecycleMu sync.Mutex
	closed      bool

	cancelMu   sync.Mutex
	cancel     context.CancelFunc
	activeTurn string
	wg         sync.WaitGroup

	statesMu sync.Mutex
	states   map[string]*webTurnState
}

type webTurnState struct {
	ID                 string
	State              string // "running" | "done" | "errored"
	Err                string
	AdmissionCompleted bool
}

type webTurnStatus struct {
	State            string
	Err              string
	PendingCount     *int
	MaxPendingInputs *int
}

func newWebTurnTransport(a *app.App) *webTurnTransport {
	return &webTurnTransport{
		app:    a,
		states: map[string]*webTurnState{},
	}
}

func (t *webTurnTransport) start(turnID string, msg llm.Message) {
	if t == nil || t.app == nil || t.app.Engine == nil || turnID == "" {
		return
	}
	t.lifecycleMu.Lock()
	if t.closed {
		t.lifecycleMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())

	var previousCancel context.CancelFunc
	var previousTurnID string
	t.cancelMu.Lock()
	previousCancel = t.cancel
	previousTurnID = t.activeTurn
	t.cancel = cancel
	t.activeTurn = turnID
	t.cancelMu.Unlock()

	t.statesMu.Lock()
	t.states[turnID] = &webTurnState{ID: turnID, State: "running"}
	t.statesMu.Unlock()

	t.wg.Add(1)
	t.lifecycleMu.Unlock()
	if previousCancel != nil {
		previousCancel()
		t.completeAdmission(previousTurnID)
	}
	go t.run(ctx, turnID, msg)
}

func (t *webTurnTransport) status(turnID string) (webTurnStatus, bool) {
	if t == nil || turnID == "" {
		return webTurnStatus{}, false
	}
	t.statesMu.Lock()
	state, ok := t.states[turnID]
	if ok {
		state = &webTurnState{ID: state.ID, State: state.State, Err: state.Err}
	}
	t.statesMu.Unlock()
	if !ok {
		return webTurnStatus{}, false
	}
	status := webTurnStatus{State: state.State, Err: state.Err}
	if state.State == "running" && t.app != nil && t.app.Engine != nil {
		pending := t.app.PendingInputStatus()
		status.PendingCount = &pending.PendingCount
		status.MaxPendingInputs = &pending.MaxPendingInputs
	}
	return status, true
}

func (t *webTurnTransport) activeStatus() (string, webTurnStatus, bool) {
	if t == nil {
		return "", webTurnStatus{}, false
	}
	t.cancelMu.Lock()
	turnID := t.activeTurn
	t.cancelMu.Unlock()
	if turnID == "" {
		return "", webTurnStatus{}, false
	}
	status, ok := t.status(turnID)
	if !ok || status.State != "running" {
		return "", webTurnStatus{}, false
	}
	return turnID, status, true
}

func (t *webTurnTransport) interrupt() bool {
	if t == nil {
		return false
	}
	t.cancelMu.Lock()
	cancel := t.cancel
	turnID := t.activeTurn
	if cancel != nil {
		t.cancel = nil
		t.activeTurn = ""
	}
	t.cancelMu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	t.completeAdmission(turnID)
	return true
}

func (t *webTurnTransport) reset() {
	if t == nil {
		return
	}
	t.statesMu.Lock()
	t.states = map[string]*webTurnState{}
	t.statesMu.Unlock()
}

func (t *webTurnTransport) close() {
	if t == nil {
		return
	}
	t.lifecycleMu.Lock()
	t.closed = true
	t.cancelMu.Lock()
	cancel := t.cancel
	t.cancelMu.Unlock()
	t.lifecycleMu.Unlock()
	if cancel != nil {
		cancel()
	}
	t.wg.Wait()
}

func (t *webTurnTransport) wait() {
	if t == nil {
		return
	}
	t.wg.Wait()
}

func (t *webTurnTransport) run(ctx context.Context, turnID string, msg llm.Message) {
	defer t.wg.Done()
	defer t.completeAdmission(turnID)
	_, err := t.app.RunAdmittedTurn(ctx, turnID, msg)

	t.cancelMu.Lock()
	if t.activeTurn == turnID {
		t.cancel = nil
		t.activeTurn = ""
	}
	t.cancelMu.Unlock()

	t.statesMu.Lock()
	if state, ok := t.states[turnID]; ok {
		if err != nil {
			state.State = "errored"
			state.Err = err.Error()
		} else {
			state.State = "done"
			state.Err = ""
		}
	}
	t.statesMu.Unlock()
}

func (t *webTurnTransport) completeAdmission(turnID string) {
	if t == nil || t.app == nil || turnID == "" {
		return
	}
	t.statesMu.Lock()
	if state, ok := t.states[turnID]; ok {
		if state.AdmissionCompleted {
			t.statesMu.Unlock()
			return
		}
		state.AdmissionCompleted = true
	}
	t.statesMu.Unlock()
	t.app.CompleteAdmittedTurn(turnID)
}
