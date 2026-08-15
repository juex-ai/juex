package e2e

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/eventcatalog"
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/prompt"
	"github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/session"
	"github.com/juex-ai/juex/internal/toolevents"
	"github.com/juex-ai/juex/internal/tools"
)

func TestEndToEnd_DurableToolOutcomeResumesWithoutDuplicateExecution(t *testing.T) {
	root := t.TempDir()
	sess, err := session.NewWithOptions(filepath.Join(root, "sessions"), session.Options{
		EventCatalog: eventcatalog.Default(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(llm.TextMessage(llm.RoleUser, "send this once")); err != nil {
		t.Fatal(err)
	}
	assistant, err := sess.AppendAssigned(llm.Message{
		ID: "assistant-recovery", Role: llm.RoleAssistant,
		Blocks: []llm.Block{{
			Type: llm.BlockToolUse, ToolUseID: "remote-call", ToolName: "mcp__remote__send",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	call := toolevents.ToolCallPayload{
		Name: "mcp__remote__send", ToolUseID: "remote-call", Iter: 3,
		CallIndex: 0, MessageID: assistant.ID,
	}
	appendCatalogEvent(t, sess, events.Event{
		Type: "llm.responded", TurnID: "turn-before-crash",
		Payload: runtime.LLMRespondedPayload{
			Iter: 3, MessageID: assistant.ID, Blocks: assistant.Blocks,
			ToolCalls: []toolevents.ToolCallPayload{call},
		},
	})
	appendCatalogEvent(t, sess, events.Event{Type: toolevents.RequestedType, TurnID: "turn-before-crash", Payload: toolevents.Requested(call)})
	appendCatalogEvent(t, sess, events.Event{Type: toolevents.RunningType, TurnID: "turn-before-crash", Payload: toolevents.Running(call)})
	completed := toolevents.Completed(call, 60, len("remote side effect committed"), "remote side effect committed", nil)
	completed.Outcome = &toolevents.RecordedOutcome{
		MessageID: "tool-result-recovery",
		Block: llm.Block{
			Type: llm.BlockToolResult, ToolUseID: call.ToolUseID,
			ToolName: call.Name, Content: "remote side effect committed",
		},
	}
	appendCatalogEvent(t, sess, events.Event{Type: toolevents.CompletedType, TurnID: "turn-before-crash", Payload: completed})
	sessionDir := sess.Dir
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}

	recovered, err := session.LoadWithOptions(sessionDir, session.Options{EventCatalog: eventcatalog.Default()})
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()

	provider := &bareScriptProvider{steps: []llm.Response{{
		Message:    llm.TextMessage(llm.RoleAssistant, "continued safely"),
		StopReason: llm.StopEndTurn,
	}}}
	registry := tools.NewRegistry()
	toolCalls := 0
	registry.MustRegister(tools.Tool{
		Name: "mcp__remote__send",
		Handler: func(context.Context, map[string]any) (string, error) {
			toolCalls++
			return "unexpected duplicate", nil
		},
	})
	bus := events.NewBus()
	sink := events.NewDurableSink(recovered)
	sink.SetCatalog(eventcatalog.Default())
	bus.SetCommitter(sink)
	defer func() { _ = sink.Close() }()
	engine := &runtime.Engine{
		Provider: provider,
		Tools:    registry,
		Bus:      bus,
		Session:  recovered,
		Prompt: &prompt.Builder{
			AgentsMDDirs: []string{root},
			Now:          func() time.Time { return time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC) },
		},
		WorkDir:     root,
		ArtifactDir: filepath.Join(root, "artifacts"),
	}

	out, err := engine.Turn(context.Background(), "continue after restart")
	if err != nil {
		t.Fatal(err)
	}
	if out != "continued safely" {
		t.Fatalf("output = %q, want continued safely", out)
	}
	if toolCalls != 0 {
		t.Fatalf("remote tool executions after recovery = %d, want 0", toolCalls)
	}
	if len(provider.history) != 1 {
		t.Fatalf("provider requests = %d, want 1", len(provider.history))
	}
	assertSingleRecoveredToolResult(t, provider.history[0], call.ToolUseID, "remote side effect committed")
	assertSingleRecoveredToolResult(t, recovered.History, call.ToolUseID, "remote side effect committed")
}

func appendCatalogEvent(t *testing.T, sess *session.Session, event events.Event) {
	t.Helper()
	prepared, err := eventcatalog.Default().Prepare(events.Normalize(event))
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.AppendEvent(prepared); err != nil {
		t.Fatal(err)
	}
}

func assertSingleRecoveredToolResult(t *testing.T, messages []llm.Message, toolUseID, content string) {
	t.Helper()
	count := 0
	for _, message := range messages {
		for _, block := range message.Blocks {
			if block.Type != llm.BlockToolResult || block.ToolUseID != toolUseID {
				continue
			}
			count++
			if block.Content != content || block.IsError {
				t.Fatalf("recovered tool result = %+v", block)
			}
		}
	}
	if count != 1 {
		t.Fatalf("tool results for %s = %d, want 1; history=%+v", toolUseID, count, messages)
	}
}
