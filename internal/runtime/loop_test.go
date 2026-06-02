package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/prompt"
	"github.com/juex-ai/juex/internal/session"
	"github.com/juex-ai/juex/internal/tools"
)

// errorProvider always fails — used to test the engine's error-bubbling.
type errorProvider struct{}

func (errorProvider) Name() string { return "errprov" }
func (errorProvider) Complete(ctx context.Context, sys string, h []llm.Message, t []llm.ToolSpec) (llm.Response, error) {
	return llm.Response{}, fmt.Errorf("boom")
}

// mockProvider scripts a sequence of LLM responses. Each Complete() call
// returns the next item in script.
type mockProvider struct {
	script    []llm.Response
	called    int
	delay     time.Duration
	histories [][]llm.Message
}

func (m *mockProvider) Name() string { return "mock" }

func (m *mockProvider) Complete(ctx context.Context, sys string, history []llm.Message, tools []llm.ToolSpec) (llm.Response, error) {
	if m.delay > 0 {
		select {
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		case <-time.After(m.delay):
		}
	}
	if m.called >= len(m.script) {
		return llm.Response{}, fmt.Errorf("mockProvider: out of script (called=%d)", m.called)
	}
	historyCopy := make([]llm.Message, len(history))
	copy(historyCopy, history)
	m.histories = append(m.histories, historyCopy)
	r := m.script[m.called]
	m.called++
	return r, nil
}

type mockProviderWithErrors struct {
	errs      []error
	responses []llm.Response
	called    int
	histories [][]llm.Message
}

func (m *mockProviderWithErrors) Name() string { return "mock" }

func (m *mockProviderWithErrors) Complete(ctx context.Context, sys string, history []llm.Message, tools []llm.ToolSpec) (llm.Response, error) {
	historyCopy := make([]llm.Message, len(history))
	copy(historyCopy, history)
	m.histories = append(m.histories, historyCopy)
	if m.called < len(m.errs) && m.errs[m.called] != nil {
		err := m.errs[m.called]
		m.called++
		return llm.Response{}, err
	}
	idx := m.called - len(m.errs)
	m.called++
	if idx < 0 || idx >= len(m.responses) {
		return llm.Response{}, fmt.Errorf("mockProviderWithErrors: out of script (called=%d)", m.called)
	}
	return m.responses[idx], nil
}

func newEngine(t *testing.T, prov llm.Provider, builtinTools bool) (*Engine, *events.Bus) {
	t.Helper()
	reg := tools.NewRegistry()
	if builtinTools {
		tools.RegisterBuiltins(reg, "")
	}
	bus := events.NewBus()
	sess, err := session.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })
	sess.SubscribeBus(bus)
	pb := &prompt.Builder{
		AgentsMDDirs: []string{t.TempDir()},
		Now:          func() time.Time { return time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC) },
	}
	return &Engine{
		Provider: prov,
		Tools:    reg,
		Bus:      bus,
		Session:  sess,
		Prompt:   pb,
		MaxIters: 10,
		MaxDur:   30 * time.Second,
	}, bus
}

func TestTurn_CompactionKeepsRecentTailInProviderContext(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "summary"), StopReason: llm.StopEndTurn, Usage: llm.Usage{InputTokens: 10, OutputTokens: 2}},
		{Message: llm.TextMessage(llm.RoleAssistant, "answer"), StopReason: llm.StopEndTurn, Usage: llm.Usage{InputTokens: 20, OutputTokens: 3}},
	}}
	eng, _ := newEngine(t, prov, false)
	eng.ContextWindow = 200
	eng.Compaction = config.DefaultCompactionConfig()
	eng.Compaction.KeepRecentTokens = 80
	eng.Compaction.TailTurns = 1
	eng.Compaction.ReserveTokens = 50
	for _, item := range []struct {
		role llm.Role
		text string
	}{
		{llm.RoleUser, strings.Repeat("old ", 80)},
		{llm.RoleAssistant, "old answer"},
		{llm.RoleUser, "recent question"},
		{llm.RoleAssistant, "recent answer"},
	} {
		if err := eng.Session.Append(llm.TextMessage(item.role, item.text)); err != nil {
			t.Fatal(err)
		}
	}
	out, err := eng.Turn(context.Background(), "latest")
	if err != nil {
		t.Fatal(err)
	}
	if out != "answer" {
		t.Fatalf("out = %q", out)
	}
	second := prov.histories[1]
	if len(second) < 4 {
		t.Fatalf("second provider history too short: %+v", second)
	}
	if second[0].Kind != llm.MessageKindCompact {
		t.Fatalf("first active message kind = %q", second[0].Kind)
	}
	if !strings.Contains(messagesText(second), "recent question") || !strings.Contains(messagesText(second), "latest") {
		t.Fatalf("active context missing retained tail or latest: %+v", second)
	}
}

