package migration

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/migration/legacy"
)

func TestInputTrackingRetainedPolicyRuntimeWireGolden(t *testing.T) {
	scope, source, bindings := trackingFixture()
	scope.TenantID = "00000000-0000-4000-8000-000000000001"
	scope.UserID = "00000000-0000-4000-8000-000000000002"
	scope.FleetID = "00000000-0000-4000-8000-000000000003"
	scope.AgentID = "00000000-0000-4000-8000-000000000004"
	// Pin the complete retained wire contract, including raw check facts and
	// distinct input/message identities. New owner fields must remain omitted.
	for _, policy := range []int{2} {
		value, err := convertRuntimeForPolicy(scope, source, bindings, policy)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(value.Import)
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != "6f8627c56b7f1bde2bfd25c256954e1a519c3d66750d23a62e169118d9102b04" {
			t.Fatalf("retained policy %d wire hash: %s", policy, got)
		}
	}
}

func trackingFixture() (managedruntime.Scope, legacy.Agent, RuntimeBindings) {
	scope, source, bindings := runtimeFixture()
	thread := &source.Threads[0]
	thread.Commits[2].Facts[0].Event = []byte(`{"type":"turn.cancelled","turn_id":"turn-old","payload":{"input_ids":["input-old","input-current"]}}`)
	event := func(kind string, payload any) legacy.Fact {
		data, _ := json.Marshal(map[string]any{"type": kind, "payload": payload})
		return legacy.Fact{Type: "event.recorded", Event: data}
	}
	for _, item := range []struct{ input, message string }{{"input-old", "old"}, {"input-current", "kept"}} {
		thread.Commits[1].Facts = append(thread.Commits[1].Facts, event("input.tracked", map[string]string{"input_id": item.input, "message_id": item.message, "scope_id": "g000001"}))
	}
	check := llm.Message{ID: "check-assistant", Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ToolUseID: "check-old", ToolName: "check_inputs", Input: map[string]any{"input_ids": []string{"input-old"}}}}}
	at := thread.Inputs[0].CreatedAt
	thread.Commits[2].Facts = append(thread.Commits[2].Facts, legacy.Fact{Type: "message.appended", Message: &check}, event("input.checked", sourceInputCheck{InputIDs: []string{"input-old"}, ScopeID: "g000001", CheckedAt: at, MessageID: check.ID, ToolUseID: "check-old"}))
	current := thread.Commits[1].Facts[1].Message
	thread.Inputs = append(thread.Inputs, legacy.Input{ID: "input-current", ScopeID: "g000001", MessageID: current.ID, Message: *current, ModelMessage: current, ProcessedAt: &at, State: "settled", TurnID: "turn-old", CreatedAt: at})
	// Original queue write may lag the committed check. Keep CheckedAt nil.
	thread.Inputs[0].ScopeID = "g000001"
	return scope, source, bindings
}

func TestInputTrackingConversionRestoresPrunedChecksAndCurrentScope(t *testing.T) {
	for _, pruned := range []bool{false, true} {
		scope, source, bindings := trackingFixture()
		if pruned {
			source.Threads[0].Inputs = source.Threads[0].Inputs[1:]
		}
		value, err := ConvertRuntime(scope, source, bindings)
		if err != nil {
			t.Fatal(err)
		}
		entries := value.Import.Threads[0].InputTracking.Entries
		if len(entries) != 2 || entries[0].CheckedAt == nil || entries[1].CheckedAt != nil || entries[0].InputID == entries[0].MessageID || entries[0].CheckMessageID != value.Identities.Messages["0"]["check-assistant"] || entries[1].MessageID != value.Identities.Messages["0"]["kept"] {
			t.Fatal("tracking identity/evidence lost", entries)
		}
		if len(value.Import.Threads[0].Inputs) != len(source.Threads[0].Inputs) {
			t.Fatal("pruned inputs were fabricated")
		}
		old, err := convertRuntimeForPolicy(scope, source, bindings, 2)
		if err != nil || old.Import.Threads[0].InputTracking != nil {
			t.Fatal("old conversion changed", err)
		}
		encoded, _ := json.Marshal(old.Import)
		if strings.Contains(string(encoded), `"input_tracking"`) {
			t.Fatal("old payload acquired optional member")
		}
		again, err := ConvertRuntime(scope, source, bindings)
		if err != nil || !reflect.DeepEqual(value, again) {
			t.Fatal("nondeterministic conversion", err)
		}
	}
}

func TestInputTrackingConversionRejectsUnprovenFacts(t *testing.T) {
	for _, scenario := range []string{"queue-missing", "scope", "check-message", "check-time", "association", "queue-time", "unknown-parameter", "missing-unchecked"} {
		t.Run(scenario, func(t *testing.T) {
			scope, source, bindings := trackingFixture()
			thread := &source.Threads[0]
			switch scenario {
			case "unknown-parameter":
				thread.Commits[2].Facts[1].Message.Blocks[0].Input["input_ids"] = []string{"input-old", "unknown"}
			case "missing-unchecked":
				thread.Commits[2].Facts[1].Message.Blocks[0].Input["input_ids"] = []string{"input-old", "input-current"}
			case "queue-missing":
				thread.Inputs = thread.Inputs[:1]
			case "scope":
				thread.ContextScopeID = "g000002"
			case "check-message":
				thread.Commits[2].Facts[1].Message.Blocks[0].ToolName = "update_notes"
			case "check-time":
				thread.Commits[2].Facts[2].Event = []byte(`{"type":"input.checked","payload":{"input_ids":["input-old"],"scope_id":"g000001","checked_at":"bad","message_id":"check-assistant","tool_use_id":"check-old"}}`)
			case "association":
				thread.Inputs[1].MessageID = "old"
			case "queue-time":
				at := thread.Inputs[0].CreatedAt.Add(1)
				thread.Inputs[0].CheckedAt = &at
			}
			if _, err := ConvertRuntime(scope, source, bindings); err == nil {
				t.Fatal("unproven source accepted")
			}
		})
	}
}

