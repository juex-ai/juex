package e2e

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/eventcatalog"
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/modules/promptcontext"
	"github.com/juex-ai/juex/internal/provenance"
	"github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/thread"
	"github.com/juex-ai/juex/internal/toolevents"
	"github.com/juex-ai/juex/internal/tools"
)

func TestEndToEnd_DurableToolOutcomeResumesWithoutDuplicateExecution(t *testing.T) {
	root := t.TempDir()
	sess, err := thread.New(filepath.Join(root, "threads"))
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
	epoch := testRequestEpoch(t, []llm.Message{sess.History[0]}, "recovery-epoch-1")
	appendCatalogEvent(t, sess, events.Event{
		Type: provenance.RequestEpochType, TurnID: "turn-before-crash",
		Payload: provenance.RequestEpochPayload{Epoch: epoch},
	})
	appendCatalogEvent(t, sess, events.Event{
		Type: "llm.requested", TurnID: "turn-before-crash",
		Payload: runtime.LLMRequestedPayload{Iter: 3, Purpose: "turn", EpochID: epoch.EpochID, RequestDigest: epoch.RequestDigest},
	})
	appendCatalogEvent(t, sess, events.Event{
		Type: "llm.responded", TurnID: "turn-before-crash",
		Payload: runtime.LLMRespondedPayload{
			Iter: 3, MessageID: assistant.ID, EpochID: epoch.EpochID, RequestDigest: epoch.RequestDigest, Blocks: assistant.Blocks,
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

	recovered, err := thread.Load(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = recovered.Close() }()

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
		Thread:   recovered,
		Prompt: e2ePromptBuilder(t, "", []string{root}, root, promptcontext.ShellProfile{}, func() time.Time {
			return time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
		}, recovered),
		WorkDir:  root,
		MediaDir: filepath.Join(root, "artifacts"),
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

func TestEndToEnd_MixedToolBatchRecoveryPreservesOrderWithoutExecution(t *testing.T) {
	root := t.TempDir()
	sess, err := thread.New(filepath.Join(root, "threads"))
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(llm.TextMessage(llm.RoleUser, "run an ordered batch once")); err != nil {
		t.Fatal(err)
	}
	assistant, err := sess.AppendAssigned(llm.Message{
		ID: "assistant-mixed-recovery", Role: llm.RoleAssistant,
		Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "recorded", ToolName: "effect_recorded"},
			{Type: llm.BlockToolUse, ToolUseID: "unknown", ToolName: "effect_unknown", Input: map[string]any{"value": "provider"}},
			{Type: llm.BlockToolUse, ToolUseID: "not-started", ToolName: "effect_not_started"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	calls := []toolevents.ToolCallPayload{
		{Name: "effect_recorded", ToolUseID: "recorded", Iter: 3, CallIndex: 0, MessageID: assistant.ID},
		{Name: "effect_unknown", ToolUseID: "unknown", Input: map[string]any{"value": "provider"}, Iter: 3, CallIndex: 1, MessageID: assistant.ID},
		{Name: "effect_not_started", ToolUseID: "not-started", Iter: 3, CallIndex: 2, MessageID: assistant.ID},
	}
	epoch := testRequestEpoch(t, []llm.Message{sess.History[0]}, "mixed-recovery-epoch")
	appendCatalogEvent(t, sess, events.Event{
		Type: provenance.RequestEpochType, TurnID: "turn-before-crash",
		Payload: provenance.RequestEpochPayload{Epoch: epoch},
	})
	appendCatalogEvent(t, sess, events.Event{
		Type: "llm.requested", TurnID: "turn-before-crash",
		Payload: runtime.LLMRequestedPayload{Iter: 3, Purpose: "turn", EpochID: epoch.EpochID, RequestDigest: epoch.RequestDigest},
	})
	appendCatalogEvent(t, sess, events.Event{
		Type: "llm.responded", TurnID: "turn-before-crash",
		Payload: runtime.LLMRespondedPayload{
			Iter: 3, MessageID: assistant.ID, EpochID: epoch.EpochID, RequestDigest: epoch.RequestDigest,
			Blocks: assistant.Blocks, ToolCalls: calls,
		},
	})
	for _, call := range calls {
		appendCatalogEvent(t, sess, events.Event{Type: toolevents.RequestedType, TurnID: "turn-before-crash", Payload: toolevents.Requested(call)})
	}
	appendCatalogEvent(t, sess, events.Event{Type: toolevents.RunningType, TurnID: "turn-before-crash", Payload: toolevents.Running(calls[0])})
	recorded := toolevents.Completed(calls[0], 60, len("recorded once"), "recorded once", nil)
	recorded.Outcome = &toolevents.RecordedOutcome{
		MessageID: "mixed-tool-results",
		Block: llm.Block{
			Type: llm.BlockToolResult, ToolUseID: calls[0].ToolUseID,
			ToolName: calls[0].Name, Content: "recorded once",
		},
	}
	appendCatalogEvent(t, sess, events.Event{Type: toolevents.CompletedType, TurnID: "turn-before-crash", Payload: recorded})
	appendCatalogEvent(t, sess, events.Event{Type: toolevents.RunningType, TurnID: "turn-before-crash", Payload: toolevents.Running(calls[1])})
	effectiveUnknown := calls[1]
	effectiveUnknown.Input = map[string]any{"value": "effective"}
	appendCatalogEvent(t, sess, events.Event{Type: toolevents.InputResolvedType, TurnID: "turn-before-crash", Payload: toolevents.InputResolved(effectiveUnknown)})
	sessionDir := sess.Dir
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}

	recovered, err := thread.Load(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = recovered.Close() }()
	provider := &bareScriptProvider{steps: []llm.Response{{
		Message: llm.TextMessage(llm.RoleAssistant, "continued safely"), StopReason: llm.StopEndTurn,
	}}}
	registry := tools.NewRegistry()
	toolCalls := 0
	for _, name := range []string{"effect_recorded", "effect_unknown", "effect_not_started"} {
		registry.MustRegister(tools.Tool{Name: name, Handler: func(context.Context, map[string]any) (string, error) {
			toolCalls++
			return "unexpected duplicate", nil
		}})
	}
	bus := events.NewBus()
	sink := events.NewDurableSink(recovered)
	sink.SetCatalog(eventcatalog.Default())
	bus.SetCommitter(sink)
	defer func() { _ = sink.Close() }()
	engine := &runtime.Engine{
		Provider: provider, Tools: registry, Bus: bus, Thread: recovered,
		Prompt: e2ePromptBuilder(t, "", []string{root}, root, promptcontext.ShellProfile{}, func() time.Time {
			return time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
		}, recovered),
		WorkDir: root, MediaDir: filepath.Join(root, "artifacts"),
	}

	out, err := engine.Turn(context.Background(), "continue after mixed recovery")
	if err != nil {
		t.Fatal(err)
	}
	if out != "continued safely" || toolCalls != 0 || len(provider.history) != 1 {
		t.Fatalf("resume = output %q, tool calls %d, provider requests %d", out, toolCalls, len(provider.history))
	}
	var recoveredBatch *llm.Message
	for i := range provider.history[0] {
		message := &provider.history[0][i]
		if message.ID == "mixed-tool-results" {
			recoveredBatch = message
			break
		}
	}
	if recoveredBatch == nil || len(recoveredBatch.Blocks) != 3 {
		t.Fatalf("recovered batch = %+v", recoveredBatch)
	}
	if recoveredBatch.Blocks[0].ToolUseID != "recorded" || recoveredBatch.Blocks[0].Content != "recorded once" || recoveredBatch.Blocks[0].IsError {
		t.Fatalf("recorded result = %+v", recoveredBatch.Blocks[0])
	}
	if recoveredBatch.Blocks[1].ToolUseID != "unknown" || !recoveredBatch.Blocks[1].IsError ||
		!strings.Contains(recoveredBatch.Blocks[1].Content, "TOOL_OUTCOME_UNKNOWN") ||
		!strings.Contains(recoveredBatch.Blocks[1].Content, `Effective input at execution: {"value":"effective"}`) {
		t.Fatalf("unknown result = %+v", recoveredBatch.Blocks[1])
	}
	if recoveredBatch.Blocks[2].ToolUseID != "not-started" || !recoveredBatch.Blocks[2].IsError ||
		!strings.Contains(recoveredBatch.Blocks[2].Content, "TOOL_NOT_STARTED") {
		t.Fatalf("not-started result = %+v", recoveredBatch.Blocks[2])
	}
	for _, call := range calls {
		count := 0
		for _, message := range provider.history[0] {
			for _, block := range message.Blocks {
				if block.Type == llm.BlockToolResult && block.ToolUseID == call.ToolUseID {
					count++
				}
			}
		}
		if count != 1 {
			t.Fatalf("tool results for %s = %d, want 1", call.ToolUseID, count)
		}
	}
}

func appendCatalogEvent(t *testing.T, sess *thread.Thread, event events.Event) {
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