func TestTurn_CompactionFailureDoesNotAppendMarker(t *testing.T) {
	prov := &mockProviderWithErrors{errs: []error{fmt.Errorf("summary failed")}}
	eng, _ := newEngine(t, prov, false)
	eng.ContextWindow = 100
	if err := eng.Session.Append(llm.TextMessage(llm.RoleUser, strings.Repeat("old ", 80))); err != nil {
		t.Fatal(err)
	}
	_, err := eng.Turn(context.Background(), "latest")
	if err == nil {
		t.Fatal("expected compaction error")
	}
	for _, msg := range eng.Session.History {
		if msg.Kind == llm.MessageKindCompact {
			t.Fatalf("unexpected compact marker after failure: %+v", msg)
		}
	}
}

func TestTurn_OverflowCompactsAndRetriesOnce(t *testing.T) {
	prov := &mockProviderWithErrors{
		errs: []error{fmt.Errorf("context_length_exceeded")},
		responses: []llm.Response{
			{Message: llm.TextMessage(llm.RoleAssistant, "summary"), StopReason: llm.StopEndTurn, Usage: llm.Usage{InputTokens: 1, OutputTokens: 1}},
			{Message: llm.TextMessage(llm.RoleAssistant, "after retry"), StopReason: llm.StopEndTurn, Usage: llm.Usage{InputTokens: 2, OutputTokens: 1}},
		},
	}
	eng, _ := newEngine(t, prov, false)
	eng.ContextWindow = 10000
	if err := eng.Session.Append(llm.TextMessage(llm.RoleUser, strings.Repeat("old ", 400))); err != nil {
		t.Fatal(err)
	}
	out, err := eng.Turn(context.Background(), "latest")
	if err != nil {
		t.Fatal(err)
	}
	if out != "after retry" {
		t.Fatalf("out = %q", out)
	}
	if prov.called != 3 {
		t.Fatalf("provider calls = %d, want normal fail + compact + retry", prov.called)
	}
}

func messagesText(msgs []llm.Message) string {
	var sb strings.Builder
	for _, msg := range msgs {
		for _, block := range msg.Blocks {
			sb.WriteString(block.Text)
			sb.WriteString(block.Content)
		}
	}
	return sb.String()
}

func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func waitSignal(t *testing.T, ch <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

func TestCompact_ReturnsAppendedMessageIDAndMetadata(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "summary"), StopReason: llm.StopEndTurn},
	}}
	eng, _ := newEngine(t, prov, false)
	if err := eng.Session.Append(llm.TextMessage(llm.RoleUser, strings.Repeat("old ", 80))); err != nil {
		t.Fatal(err)
	}

	result, err := eng.Compact(context.Background(), "turn-1", "system", "manual", false)
	if err != nil {
		t.Fatal(err)
	}
	if result.MessageID == "" {
		t.Fatal("missing compact message id")
	}
	compact := eng.Session.History[len(eng.Session.History)-1]
	if compact.ID != result.MessageID {
		t.Fatalf("result message id = %q, compact id = %q", result.MessageID, compact.ID)
	}
	if compact.Compaction == nil || compact.Compaction.Reason != "manual" || compact.Compaction.SummaryChars != len("summary") {
		t.Fatalf("compaction metadata = %+v", compact.Compaction)
	}
}

func TestCompact_RecordsUsageAndActiveContextStats(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{
			Message:    llm.TextMessage(llm.RoleAssistant, "summary"),
			StopReason: llm.StopEndTurn,
			Usage:      llm.Usage{InputTokens: 11, OutputTokens: 3},
		},
	}}
	eng, _ := newEngine(t, prov, false)
	eng.ContextWindow = 1000
	if err := eng.Session.Append(llm.TextMessage(llm.RoleUser, strings.Repeat("old ", 80))); err != nil {
		t.Fatal(err)
	}

	result, err := eng.Compact(context.Background(), "turn-1", "system", "manual", false)
	if err != nil {
		t.Fatal(err)
	}
	info := eng.Session.Info(time.Now())
	if info.TokenUsage.InputTokens != 11 || info.TokenUsage.OutputTokens != 3 {
		t.Fatalf("token usage = %+v", info.TokenUsage)
	}
	if info.ContextUsage == nil {
		t.Fatal("context usage is nil")
	}
	if info.ContextUsage.TotalTokens != result.TokensAfter {
		t.Fatalf("context total = %d, want compact tokens_after %d", info.ContextUsage.TotalTokens, result.TokensAfter)
	}
	if info.ContextUsage.ContextWindow != 1000 {
		t.Fatalf("context window = %d", info.ContextUsage.ContextWindow)
	}
}

