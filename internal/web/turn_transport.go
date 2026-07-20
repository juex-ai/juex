package web

import (
	"context"
	"sync"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/cancellation"
	"github.com/juex-ai/juex/internal/llm"
)

type webTurnTransport struct {
	app *app.App

	lifecycleMu sync.Mutex
	closed      bool

	cancelMu   sync.Mutex
	cancel     context.CancelCauseFunc
	activeTurn string
	wg         sync.WaitGroup

	admissionsMu sync.Mutex
	admissions   map[string]bool
}

func newWebTurnTransport(a *app.App) *webTurnTransport {
	return &webTurnTransport{
		app:        a,
		admissions: map[string]bool{},
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
	ctx, cancel := context.WithCancelCause(context.Background())

	var previousCancel context.CancelCauseFunc
	var previousTurnID string
	t.cancelMu.Lock()
	previousCancel = t.cancel
	previousTurnID = t.activeTurn
	t.cancel = cancel
	t.activeTurn = turnID
	t.cancelMu.Unlock()

	t.admissionsMu.Lock()
	t.admissions[turnID] = false
	t.admissionsMu.Unlock()

	t.wg.Add(1)
	t.lifecycleMu.Unlock()
	if previousCancel != nil {
		previousCancel(cancellation.ErrUserCancelled)
		t.completeAdmission(previousTurnID)
	}
	go t.run(ctx, turnID, msg)
}

func (t *webTurnTransport) interrupt() bool {
	return t.interruptWithCause(cancellation.ErrUserCancelled)
}

func (t *webTurnTransport) interruptWithCause(cause error) bool {
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
	cancel(cause)
	t.completeAdmission(turnID)
	return true
}

func (t *webTurnTransport) reset() {
	if t == nil {
		return
	}
	t.admissionsMu.Lock()
	t.admissions = map[string]bool{}
	t.admissionsMu.Unlock()
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
		cancel(cancellation.ErrUserCancelled)
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
	_, _ = t.app.RunAdmittedTurn(ctx, turnID, msg)

	t.cancelMu.Lock()
	if t.activeTurn == turnID {
		t.cancel = nil
		t.activeTurn = ""
	}
	t.cancelMu.Unlock()
}

func (t *webTurnTransport) completeAdmission(turnID string) {
	if t == nil || t.app == nil || turnID == "" {
		return
	}
	t.admissionsMu.Lock()
	if t.admissions[turnID] {
		t.admissionsMu.Unlock()
		return
	}
	t.admissions[turnID] = true
	t.admissionsMu.Unlock()
	t.app.CompleteAdmittedTurn(turnID)
}
