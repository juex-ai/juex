// Package agent owns execution, admission, recovery and ordered cleanup for a
// Thread runtime. App supplies prepared resources and product policy callbacks.
package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/juex-ai/juex/internal/foundation/events"
	"github.com/juex-ai/juex/internal/foundation/llm"
	usermedia "github.com/juex-ai/juex/internal/framework/inputmedia"
	"github.com/juex-ai/juex/internal/framework/prompt"
	"github.com/juex-ai/juex/internal/framework/runtime"
	"github.com/juex-ai/juex/internal/framework/thread"
	observability "github.com/juex-ai/juex/internal/framework/threadlog"
)

// CloseDeferredError reports that another Agent cleanup pass is in progress.
// Callback callers must return before waiting on it.
type CloseDeferredError struct {
	done   <-chan struct{}
	result *error
}

func (*CloseDeferredError) Error() string {
	return "app: close deferred while cleanup is in progress"
}

func (e *CloseDeferredError) Wait() error {
	if e == nil || e.done == nil {
		return nil
	}
	<-e.done
	if e.result == nil {
		return nil
	}
	return *e.result
}

func (a *Agent) NewContext(ctx context.Context) error {
	if a == nil || a.Engine == nil {
		return ErrThreadUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := a.waitPendingInputRecoveryContext(ctx); err != nil {
		return err
	}
	a.threadHandoffMu.Lock()
	defer a.threadHandoffMu.Unlock()
	if err := a.Engine.NewContext(ctx); err != nil {
		return err
	}
	if a.Status != nil {
		a.Status.ClearContextUsage()
	}
	return nil
}

func (a *Agent) closeActiveThreadResources() error {
	if a == nil {
		return nil
	}
	a.threadMu.Lock()
	threadState := a.threadResource
	a.threadResource = nil
	a.Thread = nil
	a.threadMu.Unlock()

	var moduleErr, threadErr error
	if a.Engine != nil {
		if modules := a.Engine.ThreadRuntimeSnapshot().Modules; modules != nil {
			moduleErr = modules.CloseThread(context.Background())
		}
	}
	if threadState != nil {
		threadErr = threadState.Close()
	}
	a.threadRelease.Do(func() {
		if a.threadReleased != nil {
			close(a.threadReleased)
		}
	})
	return errors.Join(moduleErr, threadErr)
}

// WaitThreadReleased waits until final Agent cleanup has closed the active
// Thread and its workspace lock, and until Worker Thread result deliveries can
// no longer write its directory. Child runtimes may still be draining.
func (a *Agent) WaitThreadReleased(ctx context.Context) error {
	if a == nil || a.threadReleased == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-a.threadReleased:
	case <-ctx.Done():
		return ctx.Err()
	}
	if a.workers == nil {
		return nil
	}
	return a.workers.WaitDeliveryWriters(ctx)
}

func RuntimeStatusSeed(threadState *thread.Thread, maxPendingInputs int) runtime.StatusSeed {
	if threadState == nil {
		return runtime.StatusSeed{MaxPendingInputs: maxPendingInputs}
	}
	info := threadState.Info()
	state := runtime.ThreadRuntimeIdle
	switch info.ExecutionState {
	case thread.ExecutionWorking:
		state = runtime.ThreadRuntimeTurnActive
	case thread.ExecutionFailed:
		state = runtime.ThreadRuntimeFailed
	}
	return runtime.StatusSeed{
		ThreadID:         threadState.ID,
		ThreadAlias:      threadState.Alias,
		ThreadState:      state,
		PendingCount:     info.PendingInputs,
		MaxPendingInputs: maxPendingInputs,
		TokenUsage:       threadState.TokenUsageSnapshot(),
		ContextUsage:     threadState.ContextUsageSnapshot(),
	}
}

func (a *Agent) AddEventDelivery(delivery events.Delivery) func() {
	if a == nil || a.eventSink == nil {
		return func() {}
	}
	return a.eventSink.AddDelivery(delivery)
}

func (a *Agent) AddEventProjection(projection events.Delivery) func() {
	if a == nil || a.eventSink == nil {
		return func() {}
	}
	return a.eventSink.AddProjection(projection)
}

func (a *Agent) ReadCommittedEvents(read func() error) error {
	if a == nil || a.eventSink == nil {
		return events.ErrDurableSinkClosed
	}
	return a.eventSink.ReadCommitted(read)
}

func (a *Agent) attachObservability(threadState *thread.Thread) error {
	if a == nil || a.Bus == nil || threadState == nil {
		return nil
	}
	rec, err := observability.NewRecorder(observability.Options{
		ThreadDir: threadState.Dir,
		Debug:     a.debug,
		LogLevel:  a.logLevel,
	})
	if err != nil {
		return err
	}
	a.recorder = rec
	a.observabilityUnsubscribe = a.Bus.Subscribe("*", func(e events.Event) {
		_ = rec.Record(e)
	})
	return nil
}

