package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/hooks"
	"github.com/juex-ai/juex/internal/llm"
)

func (e *Engine) newHookRequest(event hooks.EventName, turnID string) hooks.Request {
	req := e.HookContext
	req.EventName = event
	req.TurnID = turnID
	if req.SessionID == "" && e.Session != nil {
		req.SessionID = e.Session.ID
	}
	req.Observer = hookObserver{engine: e, turnID: turnID}
	return req
}

func (e *Engine) runHooks(ctx context.Context, req hooks.Request) ([]hooks.Result, error) {
	if e.Hooks == nil {
		return nil, nil
	}
	return e.Hooks.Run(ctx, req)
}

func (e *Engine) RunSessionStartHooks(ctx context.Context) error {
	req := e.newHookRequest(hooks.EventSessionStart, "")
	results, err := e.runHooks(ctx, req)
	if err != nil {
		return err
	}
	if denied, reason := hookDenied(results); denied {
		return hookDeniedError(hooks.EventSessionStart, reason)
	}
	return nil
}

func appendHookAdditionalContext(msg llm.Message, results []hooks.Result) llm.Message {
	copied := false
	for _, result := range results {
		contextText := strings.TrimSpace(result.Output.AdditionalContext)
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

func hookDenied(results []hooks.Result) (bool, string) {
	for _, result := range results {
		if result.Output.Decision == hooks.DecisionDeny {
			return true, result.Output.AdditionalContext
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
		if result.Output.BlockStop {
			return result.Output.ContinuePrompt, true
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
	o.engine.emit(events.Event{Type: "hook.completed", TurnID: o.turnID, Payload: hookCompletedPayload(result)})
}

func (o hookObserver) HookErrored(result hooks.Result, err error) {
	if o.engine == nil {
		return
	}
	payload := hookErroredPayload(result, err)
	o.engine.emit(events.Event{Type: "hook.errored", TurnID: o.turnID, Payload: payload})
}

func hookCompletedPayload(result hooks.Result) HookCompletedPayload {
	return HookCompletedPayload{
		Name:                 result.Hook.Name,
		Source:               result.Hook.Source,
		EventName:            resultEventName(result),
		ToolName:             resultToolName(result),
		DurationMS:           result.Duration.Milliseconds(),
		Decision:             string(result.Output.Decision),
		AdditionalContextLen: len(result.Output.AdditionalContext),
		BlockStop:            result.Output.BlockStop,
		ContinuePromptLen:    len(result.Output.ContinuePrompt),
		StdoutLen:            len(result.Stdout),
		StderrLen:            len(result.Stderr),
		StdoutPreview:        truncate(result.Stdout, 200),
		StderrPreview:        truncate(result.Stderr, 200),
	}
}

func hookErroredPayload(result hooks.Result, err error) HookErroredPayload {
	payload := HookErroredPayload{
		Name:          result.Hook.Name,
		Source:        result.Hook.Source,
		EventName:     resultEventName(result),
		ToolName:      resultToolName(result),
		DurationMS:    result.Duration.Milliseconds(),
		Error:         fmt.Sprint(err),
		StdoutLen:     len(result.Stdout),
		StderrLen:     len(result.Stderr),
		StdoutPreview: truncate(result.Stdout, 200),
		StderrPreview: truncate(result.Stderr, 200),
	}
	return payload
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
