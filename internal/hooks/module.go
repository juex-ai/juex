package hooks

import (
	"context"
	"fmt"
	"strings"

	"github.com/juex-ai/juex/internal/llm"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
)

const ModuleID runtimemodule.ID = "hooks"

type PolicyRunner interface {
	Run(context.Context, Request) ([]Result, error)
}

type ModuleOptions struct {
	BaseRequest           Request
	GenerationJournalPath func() string
	GoalState             func() []byte
}

// Module adapts trusted command Hooks to Framework-owned typed policy seams.
// The Runner remains responsible only for command execution and matching.
type Module struct {
	runner                PolicyRunner
	base                  Request
	goalState             func() []byte
	generationJournalPath func() string
}

func NewModule(runner PolicyRunner, opts ModuleOptions) *Module {
	base := opts.BaseRequest
	base.WorkspaceRoots = append([]string(nil), base.WorkspaceRoots...)
	base.GoalState = append([]byte(nil), base.GoalState...)
	base.Observer = nil
	return &Module{
		runner: runner, base: base, goalState: opts.GoalState,
		generationJournalPath: opts.GenerationJournalPath,
	}
}

func (*Module) ID() runtimemodule.ID { return ModuleID }

// PostToolUse Hooks can add context or report an error, but they never replace
// or hide the raw Tool result, so existing live output remains safe to expose.
func (*Module) AllowsLiveToolOutput() bool { return true }

func (m *Module) ApplyThreadStart(ctx context.Context, request runtimemodule.ThreadStartRequest) (runtimemodule.ThreadStartDecision, error) {
	results, err := m.run(ctx, EventThreadStart, request.Observer, func(*Request) {})
	if err != nil {
		return runtimemodule.ThreadStartDecision{}, err
	}
	if denied, reason := blocked(results); denied {
		return runtimemodule.ThreadStartDecision{Reject: true, Reason: reason}, nil
	}
	return runtimemodule.ThreadStartDecision{Context: runtimeContexts(results)}, nil
}

func (m *Module) ApplyTurnInput(ctx context.Context, request runtimemodule.TurnInputRequest) (runtimemodule.TurnInputDecision, error) {
	results, err := m.run(ctx, EventUserPromptSubmit, request.Observer, func(hookRequest *Request) {
		hookRequest.TurnID = request.TurnID
		hookRequest.UserInput = request.Message.FirstText()
	})
	if err != nil {
		return runtimemodule.TurnInputDecision{}, err
	}
	if denied, reason := blocked(results); denied {
		return runtimemodule.TurnInputDecision{Action: runtimemodule.TurnInputReject, Reason: reason}, nil
	}
	message := request.Message
	message.Blocks = append([]llm.Block(nil), message.Blocks...)
	for _, result := range results {
		if result.ExitCode != 0 {
			continue
		}
		text := strings.TrimSpace(result.Stdout)
		if text == "" {
			continue
		}
		message.Blocks = append(message.Blocks, llm.Block{
			Type: llm.BlockText,
			Text: policyContextLabel(result, "additional context") + text,
		})
	}
	return runtimemodule.TurnInputDecision{Action: runtimemodule.TurnInputReplace, Message: message}, nil
}

func (m *Module) ApplyTool(ctx context.Context, request runtimemodule.ToolPolicyRequest) (runtimemodule.ToolPolicyDecision, error) {
	event := EventPreToolUse
	point := runtimemodule.PolicyPointToolBefore
	if request.Stage == runtimemodule.ToolPolicyAfterExecution {
		event = EventPostToolUse
		point = runtimemodule.PolicyPointToolAfter
	}
	results, err := m.runAt(ctx, event, point, request.Observer, func(hookRequest *Request) {
		hookRequest.TurnID = request.TurnID
		hookRequest.ToolName = request.ToolName
		hookRequest.ToolInput = cloneInput(request.Input)
		if request.Stage == runtimemodule.ToolPolicyAfterExecution {
			hookRequest.ToolResult = request.Result.Content
		}
	})
	decision := runtimemodule.ToolPolicyDecision{
		Action:  runtimemodule.ToolPolicyAllow,
		Context: toolContexts(results, request.Stage == runtimemodule.ToolPolicyAfterExecution),
	}
	if request.Stage == runtimemodule.ToolPolicyBeforeExecution {
		if denied, reason := blocked(results); denied {
			decision.Action = runtimemodule.ToolPolicyDeny
			decision.Reason = reason
		}
	}
	return decision, err
}

