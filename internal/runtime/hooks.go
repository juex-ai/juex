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

const goalCompletionGateName = "goal-completion-gate"

func (e *Engine) newHookRequest(event hooks.EventName, turnID string) hooks.Request {
	req := e.HookContext
	req.EventName = event
	req.TurnID = turnID
	if req.SessionID == "" && e.Session != nil {
		req.SessionID = e.Session.ID
	}
	if state, ok := e.goalStateRawLocked(); ok {
		req.GoalState = state
	}
	req.Observer = hookObserver{engine: e, turnID: turnID}
	return req
}

func (e *Engine) runHooks(ctx context.Context, req hooks.Request) ([]hooks.Result, error) {
	if e.Hooks == nil {
		return nil, nil
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
	e.emit(events.Event{Type: "hook.started", TurnID: turnID, Payload: HookStartedPayload{
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
		if err := store.RecordContinuation(decision); err != nil {
			return "", GoalContinuedPayload{}, false, err
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
	return e.appendHookRuntimeContext(results)
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
	o.engine.emit(events.Event{Type: "hook.started", TurnID: o.turnID, Payload: HookStartedPayload{
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
	e.emit(events.Event{Type: "hook.completed", TurnID: turnID, Payload: payload})
	e.appendHookTraceMessage(turnID, hookCompletedTraceText(payload, e.ShowBuiltinHookTraces))
}

func (e *Engine) emitHookErrored(turnID string, payload HookErroredPayload) {
	if e == nil {
		return
	}
	e.emit(events.Event{Type: "hook.errored", TurnID: turnID, Payload: payload})
	e.appendHookTraceMessage(turnID, hookErroredTraceText(payload, e.ShowBuiltinHookTraces))
}

func (e *Engine) appendHookTraceMessage(turnID, text string) {
	if e == nil || e.Session == nil || strings.TrimSpace(text) == "" {
		return
	}
	msg := llm.TextMessage(llm.RoleSystem, text)
	msg.Kind = llm.MessageKindHookEvent
	_ = e.Session.Append(msg)
	e.emit(events.Event{Type: "hook.trace", TurnID: turnID, Payload: HookTracePayload{Text: text}})
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

func (e *Engine) appendHookRuntimeContext(results []hooks.Result) error {
	if e == nil || e.Session == nil {
		return nil
	}
	for _, result := range results {
		if result.ExitCode != 0 {
			continue
		}
		text := strings.TrimSpace(result.Stdout)
		if text == "" {
			continue
		}
		msg := llm.TextMessage(llm.RoleUser, hookContextLabel(result, "additional context")+text)
		msg.Kind = llm.MessageKindRuntimeContext
		if err := e.Session.Append(msg); err != nil {
			return fmt.Errorf("append hook runtime context: %w", err)
		}
	}
	return nil
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
