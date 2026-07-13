package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/hooks"
	"github.com/juex-ai/juex/internal/llm"
)

func TestHookTraceMessageIsUIOnly(t *testing.T) {
	runner, err := hooks.NewRunner(hooks.Config{Commands: []hooks.CommandHook{{
		Name:    "fake",
		Events:  []hooks.EventName{hooks.EventUserPromptSubmit},
		Command: runtimeHookCommand("ok"),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	prov := &mockProvider{script: []llm.Response{{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn}}}
	eng, bus := newEngine(t, prov, false)
	eng.Hooks = runner
	var traceEvent HookTracePayload
	bus.Subscribe("hook.trace", func(e events.Event) {
		payload, _ := e.Payload.(HookTracePayload)
		traceEvent = payload
	})

	if _, err := eng.Turn(context.Background(), "start"); err != nil {
		t.Fatal(err)
	}
	var trace *llm.Message
	for i := range eng.Session.History {
		message := &eng.Session.History[i]
		if message.Kind == llm.MessageKindHookEvent {
			trace = message
			break
		}
	}
	if trace == nil {
		t.Fatalf("missing hook trace message in history: %+v", eng.Session.History)
	}
	if trace.Role != llm.RoleSystem || !strings.Contains(trace.FirstText(), "hook fake completed UserPromptSubmit") {
		t.Fatalf("hook trace message = %+v", *trace)
	}
	if !strings.Contains(traceEvent.Text, "hook fake completed UserPromptSubmit") {
		t.Fatalf("hook trace event = %+v", traceEvent)
	}
	for _, history := range prov.histories {
		for _, message := range history {
			if message.Kind == llm.MessageKindHookEvent {
				t.Fatalf("hook trace leaked into provider context: %+v", history)
			}
		}
	}
}

func TestBuiltinHookTraceTextRequiresPolicy(t *testing.T) {
	payload := HookCompletedPayload{
		Name:       goalCompletionGateName,
		Source:     "builtin",
		EventName:  string(hooks.EventStop),
		DurationMS: 3,
		Decision:   string(hooks.DecisionAllow),
	}
	if got := hookCompletedTraceText(payload, false); got != "" {
		t.Fatalf("builtin trace without policy = %q", got)
	}
	got := hookCompletedTraceText(payload, true)
	if !strings.Contains(got, "hook goal-completion-gate allow Stop in 3ms") {
		t.Fatalf("builtin trace with policy = %q", got)
	}
}

func TestBuiltinHookTraceMessageRequiresPolicy(t *testing.T) {
	payload := HookCompletedPayload{
		Name:       goalCompletionGateName,
		Source:     "builtin",
		EventName:  string(hooks.EventStop),
		DurationMS: 3,
		Decision:   string(hooks.DecisionAllow),
	}
	eng, bus := newEngine(t, &mockProvider{}, false)
	var traces []HookTracePayload
	bus.Subscribe("hook.trace", func(e events.Event) {
		payload, _ := e.Payload.(HookTracePayload)
		traces = append(traces, payload)
	})

	eng.emitHookCompleted("turn-1", payload)
	if len(traces) != 0 {
		t.Fatalf("builtin trace should be hidden by default: %+v", traces)
	}
	for _, message := range eng.Session.History {
		if message.Kind == llm.MessageKindHookEvent {
			t.Fatalf("builtin trace leaked without policy: %+v", message)
		}
	}

	eng.ShowBuiltinHookTraces = true
	eng.emitHookCompleted("turn-2", payload)
	if len(traces) != 1 || !strings.Contains(traces[0].Text, "hook goal-completion-gate allow Stop in 3ms") {
		t.Fatalf("builtin trace event with policy = %+v", traces)
	}
	var hookEvents int
	for _, message := range eng.Session.History {
		if message.Kind == llm.MessageKindHookEvent {
			hookEvents++
		}
	}
	if hookEvents != 1 {
		t.Fatalf("hook event messages = %d, history = %+v", hookEvents, eng.Session.History)
	}
}
