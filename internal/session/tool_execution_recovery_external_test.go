package session_test

import (
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