func TestTurn_PlainResponse(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{
			Message:    llm.TextMessage(llm.RoleAssistant, "hello user"),
			StopReason: llm.StopEndTurn,
			Usage:      llm.Usage{InputTokens: 10, OutputTokens: 5},
		},
	}}
	eng, bus := newEngine(t, prov, false)
	var eventUsage llm.Usage
	bus.Subscribe("llm.responded", func(e events.Event) {
		p := e.Payload.(map[string]any)
		eventUsage = p["token_usage"].(llm.Usage)
	})
	out, err := eng.Turn(context.Background(), "hi")
	if err != nil {
		t.Fatal(err)
	}
	if out != "hello user" {
		t.Fatalf("got %q", out)
	}
	if len(eng.Session.History) != 2 {
		t.Fatalf("history len = %d", len(eng.Session.History))
	}
	if got := eng.Session.Info(time.Now()).TokenUsage; got != (llm.Usage{InputTokens: 10, OutputTokens: 5}) {
		t.Fatalf("session token usage = %+v", got)
	}
	if eventUsage != (llm.Usage{InputTokens: 10, OutputTokens: 5}) {
		t.Fatalf("event usage = %+v", eventUsage)
	}
}

func TestTurn_LLMRespondedCarriesOrderedBlocks(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockReasoning, Text: "think first"},
			{Type: llm.BlockText, Text: "I will inspect it."},
			{Type: llm.BlockToolUse, ToolUseID: "tu1", ToolName: "echo", Input: map[string]any{"value": "x"}},
			{Type: llm.BlockText, Text: "Then I will continue."},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, false)
	eng.Tools.MustRegister(tools.Tool{
		Name:   "echo",
		Schema: map[string]any{"type": "object"},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			return "echoed", nil
		},
	})

	var got []llm.Block
	bus.Subscribe("llm.responded", func(e events.Event) {
		if got != nil {
			return
		}
		p := e.Payload.(map[string]any)
		blocks, ok := p["blocks"].([]llm.Block)
		if !ok {
			t.Fatalf("llm.responded blocks = %T, want []llm.Block", p["blocks"])
		}
		got = blocks
	})

	if _, err := eng.Turn(context.Background(), "inspect"); err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("blocks len = %d, want 4: %+v", len(got), got)
	}
	wantTypes := []llm.BlockType{llm.BlockReasoning, llm.BlockText, llm.BlockToolUse, llm.BlockText}
	for i, want := range wantTypes {
		if got[i].Type != want {
			t.Fatalf("block %d type = %s, want %s; blocks=%+v", i, got[i].Type, want, got)
		}
	}
	if got[2].ToolName != "echo" || got[3].Text != "Then I will continue." {
		t.Fatalf("ordered block fields not preserved: %+v", got)
	}
}

func TestTurn_RecordsContextUsageForAssistantResponse(t *testing.T) {
	msg := llm.TextMessage(llm.RoleAssistant, "hello user")
	msg.Model = "mock:model"
	prov := &mockProvider{script: []llm.Response{
		{
			Message:    msg,
			StopReason: llm.StopEndTurn,
			Usage:      llm.Usage{InputTokens: 20, OutputTokens: 5},
		},
	}}
	eng, bus := newEngine(t, prov, true)
	eng.ContextWindow = 1000
	var eventContext llm.ContextUsage
	bus.Subscribe("llm.responded", func(e events.Event) {
		p := e.Payload.(map[string]any)
		eventContext = p["context_usage"].(llm.ContextUsage)
	})

	if _, err := eng.Turn(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}

	info := eng.Session.Info(time.Now())
	got := info.ContextUsage
	if got == nil {
		t.Fatal("session context usage is nil")
	}
	if got.Model != "mock:model" {
		t.Fatalf("model = %q", got.Model)
	}
	if got.ContextWindow != 1000 {
		t.Fatalf("context window = %d", got.ContextWindow)
	}
	if got.InputTokens != 20 || got.OutputTokens != 5 || got.TotalTokens != 25 {
		t.Fatalf("tokens = input %d output %d total %d", got.InputTokens, got.OutputTokens, got.TotalTokens)
	}
	if eventContext.Model != got.Model ||
		eventContext.ContextWindow != got.ContextWindow ||
		eventContext.InputTokens != got.InputTokens ||
		eventContext.OutputTokens != got.OutputTokens ||
		eventContext.TotalTokens != got.TotalTokens ||
		len(eventContext.Breakdown) != len(got.Breakdown) {
		t.Fatalf("event context usage = %+v, want %+v", eventContext, *got)
	}
	parts := contextPartsByKey(got.Breakdown)
	for _, key := range []string{"system_prompt", "system_tools", "mcp_tools", "memory_files", "skills", "messages", "response"} {
		if _, ok := parts[key]; !ok {
			t.Fatalf("missing context breakdown part %q in %+v", key, got.Breakdown)
		}
	}
	if parts["system_prompt"].Tokens == 0 {
		t.Fatalf("system prompt tokens = 0")
	}
	if parts["system_tools"].Tokens == 0 {
		t.Fatalf("system tools tokens = 0")
	}
	if parts["messages"].Tokens == 0 {
		t.Fatalf("messages tokens = 0")
	}
	if parts["response"].Tokens != 5 {
		t.Fatalf("response tokens = %d, want 5", parts["response"].Tokens)
	}
}

