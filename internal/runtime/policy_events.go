package runtime

import (
	"fmt"
	"strings"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
)

func (e *Engine) emitPolicyCompleted(turnID string, payload PolicyCompletedPayload) {
	if e == nil {
		return
	}
	_ = e.emit(events.Event{Type: "policy.completed", TurnID: turnID, Payload: payload})
	e.appendPolicyTraceMessage(turnID, policyCompletedTraceText(payload, e.ShowBuiltinPolicyTraces))
}

func (e *Engine) emitPolicyErrored(turnID string, payload PolicyErroredPayload) {
	if e == nil {
		return
	}
	_ = e.emit(events.Event{Type: "policy.errored", TurnID: turnID, Payload: payload})
	e.appendPolicyTraceMessage(turnID, policyErroredTraceText(payload, e.ShowBuiltinPolicyTraces))
}

func (e *Engine) appendPolicyTraceMessage(turnID, text string) {
	if e == nil || strings.TrimSpace(text) == "" {
		return
	}
	sess := e.currentSession()
	if sess == nil {
		return
	}
	msg := llm.TextMessage(llm.RoleSystem, text)
	msg.Kind = llm.MessageKindPolicyEvent
	persisted, err := sess.AppendAssigned(msg)
	if err != nil {
		return
	}
	_ = e.emit(events.Event{Type: "policy.trace", TurnID: turnID, Payload: PolicyTracePayload{
		Text:      text,
		MessageID: persisted.ID,
	}})
}

func policyCompletedTraceText(payload PolicyCompletedPayload, includeBuiltin bool) string {
	if payload.Source == "builtin" && !includeBuiltin {
		return ""
	}
	status := "completed"
	if payload.Source == "builtin" {
		if payload.ExitCode == 2 {
			status = "blocked"
		} else {
			status = "allow"
		}
	} else if payload.ExitCode != 0 {
		status = fmt.Sprintf("exit %d", payload.ExitCode)
	}
	return fmt.Sprintf(
		"policy %s %s %s in %dms",
		policyTraceName(string(payload.ModuleID), payload.Name),
		status,
		policyTraceTarget(string(payload.PolicyPoint), payload.ToolName),
		payload.DurationMS,
	)
}

func policyErroredTraceText(payload PolicyErroredPayload, includeBuiltin bool) string {
	if payload.Source == "builtin" && !includeBuiltin {
		return ""
	}
	return fmt.Sprintf(
		"policy %s failed %s in %dms: %s",
		policyTraceName(string(payload.ModuleID), payload.Name),
		policyTraceTarget(string(payload.PolicyPoint), payload.ToolName),
		payload.DurationMS,
		payload.Error,
	)
}

func policyTraceName(moduleID, name string) string {
	owner := strings.TrimSpace(moduleID)
	displayName := strings.TrimSpace(name)
	if owner == "" {
		owner = "unknown"
	}
	if displayName == "" {
		return owner
	}
	return owner + "/" + displayName
}

func policyTraceTarget(point, toolName string) string {
	policyPoint := strings.TrimSpace(point)
	toolName = strings.TrimSpace(toolName)
	if policyPoint == "" {
		policyPoint = "checkpoint"
	}
	if toolName == "" {
		return policyPoint
	}
	return policyPoint + "/" + toolName
}

func (e *Engine) pendingPolicyRuntimeContextSnapshot() []llm.Message {
	if e == nil {
		return nil
	}
	e.policyRuntimeContextMu.Lock()
	defer e.policyRuntimeContextMu.Unlock()
	return append([]llm.Message(nil), e.pendingPolicyRuntimeContext...)
}