func (m *Module) EvaluateFinish(ctx context.Context, request runtimemodule.FinishRequest) (runtimemodule.FinishDecision, error) {
	results, err := m.run(ctx, EventStop, request.Observer, func(hookRequest *Request) {
		hookRequest.TurnID = request.TurnID
		hookRequest.UserInput = request.UserInput
	})
	if err != nil {
		return runtimemodule.FinishDecision{}, err
	}
	decision := runtimemodule.FinishDecision{
		Action:  runtimemodule.FinishComplete,
		Context: runtimeContexts(results),
	}
	if prompt, ok := continuation(results); ok {
		if strings.TrimSpace(prompt) == "" {
			return runtimemodule.FinishDecision{}, fmt.Errorf("hooks: Stop hook exited with code 2 without text")
		}
		decision.Action = runtimemodule.FinishContinue
		decision.Continuation = prompt
	}
	return decision, nil
}

func (m *Module) ApplyCompaction(ctx context.Context, request runtimemodule.CompactionPolicyRequest) (runtimemodule.CompactionPolicyDecision, error) {
	event := EventPreCompact
	point := runtimemodule.PolicyPointCompactionBefore
	if request.Stage == runtimemodule.CompactionPolicyAfter {
		event = EventPostCompact
		point = runtimemodule.PolicyPointCompactionAfter
	}
	results, err := m.runAt(ctx, event, point, request.Observer, func(hookRequest *Request) {
		hookRequest.TurnID = request.TurnID
		hookRequest.CompactReason = request.Reason
		hookRequest.CompactAuto = request.Auto
	})
	decision := runtimemodule.CompactionPolicyDecision{}
	if request.Stage == runtimemodule.CompactionPolicyBefore {
		for _, result := range results {
			if result.ExitCode == 0 && strings.TrimSpace(result.Stdout) != "" {
				decision.Instructions = append(decision.Instructions, policyContextLabel(result, "compact instructions")+strings.TrimSpace(result.Stdout))
			}
		}
	} else {
		decision.Context = runtimeContexts(results)
	}
	return decision, err
}

func (m *Module) run(ctx context.Context, event EventName, observer runtimemodule.PolicyObserver, fill func(*Request)) ([]Result, error) {
	return m.runAt(ctx, event, policyPoint(event), observer, fill)
}

func (m *Module) runAt(ctx context.Context, event EventName, point runtimemodule.PolicyPoint, observer runtimemodule.PolicyObserver, fill func(*Request)) ([]Result, error) {
	if m == nil || m.runner == nil {
		return nil, nil
	}
	req := m.request(event)
	fill(&req)
	if matcher, ok := m.runner.(interface {
		Matching(EventName, string) []CommandHook
	}); ok && len(matcher.Matching(event, req.ToolName)) == 0 {
		return nil, nil
	}
	execution := runtimemodule.PolicyExecution{
		Point:    point,
		Name:     string(event),
		Source:   string(ModuleID),
		ToolName: req.ToolName,
	}
	if err := runtimemodule.CheckpointPolicy(observer, execution); err != nil {
		return nil, err
	}
	req.Observer = policyObserver{next: observer, event: event, point: point}
	return m.runner.Run(ctx, req)
}

func (m *Module) request(event EventName) Request {
	req := m.base
	req.EventName = event
	req.WorkspaceRoots = append([]string(nil), m.base.WorkspaceRoots...)
	req.GoalState = append([]byte(nil), m.base.GoalState...)
	req.Observer = nil
	if m.goalState != nil {
		req.GoalState = append([]byte(nil), m.goalState()...)
	}
	if m.generationJournalPath != nil {
		req.GenerationJournalPath = m.generationJournalPath()
	}
	return req
}

type policyObserver struct {
	next  runtimemodule.PolicyObserver
	event EventName
	point runtimemodule.PolicyPoint
}

func (o policyObserver) execution(hook CommandHook, toolName string) runtimemodule.PolicyExecution {
	return runtimemodule.PolicyExecution{
		Point:    o.point,
		Name:     policyDisplayName(o.event, hook.Name),
		Source:   hook.Source,
		ToolName: toolName,
	}
}