func contextPartsByKey(parts []llm.ContextUsagePart) map[string]llm.ContextUsagePart {
	out := make(map[string]llm.ContextUsagePart, len(parts))
	for _, part := range parts {
		out[part.Key] = part
	}
	return out
}

func TestTurnMessage_PreservesUserMessageKind(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "received"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, false)
	var payloadKind string
	bus.Subscribe("turn.started", func(e events.Event) {
		p := e.Payload.(map[string]any)
		payloadKind, _ = p["kind"].(string)
	})

	msg := llm.TextMessage(llm.RoleUser, "local:message:hello")
	msg.Kind = llm.MessageKindMCPEvent
	out, err := eng.TurnMessage(context.Background(), msg)
	if err != nil {
		t.Fatal(err)
	}
	if out != "received" {
		t.Fatalf("out = %q", out)
	}
	if got := eng.Session.History[0].Kind; got != llm.MessageKindMCPEvent {
		t.Fatalf("history kind = %q", got)
	}
	if payloadKind != llm.MessageKindMCPEvent {
		t.Fatalf("turn.started kind = %q", payloadKind)
	}
}

func TestTurn_CompactsWhenProjectedContextExceedsThreshold(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "summary of old work"), StopReason: llm.StopEndTurn},
		{Message: llm.TextMessage(llm.RoleAssistant, "answered latest"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, false)
	eng.ContextWindow = 120
	if err := eng.Session.Append(llm.TextMessage(llm.RoleUser, strings.Repeat("old ", 80))); err != nil {
		t.Fatal(err)
	}
	if err := eng.Session.Append(llm.TextMessage(llm.RoleAssistant, strings.Repeat("reply ", 80))); err != nil {
		t.Fatal(err)
	}

	var completed bool
	bus.Subscribe("context.compact.completed", func(e events.Event) {
		completed = true
	})

	out, err := eng.Turn(context.Background(), "latest question")
	if err != nil {
		t.Fatal(err)
	}
	if out != "answered latest" {
		t.Fatalf("out = %q", out)
	}
	if !completed {
		t.Fatal("missing context.compact.completed event")
	}
	if prov.called != 2 {
		t.Fatalf("provider calls = %d, want compact + answer", prov.called)
	}
	if len(eng.Session.History) != 5 {
		t.Fatalf("history len = %d, want old history retained plus compact/user/assistant", len(eng.Session.History))
	}
	compact := eng.Session.History[2]
	if compact.Kind != llm.MessageKindCompact {
		t.Fatalf("compact kind = %q", compact.Kind)
	}
	if !strings.Contains(compact.FirstText(), "summary of old work") {
		t.Fatalf("compact text = %q", compact.FirstText())
	}
	secondCallHistory := prov.histories[1]
	if len(secondCallHistory) < 2 {
		t.Fatalf("second call history len = %d, want compact marker plus active context", len(secondCallHistory))
	}
	if secondCallHistory[0].Kind != llm.MessageKindCompact {
		t.Fatalf("second call first kind = %q", secondCallHistory[0].Kind)
	}
	if got := secondCallHistory[len(secondCallHistory)-1].FirstText(); got != "latest question" {
		t.Fatalf("second call latest text = %q", got)
	}
}

func TestTurn_DoesNotCompactBelowThreshold(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn},
	}}
	eng, _ := newEngine(t, prov, false)
	eng.ContextWindow = 10000
	if err := eng.Session.Append(llm.TextMessage(llm.RoleUser, "small previous turn")); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.Turn(context.Background(), "next"); err != nil {
		t.Fatal(err)
	}
	if prov.called != 1 {
		t.Fatalf("provider calls = %d, want no compact", prov.called)
	}
	if len(prov.histories[0]) != 2 {
		t.Fatalf("history len = %d, want previous + next", len(prov.histories[0]))
	}
}

