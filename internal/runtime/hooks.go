package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/hooks"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/provenance"
)

const goalCompletionGateName = "goal-completion-gate"

type hookRequestCommitError struct {
	err error
}

type hookBeforeRunError struct {
	err error
}

func (e *hookRequestCommitError) Error() string {
	return "commit hook request: " + e.err.Error()
}

func (e *hookRequestCommitError) Unwrap() error {
	return e.err
}

func (e *hookBeforeRunError) Error() string {
	return "checkpoint before hook run: " + e.err.Error()
}

func (e *hookBeforeRunError) Unwrap() error {
	return e.err
}

func isHookRequestCommitError(err error) bool {
	var target *hookRequestCommitError
	return errors.As(err, &target)
}

func isHookBeforeRunError(err error) bool {
	var target *hookBeforeRunError
	return errors.As(err, &target)
}

func (e *Engine) newHookRequest(event hooks.EventName, turnID string) hooks.Request {
	runtime := e.SessionRuntimeSnapshot()
	req := runtime.HookContext
	req.EventName = event
	req.TurnID = turnID
	if req.SessionID == "" && runtime.Session != nil {
		req.SessionID = runtime.Session.ID
	}
	if state, ok := goalStateRawFromStore(runtime.GoalState); ok {
		req.GoalState = state
	}
	req.Observer = hookObserver{engine: e, turnID: turnID}
	return req
}

func (e *Engine) runHooks(ctx context.Context, req hooks.Request) ([]hooks.Result, error) {
	return e.runHooksBeforeRun(ctx, req, nil)
}

func (e *Engine) runHooksBeforeRun(ctx context.Context, req hooks.Request, beforeRun func() error) ([]hooks.Result, error) {
	if e.Hooks == nil {
		return nil, nil
	}
	if matcher, ok := e.Hooks.(interface {
		Matching(hooks.EventName, string) []hooks.CommandHook
	}); ok && len(matcher.Matching(req.EventName, req.ToolName)) == 0 {
		return nil, nil
	}
	if err := e.emit(events.Event{Type: "hook.requested", TurnID: req.TurnID, Payload: HookStartedPayload{
		Source:    "runtime",
		EventName: string(req.EventName),
		ToolName:  req.ToolName,
	}}); err != nil {
		return nil, &hookRequestCommitError{err: err}
	}
	if beforeRun != nil {
		if err := beforeRun(); err != nil {
			return nil, &hookBeforeRunError{err: err}
		}
	}
	results, err := e.Hooks.Run(ctx, req)
	if err != nil {
		return results, err
	}
	return results, nil
}

