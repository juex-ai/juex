package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/provenance"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
)

func (e *Engine) policySets() []*runtimemodule.Set {
	if e == nil {
		return nil
	}
	sessionModules := e.SessionRuntimeSnapshot().Modules
	return []*runtimemodule.Set{sessionModules, e.RuntimeModules}
}

func (e *Engine) policyRuntimeContext() runtimemodule.RuntimeContext {
	if e == nil {
		return runtimemodule.RuntimeContext{}
	}
	ctx := e.RuntimeContext
	if ctx.WorkDir == "" {
		ctx.WorkDir = e.WorkDir
	}
	if ctx.ArtifactDir == "" {
		ctx.ArtifactDir = e.ArtifactDir
	}
	return ctx
}

func (e *Engine) policySessionContext() *runtimemodule.SessionContext {
	if e == nil {
		return nil
	}
	sess := e.currentSession()
	if sess == nil {
		return nil
	}
	return &runtimemodule.SessionContext{
		ID:            sess.ID,
		Dir:           sess.Dir,
		ScratchpadDir: sess.ScratchpadDir(),
	}
}

func (e *Engine) policyObserver(turnID string) runtimemodule.PolicyObserver {
	return modulePolicyObserver{engine: e, turnID: turnID}
}

type modulePolicyObserver struct {
	engine *Engine
	turnID string
}

func (o modulePolicyObserver) Requested(execution runtimemodule.PolicyExecution) error {
	if o.engine == nil {
		return nil
	}
	if err := o.engine.emit(events.Event{Type: "policy.requested", TurnID: o.turnID, Payload: PolicyStartedPayload{
		ModuleID:    execution.ModuleID,
		PolicyPoint: execution.Point,
		Name:        execution.Name,
		Source:      execution.Source,
		ToolName:    execution.ToolName,
	}}); err != nil {
		return err
	}
	return nil
}

func (o modulePolicyObserver) Started(execution runtimemodule.PolicyExecution) {
	if o.engine == nil {
		return
	}
	_ = o.engine.emit(events.Event{Type: "policy.started", TurnID: o.turnID, Payload: PolicyStartedPayload{
		ModuleID:    execution.ModuleID,
		PolicyPoint: execution.Point,
		Name:        execution.Name,
		Source:      execution.Source,
		ToolName:    execution.ToolName,
	}})
}

func (o modulePolicyObserver) Completed(execution runtimemodule.PolicyExecution, result runtimemodule.PolicyResult) {
	if o.engine == nil {
		return
	}
	o.engine.emitPolicyCompleted(o.turnID, PolicyCompletedPayload{
		ModuleID:      execution.ModuleID,
		PolicyPoint:   execution.Point,
		Name:          execution.Name,
		Source:        execution.Source,
		ToolName:      execution.ToolName,
		DurationMS:    result.Duration.Milliseconds(),
		ExitCode:      result.ExitCode,
		StdoutLen:     len(result.Stdout),
		StderrLen:     len(result.Stderr),
		StdoutPreview: truncate(result.Stdout, 200),
		StderrPreview: truncate(result.Stderr, 200),
	})
}

func (o modulePolicyObserver) Errored(execution runtimemodule.PolicyExecution, result runtimemodule.PolicyResult, err error) {
	if o.engine == nil {
		return
	}
	o.engine.emitPolicyErrored(o.turnID, PolicyErroredPayload{
		ModuleID:      execution.ModuleID,
		PolicyPoint:   execution.Point,
		Name:          execution.Name,
		Source:        execution.Source,
		ToolName:      execution.ToolName,
		DurationMS:    result.Duration.Milliseconds(),
		ExitCode:      result.ExitCode,
		Error:         fmt.Sprint(err),
		StdoutLen:     len(result.Stdout),
		StderrLen:     len(result.Stderr),
		StdoutPreview: truncate(result.Stdout, 200),
		StderrPreview: truncate(result.Stderr, 200),
	})
}

func (e *Engine) queuePolicyRuntimeContext(contexts []runtimemodule.PolicyContext) error {
	if e == nil {
		return nil
	}
	var messages []llm.Message
	for _, item := range contexts {
		text := strings.TrimSpace(item.Text)
		if text == "" {
			continue
		}
		msg := llm.TextMessage(llm.RoleUser, item.Label+text)
		msg.ID = randomProvenanceID("policy-context-")
		msg.Kind = llm.MessageKindRuntimeContext
		messages = append(messages, msg)
	}
	if len(messages) == 0 {
		return nil
	}
	turnID := e.PendingInputStatus().TurnID
	payload := provenance.PolicyContextQueuedPayload{Messages: messages}
	if err := provenance.ValidatePolicyContextQueued(payload); err != nil {
		return err
	}
	if err := e.emit(events.Event{Type: provenance.PolicyContextQueuedType, TurnID: turnID, Payload: payload}); err != nil {
		return fmt.Errorf("commit policy runtime context: %w", err)
	}
	tracker := e.requestProvenanceTracker()
	for _, message := range messages {
		tracker.AddQueued(message)
	}
	e.policyRuntimeContextMu.Lock()
	e.pendingPolicyRuntimeContext = append(e.pendingPolicyRuntimeContext, messages...)
	e.policyRuntimeContextMu.Unlock()
	return nil
}

func (e *Engine) RunSessionStartPolicies(ctx context.Context) error {
	contexts, err := runtimemodule.ApplySessionStartPolicies(ctx, runtimemodule.SessionStartRequest{
		Runtime:  e.policyRuntimeContext(),
		Session:  e.policySessionContext(),
		Observer: e.policyObserver(""),
	}, e.policySets()...)
	if err != nil {
		return err
	}
	return e.queuePolicyRuntimeContext(contexts)
}

func appendToolPolicyContext(block *llm.Block, contexts []runtimemodule.PolicyContext) {
	if block == nil {
		return
	}
	for _, item := range contexts {
		text := strings.TrimSpace(item.Text)
		if text == "" {
			continue
		}
		if strings.TrimSpace(block.Content) != "" {
			block.Content += "\n\n"
		}
		block.Content += item.Label + text
	}
}

func policyReasonSuffix(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return ""
	}
	return ": " + reason
}
