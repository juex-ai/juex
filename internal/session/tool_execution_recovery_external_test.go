package session_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/eventcatalog"
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/session"
	"github.com/juex-ai/juex/internal/toolevents"
)

func TestToolExecutionRecoveryDistinguishesCrashBoundaries(t *testing.T) {
	tests := []struct {
		name        string
		checkpoint  func(*testing.T, *session.Session, llm.Message)
		wantCode    string
		wantUnknown bool
		wantContent string
	}{
		{
			name:     "before declaration",
			wantCode: "TOOL_NOT_STARTED",
		},
		{
			name: "after declaration",
			checkpoint: func(t *testing.T, sess *session.Session, assistant llm.Message) {
				appendExecutionEvent(t, sess, declaredResponseEvent(assistant))
			},
			wantCode: "TOOL_NOT_STARTED",
		},
		{
			name: "after start",
			checkpoint: func(t *testing.T, sess *session.Session, assistant llm.Message) {
				appendExecutionEvent(t, sess, declaredResponseEvent(assistant))
				appendExecutionEvent(t, sess, toolEvent(toolevents.RequestedType, toolevents.RequestedPayload{
					Name: "mcp__remote__send", ToolUseID: "call-1", Iter: 2, MessageID: assistant.ID,
				}))
				appendExecutionEvent(t, sess, toolEvent(toolevents.RunningType, toolevents.RunningPayload{
					Name: "mcp__remote__send", ToolUseID: "call-1", Iter: 2, MessageID: assistant.ID,
				}))
			},
			wantCode:    "TOOL_OUTCOME_UNKNOWN",
			wantUnknown: true,
		},
		{
			name: "after tool return before outcome sync",
			checkpoint: func(t *testing.T, sess *session.Session, assistant llm.Message) {
				appendExecutionEvent(t, sess, declaredResponseEvent(assistant))
				appendExecutionEvent(t, sess, toolEvent(toolevents.RequestedType, toolevents.RequestedPayload{
					Name: "mcp__remote__send", ToolUseID: "call-1", Iter: 2, MessageID: assistant.ID,
				}))
				appendExecutionEvent(t, sess, toolEvent(toolevents.RunningType, toolevents.RunningPayload{
					Name: "mcp__remote__send", ToolUseID: "call-1", Iter: 2, MessageID: assistant.ID,
				}))
			},
			wantCode:    "TOOL_OUTCOME_UNKNOWN",
			wantUnknown: true,
		},
		{
			name: "after outcome commit",
			checkpoint: func(t *testing.T, sess *session.Session, assistant llm.Message) {
				appendExecutionEvent(t, sess, declaredResponseEvent(assistant))
				appendExecutionEvent(t, sess, toolEvent(toolevents.RequestedType, toolevents.RequestedPayload{
					Name: "mcp__remote__send", ToolUseID: "call-1", Iter: 2, MessageID: assistant.ID,
				}))
				appendExecutionEvent(t, sess, toolEvent(toolevents.RunningType, toolevents.RunningPayload{
					Name: "mcp__remote__send", ToolUseID: "call-1", Iter: 2, MessageID: assistant.ID,
				}))
				appendExecutionEvent(t, sess, toolEvent(toolevents.CompletedType, toolevents.CompletedPayload{
					Name: "mcp__remote__send", ToolUseID: "call-1", Iter: 2, MessageID: assistant.ID,
					Outcome: &toolevents.RecordedOutcome{
						MessageID: "result-message-1",
						Block:     llm.Block{Type: llm.BlockToolResult, ToolUseID: "call-1", ToolName: "mcp__remote__send", Content: "sent once"},
					},
				}))
			},
			wantContent: "sent once",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			sess, err := session.NewWithOptions(root, session.Options{EventCatalog: eventcatalog.Default()})
			if err != nil {
				t.Fatal(err)
			}
			if err := sess.Append(llm.TextMessage(llm.RoleUser, "send once")); err != nil {
				t.Fatal(err)
			}
			assistant, err := sess.AppendAssigned(llm.Message{ID: "assistant-message-1", Role: llm.RoleAssistant, Blocks: []llm.Block{{
				Type: llm.BlockToolUse, ToolUseID: "call-1", ToolName: "mcp__remote__send",
			}}})
			if err != nil {
				t.Fatal(err)
			}
			if tt.checkpoint != nil {
				tt.checkpoint(t, sess, assistant)
			}
			dir := sess.Dir
			if err := sess.Close(); err != nil {
				t.Fatal(err)
			}

			recovered, err := session.LoadWithOptions(dir, session.Options{
				RepairTranscript: true,
				EventCatalog:     eventcatalog.Default(),
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = recovered.Close() })
			if got := len(recovered.History); got != 3 {
				t.Fatalf("history len = %d, want 3: %+v", got, recovered.History)
			}
			resultMessage := recovered.History[2]
			if resultMessage.Kind != llm.MessageKindToolResult || len(resultMessage.Blocks) != 1 {
				t.Fatalf("recovered result message = %+v", resultMessage)
			}
			result := resultMessage.Blocks[0]
			if tt.wantContent != "" {
				if result.Content != tt.wantContent || result.IsError || resultMessage.ID != "result-message-1" {
					t.Fatalf("restored outcome = message=%+v block=%+v", resultMessage, result)
				}
			} else if !result.IsError || !strings.Contains(result.Content, tt.wantCode) {
				t.Fatalf("recovery result = %+v, want %s", result, tt.wantCode)
			}

			journal, err := session.ReadEventsWithCatalog(dir, eventcatalog.Default())
			if err != nil {
				t.Fatal(err)
			}
			unknown := 0
			for _, event := range journal {
				if event.Type == toolevents.OutcomeUnknownType {
					unknown++
					payload, ok := event.Payload.(toolevents.OutcomeUnknownPayload)
					if !ok || payload.Iter != 2 || payload.MessageID != assistant.ID || payload.ToolUseID != "call-1" {
						t.Fatalf("unknown event = %+v", event)
					}
				}
			}
			if tt.wantUnknown && unknown != 1 {
				t.Fatalf("unknown events = %d, want 1", unknown)
			}
			if !tt.wantUnknown && unknown != 0 {
				t.Fatalf("unknown events = %d, want 0", unknown)
			}
		})
	}
}