func (e *Engine) runGoalCompletionGate(turnID string) (string, GoalContinuedPayload, bool, error) {
	store := e.goalStateStoreLocked()
	if store == nil {
		return "", GoalContinuedPayload{}, false, nil
	}
	start := time.Now()
	_ = e.emit(events.Event{Type: "hook.started", TurnID: turnID, Payload: HookStartedPayload{
		Name:      goalCompletionGateName,
		Source:    "builtin",
		EventName: string(hooks.EventStop),
	}})
	decision, err := store.CompletionGateDecision()
	if err != nil {
		e.emitHookErrored(turnID, HookErroredPayload{
			Name:       goalCompletionGateName,
			Source:     "builtin",
			EventName:  string(hooks.EventStop),
			DurationMS: time.Since(start).Milliseconds(),
			Error:      err.Error(),
		})
		return "", GoalContinuedPayload{}, false, err
	}
	if decision.BlockStop {
		if strings.TrimSpace(decision.ContinuePrompt) == "" {
			err := fmt.Errorf("goal state: completion gate requested block_stop without continue_prompt")
			e.emitHookErrored(turnID, HookErroredPayload{
				Name:       goalCompletionGateName,
				Source:     "builtin",
				EventName:  string(hooks.EventStop),
				DurationMS: time.Since(start).Milliseconds(),
				Error:      err.Error(),
			})
			return "", GoalContinuedPayload{}, false, err
		}
		if e.ShouldDeferGoalContinuation != nil && e.ShouldDeferGoalContinuation() {
			e.emitHookCompleted(turnID, HookCompletedPayload{
				Name:       goalCompletionGateName,
				Source:     "builtin",
				EventName:  string(hooks.EventStop),
				DurationMS: time.Since(start).Milliseconds(),
				ExitCode:   0,
			})
			return "", GoalContinuedPayload{}, false, nil
		}
		recorded, err := store.RecordContinuation(decision)
		if err != nil {
			return "", GoalContinuedPayload{}, false, err
		}
		if !recorded {
			e.emitHookCompleted(turnID, HookCompletedPayload{
				Name:       goalCompletionGateName,
				Source:     "builtin",
				EventName:  string(hooks.EventStop),
				DurationMS: time.Since(start).Milliseconds(),
				ExitCode:   0,
			})
			return "", GoalContinuedPayload{}, false, nil
		}
		snapshot, _ := store.StatusSnapshot()
		payload := goalContinuedPayload(decision, snapshot)
		e.emitHookCompleted(turnID, HookCompletedPayload{
			Name:       goalCompletionGateName,
			Source:     "builtin",
			EventName:  string(hooks.EventStop),
			DurationMS: time.Since(start).Milliseconds(),
			ExitCode:   2,
			StdoutLen:  len(decision.ContinuePrompt),
		})
		e.emitGoalUpdated(turnID)
		return decision.ContinuePrompt, payload, true, nil
	}
	e.emitHookCompleted(turnID, HookCompletedPayload{
		Name:       goalCompletionGateName,
		Source:     "builtin",
		EventName:  string(hooks.EventStop),
		DurationMS: time.Since(start).Milliseconds(),
		ExitCode:   0,
	})
	return "", GoalContinuedPayload{}, false, nil
}

func (e *Engine) RunSessionStartHooks(ctx context.Context) error {
	req := e.newHookRequest(hooks.EventSessionStart, "")
	results, err := e.runHooks(ctx, req)
	if err != nil {
		return err
	}
	if denied, reason := hookBlocked(results); denied {
		return hookDeniedError(hooks.EventSessionStart, reason)
	}
	return e.queueHookRuntimeContext(results)
}

func appendHookAdditionalContext(msg llm.Message, results []hooks.Result) llm.Message {
	copied := false
	for _, result := range results {
		if result.ExitCode != 0 {
			continue
		}
		contextText := strings.TrimSpace(result.Stdout)
		if contextText == "" {
			continue
		}
		if !copied {
			msg.Blocks = append([]llm.Block(nil), msg.Blocks...)
			copied = true
		}
		label := result.Hook.Name
		if label == "" {
			label = "hook"
		}
		msg.Blocks = append(msg.Blocks, llm.Block{
			Type: llm.BlockText,
			Text: "Hook additional context (" + label + "):\n" + contextText,
		})
	}
	return msg
}

func hookBlocked(results []hooks.Result) (bool, string) {
	for _, result := range results {
		if result.ExitCode == 2 {
			return true, hookText(result)
		}
	}
	return false, ""
}

func hookReasonSuffix(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return ""
	}
	return ": " + reason
}

func hookDeniedError(event hooks.EventName, reason string) error {
	return fmt.Errorf("hooks: %s denied%s", event, hookReasonSuffix(reason))
}

func stopContinuation(results []hooks.Result) (string, bool) {
	for _, result := range results {
		if result.ExitCode == 2 {
			return hookText(result), true
		}
	}
	return "", false
}

type hookObserver struct {
	engine *Engine
	turnID string
}

func (o hookObserver) HookStarted(hook hooks.CommandHook, req hooks.Request) {
	if o.engine == nil {
		return
	}
	_ = o.engine.emit(events.Event{Type: "hook.started", TurnID: o.turnID, Payload: HookStartedPayload{
		Name:      hook.Name,
		Source:    hook.Source,
		EventName: string(req.EventName),
		ToolName:  req.ToolName,
	}})
}