func TestTurn_PersistsEmptyAssistantResponse(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: nil}, StopReason: llm.StopEndTurn},
	}}
	eng, _ := newEngine(t, prov, false)
	out, err := eng.Turn(context.Background(), "hi")
	if err != nil {
		t.Fatal(err)
	}
	if out != "" {
		t.Fatalf("out = %q, want empty", out)
	}
	if len(eng.Session.History) != 2 {
		t.Fatalf("history len = %d, want user and assistant messages; history=%+v", len(eng.Session.History), eng.Session.History)
	}
	if eng.Session.History[1].Role != llm.RoleAssistant || len(eng.Session.History[1].Blocks) != 0 {
		t.Fatalf("assistant message = %+v, want empty assistant", eng.Session.History[1])
	}
}

func TestTurn_OneToolCallThenEnd(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockText, Text: "ok let me read that"},
			{Type: llm.BlockToolUse, ToolUseID: "tu1", ToolName: "read", Input: map[string]any{"path": "MISSING"}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, true)

	var toolEvents int32
	bus.Subscribe("tool.*", func(e events.Event) { atomic.AddInt32(&toolEvents, 1) })

	out, err := eng.Turn(context.Background(), "read MISSING")
	if err != nil {
		t.Fatal(err)
	}
	if out != "done" {
		t.Fatalf("out = %q", out)
	}

	// History: user, assistant(tool_use), user(tool_result), assistant(done)
	if len(eng.Session.History) != 4 {
		t.Fatalf("history len = %d, %+v", len(eng.Session.History), eng.Session.History)
	}
	tr := eng.Session.History[2]
	if tr.Role != llm.RoleUser || len(tr.Blocks) != 1 || tr.Blocks[0].Type != llm.BlockToolResult {
		t.Fatalf("tool result message wrong: %+v", tr)
	}
	if !tr.Blocks[0].IsError {
		t.Errorf("expected tool error for missing file: %q", tr.Blocks[0].Content)
	}
	if atomic.LoadInt32(&toolEvents) < 2 {
		t.Errorf("expected requested+errored events, got %d", toolEvents)
	}
}

func TestTurn_DrainsPendingInputAfterToolResults(t *testing.T) {
	prov := &mockProvider{
		delay: 50 * time.Millisecond,
		script: []llm.Response{
			{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
				{Type: llm.BlockToolUse, ToolUseID: "tu1", ToolName: "echo", Input: map[string]any{}},
			}}, StopReason: llm.StopToolUse},
			{Message: llm.TextMessage(llm.RoleAssistant, "steered"), StopReason: llm.StopEndTurn},
		},
	}
	eng, bus := newEngine(t, prov, false)
	eng.Tools.MustRegister(tools.Tool{
		Name:   "echo",
		Schema: map[string]any{"type": "object"},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			return "tool-ok", nil
		},
	})
	requested := make(chan struct{}, 1)
	var queued, drained int32
	bus.Subscribe("llm.requested", func(e events.Event) { signal(requested) })
	bus.Subscribe("pending_input.queued", func(e events.Event) { atomic.AddInt32(&queued, 1) })
	bus.Subscribe("pending_input.drained", func(e events.Event) { atomic.AddInt32(&drained, 1) })

	done := make(chan error, 1)
	go func() {
		out, err := eng.Turn(context.Background(), "start")
		if err == nil && out != "steered" {
			err = fmt.Errorf("out = %q", out)
		}
		done <- err
	}()
	waitSignal(t, requested, "llm.requested")
	status, err := eng.EnqueuePendingInput(context.Background(), "please steer")
	if err != nil {
		t.Fatal(err)
	}
	if status.PendingCount != 1 {
		t.Fatalf("pending count = %d", status.PendingCount)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&queued) != 1 || atomic.LoadInt32(&drained) != 1 {
		t.Fatalf("pending events queued=%d drained=%d", queued, drained)
	}
	if len(prov.histories) != 2 {
		t.Fatalf("provider calls = %d, want 2", len(prov.histories))
	}
	second := prov.histories[1]
	if len(second) != 4 {
		t.Fatalf("second history len = %d, history=%+v", len(second), second)
	}
	if second[2].Role != llm.RoleUser || second[2].Blocks[0].Type != llm.BlockToolResult {
		t.Fatalf("tool result not before pending input: %+v", second)
	}
	if got := second[3].FirstText(); got != "please steer" {
		t.Fatalf("pending input text = %q", got)
	}
}