func TestInputTrackingConversionPolicyBindsFullModuleDeclaration(t *testing.T) {
	source, definition, bindings := agentConfigFixture(false)
	for _, enabled := range []bool{false, true} {
		source.Modules["input-tracking"] = enabled
		latest, err := ConvertAgentConfig(source, definition, bindings)
		if err != nil || len(latest.Configuration.Modules) != len(agentpolicy.Capabilities()) || latest.Configuration.Modules[agentpolicy.InputTracking] != enabled {
			t.Fatal("new declaration lost tracking", err)
		}
		old, err := prepareAgentConfigForPolicy(source, definition, bindings, 2)
		if err != nil || len(old.Configuration.Modules) != 14 {
			t.Fatal("old module set changed", err)
		}
		if _, ok := old.Configuration.Modules[agentpolicy.InputTracking]; ok {
			t.Fatal("retry appended new module")
		}
	}
}

func TestInputTrackingConversionOrdersImportedInputsBeforeLiveSequence(t *testing.T) {
	scope, source, bindings := trackingFixture()
	thread := &source.Threads[0]
	// Lifecycle facts collapse into one import.commit record; their count is
	// deliberately larger than the resulting Runtime event sequence.
	for range 40 {
		thread.Commits[1].Facts = append([]legacy.Fact{{Type: "turn.started", TurnID: "historical"}, {Type: "turn.completed", TurnID: "historical"}, {Type: "thread.settled"}}, thread.Commits[1].Facts...)
	}
	converted, err := ConvertRuntime(scope, source, bindings)
	if err != nil {
		t.Fatal(err)
	}
	main := converted.Import.Threads[0]
	for i, item := range main.InputTracking.Entries {
		if item.AcceptedOrder != int64(i+1) || item.AcceptedOrder >= main.Thread.Sequence {
			t.Fatal("source fact ordinal escaped imported sequence", item.AcceptedOrder, main.Thread.Sequence)
		}
	}
}

func TestInputTrackingConversionPreservesQueuePriorityAcrossDelayedDelivery(t *testing.T) {
	scope, source, bindings := trackingFixture()
	thread := &source.Threads[0]
	thread.Commits[2].Facts = thread.Commits[2].Facts[:1]
	thread.Commits[1].Facts[2], thread.Commits[1].Facts[3] = thread.Commits[1].Facts[3], thread.Commits[1].Facts[2]
	value, err := ConvertRuntime(scope, source, bindings)
	if err != nil {
		t.Fatal(err)
	}
	rows := value.Import.Threads[0].InputTracking.Entries
	if len(rows) != 2 || rows[0].InputID != value.Identities.Inputs["0"]["input-old"] || rows[1].InputID != value.Identities.Inputs["0"]["input-current"] {
		t.Fatal("delivery order replaced accepted priority", rows)
	}
}

func TestInputTrackingConversionAcceptsAlreadyCheckedParametersAndEndedScope(t *testing.T) {
	for _, ended := range []bool{false, true} {
		scope, source, bindings := trackingFixture()
		thread := &source.Threads[0]
		if ended {
			generation := legacy.Generation{ID: "g000003", Ordinal: 3, BoundarySeq: 5}
			thread.Metadata.Generations = append(thread.Metadata.Generations, generation)
			thread.Metadata.CurrentGeneration = generation
			thread.Commits = append(thread.Commits, legacy.Commit{Version: 1, Seq: 5, At: thread.Metadata.UpdatedAt, GenerationID: generation.ID, Facts: []legacy.Fact{{Type: "context.renewed", Seed: &legacy.GenerationSeed{Version: 1, ContextScopeID: generation.ID}}}})
			thread.ContextScopeID = generation.ID
			thread.Context = nil
			thread.Inputs = nil
		} else {
			message := llm.Message{ID: "check-next", Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ToolUseID: "next", ToolName: "check_inputs", Input: map[string]any{"input_ids": []string{"input-old", "input-current", "input-old"}}}}}
			payload := sourceInputCheck{InputIDs: []string{"input-current"}, ScopeID: "g000001", CheckedAt: thread.Inputs[0].CreatedAt, MessageID: message.ID, ToolUseID: "next"}
			raw, _ := json.Marshal(map[string]any{"type": "input.checked", "payload": payload})
			thread.Commits[2].Facts = append(thread.Commits[2].Facts, legacy.Fact{Type: "message.appended", Message: &message}, legacy.Fact{Type: "event.recorded", Event: raw})
		}
		value, err := ConvertRuntime(scope, source, bindings)
		if err != nil {
			t.Fatal(ended, err)
		}
		tracking := value.Import.Threads[0].InputTracking
		if ended {
			if tracking.Entries[1].CheckedAt != nil || tracking.Entries[1].ScopeID == tracking.ScopeID {
				t.Fatal("scope end fabricated check", tracking)
			}
		} else if tracking.Entries[1].CheckedAt == nil {
			t.Fatal("valid atomic subset rejected")
		}
	}
}