func (o hookObserver) HookCompleted(result hooks.Result) {
	if o.engine == nil {
		return
	}
	if result.ExitCode == 2 && (result.EventName == hooks.EventPreCompact || result.EventName == hooks.EventPostCompact) {
		o.engine.emitHookErrored(o.turnID, hookErroredPayload(result, fmt.Errorf(
			"hooks: %s hook %q cannot block compaction",
			result.EventName,
			result.Hook.Name,
		)))
		return
	}
	payload := hookCompletedPayload(result)
	o.engine.emitHookCompleted(o.turnID, payload)
}

func (o hookObserver) HookErrored(result hooks.Result, err error) {
	if o.engine == nil {
		return
	}
	payload := hookErroredPayload(result, err)
	o.engine.emitHookErrored(o.turnID, payload)
}

func hookCompletedPayload(result hooks.Result) HookCompletedPayload {
	return HookCompletedPayload{
		Name:          result.Hook.Name,
		Source:        result.Hook.Source,
		EventName:     resultEventName(result),
		ToolName:      resultToolName(result),
		DurationMS:    result.Duration.Milliseconds(),
		ExitCode:      result.ExitCode,
		StdoutLen:     len(result.Stdout),
		StderrLen:     len(result.Stderr),
		StdoutPreview: truncate(result.Stdout, 200),
		StderrPreview: truncate(result.Stderr, 200),
	}
}

func hookErroredPayload(result hooks.Result, err error) HookErroredPayload {
	payload := HookErroredPayload{
		Name:          result.Hook.Name,
		Source:        result.Hook.Source,
		EventName:     resultEventName(result),
		ToolName:      resultToolName(result),
		DurationMS:    result.Duration.Milliseconds(),
		ExitCode:      result.ExitCode,
		Error:         fmt.Sprint(err),
		StdoutLen:     len(result.Stdout),
		StderrLen:     len(result.Stderr),
		StdoutPreview: truncate(result.Stdout, 200),
		StderrPreview: truncate(result.Stderr, 200),
	}
	return payload
}

func (e *Engine) emitHookCompleted(turnID string, payload HookCompletedPayload) {
	if e == nil {
		return
	}
	_ = e.emit(events.Event{Type: "hook.completed", TurnID: turnID, Payload: payload})
	e.appendHookTraceMessage(turnID, hookCompletedTraceText(payload, e.ShowBuiltinHookTraces))
}

func (e *Engine) emitHookErrored(turnID string, payload HookErroredPayload) {
	if e == nil {
		return
	}
	_ = e.emit(events.Event{Type: "hook.errored", TurnID: turnID, Payload: payload})
	e.appendHookTraceMessage(turnID, hookErroredTraceText(payload, e.ShowBuiltinHookTraces))
}

func (e *Engine) appendHookTraceMessage(turnID, text string) {
	if e == nil || strings.TrimSpace(text) == "" {
		return
	}
	sess := e.currentSession()
	if sess == nil {
		return
	}
	msg := llm.TextMessage(llm.RoleSystem, text)
	msg.Kind = llm.MessageKindHookEvent
	persisted, err := sess.AppendAssigned(msg)
	if err != nil {
		return
	}
	_ = e.emit(events.Event{Type: "hook.trace", TurnID: turnID, Payload: HookTracePayload{
		Text:      text,
		MessageID: persisted.ID,
	}})
}

func hookCompletedTraceText(payload HookCompletedPayload, includeBuiltin bool) string {
	if payload.Source == "builtin" && !includeBuiltin {
		return ""
	}
	status := "completed"
	if payload.Source == "builtin" {
		if payload.ExitCode == 2 {
			status = "blocked stop"
		} else {
			status = "allow"
		}
	} else if payload.ExitCode != 0 {
		status = fmt.Sprintf("exit %d", payload.ExitCode)
	}
	return fmt.Sprintf(
		"hook %s %s %s in %dms",
		hookTraceName(payload.Name),
		status,
		hookTraceTarget(payload.EventName, payload.ToolName),
		payload.DurationMS,
	)
}

func hookText(result hooks.Result) string {
	if text := strings.TrimSpace(result.Stdout); text != "" {
		return text
	}
	if result.ExitCode == 2 {
		return strings.TrimSpace(result.Stderr)
	}
	return ""
}

func hookContextLabel(result hooks.Result, kind string) string {
	name := strings.TrimSpace(result.Hook.Name)
	if name == "" {
		name = "hook"
	}
	return "Hook " + kind + " (" + name + "):\n"
}

