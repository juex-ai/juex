package runtime

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/hooks"
	"github.com/juex-ai/juex/internal/llm"
)

type turnLifecycle struct {
	engine  *Engine
	ctx     context.Context
	turnID  string
	userMsg llm.Message
	start   time.Time

	prepared        preparedTurnContext
	lastText        string
	retriedOverflow bool
	activeClosed    bool
}

type turnLifecycleResult struct {
	output string
}

type turnFinishAction int

const (
	turnFinishContinue turnFinishAction = iota
	turnFinishComplete
)

type turnFinishOutcome struct {
	action       turnFinishAction
	output       string
	activeClosed bool
}

func (o turnFinishOutcome) shouldContinue() bool {
	return o.action == turnFinishContinue
}

func (l *turnLifecycle) runLocked() (turnLifecycleResult, error) {
	if err := l.engine.restorePendingInput(l.turnID, l.userMsg.ID); err != nil {
		return turnLifecycleResult{}, err
	}

	prepared, err := l.engine.prepareTurnContextLocked(l.ctx, l.turnID, l.userMsg)
	if err != nil {
		return turnLifecycleResult{}, err
	}
	l.prepared = prepared

	if err := l.engine.recordTurnStartLocked(l.turnID, prepared.userMessage); err != nil {
		return turnLifecycleResult{}, err
	}

	for iter := 0; ; iter++ {
		if err := l.runProviderIterationLocked(iter); err != nil {
			return turnLifecycleResult{}, err
		}
		if l.activeClosed {
			break
		}
	}

	l.engine.recordTurnCompletionLocked(l.turnID, l.start, l.lastText)
	return turnLifecycleResult{output: l.lastText}, nil
}

func (l *turnLifecycle) runProviderIterationLocked(iter int) error {
	if err := l.ctx.Err(); err != nil {
		return err
	}
	if err := l.engine.drainPendingInputLocked(l.ctx, l.turnID); err != nil {
		return err
	}

	request, err := l.engine.prepareProviderRequestLocked(l.turnID, iter, l.prepared)
	if err != nil {
		return err
	}
	resp, err := l.engine.requestProviderTurnLocked(l.ctx, l.prepared, request)
	if err != nil {
		if llm.IsContextOverflowError(err) && !l.retriedOverflow {
			if _, compactErr := l.engine.compactLocked(l.ctx, l.turnID, l.prepared.systemPrompt, "overflow_retry", true, ""); compactErr != nil {
				return fmt.Errorf("llm: %w; compact retry failed: %w", err, compactErr)
			}
			l.retriedOverflow = true
			return nil
		}
		return fmt.Errorf("llm: %w", err)
	}

	recorded, err := l.engine.recordProviderResponseLocked(l.turnID, l.prepared, request, resp)
	if err != nil {
		return err
	}
	if len(recorded.toolCalls) > 0 {
		return l.engine.recordToolBatchLocked(l.ctx, l.turnID, l.prepared.policy, recorded.toolCalls)
	}
	outcome, err := l.applyFinishPolicyLocked(recorded)
	if err != nil {
		return err
	}
	l.lastText = outcome.output
	l.activeClosed = outcome.activeClosed
	if outcome.shouldContinue() {
		l.activeClosed = false
	}
	return nil
}

func (l *turnLifecycle) applyFinishPolicyLocked(recorded recordedProviderResponse) (turnFinishOutcome, error) {
	e := l.engine
	finalText := recorded.finalText
	e.emit(events.Event{Type: "finish.attempted", TurnID: l.turnID, Payload: FinishAttemptedPayload{
		StopReason: recorded.stopReason,
		OutputLen:  len(finalText),
	}})

	if prompt, payload, ok := e.runUnresolvedFailureGate(l.turnID); ok {
		if err := l.enqueueContinuationLocked(prompt); err != nil {
			return turnFinishOutcome{}, err
		}
		e.emit(events.Event{Type: "tool.failure.continued", TurnID: l.turnID, Payload: payload})
		return l.finishOrContinueLocked(finalText), nil
	}

	stopResults, err := l.runStopHooksLocked()
	if err != nil {
		return turnFinishOutcome{}, err
	}

	if prompt, payload, ok, err := e.runGoalCompletionGate(l.turnID); err != nil {
		return turnFinishOutcome{}, err
	} else if ok {
		if err := l.enqueueContinuationLocked(prompt); err != nil {
			return turnFinishOutcome{}, err
		}
		e.emit(events.Event{Type: "goal.continued", TurnID: l.turnID, Payload: payload})
		return l.finishOrContinueLocked(finalText), nil
	}

	if prompt, ok := stopContinuation(stopResults); ok {
		if strings.TrimSpace(prompt) == "" {
			return turnFinishOutcome{}, fmt.Errorf("hooks: Stop hook requested block_stop without continue_prompt")
		}
		if err := l.enqueueContinuationLocked(prompt); err != nil {
			return turnFinishOutcome{}, err
		}
	}
	return l.finishOrContinueLocked(finalText), nil
}

func (l *turnLifecycle) runStopHooksLocked() ([]hooks.Result, error) {
	stopReq := l.engine.newHookRequest(hooks.EventStop, l.turnID)
	stopReq.UserInput = l.prepared.userMessage.FirstText()
	return l.engine.runHooks(l.ctx, stopReq)
}

func (l *turnLifecycle) enqueueContinuationLocked(prompt string) error {
	_, err := l.engine.EnqueuePendingInput(l.ctx, prompt)
	return err
}

func (l *turnLifecycle) finishOrContinueLocked(output string) turnFinishOutcome {
	if !l.engine.finishActiveTurnIfNoPending(l.turnID) {
		return turnFinishOutcome{action: turnFinishContinue, output: output}
	}
	return turnFinishOutcome{action: turnFinishComplete, output: output, activeClosed: true}
}