func (a *Agent) detachObservability() error {
	if a == nil {
		return nil
	}
	if a.observabilityUnsubscribe != nil {
		a.observabilityUnsubscribe()
		a.observabilityUnsubscribe = nil
	}
	if a.recorder == nil {
		return nil
	}
	err := a.recorder.Close()
	a.recorder = nil
	return err
}

// Run drives a single turn synchronously.
func (a *Agent) Run(ctx context.Context, prompt string) (string, error) {
	if a != nil && a.Engine != nil {
		identity, ok := a.ThreadIdentity()
		if !ok {
			return "", ErrThreadUnavailable
		}
		if err := a.checkInput(identity.ID, TurnAdmissionRequest{Prompt: prompt}); err != nil {
			return "", err
		}
	}
	if err := a.waitPendingInputRecoveryContext(ctx); err != nil {
		return "", err
	}
	if cmd, handled, err := a.parseCommand(prompt); handled || err != nil {
		if err != nil {
			return "", err
		}
		if cmd.Kind == CommandKindPrompt {
			return a.runEngineTurn(ctx, cmd.Prompt)
		}
		result, err := a.ExecuteCommand(ctx, cmd)
		if err != nil {
			return "", err
		}
		if cmd.Kind == CommandKindNew && a.executionError() == nil {
			return a.runEngineTurnMessage(ctx, a.executionPolicy.NewContextInput)
		}
		return result.Text, nil
	}
	return a.runEngineTurn(ctx, prompt)
}

// RunWithAttachments drives one synchronous text, image, or mixed-content
// user turn. Attachment references must belong to the current Thread.
func (a *Agent) RunWithAttachments(ctx context.Context, prompt string, attachments []llm.MediaRef) (string, error) {
	if a == nil || a.Engine == nil {
		return "", errors.New("app: attachment turn requires an initialized Thread and engine")
	}
	if len(attachments) == 0 {
		return a.Run(ctx, prompt)
	}
	if err := a.waitPendingInputRecoveryContext(ctx); err != nil {
		return "", err
	}
	if _, handled, err := a.parseCommand(prompt); handled || err != nil {
		if err != nil {
			return "", err
		}
		return "", errors.New("slash commands cannot include attachments")
	}
	a.threadMu.RLock()
	defer a.threadMu.RUnlock()
	if a.Thread == nil {
		return "", errors.New("app: attachment turn requires an initialized Thread and engine")
	}
	if err := a.executionError(); err != nil {
		return "", err
	}
	if err := usermedia.ValidateThreadMediaRefs(a.mediaDir, a.Thread.ID, attachments, usermedia.Limits{}); err != nil {
		return "", err
	}
	return a.Engine.TurnMessage(ctx, userTurnMessage(prompt, attachments))
}

func (a *Agent) runEngineTurn(ctx context.Context, input string) (string, error) {
	if a == nil || a.Engine == nil {
		return "", errors.New("app: turn requires an initialized engine")
	}
	a.threadMu.RLock()
	defer a.threadMu.RUnlock()
	if a.Thread == nil {
		return "", ErrThreadUnavailable
	}
	if err := a.executionError(); err != nil {
		return "", err
	}
	return a.Engine.Turn(ctx, input)
}

func (a *Agent) runEngineTurnMessage(ctx context.Context, message llm.Message) (string, error) {
	if a == nil || a.Engine == nil {
		return "", errors.New("app: turn requires an initialized engine")
	}
	a.threadMu.RLock()
	defer a.threadMu.RUnlock()
	if a.Thread == nil {
		return "", ErrThreadUnavailable
	}
	if err := a.executionError(); err != nil {
		return "", err
	}
	return a.Engine.TurnMessage(ctx, message)
}

func (a *Agent) CompactWithInstructions(ctx context.Context, reason string, auto bool, instructions string) (runtime.CompactionResult, error) {
	if a == nil || a.Engine == nil {
		return runtime.CompactionResult{}, fmt.Errorf("app: nil engine")
	}
	if err := a.waitPendingInputRecoveryContext(ctx); err != nil {
		return runtime.CompactionResult{}, err
	}
	admitted := events.Normalize(events.Event{Type: runtime.TurnAdmittedType, Payload: runtime.TurnAdmittedPayload{}})
	turnID := "compact-" + admitted.ID
	admitted.TurnID = turnID
	if err := a.Bus.Emit(admitted); err != nil {
		return runtime.CompactionResult{}, fmt.Errorf("commit compaction admission: %w", err)
	}
	return a.compactWithTurnID(ctx, turnID, reason, auto, instructions)
}

