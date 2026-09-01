package thread

import (
	"reflect"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/toolevents"
)

func TestRepairProtocolTailPreservesProviderOrderAndCrashBoundaries(t *testing.T) {
	t.Parallel()
	target, err := createStandalone(
		t.TempDir()+"/123456",
		"123456",
		"reviewer",
		MainID,
		fixedNow(),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()

	calls := []toolevents.ToolCallPayload{
		{ToolUseID: "recorded", Name: "read", Input: map[string]any{"path": "a.txt"}, Iter: 2, CallIndex: 0, MessageID: "assistant-1"},
		{ToolUseID: "unknown", Name: "write", Input: map[string]any{"path": "b.txt", "content": "declared"}, Iter: 2, CallIndex: 1, MessageID: "assistant-1"},
		{ToolUseID: "not-started", Name: "grep", Input: map[string]any{"pattern": "TODO"}, Iter: 2, CallIndex: 2, MessageID: "assistant-1"},
	}
	assistant := llm.Message{ID: "assistant-1", Role: llm.RoleAssistant, Blocks: []llm.Block{
		{Type: llm.BlockToolUse, ToolUseID: calls[0].ToolUseID, ToolName: calls[0].Name, Input: calls[0].Input},
		{Type: llm.BlockToolUse, ToolUseID: calls[1].ToolUseID, ToolName: calls[1].Name, Input: calls[1].Input},
		{Type: llm.BlockToolUse, ToolUseID: calls[2].ToolUseID, ToolName: calls[2].Name, Input: calls[2].Input},
	}}
	if err := target.Append(assistant); err != nil {
		t.Fatal(err)
	}
	for _, call := range calls {
		appendToolRecoveryEvent(t, target, "turn-1", toolevents.RequestedType, toolevents.Requested(call))
	}

	appendToolRecoveryEvent(t, target, "turn-1", toolevents.RunningType, toolevents.Running(calls[0]))
	recordedBlock := llm.Block{
		Type:      llm.BlockToolResult,
		ToolUseID: "recorded",
		ToolName:  "read",
		Content:   "exact projected result\nwith formatting",
		IsError:   false,
	}
	completed := toolevents.Completed(calls[0], 30, len(recordedBlock.Content), "exact projected", map[string]any{"diagnostic": "raw"})
	completed.Outcome = &toolevents.RecordedOutcome{MessageID: "tool-results-1", Block: recordedBlock}
	appendToolRecoveryEvent(t, target, "turn-1", toolevents.CompletedType, completed)

	appendToolRecoveryEvent(t, target, "turn-1", toolevents.RunningType, toolevents.Running(calls[1]))
	effectiveInput := map[string]any{"path": "b.txt", "content": "policy transformed"}
	resolved := calls[1]
	resolved.Input = effectiveInput
	appendToolRecoveryEvent(t, target, "turn-1", toolevents.InputResolvedType, toolevents.InputResolved(resolved))

	repairs, err := target.RepairProtocolTail("restart")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(repairs), 3; got != want {
		t.Fatalf("repairs = %d, want %d: %#v", got, want, repairs)
	}
	for index, wantID := range []string{"recorded", "unknown", "not-started"} {
		if repairs[index].ToolUseID != wantID {
			t.Fatalf("repair %d id = %q, want %q", index, repairs[index].ToolUseID, wantID)
		}
	}
	if repairs[0].RecoveryCode != "OUTCOME_RECORDED" ||
		repairs[1].RecoveryCode != "TOOL_OUTCOME_UNKNOWN" ||
		repairs[2].RecoveryCode != "TOOL_NOT_STARTED" {
		t.Fatalf("recovery codes = %#v", repairs)
	}
	if !reflect.DeepEqual(repairs[1].EffectiveInput, effectiveInput) {
		t.Fatalf("effective input = %#v, want %#v", repairs[1].EffectiveInput, effectiveInput)
	}

	history := target.ReplaySnapshot().Messages
	result := history[len(history)-1]
	if result.ID != "tool-results-1" || len(result.Blocks) != 3 {
		t.Fatalf("repair message = %#v", result)
	}
	if !reflect.DeepEqual(result.Blocks[0], recordedBlock) {
		t.Fatalf("recorded outcome changed\ngot:  %#v\nwant: %#v", result.Blocks[0], recordedBlock)
	}
	if !strings.Contains(result.Blocks[1].Content, toolOutcomeUnknownContent) ||
		!strings.Contains(result.Blocks[1].Content, `"content":"policy transformed"`) {
		t.Fatalf("unknown outcome = %#v", result.Blocks[1])
	}
	if result.Blocks[2].Content != toolNotStartedContent {
		t.Fatalf("not-started outcome = %#v", result.Blocks[2])
	}

	projected := ProjectProtocolRepairEvents("repair-turn", "restart", repairs)
	if len(projected) != 2 || projected[0].Type != toolevents.OutcomeUnknownType || projected[1].Type != "transcript.repaired" {
		t.Fatalf("projected events = %#v", projected)
	}
	second, err := target.RepairProtocolTail("repeat")
	if err != nil || len(second) != 0 {
		t.Fatalf("second repair = %#v, %v", second, err)
	}
}

func appendToolRecoveryEvent(t *testing.T, target *Thread, turnID, eventType string, payload any) {
	t.Helper()
	if err := target.AppendEvent(events.Event{Type: eventType, TurnID: turnID, Payload: payload}); err != nil {
		t.Fatal(err)
	}
}