func TestToolExecutionRecoveryUsesSameCrashSemanticsForSizeOneAndMulti(t *testing.T) {
	tests := []struct {
		name        string
		phase       string
		wantCode    string
		wantContent string
	}{
		{name: "before declaration", phase: "uncheckpointed", wantCode: "TOOL_NOT_STARTED"},
		{name: "after declaration", phase: "declared", wantCode: "TOOL_NOT_STARTED"},
		{name: "after start", phase: "started", wantCode: "TOOL_OUTCOME_UNKNOWN"},
		{name: "after outcome before transcript append", phase: "outcome", wantContent: "recorded once"},
	}
	for _, callCount := range []int{1, 3} {
		for _, tt := range tests {
			t.Run(fmt.Sprintf("size_%d/%s", callCount, tt.name), func(t *testing.T) {
				root := t.TempDir()
				sess, err := session.NewWithOptions(root, session.Options{EventCatalog: eventcatalog.Default()})
				if err != nil {
					t.Fatal(err)
				}
				blocks := make([]llm.Block, callCount)
				calls := make([]toolevents.ToolCallPayload, callCount)
				for i := range blocks {
					id := fmt.Sprintf("call-%d", i)
					name := fmt.Sprintf("effect_%d", i)
					blocks[i] = llm.Block{Type: llm.BlockToolUse, ToolUseID: id, ToolName: name}
					calls[i] = toolevents.ToolCallPayload{Name: name, ToolUseID: id, Iter: 5, CallIndex: i, MessageID: "assistant-matrix"}
				}
				assistant, err := sess.AppendAssigned(llm.Message{ID: "assistant-matrix", Role: llm.RoleAssistant, Blocks: blocks})
				if err != nil {
					t.Fatal(err)
				}
				if tt.phase != "uncheckpointed" {
					appendExecutionEvent(t, sess, events.Event{Type: "llm.responded", TurnID: "turn-matrix", Payload: runtime.LLMRespondedPayload{
						Iter: 5, MessageID: assistant.ID, EpochID: "epoch-matrix", RequestDigest: strings.Repeat("a", 64),
						Blocks: assistant.Blocks, ToolCalls: calls,
					}})
					for _, call := range calls {
						appendExecutionEvent(t, sess, toolEvent(toolevents.RequestedType, toolevents.Requested(call)))
					}
				}
				if tt.phase == "started" || tt.phase == "outcome" {
					for _, call := range calls {
						appendExecutionEvent(t, sess, toolEvent(toolevents.RunningType, toolevents.Running(call)))
					}
				}
				if tt.phase == "outcome" {
					for _, call := range calls {
						appendExecutionEvent(t, sess, toolEvent(toolevents.CompletedType, toolevents.CompletedPayload{
							Name: call.Name, ToolUseID: call.ToolUseID, Iter: call.Iter, CallIndex: call.CallIndex, MessageID: call.MessageID,
							Outcome: &toolevents.RecordedOutcome{MessageID: "result-matrix", Block: llm.Block{
								Type: llm.BlockToolResult, ToolUseID: call.ToolUseID, ToolName: call.Name, Content: tt.wantContent,
							}},
						}))
					}
				}
				dir := sess.Dir
				if err := sess.Close(); err != nil {
					t.Fatal(err)
				}

				recovered, err := session.LoadWithOptions(dir, session.Options{RepairTranscript: true, EventCatalog: eventcatalog.Default()})
				if err != nil {
					t.Fatal(err)
				}
				defer recovered.Close()
				result := recovered.History[len(recovered.History)-1]
				if len(result.Blocks) != callCount {
					t.Fatalf("result blocks = %+v", result.Blocks)
				}
				for i, block := range result.Blocks {
					if block.ToolUseID != calls[i].ToolUseID {
						t.Fatalf("result order = %+v", result.Blocks)
					}
					if tt.wantContent != "" {
						if block.Content != tt.wantContent || block.IsError || result.ID != "result-matrix" {
							t.Fatalf("recorded result = message=%+v block=%+v", result, block)
						}
					} else if !block.IsError || !strings.Contains(block.Content, tt.wantCode) {
						t.Fatalf("recovery result = %+v, want %s", block, tt.wantCode)
					}
				}
			})
		}
	}
}

