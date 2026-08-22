package runtime

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/provenance"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/toolevents"
	"github.com/juex-ai/juex/internal/tools"
)

type lifecycleOrder struct {
	mu    sync.Mutex
	steps []string
}

func (o *lifecycleOrder) add(step string) {
	o.mu.Lock()
	o.steps = append(o.steps, step)
	o.mu.Unlock()
}

func (o *lifecycleOrder) snapshot() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.steps...)
}

type lifecyclePolicyModule struct {
	order *lifecycleOrder
}

func (*lifecyclePolicyModule) ID() runtimemodule.ID { return "lifecycle-policy" }

func (m *lifecyclePolicyModule) ApplyTurnInput(_ context.Context, _ runtimemodule.TurnInputRequest) (runtimemodule.TurnInputDecision, error) {
	m.order.add("policy.turn_input")
	return runtimemodule.TurnInputDecision{Action: runtimemodule.TurnInputAllow}, nil
}

func (m *lifecyclePolicyModule) ApplyTool(_ context.Context, request runtimemodule.ToolPolicyRequest) (runtimemodule.ToolPolicyDecision, error) {
	m.order.add("policy.tool." + string(request.Stage))
	return runtimemodule.ToolPolicyDecision{Action: runtimemodule.ToolPolicyAllow}, nil
}

func (m *lifecyclePolicyModule) EvaluateFinish(_ context.Context, _ runtimemodule.FinishRequest) (runtimemodule.FinishDecision, error) {
	m.order.add("policy.finish")
	return runtimemodule.FinishDecision{Action: runtimemodule.FinishComplete}, nil
}

type observablePolicyModule struct{}

func (*observablePolicyModule) ID() runtimemodule.ID { return "quota-policy" }

func (*observablePolicyModule) ApplyTurnInput(_ context.Context, request runtimemodule.TurnInputRequest) (runtimemodule.TurnInputDecision, error) {
	execution := runtimemodule.PolicyExecution{
		ModuleID: "forged-owner",
		Point:    runtimemodule.PolicyPoint("forged-point"),
		Name:     "quota-check",
		Source:   "project",
	}
	if err := runtimemodule.CheckpointPolicy(request.Observer, execution); err != nil {
		return runtimemodule.TurnInputDecision{}, err
	}
	request.Observer.Started(execution)
	request.Observer.Completed(execution, runtimemodule.PolicyResult{})
	return runtimemodule.TurnInputDecision{Action: runtimemodule.TurnInputAllow}, nil
}

func TestNonHookPolicyFactsUseOwnedNeutralVocabulary(t *testing.T) {
	eng, bus := newEngine(t, &mockProvider{}, false)
	policyModule := &observablePolicyModule{}
	set, err := runtimemodule.BuildRuntimeSet(context.Background(), []runtimemodule.RuntimeFactorySpec{{
		ID:      policyModule.ID(),
		Enabled: true,
		New: func(context.Context, runtimemodule.RuntimeContext) (runtimemodule.Module, error) {
			return policyModule, nil
		},
	}}, runtimemodule.RuntimeContext{WorkDir: eng.WorkDir}, runtimemodule.ToolContext{})
	if err != nil {
		t.Fatal(err)
	}

	var requested PolicyStartedPayload
	var started PolicyStartedPayload
	var completed PolicyCompletedPayload
	bus.Subscribe("policy.requested", func(event events.Event) { requested, _ = event.Payload.(PolicyStartedPayload) })
	bus.Subscribe("policy.started", func(event events.Event) { started, _ = event.Payload.(PolicyStartedPayload) })
	bus.Subscribe("policy.completed", func(event events.Event) { completed, _ = event.Payload.(PolicyCompletedPayload) })

	_, err = runtimemodule.ApplyTurnInputPolicies(context.Background(), runtimemodule.TurnInputRequest{
		Runtime:  eng.policyRuntimeContext(),
		Session:  eng.policySessionContext(),
		TurnID:   "turn-policy",
		Message:  llm.TextMessage(llm.RoleUser, "hello"),
		Observer: eng.policyObserver("turn-policy"),
	}, set)
	if err != nil {
		t.Fatal(err)
	}

	for name, payload := range map[string]PolicyStartedPayload{"requested": requested, "started": started} {
		if payload.ModuleID != "quota-policy" || payload.PolicyPoint != runtimemodule.PolicyPointTurnInput || payload.Name != "quota-check" || payload.Source != "project" {
			t.Fatalf("%s payload = %+v", name, payload)
		}
	}
	if completed.ModuleID != "quota-policy" || completed.PolicyPoint != runtimemodule.PolicyPointTurnInput || completed.Name != "quota-check" {
		t.Fatalf("completed payload = %+v", completed)
	}
	raw, err := json.Marshal(completed)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == "" || json.Valid(raw) == false {
		t.Fatalf("completed payload JSON = %q", raw)
	}
	if bytesContainAny(raw, []string{"hook", "event_name", "UserPromptSubmit"}) {
		t.Fatalf("non-hook policy payload leaked hook vocabulary: %s", raw)
	}
}