func (e *Engine) queueHookRuntimeContext(results []hooks.Result) error {
	if e == nil {
		return nil
	}
	var messages []llm.Message
	for _, result := range results {
		if result.ExitCode != 0 {
			continue
		}
		text := strings.TrimSpace(result.Stdout)
		if text == "" {
			continue
		}
		msg := llm.TextMessage(llm.RoleUser, hookContextLabel(result, "additional context")+text)
		msg.ID = randomProvenanceID("hook-context-")
		msg.Kind = llm.MessageKindRuntimeContext
		messages = append(messages, msg)
	}
	if len(messages) == 0 {
		return nil
	}
	turnID := e.PendingInputStatus().TurnID
	payload := provenance.HookContextQueuedPayload{Messages: messages}
	if err := provenance.ValidateHookContextQueued(payload); err != nil {
		return err
	}
	if err := e.emit(events.Event{Type: provenance.HookContextQueuedType, TurnID: turnID, Payload: payload}); err != nil {
		return fmt.Errorf("commit hook runtime context: %w", err)
	}
	tracker := e.requestProvenanceTracker()
	for _, message := range messages {
		tracker.AddQueued(message)
	}
	e.hookRuntimeContextMu.Lock()
	e.pendingHookRuntimeContext = append(e.pendingHookRuntimeContext, messages...)
	e.hookRuntimeContextMu.Unlock()
	return nil
}

func (e *Engine) pendingHookRuntimeContextSnapshot() []llm.Message {
	if e == nil {
		return nil
	}
	e.hookRuntimeContextMu.Lock()
	defer e.hookRuntimeContextMu.Unlock()
	return append([]llm.Message(nil), e.pendingHookRuntimeContext...)
}

func appendToolHookContext(block *llm.Block, results []hooks.Result, includeExitTwo bool) {
	if block == nil {
		return
	}
	for _, result := range results {
		kind := "additional context"
		text := ""
		switch {
		case result.ExitCode == 0:
			text = strings.TrimSpace(result.Stdout)
		case includeExitTwo && result.ExitCode == 2:
			kind = "corrective context"
			text = hookText(result)
		}
		if text == "" {
			continue
		}
		if strings.TrimSpace(block.Content) != "" {
			block.Content += "\n\n"
		}
		block.Content += hookContextLabel(result, kind) + text
	}
}

func appendCompactHookInstructions(instructions string, results []hooks.Result) string {
	for _, result := range results {
		if result.ExitCode != 0 {
			continue
		}
		text := strings.TrimSpace(result.Stdout)
		if text == "" {
			continue
		}
		if strings.TrimSpace(instructions) != "" {
			instructions += "\n\n"
		}
		instructions += hookContextLabel(result, "compact instructions") + text
	}
	return instructions
}

func hookErroredTraceText(payload HookErroredPayload, includeBuiltin bool) string {
	if payload.Source == "builtin" && !includeBuiltin {
		return ""
	}
	return fmt.Sprintf(
		"hook %s failed %s in %dms: %s",
		hookTraceName(payload.Name),
		hookTraceTarget(payload.EventName, payload.ToolName),
		payload.DurationMS,
		payload.Error,
	)
}

func hookTraceName(name string) string {
	if strings.TrimSpace(name) == "" {
		return "unnamed"
	}
	return name
}

func hookTraceTarget(eventName, toolName string) string {
	eventName = strings.TrimSpace(eventName)
	toolName = strings.TrimSpace(toolName)
	if eventName == "" {
		eventName = "event"
	}
	if toolName == "" {
		return eventName
	}
	return eventName + "/" + toolName
}

func resultEventName(result hooks.Result) string {
	if result.EventName != "" {
		return string(result.EventName)
	}
	if len(result.Hook.Events) == 0 {
		return ""
	}
	return string(result.Hook.Events[0])
}

func resultToolName(result hooks.Result) string {
	if result.ToolName != "" {
		return result.ToolName
	}
	if len(result.Hook.Tools) == 0 {
		return ""
	}
	return result.Hook.Tools[0]
}