func policyDisplayName(event EventName, hookName string) string {
	eventName := strings.TrimSpace(string(event))
	hookName = strings.TrimSpace(hookName)
	if hookName == "" {
		return eventName
	}
	if eventName == "" {
		return hookName
	}
	return eventName + "/" + hookName
}

func (o policyObserver) HookStarted(hook CommandHook, request Request) {
	if o.next != nil {
		o.next.Started(o.execution(hook, request.ToolName))
	}
}

func (o policyObserver) HookCompleted(result Result) {
	if o.next == nil {
		return
	}
	execution := o.execution(result.Hook, result.ToolName)
	policyResult := result.policyResult()
	if result.ExitCode == 2 && (result.EventName == EventPreCompact || result.EventName == EventPostCompact) {
		o.next.Errored(execution, policyResult, fmt.Errorf("hooks: %s hook %q cannot block compaction", result.EventName, result.Hook.Name))
		return
	}
	o.next.Completed(execution, policyResult)
}

func (o policyObserver) HookErrored(result Result, err error) {
	if o.next != nil {
		o.next.Errored(o.execution(result.Hook, result.ToolName), result.policyResult(), err)
	}
}

func (r Result) policyResult() runtimemodule.PolicyResult {
	return runtimemodule.PolicyResult{
		ExitCode: r.ExitCode,
		Stdout:   r.Stdout,
		Stderr:   r.Stderr,
		Duration: r.Duration,
	}
}

func policyPoint(event EventName) runtimemodule.PolicyPoint {
	switch event {
	case EventThreadStart:
		return runtimemodule.PolicyPointThreadStart
	case EventUserPromptSubmit:
		return runtimemodule.PolicyPointTurnInput
	case EventPreToolUse:
		return runtimemodule.PolicyPointToolBefore
	case EventPostToolUse:
		return runtimemodule.PolicyPointToolAfter
	case EventStop:
		return runtimemodule.PolicyPointFinish
	case EventPreCompact:
		return runtimemodule.PolicyPointCompactionBefore
	case EventPostCompact:
		return runtimemodule.PolicyPointCompactionAfter
	default:
		return runtimemodule.PolicyPoint(event)
	}
}

func blocked(results []Result) (bool, string) {
	for _, result := range results {
		if result.ExitCode == 2 {
			return true, resultText(result)
		}
	}
	return false, ""
}

func continuation(results []Result) (string, bool) {
	for _, result := range results {
		if result.ExitCode == 2 {
			return resultText(result), true
		}
	}
	return "", false
}

func resultText(result Result) string {
	if text := strings.TrimSpace(result.Stdout); text != "" {
		return text
	}
	if result.ExitCode == 2 {
		return strings.TrimSpace(result.Stderr)
	}
	return ""
}

func runtimeContexts(results []Result) []runtimemodule.PolicyContext {
	var contexts []runtimemodule.PolicyContext
	for _, result := range results {
		if result.ExitCode != 0 || strings.TrimSpace(result.Stdout) == "" {
			continue
		}
		contexts = append(contexts, runtimemodule.PolicyContext{
			Label: policyContextLabel(result, "additional context"),
			Text:  strings.TrimSpace(result.Stdout),
		})
	}
	return contexts
}

func toolContexts(results []Result, includeExitTwo bool) []runtimemodule.PolicyContext {
	var contexts []runtimemodule.PolicyContext
	for _, result := range results {
		kind := "additional context"
		text := ""
		switch {
		case result.ExitCode == 0:
			text = strings.TrimSpace(result.Stdout)
		case includeExitTwo && result.ExitCode == 2:
			kind = "corrective context"
			text = resultText(result)
		}
		if text == "" {
			continue
		}
		contexts = append(contexts, runtimemodule.PolicyContext{
			Label: policyContextLabel(result, kind),
			Text:  text,
		})
	}
	return contexts
}

func policyContextLabel(result Result, kind string) string {
	name := strings.TrimSpace(result.Hook.Name)
	if name == "" {
		name = "hook"
	}
	return "Hook " + kind + " (" + name + "):\n"
}

func cloneInput(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	cloned := make(map[string]any, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}