func TestTurn_PendingInputContinuesAfterPlainResponse(t *testing.T) {
	prov := &mockProvider{
		delay: 50 * time.Millisecond,
		script: []llm.Response{
			{Message: llm.TextMessage(llm.RoleAssistant, "first"), StopReason: llm.StopEndTurn},
			{Message: llm.TextMessage(llm.RoleAssistant, "second"), StopReason: llm.StopEndTurn},
		},
	}
	eng, bus := newEngine(t, prov, false)
	requested := make(chan struct{}, 1)
	bus.Subscribe("llm.requested", func(e events.Event) { signal(requested) })

	done := make(chan error, 1)
	go func() {
		out, err := eng.Turn(context.Background(), "start")
		if err == nil && out != "second" {
			err = fmt.Errorf("out = %q", out)
		}
		done <- err
	}()
	waitSignal(t, requested, "llm.requested")
	if _, err := eng.EnqueuePendingInput(context.Background(), "follow up"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(prov.histories) != 2 {
		t.Fatalf("provider calls = %d, want second call for pending input", len(prov.histories))
	}
	second := prov.histories[1]
	if got := second[len(second)-1].FirstText(); got != "follow up" {
		t.Fatalf("second call last message = %q", got)
	}
}

func TestEngine_PendingInputBackpressure(t *testing.T) {
	prov := &mockProvider{
		delay: 80 * time.Millisecond,
		script: []llm.Response{
			{Message: llm.TextMessage(llm.RoleAssistant, "first"), StopReason: llm.StopEndTurn},
			{Message: llm.TextMessage(llm.RoleAssistant, "second"), StopReason: llm.StopEndTurn},
		},
	}
	eng, bus := newEngine(t, prov, false)
	eng.MaxPendingInputs = 1
	requested := make(chan struct{}, 1)
	rejected := make(chan struct{}, 1)
	bus.Subscribe("llm.requested", func(e events.Event) { signal(requested) })
	bus.Subscribe("pending_input.rejected", func(e events.Event) { signal(rejected) })
	done := make(chan error, 1)
	go func() {
		_, err := eng.Turn(context.Background(), "start")
		done <- err
	}()
	waitSignal(t, requested, "llm.requested")
	if _, err := eng.EnqueuePendingInput(context.Background(), "one"); err != nil {
		t.Fatal(err)
	}
	status, err := eng.EnqueuePendingInput(context.Background(), "two")
	if !errors.Is(err, ErrPendingInputQueueFull) {
		t.Fatalf("err = %v, want ErrPendingInputQueueFull", err)
	}
	if status.PendingCount != 1 || status.MaxPendingInputs != 1 {
		t.Fatalf("status = %+v", status)
	}
	waitSignal(t, rejected, "pending_input.rejected")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestTurn_ParallelToolCalls(t *testing.T) {
	const toolCallCount = 3
	started := make(chan struct{}, toolCallCount)
	release := make(chan struct{})
	reg := tools.NewRegistry()
	reg.MustRegister(tools.Tool{
		Name:   "slow",
		Schema: map[string]any{"type": "object"},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			started <- struct{}{}
			select {
			case <-release:
				return "ok", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		},
	})

	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "a", ToolName: "slow", Input: map[string]any{}},
			{Type: llm.BlockToolUse, ToolUseID: "b", ToolName: "slow", Input: map[string]any{}},
			{Type: llm.BlockToolUse, ToolUseID: "c", ToolName: "slow", Input: map[string]any{}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "all done"), StopReason: llm.StopEndTurn},
	}}
	bus := events.NewBus()
	sess, err := session.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })
	pb := &prompt.Builder{AgentsMDDirs: []string{t.TempDir()}, Now: func() time.Time { return time.Now() }}
	eng := &Engine{Provider: prov, Tools: reg, Bus: bus, Session: sess, Prompt: pb}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	releaseClosed := false
	closeRelease := func() {
		if !releaseClosed {
			close(release)
			releaseClosed = true
		}
	}
	defer closeRelease()
	type turnResult struct {
		out string
		err error
	}
	done := make(chan turnResult, 1)
	go func() {
		out, err := eng.Turn(ctx, "x")
		done <- turnResult{out: out, err: err}
	}()
	for i := 0; i < toolCallCount; i++ {
		select {
		case <-started:
		case result := <-done:
			t.Fatalf("turn completed before all tool calls started: out=%q err=%v", result.out, result.err)
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for parallel tool call %d/%d", i+1, toolCallCount)
		}
	}
	closeRelease()
	var result turnResult
	select {
	case result = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for turn to complete after releasing tools")
	}
	out, err := result.out, result.err
	if err != nil {
		t.Fatal(err)
	}
	if out != "all done" {
		t.Fatalf("got %q", out)
	}
	tr := eng.Session.History[2]
	if len(tr.Blocks) != 3 {
		t.Fatalf("expected 3 tool results, got %d", len(tr.Blocks))
	}
	gotIDs := []string{tr.Blocks[0].ToolUseID, tr.Blocks[1].ToolUseID, tr.Blocks[2].ToolUseID}
	wantIDs := []string{"a", "b", "c"}
	for i := range gotIDs {
		if gotIDs[i] != wantIDs[i] {
			t.Fatalf("ordering broken: got %v want %v", gotIDs, wantIDs)
		}
	}
}