func (a *Agent) CompactAdmittedWithInstructions(ctx context.Context, turnID, reason string, auto bool, instructions string) (runtime.CompactionResult, error) {
	if a == nil || a.Engine == nil {
		return runtime.CompactionResult{}, fmt.Errorf("app: nil engine")
	}
	if turnID == "" {
		return runtime.CompactionResult{}, fmt.Errorf("app: empty compact turn id")
	}
	return a.compactWithTurnID(ctx, turnID, reason, auto, instructions)
}

func (a *Agent) compactWithTurnID(ctx context.Context, turnID, reason string, auto bool, instructions string) (runtime.CompactionResult, error) {
	a.threadMu.RLock()
	defer a.threadMu.RUnlock()
	if a.Thread == nil {
		return runtime.CompactionResult{}, a.emitCompactionError(turnID, ErrThreadUnavailable)
	}
	sections, err := a.Engine.PromptSectionsWithError()
	if err != nil {
		return runtime.CompactionResult{}, a.emitCompactionError(turnID, fmt.Errorf("app: build compaction prompt: %w", err))
	}
	systemPrompt := prompt.JoinSections(sections)
	result, err := a.Engine.CompactWithInstructions(ctx, turnID, systemPrompt, reason, auto, instructions)
	if err != nil {
		return result, a.emitCompactionError(turnID, err)
	}
	if err := a.Bus.Emit(events.Event{Type: "turn.completed", TurnID: turnID, Payload: runtime.TurnCompletedPayload{
		TokenUsage: a.Thread.TokenUsageSnapshot(),
	}}); err != nil {
		return result, fmt.Errorf("commit compaction completion: %w", err)
	}
	return result, nil
}

func (a *Agent) emitCompactionError(turnID string, err error) error {
	if emitErr := a.Bus.Emit(events.Event{Type: "turn.errored", TurnID: turnID, Payload: runtime.NewTurnErroredPayload(err)}); emitErr != nil {
		return errors.Join(err, fmt.Errorf("commit compaction error: %w", emitErr))
	}
	return err
}

func (a *Agent) TokenUsage() llm.Usage {
	info, ok := a.ThreadInfo()
	if !ok {
		return llm.Usage{}
	}
	return info.TokenUsage.Total
}

// BeginClose cancels Agent-owned work without waiting for active turns or
// deferred cleanup to drain.
func (a *Agent) BeginClose() error {
	if a == nil {
		return nil
	}
	a.closeCancel.Do(func() {
		if a.cancel != nil {
			a.cancel()
		}
		if a.runtimeModules != nil {
			// BeginClose is intentionally non-blocking. Close/CloseAndWait
			// serializes with this generic Module quiesce pass and reports its
			// cached result before releasing later resources.
			go func() { _ = a.runtimeModules.QuiesceRuntime(context.Background()) }()
		}
	})
	return nil
}

// Close advances cleanup until it completes or an observable close must be
// deferred. A deferred result leaves later resources untouched so callback
// callers can return safely and an external owner can resume cleanup.
func (a *Agent) Close() (result error) {
	if a == nil {
		return nil
	}
	a.closeMu.Lock()
	if a.closeRunning {
		done := a.closeRunDone
		activeResult := a.closeRunResult
		a.closeMu.Unlock()
		return &CloseDeferredError{done: done, result: activeResult}
	}
	a.closeRunning = true
	a.closeRunDone = make(chan struct{})
	a.closeRunResult = &result
	done := a.closeRunDone
	a.closeMu.Unlock()
	defer func() {
		a.closeMu.Lock()
		a.closeRunning = false
		close(done)
		a.closeMu.Unlock()
	}()
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	_ = a.BeginClose()
	for {
		a.closeMu.Lock()
		if a.cleanupIndex >= len(a.cleanup) {
			result = a.closeErr
			a.closeMu.Unlock()
			return result
		}
		fn := a.cleanup[a.cleanupIndex]
		a.closeMu.Unlock()
		err := fn()
		var deferred interface{ Wait() error }
		if errors.As(err, &deferred) {
			return err
		}
		a.closeMu.Lock()
		a.cleanupIndex++
		if err != nil && a.closeErr == nil {
			a.closeErr = err
		}
		a.closeMu.Unlock()
	}
}

// CloseAndWait fully drains deferred observable work before releasing later
// resources. It is for process and transport owners, not callback code.
func (a *Agent) CloseAndWait() error {
	if a == nil {
		return nil
	}
	for {
		err := a.Close()
		var deferred interface{ Wait() error }
		if !errors.As(err, &deferred) {
			return err
		}
		_ = deferred.Wait()
	}
}
