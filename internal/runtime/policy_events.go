package runtime

import (
	"fmt"
	"strings"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
)

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

func (e *Engine) pendingHookRuntimeContextSnapshot() []llm.Message {
	if e == nil {
		return nil
	}
	e.hookRuntimeContextMu.Lock()
	defer e.hookRuntimeContextMu.Unlock()
	return append([]llm.Message(nil), e.pendingHookRuntimeContext...)
}
