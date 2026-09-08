package agenthttp

import (
	"context"
	"sync"

	"github.com/juex-ai/juex/internal/framework/agent"

	"github.com/juex-ai/juex/internal/foundation/cancellation"
	"github.com/juex-ai/juex/internal/foundation/llm"
)

type webTurnTransport struct {
	agent *agent.Agent

	lifecycleMu sync.Mutex
	closed      bool

	cancelMu   sync.Mutex
	cancel     context.CancelCauseFunc
	activeTurn string
	wg         sync.WaitGroup
}

func newWebTurnTransport(a *agent.Agent) *webTurnTransport {
	return &webTurnTransport{agent: a}
}

func (t *webTurnTransport) start(turnID string, msg llm.Message) {
	if t == nil || t.agent == nil || t.agent.Engine == nil || turnID == "" {
		return
	}
	t.lifecycleMu.Lock()
	if t.closed {
		t.lifecycleMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancelCause(context.Background())

	var previousCancel context.CancelCauseFunc
	t.cancelMu.Lock()
	previousCancel = t.cancel
	t.cancel = cancel
	t.activeTurn = turnID
	t.cancelMu.Unlock()

	t.wg.Add(1)
	t.lifecycleMu.Unlock()
	if previousCancel != nil {
		previousCancel(cancellation.ErrUserCancelled)
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
	runtimeCancelled := t.agent != nil && t.agent.CancelActiveTurn(cause)
	t.cancelMu.Lock()
	cancel := t.cancel
	if cancel != nil {
		t.cancel = nil
		t.activeTurn = ""
	}
	t.cancelMu.Unlock()
	if cancel == nil && !runtimeCancelled {
		return false
	}
	if cancel != nil {
		cancel(cause)
	}
	return true
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
	_, _ = t.agent.RunAdmittedTurn(ctx, turnID, msg)

	t.cancelMu.Lock()
	if t.activeTurn == turnID {
		t.cancel = nil
		t.activeTurn = ""
	}
	t.cancelMu.Unlock()
}