func TestToolExecutionRecoveryPreservesPolicyTransformedInput(t *testing.T) {
	root := t.TempDir()
	sess, err := session.NewWithOptions(root, session.Options{EventCatalog: eventcatalog.Default()})
	if err != nil {
		t.Fatal(err)
	}
	originalInput := map[string]any{"path": "provider.txt"}
	intermediateInput := map[string]any{"path": "intermediate.txt"}
	effectiveInput := map[string]any{"path": "effective.txt"}
	assistant, err := sess.AppendAssigned(llm.Message{ID: "assistant-transformed", Role: llm.RoleAssistant, Blocks: []llm.Block{{
		Type: llm.BlockToolUse, ToolUseID: "call-transformed", ToolName: "write", Input: originalInput,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	call := toolevents.ToolCallPayload{
		Name: "write", Input: originalInput, ToolUseID: "call-transformed",
		Iter: 3, CallIndex: 0, MessageID: assistant.ID,
	}
	appendExecutionEvent(t, sess, declaredResponseEventForCall(assistant, call))
	appendExecutionEvent(t, sess, toolEvent(toolevents.RequestedType, toolevents.Requested(call)))
	appendExecutionEvent(t, sess, toolEvent(toolevents.RunningType, toolevents.Running(call)))
	intermediateCall := call
	intermediateCall.Input = intermediateInput
	appendExecutionEvent(t, sess, toolEvent(toolevents.InputResolvedType, toolevents.InputResolved(intermediateCall)))
	effectiveCall := call
	effectiveCall.Input = effectiveInput
	appendExecutionEvent(t, sess, toolEvent(toolevents.InputResolvedType, toolevents.InputResolved(effectiveCall)))
	dir := sess.Dir
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}

	recovered, err := session.LoadWithOptions(dir, session.Options{RepairTranscript: true, EventCatalog: eventcatalog.Default()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = recovered.Close() })
	result := recovered.History[len(recovered.History)-1].Blocks[0]
	if !result.IsError || !strings.Contains(result.Content, "TOOL_OUTCOME_UNKNOWN") || !strings.Contains(result.Content, `Effective input at execution: {"path":"effective.txt"}`) {
		t.Fatalf("recovery result = %+v", result)
	}

	journal, err := session.ReadEventsWithCatalog(dir, eventcatalog.Default())
	if err != nil {
		t.Fatal(err)
	}
	unknown := 0
	for _, event := range journal {
		if event.Type != toolevents.OutcomeUnknownType {
			continue
		}
		unknown++
		payload, ok := event.Payload.(toolevents.OutcomeUnknownPayload)
		if !ok || !reflect.DeepEqual(payload.Input, effectiveInput) {
			t.Fatalf("outcome unknown payload = %+v", event.Payload)
		}
	}
	if unknown != 1 {
		t.Fatalf("outcome unknown events = %d, want 1", unknown)
	}
}

func TestToolExecutionRecoveryPreservesProviderOrderForMixedBatch(t *testing.T) {
	root := t.TempDir()
	sess, err := session.NewWithOptions(root, session.Options{EventCatalog: eventcatalog.Default()})
	if err != nil {
		t.Fatal(err)
	}
	assistant, err := sess.AppendAssigned(llm.Message{ID: "assistant-batch", Role: llm.RoleAssistant, Blocks: []llm.Block{
		{Type: llm.BlockToolUse, ToolUseID: "shell", ToolName: "exec_command"},
		{Type: llm.BlockToolUse, ToolUseID: "write", ToolName: "write"},
		{Type: llm.BlockToolUse, ToolUseID: "remote", ToolName: "mcp__remote__send"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	appendExecutionEvent(t, sess, events.Event{Type: "llm.responded", TurnID: "turn-batch", Payload: runtime.LLMRespondedPayload{
		Iter: 4, MessageID: assistant.ID, EpochID: "epoch-batch", RequestDigest: strings.Repeat("a", 64), Blocks: assistant.Blocks, ToolCalls: []toolevents.ToolCallPayload{
			{Name: "exec_command", ToolUseID: "shell", Iter: 4, CallIndex: 0, MessageID: assistant.ID},
			{Name: "write", ToolUseID: "write", Iter: 4, CallIndex: 1, MessageID: assistant.ID},
			{Name: "mcp__remote__send", ToolUseID: "remote", Iter: 4, CallIndex: 2, MessageID: assistant.ID},
		},
	}})
	for callIndex, call := range []struct{ id, name string }{{"shell", "exec_command"}, {"write", "write"}, {"remote", "mcp__remote__send"}} {
		appendExecutionEvent(t, sess, toolEvent(toolevents.RequestedType, toolevents.RequestedPayload{
			Name: call.name, ToolUseID: call.id, Iter: 4, CallIndex: callIndex, MessageID: assistant.ID,
		}))
	}
	appendExecutionEvent(t, sess, toolEvent(toolevents.CompletedType, toolevents.CompletedPayload{
		Name: "exec_command", ToolUseID: "shell", Iter: 4, MessageID: assistant.ID,
		Outcome: &toolevents.RecordedOutcome{MessageID: "batch-result", Block: llm.Block{
			Type: llm.BlockToolResult, ToolUseID: "shell", ToolName: "exec_command", Content: "shell done",
		}},
	}))
	appendExecutionEvent(t, sess, toolEvent(toolevents.RunningType, toolevents.RunningPayload{
		Name: "mcp__remote__send", ToolUseID: "remote", Iter: 4, CallIndex: 2, MessageID: assistant.ID,
	}))
	dir := sess.Dir
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}

	recovered, err := session.LoadWithOptions(dir, session.Options{RepairTranscript: true, EventCatalog: eventcatalog.Default()})
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	result := recovered.History[len(recovered.History)-1]
	if len(result.Blocks) != 3 {
		t.Fatalf("result blocks = %+v", result.Blocks)
	}
	if result.Blocks[0].Content != "shell done" || !strings.Contains(result.Blocks[1].Content, "TOOL_NOT_STARTED") || !strings.Contains(result.Blocks[2].Content, "TOOL_OUTCOME_UNKNOWN") {
		t.Fatalf("mixed recovery order/content = %+v", result.Blocks)
	}
}

func TestToolExecutionRecoveryDoesNotReclassifyNormalRecordedOutcomeAsRepair(t *testing.T) {
	root := t.TempDir()
	sess, err := session.NewWithOptions(root, session.Options{EventCatalog: eventcatalog.Default()})
	if err != nil {
		t.Fatal(err)
	}
	assistant, err := sess.AppendAssigned(llm.Message{ID: "assistant-normal", Role: llm.RoleAssistant, Blocks: []llm.Block{{
		Type: llm.BlockToolUse, ToolUseID: "call-normal", ToolName: "read",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	call := toolevents.ToolCallPayload{Name: "read", ToolUseID: "call-normal", MessageID: assistant.ID}
	result := llm.Block{Type: llm.BlockToolResult, ToolUseID: call.ToolUseID, ToolName: call.Name, Content: "normal result"}
	for _, event := range []events.Event{
		declaredResponseEventForCall(assistant, call),
		toolEvent(toolevents.RunningType, toolevents.Running(call)),
		toolEvent(toolevents.CompletedType, toolevents.CompletedPayload{
			Name: call.Name, ToolUseID: call.ToolUseID, MessageID: call.MessageID,
			Outcome: &toolevents.RecordedOutcome{MessageID: "result-normal", Block: result},
		}),
	} {
		appendExecutionEvent(t, sess, event)
	}
	if _, err := sess.AppendAssigned(llm.Message{
		ID: "result-normal", Role: llm.RoleUser, Kind: llm.MessageKindToolResult, Blocks: []llm.Block{result},
	}); err != nil {
		t.Fatal(err)
	}
	dir := sess.Dir
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}

	recovered, err := session.LoadWithOptions(dir, session.Options{RepairTranscript: true, EventCatalog: eventcatalog.Default()})
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	journal, err := session.ReadEventsWithCatalog(dir, eventcatalog.Default())
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range journal {
		if event.Type == "transcript.repaired" {
			t.Fatalf("normal recorded outcome was reclassified as transcript repair: %+v", event)
		}
	}
}

func TestProjectTranscriptRepairEventsUnifiesSessionAndRuntimeFacts(t *testing.T) {
	if projected := session.ProjectTranscriptRepairEvents("recovery-turn", "turn_start", nil); len(projected) != 0 {
		t.Fatalf("empty repairs projected events = %+v", projected)
	}
	repairs := []session.TranscriptRepair{
		{
			ToolUseID: "unknown", ToolName: "mcp__remote__send", TurnID: "tool-turn",
			ProviderIteration: 3, CallIndex: 1, AssistantMessageID: "assistant-1",
			EffectiveInput: map[string]any{"value": "effective"}, RecoveryCode: "TOOL_OUTCOME_UNKNOWN",
		},
		{ToolUseID: "already-recorded", RecoveryCode: "TOOL_OUTCOME_UNKNOWN", OutcomeUnknownRecorded: true},
		{ToolUseID: "not-started", RecoveryCode: "TOOL_NOT_STARTED"},
	}

	projected := session.ProjectTranscriptRepairEvents("recovery-turn", "turn_start", repairs)
	if len(projected) != 2 {
		t.Fatalf("projected events = %+v", projected)
	}
	unknown := projected[0]
	if unknown.Type != toolevents.OutcomeUnknownType || unknown.TurnID != "tool-turn" {
		t.Fatalf("outcome unknown event = %+v", unknown)
	}
	payload, ok := unknown.Payload.(toolevents.OutcomeUnknownPayload)
	if !ok || payload.MessageID != "assistant-1" || payload.ToolUseID != "unknown" || payload.CallIndex != 1 ||
		payload.Error != "TOOL_OUTCOME_UNKNOWN: JueX recorded that this tool call started, but no durable outcome was recorded. It may already have produced external side effects. Do not retry it until the external state has been checked." {
		t.Fatalf("outcome unknown payload = %+v", unknown.Payload)
	}
	final := projected[1]
	if final.Type != "transcript.repaired" || final.TurnID != "recovery-turn" {
		t.Fatalf("transcript repaired event = %+v", final)
	}
	finalPayload, ok := final.Payload.(session.TranscriptRepairedPayload)
	if !ok || finalPayload.Reason != "turn_start" || !reflect.DeepEqual(finalPayload.Repairs, repairs) {
		t.Fatalf("transcript repaired payload = %+v", final.Payload)
	}
}

func declaredResponseEvent(assistant llm.Message) events.Event {
	return events.Event{Type: "llm.responded", TurnID: "turn-1", Payload: runtime.LLMRespondedPayload{
		Iter: 2, MessageID: assistant.ID, EpochID: "epoch-1", RequestDigest: strings.Repeat("a", 64), Blocks: assistant.Blocks,
		ToolCalls: []toolevents.ToolCallPayload{{
			Name: "mcp__remote__send", ToolUseID: "call-1", Iter: 2, MessageID: assistant.ID,
		}},
	}}
}

func declaredResponseEventForCall(assistant llm.Message, call toolevents.ToolCallPayload) events.Event {
	return events.Event{Type: "llm.responded", TurnID: "turn-1", Payload: runtime.LLMRespondedPayload{
		Iter: call.Iter, MessageID: assistant.ID, EpochID: "epoch-1", RequestDigest: strings.Repeat("a", 64), Blocks: assistant.Blocks, ToolCalls: []toolevents.ToolCallPayload{call},
	}}
}

func toolEvent(eventType string, payload any) events.Event {
	return events.Event{Type: eventType, TurnID: "turn-1", Payload: payload}
}

func appendExecutionEvent(t *testing.T, sess *session.Session, event events.Event) {
	t.Helper()
	prepared, err := eventcatalog.Default().Prepare(events.Normalize(event))
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.AppendEvent(prepared); err != nil {
		t.Fatal(err)
	}
}