func bytesContainAny(raw []byte, values []string) bool {
	for _, value := range values {
		if strings.Contains(string(raw), value) {
			return true
		}
	}
	return false
}

type lifecycleProvider struct {
	order     *lifecycleOrder
	responses []llm.Response
}

func (*lifecycleProvider) Name() string { return "lifecycle-provider" }

func (p *lifecycleProvider) Complete(context.Context, string, []llm.Message, []llm.ToolSpec) (llm.Response, error) {
	p.order.add("provider.call")
	response := p.responses[0]
	p.responses = p.responses[1:]
	return response, nil
}

func TestTypedPolicyLifecycleGoldenOrder(t *testing.T) {
	order := &lifecycleOrder{}
	provider := &lifecycleProvider{order: order, responses: []llm.Response{
		{
			Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{
				Type: llm.BlockToolUse, ToolUseID: "call-1", ToolName: "ordered_tool", Input: map[string]any{},
			}}},
			StopReason: llm.StopToolUse,
		},
		{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, provider, false)
	eng.Tools.MustRegister(tools.Tool{
		Name: "ordered_tool",
		Handler: func(context.Context, map[string]any) (string, error) {
			order.add("tool.handler")
			return "ok", nil
		},
	})
	policyModule := &lifecyclePolicyModule{order: order}
	set, err := runtimemodule.BuildRuntimeSet(context.Background(), []runtimemodule.RuntimeFactorySpec{{
		ID:      policyModule.ID(),
		Enabled: true,
		New: func(context.Context, runtimemodule.RuntimeContext) (runtimemodule.Module, error) {
			return policyModule, nil
		},
	}}, runtimemodule.RuntimeContext{WorkDir: eng.WorkDir}, runtimemodule.ToolContext{})
	if err != nil {
		t.Fatal(err)
	}
	eng.RuntimeModules = set
	eng.RuntimeContext = runtimemodule.RuntimeContext{WorkDir: eng.WorkDir}

	for _, eventType := range []string{
		TurnAdmittedType,
		"transcript.repaired",
		"turn.started",
		provenance.RequestEpochType,
		"llm.responded",
		toolevents.RequestedType,
		toolevents.RunningType,
		toolevents.CompletedType,
		"finish.attempted",
		"turn.completed",
	} {
		typeCopy := eventType
		bus.Subscribe(typeCopy, func(events.Event) { order.add(typeCopy) })
	}
	bus.Subscribe(TurnPhaseType, func(event events.Event) {
		payload, _ := event.Payload.(TurnPhasePayload)
		order.add("turn.phase." + string(payload.Phase))
	})

	if err := eng.Session.Append(llm.TextMessage(llm.RoleUser, "unfinished earlier request")); err != nil {
		t.Fatal(err)
	}
	if err := eng.Session.Append(llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{
		Type: llm.BlockToolUse, ToolUseID: "orphan", ToolName: "ordered_tool", Input: map[string]any{},
	}}}); err != nil {
		t.Fatal(err)
	}

	out, err := eng.TurnMessageWithID(context.Background(), llm.TextMessage(llm.RoleUser, "run ordered lifecycle"), "turn-golden")
	if err != nil {
		t.Fatal(err)
	}
	if out != "done" {
		t.Fatalf("output = %q", out)
	}

	want := []string{
		TurnAdmittedType,
		"transcript.repaired",
		"policy.turn_input",
		"turn.started",
		"turn.phase.provider_iteration",
		provenance.RequestEpochType,
		"provider.call",
		"llm.responded",
		"turn.phase.tool_batch",
		toolevents.RequestedType,
		toolevents.RunningType,
		"policy.tool.before_execution",
		"tool.handler",
		"policy.tool.after_execution",
		toolevents.CompletedType,
		"turn.phase.provider_iteration",
		provenance.RequestEpochType,
		"provider.call",
		"llm.responded",
		"finish.attempted",
		"policy.finish",
		"turn.completed",
	}
	if got := order.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("lifecycle order =\n%q\nwant =\n%q", got, want)
	}
}