func TestTurn_BudgetExceeded(t *testing.T) {
	// Provider keeps issuing tool calls forever; engine should bail.
	loopResp := llm.Response{
		Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "a", ToolName: "echo", Input: map[string]any{}},
		}},
		StopReason: llm.StopToolUse,
	}
	prov := &mockProvider{script: []llm.Response{loopResp, loopResp, loopResp, loopResp, loopResp}}
	reg := tools.NewRegistry()
	reg.MustRegister(tools.Tool{
		Name:    "echo",
		Schema:  map[string]any{"type": "object"},
		Handler: func(ctx context.Context, in map[string]any) (string, error) { return "x", nil },
	})

	sess, err := session.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })
	pb := &prompt.Builder{AgentsMDDirs: []string{t.TempDir()}, Now: func() time.Time { return time.Now() }}
	eng := &Engine{Provider: prov, Tools: reg, Bus: events.NewBus(), Session: sess, Prompt: pb, MaxIters: 3, MaxDur: 30 * time.Second}

	_, err = eng.Turn(context.Background(), "loop")
	if err == nil || !strings.Contains(err.Error(), "iterations exceeded") {
		t.Fatalf("expected budget breach, got %v", err)
	}
}

func TestTurn_ContextCancellation(t *testing.T) {
	prov := &mockProvider{
		script: []llm.Response{{Message: llm.TextMessage(llm.RoleAssistant, "x"), StopReason: llm.StopEndTurn}},
		delay:  500 * time.Millisecond,
	}
	eng, _ := newEngine(t, prov, false)

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	_, err := eng.Turn(ctx, "hi")
	if err == nil {
		t.Fatal("expected error on cancellation")
	}
}

func TestTurn_CancellationDuringToolPersistsToolResult(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "cancel_me", ToolName: "slow", Input: map[string]any{}},
		}}, StopReason: llm.StopToolUse},
	}}
	eng, _ := newEngine(t, prov, false)
	toolStarted := make(chan struct{}, 1)
	eng.Tools.MustRegister(tools.Tool{
		Name:   "slow",
		Schema: map[string]any{"type": "object"},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			signal(toolStarted)
			<-ctx.Done()
			return "", ctx.Err()
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := eng.Turn(ctx, "hi")
		done <- err
	}()
	waitSignal(t, toolStarted, "tool start")
	cancel()
	err := <-done
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if len(eng.Session.History) != 3 {
		t.Fatalf("history len = %d, want user, assistant tool_use, user tool_result; history=%+v", len(eng.Session.History), eng.Session.History)
	}
	result := eng.Session.History[2]
	if result.Role != llm.RoleUser || len(result.Blocks) != 1 {
		t.Fatalf("tool result message wrong: %+v", result)
	}
	block := result.Blocks[0]
	if block.Type != llm.BlockToolResult || block.ToolUseID != "cancel_me" || !block.IsError {
		t.Fatalf("tool result block wrong: %+v", block)
	}
	if !strings.Contains(block.Content, "context canceled") {
		t.Fatalf("tool result content = %q, want context canceled", block.Content)
	}
}

