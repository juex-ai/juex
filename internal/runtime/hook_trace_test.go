package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/hooks"
	"github.com/juex-ai/juex/internal/llm"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
)

func TestPolicyTraceMessageIsUIOnly(t *testing.T) {
	runner, err := hooks.NewRunner(hooks.Config{Commands: []hooks.CommandHook{{
		Name:    "fake",
		Source:  "ext:demo",
		Events:  []hooks.EventName{hooks.EventUserPromptSubmit},
		Command: runtimeHookCommand("ok"),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	prov := &mockProvider{script: []llm.Response{{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn}}}
	eng, bus := newEngine(t, prov, false)
	installHookRunner(t, eng, runner)
	var traceEvent PolicyTracePayload
	bus.Subscribe("policy.trace", func(e events.Event) {
		payload, _ := e.Payload.(PolicyTracePayload)
		traceEvent = payload
	})

	if _, err := eng.Turn(context.Background(), "start"); err != nil {
		t.Fatal(err)
	}
	var trace *llm.Message
	for i := range eng.Session.History {
		message := &eng.Session.History[i]
		if message.Kind == llm.MessageKindPolicyEvent {
			trace = message
			break
		}
	}
	if trace == nil {
		t.Fatalf("missing policy trace message in history: %+v", eng.Session.History)
	}
	if trace.Role != llm.RoleSystem || !strings.Contains(trace.FirstText(), "policy hooks/UserPromptSubmit/fake completed turn_input") {
		t.Fatalf("policy trace message = %+v", *trace)
	}
	if !strings.Contains(traceEvent.Text, "policy hooks/UserPromptSubmit/fake completed turn_input") {
		t.Fatalf("policy trace event = %+v", traceEvent)
	}
	if traceEvent.MessageID == "" || traceEvent.MessageID != trace.ID {
		t.Fatalf("policy trace message id = %q, history id = %q", traceEvent.MessageID, trace.ID)
	}
	for _, history := range prov.histories {
		for _, message := range history {
			if message.Kind == llm.MessageKindPolicyEvent {
				t.Fatalf("policy trace leaked into provider context: %+v", history)
			}
		}
	}
}

func TestBuiltinPolicyTraceTextRequiresPolicy(t *testing.T) {
	payload := PolicyCompletedPayload{
		ModuleID:    GoalModuleID,
		PolicyPoint: runtimemodule.PolicyPointFinish,
		Name:        goalCompletionGateName,
		Source:      "builtin",
		DurationMS:  3,
		ExitCode:    0,
	}
	if got := policyCompletedTraceText(payload, false); got != "" {
		t.Fatalf("builtin trace without policy = %q", got)
	}
	got := policyCompletedTraceText(payload, true)
	if !strings.Contains(got, "policy goal/goal-completion-gate allow finish in 3ms") {
		t.Fatalf("builtin trace with policy = %q", got)
	}
}

func TestBuiltinPolicyTraceMessageRequiresPolicy(t *testing.T) {
	payload := PolicyCompletedPayload{
		ModuleID:    GoalModuleID,
		PolicyPoint: runtimemodule.PolicyPointFinish,
		Name:        goalCompletionGateName,
		Source:      "builtin",
		DurationMS:  3,
		ExitCode:    0,
	}
	eng, bus := newEngine(t, &mockProvider{}, false)
	var traces []PolicyTracePayload
	bus.Subscribe("policy.trace", func(e events.Event) {
		payload, _ := e.Payload.(PolicyTracePayload)
		traces = append(traces, payload)
	})

	eng.emitPolicyCompleted("turn-1", payload)
	if len(traces) != 0 {
		t.Fatalf("builtin trace should be hidden by default: %+v", traces)
	}
	for _, message := range eng.Session.History {
		if message.Kind == llm.MessageKindPolicyEvent {
			t.Fatalf("builtin trace leaked without policy: %+v", message)
		}
	}

	eng.ShowBuiltinPolicyTraces = true
	eng.emitPolicyCompleted("turn-2", payload)
	if len(traces) != 1 || !strings.Contains(traces[0].Text, "policy goal/goal-completion-gate allow finish in 3ms") {
		t.Fatalf("builtin trace event with policy = %+v", traces)
	}
	var policyEvents int
	for _, message := range eng.Session.History {
		if message.Kind == llm.MessageKindPolicyEvent {
			policyEvents++
		}
	}
	if policyEvents != 1 {
		t.Fatalf("policy event messages = %d, history = %+v", policyEvents, eng.Session.History)
	}
}
