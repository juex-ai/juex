package migration

import (
	"encoding/json"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/migration/legacy"
)

func trackingTurnEvent(kind, turn string, payload any) legacy.Fact {
	b, _ := json.Marshal(map[string]any{"type": kind, "turn_id": turn, "payload": payload})
	return legacy.Fact{Type: "event.recorded", Event: b}
}
func trackingTurnFixture() (managedruntime.Scope, legacy.Agent, RuntimeBindings) {
	scope, source, bindings := trackingFixture()
	th := &source.Threads[0]
	th.Commits[1].Facts = append(th.Commits[1].Facts[:2], th.Commits[1].Facts[3:]...)
	th.Commits[1].Facts[0].Message.Kind = llm.MessageKindDirect
	th.Inputs = th.Inputs[1:]
	admit := trackingTurnEvent("turn.admitted", "early-turn", map[string]any{"message_id": "old"})
	start := trackingTurnEvent("turn.started", "early-turn", map[string]any{"message_id": "old", "kind": "direct"})
	th.Commits[1].Facts = append([]legacy.Fact{admit}, th.Commits[1].Facts...)
	th.Commits[1].Facts = append(th.Commits[1].Facts, start)
	th.Commits[2].Facts[0] = trackingTurnEvent("turn.cancelled", "turn-old", map[string]any{"input_ids": []string{"input-current"}})
	terminal := trackingTurnEvent("turn.completed", "early-turn", map[string]any{"input_ids": []string{"input-old"}})
	th.Commits[2].Facts = append([]legacy.Fact{terminal}, th.Commits[2].Facts...)
	return scope, source, bindings
}
func TestInputTrackingConversionRecoversCheckedSingleInputTurn(t *testing.T) {
	scope, source, bindings := trackingTurnFixture()
	value, err := ConvertRuntime(scope, source, bindings)
	if err != nil {
		t.Fatal(err)
	}
	entries := value.Import.Threads[0].InputTracking.Entries
	if len(entries) != 2 || entries[0].CheckedAt == nil || entries[0].InputID != value.Identities.Inputs["0"]["input-old"] || entries[0].MessageID != value.Identities.Messages["0"]["old"] || entries[1].CheckedAt != nil {
		t.Fatal("lost historical check association", entries)
	}
	if len(value.Import.Threads[0].Inputs) != 1 {
		t.Fatal("recreated pruned input receipt")
	}
}
func TestInputTrackingConversionRejectsUnprovenCompletedTurn(t *testing.T) {
	for _, name := range []string{"no-admission", "different-message", "not-direct", "blocked-message", "multiple-inputs", "repeated-terminal", "conflicting-input", "late-terminal", "scope-crossing", "terminal-after-check", "cancelled-after-check", "errored-after-check", "other-input-same-message"} {
		t.Run(name, func(t *testing.T) {
			scope, source, bindings := trackingTurnFixture()
			th := &source.Threads[0]
			switch name {
			case "no-admission":
				th.Commits[1].Facts = th.Commits[1].Facts[1:]
			case "different-message":
				th.Commits[1].Facts[0] = trackingTurnEvent("turn.admitted", "early-turn", map[string]any{"message_id": "kept"})
			case "not-direct":
				th.Commits[1].Facts[1].Message.Kind = llm.MessageKindSystemNotice
			case "blocked-message":
				th.Commits[1].Facts[1].Message.PolicyBlocked = true
			case "multiple-inputs":
				th.Commits[2].Facts[0] = trackingTurnEvent("turn.completed", "early-turn", map[string]any{"input_ids": []string{"input-old", "input-current"}})
			case "repeated-terminal":
				th.Commits[2].Facts = append([]legacy.Fact{th.Commits[2].Facts[0]}, th.Commits[2].Facts...)
			case "conflicting-input":
				th.Commits[1].Facts = append(th.Commits[1].Facts, trackingTurnEvent("input.tracked", "", map[string]any{"input_id": "other", "message_id": "old", "scope_id": "g000001"}))
			case "late-terminal":
				terminal := th.Commits[2].Facts[0]
				th.Commits[2].Facts = append(th.Commits[2].Facts[1:], terminal)
			case "terminal-after-check":
				th.Commits[2].Facts = append(th.Commits[2].Facts, th.Commits[2].Facts[0])
			case "cancelled-after-check":
				th.Commits[2].Facts = append(th.Commits[2].Facts, trackingTurnEvent("turn.cancelled", "early-turn", map[string]any{"input_ids": []string{"input-old"}}))
			case "errored-after-check":
				th.Commits[2].Facts = append(th.Commits[2].Facts, trackingTurnEvent("turn.errored", "early-turn", map[string]any{"input_ids": []string{"input-old"}}))
			case "other-input-same-message":
				th.Commits[2].Facts = append(th.Commits[2].Facts,
					trackingTurnEvent("turn.admitted", "other-turn", map[string]any{"message_id": "old"}),
					trackingTurnEvent("turn.started", "other-turn", map[string]any{"message_id": "old", "kind": "direct"}),
					trackingTurnEvent("turn.completed", "other-turn", map[string]any{"input_ids": []string{"other-input"}}))
			case "scope-crossing":
				th.Commits[1].Facts = append(th.Commits[1].Facts, legacy.Fact{Type: "context.renewed", Seed: &legacy.GenerationSeed{ContextScopeID: "g000003"}})
			}
			if _, err := ConvertRuntime(scope, source, bindings); err == nil {
				t.Fatal("unproven association accepted")
			}
		})
	}
}