func TestTurn_ToolTimeoutPersistsErrorAndContinues(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "slow_1", ToolName: "slow", Input: map[string]any{"timeout": 1}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "recovered"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, false)
	eng.MaxDur = 3 * time.Second
	eng.Tools.MustRegister(tools.Tool{
		Name:   "slow",
		Schema: map[string]any{"type": "object"},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			<-ctx.Done()
			return "partial stdout\npartial stderr\n", ctx.Err()
		},
	})

	var requestedPayload, erroredPayload map[string]any
	bus.Subscribe("tool.requested", func(e events.Event) {
		requestedPayload, _ = e.Payload.(map[string]any)
	})
	bus.Subscribe("tool.errored", func(e events.Event) {
		erroredPayload, _ = e.Payload.(map[string]any)
	})

	out, err := eng.Turn(context.Background(), "run slow")
	if err != nil {
		t.Fatal(err)
	}
	if out != "recovered" {
		t.Fatalf("out = %q, want recovered", out)
	}
	result := eng.Session.History[2]
	if result.Role != llm.RoleUser || len(result.Blocks) != 1 {
		t.Fatalf("tool result message wrong: %+v", result)
	}
	block := result.Blocks[0]
	if block.Type != llm.BlockToolResult || !block.IsError {
		t.Fatalf("tool result block = %+v, want error result", block)
	}
	if !strings.Contains(block.Content, "timed out after 1s") {
		t.Fatalf("tool result content = %q, want timeout detail", block.Content)
	}
	if !strings.Contains(block.Content, "partial stdout") || !strings.Contains(block.Content, "partial stderr") {
		t.Fatalf("tool result content = %q, want captured output", block.Content)
	}
	if got := requestedPayload["timeout_seconds"]; got != 1 {
		t.Fatalf("requested timeout_seconds = %v, want 1", got)
	}
	if got := requestedPayload["tool_use_id"]; got != "slow_1" {
		t.Fatalf("requested tool_use_id = %v, want slow_1", got)
	}
	if got := erroredPayload["timeout_seconds"]; got != 1 {
		t.Fatalf("errored timeout_seconds = %v, want 1", got)
	}
	if got := erroredPayload["timed_out"]; got != true {
		t.Fatalf("errored timed_out = %v, want true", got)
	}
	if got := erroredPayload["len"]; got != len("partial stdout\npartial stderr\n") {
		t.Fatalf("errored len = %v, want captured output length", got)
	}
	if got := erroredPayload["preview"]; got != "partial stdout\npartial stderr\n" {
		t.Fatalf("errored preview = %v, want captured output preview", got)
	}
}

func TestToolErrorContentTruncatesLargeOutput(t *testing.T) {
	out := strings.Repeat("x", 40*1024)
	got := toolErrorContent(out, errors.New("tools: bash timed out after 1s"))
	if len(got) >= len(out) {
		t.Fatalf("tool error content len = %d, want less than unbounded output len %d", len(got), len(out))
	}
	if !strings.Contains(got, "... (remaining output truncated) ...") {
		t.Fatalf("tool error content = %q, want truncation marker", got)
	}
	if !strings.Contains(got, "[tool error]\ntools: bash timed out after 1s") {
		t.Fatalf("tool error content = %q, want timeout detail", got)
	}
}

func TestTurn_UnknownToolName(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "x1", ToolName: "does_not_exist", Input: map[string]any{}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "recovered"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, true)
	var errs int32
	bus.Subscribe("tool.errored", func(e events.Event) { atomic.AddInt32(&errs, 1) })

	out, err := eng.Turn(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if out != "recovered" {
		t.Fatalf("got %q", out)
	}
	if errs != 1 {
		t.Fatalf("expected 1 tool error event, got %d", errs)
	}
	tr := eng.Session.History[2]
	if !tr.Blocks[0].IsError || !strings.Contains(tr.Blocks[0].Content, "unknown tool") {
		t.Fatalf("expected unknown-tool error in result; got %+v", tr.Blocks[0])
	}
}

func TestTurn_ProviderError(t *testing.T) {
	prov := &errorProvider{}
	eng, _ := newEngine(t, prov, false)
	_, err := eng.Turn(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected provider error, got %v", err)
	}
}

func TestEngine_MultipleTurnsShareSession(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "first answer"), StopReason: llm.StopEndTurn},
		{Message: llm.TextMessage(llm.RoleAssistant, "second answer"), StopReason: llm.StopEndTurn},
	}}
	eng, _ := newEngine(t, prov, false)

	if _, err := eng.Turn(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Turn(context.Background(), "follow up"); err != nil {
		t.Fatal(err)
	}
	// 4 messages: u1, a1, u2, a2
	if len(eng.Session.History) != 4 {
		t.Fatalf("history len = %d", len(eng.Session.History))
	}
	if eng.Session.History[1].FirstText() != "first answer" || eng.Session.History[3].FirstText() != "second answer" {
		t.Fatalf("history mismatch: %+v", eng.Session.History)
	}
}

func TestTurn_EmitsLifecycleEvents(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, false)
	seen := map[string]int{}
	bus.Subscribe("*", func(e events.Event) { seen[e.Type]++ })
	if _, err := eng.Turn(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"turn.started", "llm.requested", "llm.responded", "turn.completed"} {
		if seen[want] == 0 {
			t.Errorf("missing event %q. seen=%v", want, seen)
		}
	}
}
