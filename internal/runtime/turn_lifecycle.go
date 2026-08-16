package runtime

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/juex-ai/juex/internal/cancellation"
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
)

type turnLifecycle struct {
	engine  *Engine
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

func (l *turnLifecycle) runLocked(ctx context.Context) (turnLifecycleResult, error) {
	if err := l.engine.repairTranscriptLocked(l.turnID, "turn_start"); err != nil {
		return turnLifecycleResult{}, err
	}
	if err := l.engine.restorePendingInput(l.turnID, l.userMsg.ID); err != nil {
		return turnLifecycleResult{}, err
	}

	prepared, err := l.engine.prepareTurnContextLocked(ctx, l.turnID, l.userMsg)
	if err != nil {
		return turnLifecycleResult{}, err
	}
	l.prepared = prepared

	if err := l.engine.recordTurnStartLocked(l.turnID, prepared.userMessage); err != nil {
		return turnLifecycleResult{}, err
	}

	for iter := 0; ; iter++ {
		if err := l.runProviderIterationLocked(ctx, iter); err != nil {
			return turnLifecycleResult{}, err
		}
		if l.activeClosed {
			break
		}
	}

	if err := l.engine.recordTurnCompletionLocked(l.turnID, l.start, l.lastText); err != nil {
		return turnLifecycleResult{}, fmt.Errorf("commit turn completion: %w", err)
	}
	return turnLifecycleResult{output: l.lastText}, nil
}

func (l *turnLifecycle) runProviderIterationLocked(ctx context.Context, iter int) error {
	if err := cancellation.ContextError(ctx); err != nil {
		return err
	}
	if err := l.engine.drainPendingInputLocked(ctx, l.turnID); err != nil {
		return err
	}
	iterCopy := iter
	if err := l.engine.emit(events.Event{Type: TurnPhaseType, TurnID: l.turnID, Payload: TurnPhasePayload{
		Phase: TurnPhaseProviderIteration,
		Iter:  &iterCopy,
	}}); err != nil {
		return fmt.Errorf("commit provider iteration phase: %w", err)
	}

	request, err := l.engine.prepareProviderRequestLocked(ctx, l.turnID, iter, l.prepared)
	if err != nil {
		return err
	}
	result, err := l.engine.requestProviderTurnLocked(ctx, l.turnID, l.prepared, request)
	if err != nil {
		if contextErr := cancellation.ContextError(ctx); contextErr != nil && errors.Is(err, context.Canceled) {
			return contextErr
		}
		if llm.IsContextOverflowError(err) && !l.retriedOverflow {
			contextWindow := l.engine.ContextWindow
			var requestErr *modelRequestError
			if errors.As(err, &requestErr) && requestErr.contextWindow > 0 {
				contextWindow = requestErr.contextWindow
			}
			if _, compactErr := l.engine.compactLockedForContextWindow(ctx, l.turnID, l.prepared.systemPrompt, l.prepared.tools, "overflow_retry", true, "", contextWindow, 0); compactErr != nil {
				return fmt.Errorf("llm: %w; compact retry failed: %w", err, compactErr)
			}
			l.retriedOverflow = true
			return nil
		}
		if l.engine.continueAfterProviderFailure(ctx, l.turnID, result.request, err) {
			return nil
		}
		return fmt.Errorf("llm: %w", err)
	}
	if err := cancellation.ContextError(ctx); err != nil {
		return err
	}

	recorded, err := l.engine.recordProviderResponseLocked(l.turnID, l.prepared, result)
	if err != nil {
		return err
	}
	if err := cancellation.ContextError(ctx); err != nil {
		return err
	}
	if len(recorded.toolCalls) > 0 {
		return l.engine.recordToolBatchLocked(ctx, l.turnID, l.prepared.policy, recorded)
	}
	outcome, err := l.applyFinishPolicyLocked(ctx, recorded)
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

func (l *turnLifecycle) applyFinishPolicyLocked(ctx context.Context, recorded recordedProviderResponse) (turnFinishOutcome, error) {
	e := l.engine
	finalText := recorded.finalText
	_ = e.emit(events.Event{Type: "finish.attempted", TurnID: l.turnID, Payload: FinishAttemptedPayload{
		StopReason: recorded.stopReason,
		OutputLen:  len(finalText),
	}})

	request := runtimemodule.FinishRequest{
		Runtime:    e.policyRuntimeContext(),
		Session:    e.policySessionContext(),
		TurnID:     l.turnID,
		UserInput:  l.prepared.userMessage.FirstText(),
		StopReason: string(recorded.stopReason),
		Output:     finalText,
		Observer:   e.policyObserver(l.turnID),
	}
	evaluation, err := runtimemodule.EvaluateFinishPolicies(ctx, request, e.policySets()...)
	if err != nil {
		return turnFinishOutcome{}, err
	}
	if err := e.queuePolicyRuntimeContext(evaluation.Context); err != nil {
		return turnFinishOutcome{}, err
	}

	for i := range evaluation.Candidates {
		candidate := evaluation.Candidates[i]
		applied, err := runtimemodule.CommitFinishCandidate(ctx, request, candidate)
		if err != nil {
			return turnFinishOutcome{}, err
		}
		if !applied {
			continue
		}
		if candidate.Decision.Continuation != "" {
			if err := l.enqueueContinuationLocked(ctx, candidate.Decision.Continuation); err != nil {
				return turnFinishOutcome{}, err
			}
			runtimemodule.ObserveFinishContinuation(ctx, request, candidate)
			return l.finishOrContinueLocked(finalText), nil
		}
		return turnFinishOutcome{action: turnFinishContinue, output: finalText}, nil
	}
	return l.finishOrContinueLocked(finalText), nil
}

func (l *turnLifecycle) enqueueContinuationLocked(ctx context.Context, prompt string) error {
	msg := llm.TextMessage(llm.RoleUser, prompt)
	msg.Kind = llm.MessageKindContinuation
	_, err := l.engine.EnqueuePendingMessage(ctx, msg)
	return err
}

func (l *turnLifecycle) finishOrContinueLocked(output string) turnFinishOutcome {
	if !l.engine.finishActiveTurnIfNoPending(l.turnID) {
		return turnFinishOutcome{action: turnFinishContinue, output: output}
	}
	return turnFinishOutcome{action: turnFinishComplete, output: output, activeClosed: true}
}
