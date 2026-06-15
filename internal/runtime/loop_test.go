package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/hooks"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/prompt"
	"github.com/juex-ai/juex/internal/session"
	"github.com/juex-ai/juex/internal/toolevents"
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

type fakeHookRunner struct {
	responses map[hooks.EventName][]hooks.Output
	errors    map[hooks.EventName]error
	requests  []hooks.Request
}

func (r *fakeHookRunner) Run(ctx context.Context, req hooks.Request) ([]hooks.Result, error) {
	r.requests = append(r.requests, req)
	if err := r.errors[req.EventName]; err != nil {
		return nil, err
	}
	outputs := r.responses[req.EventName]
	if len(outputs) == 0 {
		return nil, nil
	}
	out := outputs[0]
	r.responses[req.EventName] = outputs[1:]
	return []hooks.Result{{
		Hook:      hooks.CommandHook{Name: "fake", Events: []hooks.EventName{req.EventName}},
		EventName: req.EventName,
		ToolName:  req.ToolName,
		Output:    out,
	}}, nil
}

func runtimeHookCommand(mode string) []string {
	return []string{os.Args[0], "-test.run=TestRuntimeHookHelperProcess", "--", mode}
}

func TestRuntimeHookHelperProcess(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "--" {
		return
	}
	switch os.Args[len(os.Args)-1] {
	case "fail":
		_, _ = os.Stderr.WriteString("stop hook failed")
		os.Exit(1)
	default:
		_, _ = os.Stdout.WriteString(`{}`)
		os.Exit(0)
	}
}

func TestAppendHookAdditionalContextDoesNotMutateInputBlocks(t *testing.T) {
	blocks := make([]llm.Block, 1, 2)
	blocks[0] = llm.Block{Type: llm.BlockText, Text: "original"}
	msg := llm.Message{Role: llm.RoleUser, Blocks: blocks}

	out := appendHookAdditionalContext(msg, []hooks.Result{{
		Hook:   hooks.CommandHook{Name: "context"},
		Output: hooks.Output{AdditionalContext: "extra"},
	}})

	if len(msg.Blocks) != 1 || msg.Blocks[0].Text != "original" {
		t.Fatalf("input message mutated: %+v", msg.Blocks)
	}
	if len(out.Blocks) != 2 || !strings.Contains(out.Blocks[1].Text, "extra") {
		t.Fatalf("output blocks = %+v", out.Blocks)
	}
	extendedOriginal := msg.Blocks[:cap(msg.Blocks)]
	if extendedOriginal[1].Text != "" {
		t.Fatalf("input backing array mutated: %+v", extendedOriginal)
	}
}

func newEngine(t *testing.T, prov llm.Provider, builtinTools bool) (*Engine, *events.Bus) {
	t.Helper()
	reg := tools.NewRegistry()
	if builtinTools {
		tools.RegisterBuiltins(reg, tools.BuiltinOptions{Shell: tools.DefaultShellProfile()})
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
	}, bus
}

func newEngineForSession(t *testing.T, sess *session.Session, prov llm.Provider) *Engine {
	t.Helper()
	reg := tools.NewRegistry()
	bus := events.NewBus()
	sess.SubscribeBus(bus)
	pb := &prompt.Builder{
		AgentsMDDirs: []string{t.TempDir()},
		Now:          func() time.Time { return time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC) },
	}
	return &Engine{
		Provider:          prov,
		Tools:             reg,
		Bus:               bus,
		Session:           sess,
		Prompt:            pb,
		PendingInputQueue: NewPendingInputQueue(sess.Dir, PendingInputQueueOptions{Now: func() time.Time { return time.Date(2026, 6, 14, 8, 0, 0, 0, time.UTC) }}),
		PendingInputTTL:   time.Hour,
		ExternalEventTTL:  24 * time.Hour,
	}
}

func TestTurn_CompactionKeepsRecentTailInProviderContext(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "summary"), StopReason: llm.StopEndTurn, Usage: llm.Usage{InputTokens: 10, OutputTokens: 2}},
		{Message: llm.TextMessage(llm.RoleAssistant, "answer"), StopReason: llm.StopEndTurn, Usage: llm.Usage{InputTokens: 20, OutputTokens: 3}},
	}}
	eng, _ := newEngine(t, prov, false)
	eng.ContextWindow = 200
	eng.Compaction = DefaultCompactionPolicy()
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

func TestTurn_ExternalizesLargeUserInputBeforeProviderRequest(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "answer"), StopReason: llm.StopEndTurn},
	}}
	eng, _ := newEngine(t, prov, false)
	eng.Compaction = DefaultCompactionPolicy()
	eng.Compaction.UserInputInlineMaxBytes = 64
	eng.Compaction.UserInputPreviewHeadBytes = 12
	eng.Compaction.UserInputPreviewTailBytes = 12

	input := "head-visible\n" + strings.Repeat("SECRET-MIDDLE ", 80) + "\ntail-visible"
	out, err := eng.Turn(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if out != "answer" {
		t.Fatalf("out = %q", out)
	}
	providerText := messagesText(prov.histories[0])
	if strings.Contains(providerText, "SECRET-MIDDLE SECRET-MIDDLE") {
		t.Fatalf("provider received unbounded user input:\n%s", providerText)
	}
	for _, want := range []string{"User input stored outside context.", "head-visible", "tail-visible", "sha256:", "path:"} {
		if !strings.Contains(providerText, want) {
			t.Fatalf("provider text missing %q:\n%s", want, providerText)
		}
	}
	block := eng.Session.History[0].Blocks[0]
	if block.Artifact == nil || block.Artifact.SourceKind != "user_input" || !block.Artifact.Truncated {
		t.Fatalf("artifact metadata missing: %+v", block)
	}
	data, err := os.ReadFile(block.Artifact.StoredPath)
	if err != nil {
		t.Fatalf("read artifact: %v", err)
	}
	if string(data) != input {
		t.Fatalf("artifact content length = %d, want original %d", len(data), len(input))
	}
}

func TestTurn_ExternalizesLargeToolResultBeforeNextProviderRequest(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "call_big", ToolName: "big"},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
	}}
	eng, _ := newEngine(t, prov, false)
	eng.Compaction = DefaultCompactionPolicy()
	eng.Compaction.ToolResultInlineMaxBytes = 64
	eng.Compaction.ToolResultPreviewHeadBytes = 10
	eng.Compaction.ToolResultPreviewTailBytes = 10
	if err := eng.Tools.Register(tools.Tool{
		Name:        "big",
		Description: "return a big result",
		Handler: func(context.Context, map[string]any) (string, error) {
			return "tool-head\n" + strings.Repeat("TOOL-SECRET ", 80) + "\ntool-tail", nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	out, err := eng.Turn(context.Background(), "run tool")
	if err != nil {
		t.Fatal(err)
	}
	if out != "done" {
		t.Fatalf("out = %q", out)
	}
	providerText := messagesText(prov.histories[1])
	if strings.Contains(providerText, "TOOL-SECRET TOOL-SECRET") {
		t.Fatalf("provider received unbounded tool output:\n%s", providerText)
	}
	for _, want := range []string{"Tool output stored outside context.", "tool-head", "tool-tail", "tool_use_id: call_big", "path:"} {
		if !strings.Contains(providerText, want) {
			t.Fatalf("provider text missing %q:\n%s", want, providerText)
		}
	}
	result := eng.Session.History[len(eng.Session.History)-2]
	block := result.Blocks[0]
	if block.Artifact == nil || block.Artifact.SourceKind != "tool_result" || block.Artifact.ToolUseID != "call_big" {
		t.Fatalf("artifact metadata missing: %+v", block)
	}
	data, err := os.ReadFile(block.Artifact.StoredPath)
	if err != nil {
		t.Fatalf("read artifact: %v", err)
	}
	if !strings.Contains(string(data), "TOOL-SECRET") {
		t.Fatalf("artifact lost original tool output")
	}
}

func TestTurn_ProjectsLegacyLargeHistoryBeforeProviderRequest(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "answer"), StopReason: llm.StopEndTurn},
	}}
	eng, _ := newEngine(t, prov, false)
	eng.ContextWindow = 10000
	eng.Compaction = DefaultCompactionPolicy()
	eng.Compaction.UserInputInlineMaxBytes = 64
	eng.Compaction.UserInputPreviewHeadBytes = 10
	eng.Compaction.UserInputPreviewTailBytes = 10
	legacy := "old-head\n" + strings.Repeat("LEGACY-SECRET ", 80) + "\nold-tail"
	if err := eng.Session.Append(llm.TextMessage(llm.RoleUser, legacy)); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.Turn(context.Background(), "latest"); err != nil {
		t.Fatal(err)
	}
	providerText := messagesText(prov.histories[0])
	if strings.Contains(providerText, "LEGACY-SECRET LEGACY-SECRET") {
		t.Fatalf("provider received unbounded legacy input:\n%s", providerText)
	}
	if !strings.Contains(providerText, "User input stored outside context.") || !strings.Contains(providerText, "old-head") || !strings.Contains(providerText, "old-tail") {
		t.Fatalf("legacy projection missing:\n%s", providerText)
	}
}

func TestTurn_AutoCompactionCircuitBreakerStopsRepeatedSummaryAttempts(t *testing.T) {
	prov := &mockProviderWithErrors{
		errs: []error{
			fmt.Errorf("summary failed 1"),
			fmt.Errorf("summary failed 2"),
			fmt.Errorf("summary failed 3"),
		},
	}
	eng, _ := newEngine(t, prov, false)
	eng.ContextWindow = 100
	eng.Compaction = DefaultCompactionPolicy()
	if err := eng.Session.Append(llm.TextMessage(llm.RoleUser, strings.Repeat("old ", 80))); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ {
		if _, err := eng.Turn(context.Background(), "latest"); err == nil {
			t.Fatalf("turn %d: expected compaction error", i+1)
		}
	}
	before := prov.called
	_, err := eng.Turn(context.Background(), "latest")
	if err == nil || !strings.Contains(err.Error(), "auto compaction paused after 3 consecutive failures") {
		t.Fatalf("err = %v", err)
	}
	if prov.called != before {
		t.Fatalf("provider calls after circuit breaker = %d, want %d", prov.called, before)
	}
}

func TestCompactWithInstructionsResetsAutoCompactionFailures(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "manual summary"), StopReason: llm.StopEndTurn},
	}}
	eng, _ := newEngine(t, prov, false)
	eng.ContextWindow = 100
	eng.Compaction = DefaultCompactionPolicy()
	eng.autoCompactFailures = 3
	if err := eng.Session.Append(llm.TextMessage(llm.RoleUser, strings.Repeat("old ", 80))); err != nil {
		t.Fatal(err)
	}

	result, err := eng.CompactWithInstructions(context.Background(), "manual-compact", "system", "manual", false, "focus on failure recovery")
	if err != nil {
		t.Fatal(err)
	}
	if result.MessageID == "" {
		t.Fatalf("manual compact did not append a compact message: %+v", result)
	}
	if eng.autoCompactFailures != 0 {
		t.Fatalf("autoCompactFailures = %d, want reset after manual compact", eng.autoCompactFailures)
	}
}

func TestTurn_CompactionFailureDoesNotAppendMarker(t *testing.T) {
	prov := &mockProviderWithErrors{errs: []error{fmt.Errorf("summary failed")}}
	eng, _ := newEngine(t, prov, false)
	eng.ContextWindow = 100
	eng.Compaction = DefaultCompactionPolicy()
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

func TestTurnMessage_MCPEventContinuesAfterAutoCompactionFailure(t *testing.T) {
	prov := &mockProviderWithErrors{
		errs: []error{fmt.Errorf("openai codex responses: codex SSE read: context deadline exceeded")},
		responses: []llm.Response{
			{Message: llm.TextMessage(llm.RoleAssistant, "handled event"), StopReason: llm.StopEndTurn},
		},
	}
	eng, bus := newEngine(t, prov, false)
	eng.ContextWindow = 100
	eng.Compaction = DefaultCompactionPolicy()
	if err := eng.Session.Append(llm.TextMessage(llm.RoleUser, strings.Repeat("old ", 80))); err != nil {
		t.Fatal(err)
	}
	var compactErr string
	bus.Subscribe("context.compact.errored", func(e events.Event) {
		payload, _ := e.Payload.(ContextCompactErroredPayload)
		compactErr = payload.Error
	})

	msg := llm.TextMessage(llm.RoleUser, "local:message:notify")
	msg.Kind = llm.MessageKindMCPEvent
	out, err := eng.TurnMessage(context.Background(), msg)
	if err != nil {
		t.Fatal(err)
	}
	if out != "handled event" {
		t.Fatalf("out = %q, want handled event", out)
	}
	if !strings.Contains(compactErr, "codex SSE read") {
		t.Fatalf("compact error event = %q, want original failure", compactErr)
	}
	if prov.called != 2 {
		t.Fatalf("provider calls = %d, want compact attempt plus event turn", prov.called)
	}
	if len(eng.Session.History) != 3 {
		t.Fatalf("history len = %d, want old message, mcp event, assistant", len(eng.Session.History))
	}
	if got := eng.Session.History[1]; got.Kind != llm.MessageKindMCPEvent || got.FirstText() != "local:message:notify" {
		t.Fatalf("mcp event not preserved: %+v", got)
	}
	for _, msg := range eng.Session.History {
		if msg.Kind == llm.MessageKindCompact {
			t.Fatalf("unexpected compact marker after failed auto compact: %+v", msg)
		}
	}
}

func TestTurnMessage_MCPEventStripsRedactedReasoningWhenAutoCompactionPaused(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "handled event"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, false)
	eng.ContextWindow = 120
	eng.Compaction = DefaultCompactionPolicy()
	eng.autoCompactFailures = effectiveCompactionPolicy(eng.Compaction, eng.ContextWindow).MaxAutoFailures
	secret := "enc_" + strings.Repeat("secret ", 200)
	if err := eng.Session.Append(llm.Message{
		ID:   "assistant-1",
		Role: llm.RoleAssistant,
		Blocks: []llm.Block{{
			Type:      llm.BlockReasoning,
			Text:      "previous reasoning summary",
			Signature: "rs_1",
			Content:   secret,
			Redacted:  true,
		}},
	}); err != nil {
		t.Fatal(err)
	}

	var stripped int
	bus.Subscribe("context.projection.applied", func(e events.Event) {
		payload, _ := e.Payload.(map[string]any)
		if n, ok := payload["reasoning_contents_stripped"].(int); ok {
			stripped += n
		}
	})

	msg := llm.TextMessage(llm.RoleUser, "local:message:notify")
	msg.Kind = llm.MessageKindMCPEvent
	out, err := eng.TurnMessage(context.Background(), msg)
	if err != nil {
		t.Fatal(err)
	}
	if out != "handled event" {
		t.Fatalf("out = %q, want handled event", out)
	}
	providerText := messagesText(prov.histories[0])
	if strings.Contains(providerText, secret) {
		t.Fatalf("provider received redacted reasoning encrypted content:\n%s", providerText)
	}
	if !strings.Contains(providerText, "previous reasoning summary") {
		t.Fatalf("provider lost reasoning summary:\n%s", providerText)
	}
	if stripped != 1 {
		t.Fatalf("stripped event count = %d, want 1", stripped)
	}
	if eng.Session.History[0].Blocks[0].Content != secret {
		t.Fatalf("session history reasoning content was mutated")
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
	eng.Compaction = DefaultCompactionPolicy()
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
	eng.Compaction = DefaultCompactionPolicy()
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
	eng.Compaction = DefaultCompactionPolicy()
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
		payload := e.Payload.(LLMRespondedPayload)
		eventUsage = payload.TokenUsage
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
		payload := e.Payload.(LLMRespondedPayload)
		got = payload.Blocks
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
		payload := e.Payload.(LLMRespondedPayload)
		if payload.ContextUsage != nil {
			eventContext = *payload.ContextUsage
		}
	})

	if _, err := eng.Turn(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}

	info := eng.Session.Info(time.Now())
	got := info.ContextUsage
	if got == nil {
		t.Fatal("session context usage is nil")
		return
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
		payload := e.Payload.(TurnStartedPayload)
		payloadKind = payload.Kind
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
	eng.Compaction = DefaultCompactionPolicy()
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

func TestCompactRunsPreAndPostHooks(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "summary of old work"), StopReason: llm.StopEndTurn},
	}}
	eng, _ := newEngine(t, prov, false)
	eng.Compaction = DefaultCompactionPolicy()
	eng.Compaction.KeepRecentTokens = 1
	eng.Compaction.TailTurns = 0
	eng.Hooks = &fakeHookRunner{responses: map[hooks.EventName][]hooks.Output{
		hooks.EventPreCompact:  {{}},
		hooks.EventPostCompact: {{}},
	}}
	if err := eng.Session.Append(llm.TextMessage(llm.RoleUser, strings.Repeat("old ", 80))); err != nil {
		t.Fatal(err)
	}
	if err := eng.Session.Append(llm.TextMessage(llm.RoleAssistant, strings.Repeat("reply ", 80))); err != nil {
		t.Fatal(err)
	}

	result, err := eng.Compact(context.Background(), "compact-turn", "system", "manual", false)
	if err != nil {
		t.Fatal(err)
	}
	if result.MessageID == "" {
		t.Fatalf("result = %+v", result)
	}
	runner := eng.Hooks.(*fakeHookRunner)
	got := []hooks.EventName{runner.requests[0].EventName, runner.requests[1].EventName}
	want := []hooks.EventName{hooks.EventPreCompact, hooks.EventPostCompact}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("hook order = %+v, want %+v", got, want)
		}
	}
}

func TestCompactPostHookFailuresAreObservational(t *testing.T) {
	cases := []struct {
		name   string
		runner *fakeHookRunner
	}{
		{
			name: "error",
			runner: &fakeHookRunner{
				responses: map[hooks.EventName][]hooks.Output{hooks.EventPreCompact: {{}}},
				errors:    map[hooks.EventName]error{hooks.EventPostCompact: errors.New("audit sink unavailable")},
			},
		},
		{
			name: "deny",
			runner: &fakeHookRunner{
				responses: map[hooks.EventName][]hooks.Output{
					hooks.EventPreCompact:  {{}},
					hooks.EventPostCompact: {{Decision: hooks.DecisionDeny, AdditionalContext: "audit failed"}},
				},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prov := &mockProvider{script: []llm.Response{
				{Message: llm.TextMessage(llm.RoleAssistant, "summary of old work"), StopReason: llm.StopEndTurn},
			}}
			eng, bus := newEngine(t, prov, false)
			var eventTypes []string
			unsub := bus.Subscribe("context.compact.*", func(ev events.Event) {
				eventTypes = append(eventTypes, ev.Type)
			})
			defer unsub()
			eng.Compaction = DefaultCompactionPolicy()
			eng.Compaction.KeepRecentTokens = 1
			eng.Compaction.TailTurns = 0
			eng.Hooks = tc.runner
			if err := eng.Session.Append(llm.TextMessage(llm.RoleUser, strings.Repeat("old ", 80))); err != nil {
				t.Fatal(err)
			}
			if err := eng.Session.Append(llm.TextMessage(llm.RoleAssistant, strings.Repeat("reply ", 80))); err != nil {
				t.Fatal(err)
			}

			result, err := eng.Compact(context.Background(), "compact-turn", "system", "manual", false)
			if err != nil {
				t.Fatal(err)
			}
			if result.MessageID == "" {
				t.Fatalf("result = %+v", result)
			}
			var sawErrored, sawCompleted bool
			for _, typ := range eventTypes {
				if typ == "context.compact.errored" {
					sawErrored = true
				}
				if typ == "context.compact.completed" {
					sawCompleted = true
				}
			}
			if !sawErrored || !sawCompleted {
				t.Fatalf("events = %+v, want errored and completed", eventTypes)
			}
		})
	}
}

func TestTurn_AutoCompactionBoundsOversizedSummaryRequest(t *testing.T) {
	prov := &budgetedCompactionProvider{compactionLimit: 800}
	eng, _ := newEngine(t, prov, false)
	eng.ContextWindow = 1200
	eng.Compaction = DefaultCompactionPolicy()
	eng.Compaction.ReserveTokens = 300
	eng.Compaction.SummaryMaxTokens = 100
	eng.Compaction.ToolResultMaxChars = 2000
	for i := 0; i < 80; i++ {
		if err := eng.Session.Append(llm.TextMessage(llm.RoleUser, fmt.Sprintf("message-%02d %s", i, strings.Repeat("x", 2000)))); err != nil {
			t.Fatal(err)
		}
	}

	out, err := eng.Turn(context.Background(), "latest question")
	if err != nil {
		t.Fatal(err)
	}
	if out != "answered after bounded compact" {
		t.Fatalf("out = %q", out)
	}
	if prov.compactionTokens > prov.compactionLimit {
		t.Fatalf("compaction request tokens = %d, want <= %d", prov.compactionTokens, prov.compactionLimit)
	}
	if !strings.Contains(prov.compactionBody, "messages omitted") {
		t.Fatalf("compaction body did not record omitted transcript:\n%s", prov.compactionBody)
	}
	if strings.Contains(prov.compactionBody, "message-00") {
		t.Fatalf("oldest transcript should be omitted when over budget:\n%s", prov.compactionBody)
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

type budgetedCompactionProvider struct {
	compactionLimit  int
	compactionTokens int
	compactionBody   string
}

func (p *budgetedCompactionProvider) Name() string { return "budgeted" }

func (p *budgetedCompactionProvider) Complete(ctx context.Context, sys string, history []llm.Message, tools []llm.ToolSpec) (llm.Response, error) {
	return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "answered after bounded compact"), StopReason: llm.StopEndTurn}, nil
}

func (p *budgetedCompactionProvider) CompleteWithOptions(ctx context.Context, sys string, history []llm.Message, tools []llm.ToolSpec, opts llm.CompleteOptions) (llm.Response, error) {
	if opts.Purpose != "compaction" {
		return p.Complete(ctx, sys, history, tools)
	}
	p.compactionTokens = estimateContextTokens(sys, nil, history)
	if len(history) > 0 {
		p.compactionBody = history[0].FirstText()
	}
	if p.compactionTokens > p.compactionLimit {
		return llm.Response{}, fmt.Errorf("context_length_exceeded: compaction request has %d tokens", p.compactionTokens)
	}
	return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "bounded summary"), StopReason: llm.StopEndTurn}, nil
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

func TestTurn_UserPromptSubmitHookInjectsContext(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "answer"), StopReason: llm.StopEndTurn},
	}}
	eng, _ := newEngine(t, prov, false)
	eng.Hooks = &fakeHookRunner{responses: map[hooks.EventName][]hooks.Output{
		hooks.EventUserPromptSubmit: {{AdditionalContext: "ticket: ABC-123"}},
	}}

	out, err := eng.Turn(context.Background(), "summarize")
	if err != nil {
		t.Fatal(err)
	}
	if out != "answer" {
		t.Fatalf("out = %q", out)
	}
	if got := messagesText(prov.histories[0]); !strings.Contains(got, "ticket: ABC-123") {
		t.Fatalf("provider history missing hook context:\n%s", got)
	}
	first := eng.Session.History[0]
	if len(first.Blocks) != 2 || !strings.Contains(first.Blocks[1].Text, "ticket: ABC-123") {
		t.Fatalf("session user message missing hook context: %+v", first)
	}
}

func TestTurn_UserPromptSubmitHookDenyStopsBeforeProvider(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "should not run"), StopReason: llm.StopEndTurn},
	}}
	eng, _ := newEngine(t, prov, false)
	eng.Hooks = &fakeHookRunner{responses: map[hooks.EventName][]hooks.Output{
		hooks.EventUserPromptSubmit: {{Decision: hooks.DecisionDeny, AdditionalContext: "missing approval"}},
	}}

	_, err := eng.Turn(context.Background(), "summarize")
	if err == nil || !strings.Contains(err.Error(), "UserPromptSubmit denied: missing approval") {
		t.Fatalf("err = %v", err)
	}
	if len(prov.histories) != 0 {
		t.Fatalf("provider should not be called, calls = %d", len(prov.histories))
	}
}

func TestTurn_PreToolUseHookDenyReturnsToolError(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "tu1", ToolName: "danger", Input: map[string]any{"path": "x"}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "done too early"), StopReason: llm.StopEndTurn},
		{Message: llm.TextMessage(llm.RoleAssistant, "recovered"), StopReason: llm.StopEndTurn},
	}}
	eng, _ := newEngine(t, prov, false)
	eng.Tools.MustRegister(tools.Tool{
		Name: "danger",
		Handler: func(context.Context, map[string]any) (string, error) {
			t.Fatal("tool should not run when denied")
			return "", nil
		},
	})
	eng.Hooks = &fakeHookRunner{responses: map[hooks.EventName][]hooks.Output{
		hooks.EventPreToolUse: {{Decision: hooks.DecisionDeny, AdditionalContext: "policy denied"}},
	}}

	out, err := eng.Turn(context.Background(), "run danger")
	if err != nil {
		t.Fatal(err)
	}
	if out != "recovered" {
		t.Fatalf("out = %q", out)
	}
	tr := eng.Session.History[2]
	if len(tr.Blocks) != 1 || !tr.Blocks[0].IsError || !strings.Contains(tr.Blocks[0].Content, "policy denied") {
		t.Fatalf("tool result = %+v", tr)
	}
}

func TestTurn_PostToolUseHookDenyReturnsToolError(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "tu1", ToolName: "audit", Input: map[string]any{"path": "x"}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "done too early"), StopReason: llm.StopEndTurn},
		{Message: llm.TextMessage(llm.RoleAssistant, "recovered"), StopReason: llm.StopEndTurn},
	}}
	eng, _ := newEngine(t, prov, false)
	eng.Tools.MustRegister(tools.Tool{
		Name: "audit",
		Handler: func(context.Context, map[string]any) (string, error) {
			return "sensitive output", nil
		},
	})
	eng.Hooks = &fakeHookRunner{responses: map[hooks.EventName][]hooks.Output{
		hooks.EventPreToolUse:  {{}},
		hooks.EventPostToolUse: {{Decision: hooks.DecisionDeny, AdditionalContext: "redaction required"}},
	}}

	out, err := eng.Turn(context.Background(), "run audit")
	if err != nil {
		t.Fatal(err)
	}
	if out != "recovered" {
		t.Fatalf("out = %q", out)
	}
	tr := eng.Session.History[2]
	if len(tr.Blocks) != 1 || !tr.Blocks[0].IsError || !strings.Contains(tr.Blocks[0].Content, "redaction required") {
		t.Fatalf("tool result = %+v", tr)
	}
}

func TestTurn_StopHookBlockContinuesWithPrompt(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "first"), StopReason: llm.StopEndTurn},
		{Message: llm.TextMessage(llm.RoleAssistant, "final"), StopReason: llm.StopEndTurn},
	}}
	eng, _ := newEngine(t, prov, false)
	eng.Hooks = &fakeHookRunner{responses: map[hooks.EventName][]hooks.Output{
		hooks.EventStop: {
			{BlockStop: true, ContinuePrompt: "continue until done"},
			{},
		},
	}}

	out, err := eng.Turn(context.Background(), "start")
	if err != nil {
		t.Fatal(err)
	}
	if out != "final" {
		t.Fatalf("out = %q", out)
	}
	if len(prov.histories) != 2 {
		t.Fatalf("provider calls = %d", len(prov.histories))
	}
	if got := prov.histories[1][len(prov.histories[1])-1].FirstText(); got != "continue until done" {
		t.Fatalf("continued prompt = %q", got)
	}
}

func TestTurn_GoalCompletionGateContinuesThenCompletes(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "too early"), StopReason: llm.StopEndTurn},
		{Message: llm.TextMessage(llm.RoleAssistant, "final"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, false)
	eng.GoalState = NewGoalStateStore(eng.Session.Dir, GoalStateOptions{})
	hookRunner := &fakeHookRunner{responses: map[hooks.EventName][]hooks.Output{
		hooks.EventStop: {
			{GoalState: mustRawMessage(t, `{"status":"continue","completion_check":{"status":"continue","summary":"tests missing","continue_prompt":"run tests before finishing"}}`)},
			{GoalState: mustRawMessage(t, `{"status":"complete","completion_check":{"status":"complete","summary":"tests passed"}}`)},
		},
	}}
	eng.Hooks = hookRunner
	var continued int32
	bus.Subscribe("goal.continued", func(e events.Event) { atomic.AddInt32(&continued, 1) })

	out, err := eng.Turn(context.Background(), "ship this")
	if err != nil {
		t.Fatal(err)
	}
	if out != "final" {
		t.Fatalf("out = %q", out)
	}
	if len(prov.histories) != 2 {
		t.Fatalf("provider calls = %d", len(prov.histories))
	}
	if got := prov.histories[1][len(prov.histories[1])-1].FirstText(); got != "run tests before finishing" {
		t.Fatalf("goal continuation = %q", got)
	}
	if atomic.LoadInt32(&continued) != 1 {
		t.Fatalf("goal.continued events = %d", continued)
	}
	if len(hookRunner.requests) < 3 || !strings.Contains(string(hookRunner.requests[2].GoalState), `"status":"continue"`) {
		t.Fatalf("second stop hook request missing current goal state: %+v", hookRunner.requests)
	}
	state, err := eng.GoalState.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != GoalStatusComplete || state.Budget.ContinuationsUsed != 1 {
		t.Fatalf("goal state = %+v", state)
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

func TestTurn_ReplaysPersistedPendingInputAfterRestart(t *testing.T) {
	root := t.TempDir()
	sess, err := session.New(root)
	if err != nil {
		t.Fatal(err)
	}
	eng := newEngineForSession(t, sess, &mockProvider{})
	if err := eng.ReserveTurnID("turn-active"); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.EnqueuePendingMessageWithOptions(context.Background(), llm.TextMessage(llm.RoleUser, "replay me"), PendingInputOptions{ID: "event-1", TTL: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}

	reloaded, err := session.Load(sess.Dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reloaded.Close() })
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
	}}
	restarted := newEngineForSession(t, reloaded, prov)
	if _, err := restarted.Turn(context.Background(), "after restart"); err != nil {
		t.Fatal(err)
	}
	if len(prov.histories) != 1 {
		t.Fatalf("provider calls = %d", len(prov.histories))
	}
	if got := prov.histories[0][len(prov.histories[0])-1].FirstText(); got != "replay me" {
		t.Fatalf("last provider message = %q", got)
	}
}

func TestTurn_DoesNotReplayProcessedPendingInputAfterRestart(t *testing.T) {
	root := t.TempDir()
	sess, err := session.New(root)
	if err != nil {
		t.Fatal(err)
	}
	eng := newEngineForSession(t, sess, &mockProvider{})
	if err := eng.ReserveTurnID("turn-active"); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.EnqueuePendingMessageWithOptions(context.Background(), llm.TextMessage(llm.RoleUser, "only once"), PendingInputOptions{ID: "event-1", TTL: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}

	firstReload, err := session.Load(sess.Dir)
	if err != nil {
		t.Fatal(err)
	}
	firstProvider := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "first done"), StopReason: llm.StopEndTurn},
	}}
	firstEngine := newEngineForSession(t, firstReload, firstProvider)
	if _, err := firstEngine.Turn(context.Background(), "first after restart"); err != nil {
		t.Fatal(err)
	}
	if err := firstReload.Close(); err != nil {
		t.Fatal(err)
	}

	secondReload, err := session.Load(sess.Dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { secondReload.Close() })
	secondProvider := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "second done"), StopReason: llm.StopEndTurn},
	}}
	secondEngine := newEngineForSession(t, secondReload, secondProvider)
	if _, err := secondEngine.Turn(context.Background(), "second after restart"); err != nil {
		t.Fatal(err)
	}
	last := secondProvider.histories[0][len(secondProvider.histories[0])-1]
	if got := last.FirstText(); got != "second after restart" {
		t.Fatalf("last provider message = %q, want second turn without replay", got)
	}
}

func TestEngine_DeduplicatesPendingInputByEventID(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	eng.PendingInputQueue = NewPendingInputQueue(eng.Session.Dir, PendingInputQueueOptions{Now: func() time.Time { return time.Date(2026, 6, 14, 8, 0, 0, 0, time.UTC) }})
	eng.PendingInputTTL = time.Hour
	if err := eng.ReserveTurnID("turn-active"); err != nil {
		t.Fatal(err)
	}
	first, err := eng.EnqueuePendingMessageWithOptions(context.Background(), llm.TextMessage(llm.RoleUser, "one"), PendingInputOptions{ID: "event-1", TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	second, err := eng.EnqueuePendingMessageWithOptions(context.Background(), llm.TextMessage(llm.RoleUser, "two"), PendingInputOptions{ID: "event-1", TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if first.PendingCount != 1 || second.PendingCount != 1 {
		t.Fatalf("pending counts first=%d second=%d, want deduped count 1", first.PendingCount, second.PendingCount)
	}
}

func TestTurn_AdmittedPendingInputWithExistingMessageIDIsNotReplayed(t *testing.T) {
	root := t.TempDir()
	sess, err := session.New(root)
	if err != nil {
		t.Fatal(err)
	}
	store := NewPendingInputQueue(sess.Dir, PendingInputQueueOptions{Now: func() time.Time { return time.Date(2026, 6, 14, 8, 0, 0, 0, time.UTC) }})
	record, err := store.Enqueue(llm.TextMessage(llm.RoleUser, "already appended"), PendingInputOptions{ID: "event-1", TTL: time.Hour}, "turn-old")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkAdmitted([]string{record.ID}, "turn-old"); err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(record.Message); err != nil {
		t.Fatal(err)
	}
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}

	reloaded, err := session.Load(sess.Dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reloaded.Close() })
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
	}}
	eng := newEngineForSession(t, reloaded, prov)
	if _, err := eng.Turn(context.Background(), "fresh input"); err != nil {
		t.Fatal(err)
	}
	if got := prov.histories[0][len(prov.histories[0])-1].FirstText(); got != "fresh input" {
		t.Fatalf("last provider message = %q, want no duplicate replay", got)
	}
	records, err := store.Records()
	if err != nil {
		t.Fatal(err)
	}
	if records[record.ID].State != PendingInputStateProcessed {
		t.Fatalf("state = %q, want processed", records[record.ID].State)
	}
}

func TestTurn_PromotedPendingInputMarksProcessedWithoutDuplicateDrain(t *testing.T) {
	root := t.TempDir()
	sess, err := session.New(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
	}}
	eng := newEngineForSession(t, sess, prov)
	if err := eng.ReserveTurnID("compact-1"); err != nil {
		t.Fatal(err)
	}
	status, err := eng.EnqueuePendingMessageWithOptions(context.Background(), llm.TextMessage(llm.RoleUser, "after compact"), PendingInputOptions{ID: "event-1", TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if status.PendingCount != 1 {
		t.Fatalf("pending count = %d", status.PendingCount)
	}
	var drained int32
	eng.Bus.Subscribe("pending_input.drained", func(e events.Event) {
		atomic.AddInt32(&drained, 1)
	})

	msg, _, ok := eng.PromotePendingInputTurn("compact-1", "turn-1")
	if !ok {
		t.Fatal("pending input was not promoted")
	}
	if _, err := eng.TurnMessageWithID(context.Background(), msg, "turn-1"); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&drained) != 0 {
		t.Fatalf("pending_input.drained events = %d, want none for promoted main message", drained)
	}
	records, err := eng.PendingInputQueue.Records()
	if err != nil {
		t.Fatal(err)
	}
	if records["event-1"].State != PendingInputStateProcessed {
		t.Fatalf("state = %q, want processed", records["event-1"].State)
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

func TestTurn_AllowsMoreThanLegacyIterationBudget(t *testing.T) {
	const toolTurns = 30
	script := make([]llm.Response, 0, toolTurns+1)
	for i := 0; i < toolTurns; i++ {
		script = append(script, llm.Response{
			Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
				{Type: llm.BlockToolUse, ToolUseID: fmt.Sprintf("echo_%02d", i), ToolName: "echo", Input: map[string]any{}},
			}},
			StopReason: llm.StopToolUse,
		})
	}
	script = append(script, llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn})
	prov := &mockProvider{script: script}
	eng, bus := newEngine(t, prov, false)
	eng.Tools.MustRegister(tools.Tool{
		Name:    "echo",
		Schema:  map[string]any{"type": "object"},
		Handler: func(ctx context.Context, in map[string]any) (string, error) { return "x", nil },
	})
	var errored bool
	bus.Subscribe("turn.errored", func(e events.Event) {
		errored = true
	})
	var lastIter int
	bus.Subscribe("llm.requested", func(e events.Event) {
		payload, ok := e.Payload.(LLMRequestedPayload)
		if ok {
			lastIter = payload.Iter
		}
	})

	out, err := eng.Turn(context.Background(), "loop for a while")
	if err != nil {
		t.Fatal(err)
	}
	if out != "done" {
		t.Fatalf("out = %q, want done", out)
	}
	if errored {
		t.Fatal("turn emitted turn.errored")
	}
	if prov.called != toolTurns+1 {
		t.Fatalf("provider calls = %d, want %d", prov.called, toolTurns+1)
	}
	if lastIter != toolTurns {
		t.Fatalf("last llm.requested iter = %d, want %d", lastIter, toolTurns)
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
		{Message: llm.TextMessage(llm.RoleAssistant, "done too early"), StopReason: llm.StopEndTurn},
		{Message: llm.TextMessage(llm.RoleAssistant, "recovered"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, false)
	eng.Tools.MustRegister(tools.Tool{
		Name:   "slow",
		Schema: map[string]any{"type": "object"},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			<-ctx.Done()
			return "partial stdout\npartial stderr\n", ctx.Err()
		},
	})

	var requestedPayload toolevents.RequestedPayload
	var erroredPayload toolevents.ErroredPayload
	bus.Subscribe(toolevents.RequestedType, func(e events.Event) {
		requestedPayload, _ = e.Payload.(toolevents.RequestedPayload)
	})
	bus.Subscribe(toolevents.ErroredType, func(e events.Event) {
		erroredPayload, _ = e.Payload.(toolevents.ErroredPayload)
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
	if got := requestedPayload.TimeoutSeconds; got != 1 {
		t.Fatalf("requested timeout_seconds = %v, want 1", got)
	}
	if got := requestedPayload.ToolUseID; got != "slow_1" {
		t.Fatalf("requested tool_use_id = %v, want slow_1", got)
	}
	if got := erroredPayload.TimeoutSeconds; got != 1 {
		t.Fatalf("errored timeout_seconds = %v, want 1", got)
	}
	if got := erroredPayload.TimedOut; got != true {
		t.Fatalf("errored timed_out = %v, want true", got)
	}
	if got := erroredPayload.Len; got != len("partial stdout\npartial stderr\n") {
		t.Fatalf("errored len = %v, want captured output length", got)
	}
	if got := erroredPayload.Preview; got != "partial stdout\npartial stderr\n" {
		t.Fatalf("errored preview = %v, want captured output preview", got)
	}
}

func TestTurn_ToolOutputDeltaEvent(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "stream_1", ToolName: "streamer", Input: map[string]any{}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, false)
	eng.Tools.MustRegister(tools.Tool{
		Name:   "streamer",
		Schema: map[string]any{"type": "object"},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			events := tools.ToolCallEventsFromContext(ctx)
			events.Emit(tools.OutputDelta{
				Name:      "streamer",
				ToolUseID: "stream_1",
				SessionID: "sh_test",
				ChunkID:   7,
				Stream:    "combined",
				Text:      "live bytes",
			})
			return "final", nil
		},
	})

	var deltaPayload toolevents.OutputDeltaPayload
	bus.Subscribe(toolevents.OutputDeltaType, func(e events.Event) {
		deltaPayload, _ = e.Payload.(toolevents.OutputDeltaPayload)
	})

	out, err := eng.Turn(context.Background(), "stream")
	if err != nil {
		t.Fatal(err)
	}
	if out != "done" {
		t.Fatalf("out = %q, want done", out)
	}
	if deltaPayload.Name != "streamer" || deltaPayload.ToolUseID != "stream_1" {
		t.Fatalf("delta payload identity = %+v", deltaPayload)
	}
	if deltaPayload.SessionID != "sh_test" || deltaPayload.ChunkID != 7 || deltaPayload.Text != "live bytes" {
		t.Fatalf("delta payload = %+v", deltaPayload)
	}
}

func TestTurn_BuiltinShellCompletedEventIncludesStructuredResult(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "exec_1", ToolName: "exec_command", Input: map[string]any{
				"cmd": "echo structured-shell",
			}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, true)

	var completedPayload toolevents.CompletedPayload
	bus.Subscribe(toolevents.CompletedType, func(e events.Event) {
		payload, _ := e.Payload.(toolevents.CompletedPayload)
		if payload.Name == "exec_command" {
			completedPayload = payload
		}
	})

	out, err := eng.Turn(context.Background(), "run shell")
	if err != nil {
		t.Fatal(err)
	}
	if out != "done" {
		t.Fatalf("out = %q, want done", out)
	}
	result, ok := completedPayload.Result.(tools.ShellResult)
	if !ok {
		t.Fatalf("completed result = %#v, want tools.ShellResult", completedPayload.Result)
	}
	if result.Running || result.ExitCode == nil || *result.ExitCode != 0 {
		t.Fatalf("shell event result = %+v, want completed exit 0", result)
	}
	if !strings.Contains(result.Output, "structured-shell") {
		t.Fatalf("shell event result output = %q, want structured-shell", result.Output)
	}
}

func TestTurn_BuiltinShellRawArgumentsNormalizeAndContinue(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "exec_raw", ToolName: "exec_command", Input: map[string]any{
				"_raw_arguments": `{"cmd":"echo raw-ok","timeout":2}`,
			}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "recovered"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, true)

	var requestedPayload toolevents.RequestedPayload
	bus.Subscribe(toolevents.RequestedType, func(e events.Event) {
		requestedPayload, _ = e.Payload.(toolevents.RequestedPayload)
	})
	var respondedPayload LLMRespondedPayload
	bus.Subscribe("llm.responded", func(e events.Event) {
		if respondedPayload.ToolCalls == nil {
			respondedPayload, _ = e.Payload.(LLMRespondedPayload)
		}
	})

	out, err := eng.Turn(context.Background(), "run shell")
	if err != nil {
		t.Fatal(err)
	}
	if out != "recovered" {
		t.Fatalf("out = %q, want recovered", out)
	}
	assistant := eng.Session.History[1]
	if assistant.Role != llm.RoleAssistant || len(assistant.Blocks) != 1 {
		t.Fatalf("assistant message wrong: %+v", assistant)
	}
	input := assistant.Blocks[0].Input
	if input["cmd"] != "echo raw-ok" || input["timeout"] != 2.0 {
		t.Fatalf("assistant tool input = %+v, want normalized command and timeout", input)
	}
	if _, ok := input["_raw_arguments"]; ok {
		t.Fatalf("assistant tool input kept raw arguments: %+v", input)
	}
	if assistant.Blocks[0].TimeoutSeconds != 2 {
		t.Fatalf("assistant timeout = %d, want 2", assistant.Blocks[0].TimeoutSeconds)
	}
	respondedCalls := respondedPayload.ToolCalls
	if len(respondedCalls) != 1 {
		t.Fatalf("responded tool_calls = %+v, want one tool call", respondedPayload.ToolCalls)
	}
	respondedInput := respondedCalls[0].Input
	if respondedInput["cmd"] != "echo raw-ok" {
		t.Fatalf("responded tool input = %+v, want normalized command", respondedInput)
	}
	if got := respondedCalls[0].TimeoutSeconds; got != 2 {
		t.Fatalf("responded timeout = %v, want 2", got)
	}
	requestedInput := requestedPayload.Input
	if requestedInput["cmd"] != "echo raw-ok" {
		t.Fatalf("requested input = %+v, want normalized command", requestedInput)
	}
	if got := requestedPayload.TimeoutSeconds; got != 2 {
		t.Fatalf("requested timeout = %v, want 2", got)
	}
	result := eng.Session.History[2]
	if result.Role != llm.RoleUser || len(result.Blocks) != 1 {
		t.Fatalf("tool result message wrong: %+v", result)
	}
	block := result.Blocks[0]
	if block.Type != llm.BlockToolResult || block.IsError {
		t.Fatalf("tool result block = %+v, want successful result", block)
	}
	if !strings.Contains(block.Content, "Process exited with code 0") || !strings.Contains(block.Content, "raw-ok") {
		t.Fatalf("tool result content = %q, want successful raw-ok output", block.Content)
	}
}

func TestToolErrorContentTruncatesLargeOutput(t *testing.T) {
	out := strings.Repeat("x", 40*1024)
	got := toolErrorContent(out, errors.New("tools: shell timed out after 1s"))
	if len(got) >= len(out) {
		t.Fatalf("tool error content len = %d, want less than unbounded output len %d", len(got), len(out))
	}
	if !strings.Contains(got, "... (remaining output truncated) ...") {
		t.Fatalf("tool error content = %q, want truncation marker", got)
	}
	if !strings.Contains(got, "[tool error]\ntools: shell timed out after 1s") {
		t.Fatalf("tool error content = %q, want timeout detail", got)
	}
}

func TestTurn_UnknownToolName(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "x1", ToolName: "does_not_exist", Input: map[string]any{}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "done too early"), StopReason: llm.StopEndTurn},
		{Message: llm.TextMessage(llm.RoleAssistant, "recovered"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, true)
	var errs int32
	bus.Subscribe(toolevents.ErroredType, func(e events.Event) { atomic.AddInt32(&errs, 1) })

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

func TestToolFailureClassificationMappings(t *testing.T) {
	cases := []struct {
		name     string
		obs      toolFailureObservation
		want     ToolFailureClassification
		blocking bool
	}{
		{
			name: "shell_exit_recoverable",
			obs: toolFailureObservation{
				ToolName: "exec_command",
				Input:    map[string]any{"cmd": "go test ./..."},
				Content:  "Process exited with code 1\nOutput:\nFAIL",
				ExitCode: intPtr(1),
			},
			want:     ToolFailureRecoverable,
			blocking: true,
		},
		{
			name: "timeout_external_blocked",
			obs: toolFailureObservation{
				ToolName: "exec_command",
				Content:  "[tool error]\ntools: exec_command timed out after 1s",
				TimedOut: true,
			},
			want:     ToolFailureExternalBlocked,
			blocking: true,
		},
		{
			name: "unknown_tool_runtime_fatal",
			obs: toolFailureObservation{
				ToolName: "missing_tool",
				Content:  `tools: unknown tool "missing_tool"`,
			},
			want:     ToolFailureRuntimeFatal,
			blocking: true,
		},
		{
			name: "hook_error_runtime_fatal_from_error_only",
			obs: toolFailureObservation{
				ToolName: "exec_command",
				Error:    "hooks: tool denied",
				Content:  "ordinary output",
			},
			want:     ToolFailureRuntimeFatal,
			blocking: true,
		},
		{
			name: "hook_word_in_output_is_not_runtime_fatal",
			obs: toolFailureObservation{
				ToolName: "exec_command",
				Error:    "process exited with code 1",
				Content:  "running hooks: pre-commit\n[tool error]\nprocess exited with code 1",
				ExitCode: intPtr(1),
			},
			want:     ToolFailureRecoverable,
			blocking: true,
		},
		{
			name: "windows_missing_read_is_nonblocking",
			obs: toolFailureObservation{
				ToolName: "read",
				Content:  "open MISSING: The system cannot find the file specified.",
			},
			want:     ToolFailureNonblockingExploratory,
			blocking: false,
		},
		{
			name: "grep_no_matches_nonblocking",
			obs: toolFailureObservation{
				ToolName: "grep",
				Content:  "(no matches)",
			},
			want:     ToolFailureNonblockingExploratory,
			blocking: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyToolFailure(tc.obs)
			if got.Classification != tc.want || got.Blocking != tc.blocking {
				t.Fatalf("classifyToolFailure() = %+v, want %s blocking=%t", got, tc.want, tc.blocking)
			}
		})
	}
}

func TestTurn_FinishGateAllowsCleanFinish(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, false)

	var gateCompleted HookCompletedPayload
	bus.Subscribe("hook.completed", func(e events.Event) {
		payload, _ := e.Payload.(HookCompletedPayload)
		if payload.Name == "unresolved-failure-gate" {
			gateCompleted = payload
		}
	})

	out, err := eng.Turn(context.Background(), "finish")
	if err != nil {
		t.Fatal(err)
	}
	if out != "ok" {
		t.Fatalf("out = %q", out)
	}
	if gateCompleted.EventName != string(hooks.EventStop) || gateCompleted.Source != "builtin" || gateCompleted.BlockStop || gateCompleted.ContinuePromptLen != 0 || gateCompleted.Decision != string(hooks.DecisionAllow) {
		t.Fatalf("gate completed payload = %+v", gateCompleted)
	}
}

func TestTurn_FinishPolicyOrdersBuiltInGatesAndStopHooks(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, false)
	eng.GoalState = NewGoalStateStore(eng.Session.Dir, GoalStateOptions{})
	runner, err := hooks.NewRunner(hooks.Config{Commands: []hooks.CommandHook{{
		Name:    "stop-ok",
		Events:  []hooks.EventName{hooks.EventStop},
		Command: runtimeHookCommand("ok"),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	eng.Hooks = runner

	var order []string
	bus.Subscribe("finish.attempted", func(e events.Event) {
		order = append(order, "finish.attempted")
	})
	bus.Subscribe("hook.started", func(e events.Event) {
		payload, _ := e.Payload.(HookStartedPayload)
		order = append(order, "start:"+payload.Name)
	})
	bus.Subscribe("hook.completed", func(e events.Event) {
		payload, _ := e.Payload.(HookCompletedPayload)
		order = append(order, "done:"+payload.Name)
	})

	out, err := eng.Turn(context.Background(), "finish")
	if err != nil {
		t.Fatal(err)
	}
	if out != "ok" {
		t.Fatalf("out = %q", out)
	}
	want := []string{
		"finish.attempted",
		"start:unresolved-failure-gate",
		"done:unresolved-failure-gate",
		"start:stop-ok",
		"done:stop-ok",
		"start:goal-completion-gate",
		"done:goal-completion-gate",
	}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("finish policy order = %#v, want %#v", order, want)
	}
}

func TestTurn_FinishGateAllowsNonblockingExploratoryFailure(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "read_1", ToolName: "read", Input: map[string]any{"path": "missing.txt"}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, false)
	eng.Tools.MustRegister(tools.Tool{
		Name:   "read",
		Schema: map[string]any{"type": "object"},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			return "", fmt.Errorf("open missing.txt: no such file or directory")
		},
	})

	var continued int32
	bus.Subscribe("tool.failure.continued", func(e events.Event) {
		atomic.AddInt32(&continued, 1)
	})

	out, err := eng.Turn(context.Background(), "inspect optional file")
	if err != nil {
		t.Fatal(err)
	}
	if out != "done" {
		t.Fatalf("out = %q", out)
	}
	if len(prov.histories) != 2 {
		t.Fatalf("provider calls = %d, want no continuation", len(prov.histories))
	}
	if atomic.LoadInt32(&continued) != 0 {
		t.Fatalf("nonblocking exploratory failure triggered continuation")
	}
}

func TestTurn_ContinuesAfterUnresolvedBlockingToolFailure(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "check_1", ToolName: "check_ready", Input: map[string]any{"path": "artifact.txt"}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "done too early"), StopReason: llm.StopEndTurn},
		{Message: llm.TextMessage(llm.RoleAssistant, "blocked after observation"), StopReason: llm.StopEndTurn},
		{Message: llm.TextMessage(llm.RoleAssistant, "blocked reason explained"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, false)
	eng.Tools.MustRegister(tools.Tool{
		Name:   "check_ready",
		Schema: map[string]any{"type": "object"},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			return "artifact is not ready", fmt.Errorf("check failed")
		},
	})

	var recorded ToolFailureRecordedPayload
	var continued ToolFailureContinuedPayload
	bus.Subscribe("tool.failure.recorded", func(e events.Event) {
		recorded, _ = e.Payload.(ToolFailureRecordedPayload)
	})
	bus.Subscribe("tool.failure.continued", func(e events.Event) {
		continued, _ = e.Payload.(ToolFailureContinuedPayload)
	})
	var gateStarted HookStartedPayload
	var gateCompleted HookCompletedPayload
	bus.Subscribe("hook.started", func(e events.Event) {
		payload, _ := e.Payload.(HookStartedPayload)
		if payload.Name == "unresolved-failure-gate" {
			gateStarted = payload
		}
	})
	bus.Subscribe("hook.completed", func(e events.Event) {
		payload, _ := e.Payload.(HookCompletedPayload)
		if payload.Name == "unresolved-failure-gate" && payload.BlockStop {
			gateCompleted = payload
		}
	})

	out, err := eng.Turn(context.Background(), "finish the artifact")
	if err != nil {
		t.Fatal(err)
	}
	if out != "blocked reason explained" {
		t.Fatalf("out = %q", out)
	}
	if len(prov.histories) != 4 {
		t.Fatalf("provider calls = %d, want continuation call", len(prov.histories))
	}
	observation := prov.histories[2][len(prov.histories[2])-1].FirstText()
	for _, want := range []string{"Runtime observation", "unresolved tool failure", "check_ready", "check failed"} {
		if !strings.Contains(observation, want) {
			t.Fatalf("continuation observation missing %q:\n%s", want, observation)
		}
	}
	if recorded.Classification != ToolFailureRecoverable || recorded.Fingerprint == "" || !recorded.Blocking {
		t.Fatalf("recorded payload = %+v", recorded)
	}
	if continued.FailureCount != 1 || continued.ContinuationPromptLen == 0 {
		t.Fatalf("continued payload = %+v", continued)
	}
	if gateStarted.EventName != string(hooks.EventStop) || gateStarted.Source != "builtin" {
		t.Fatalf("gate started payload = %+v", gateStarted)
	}
	if gateCompleted.EventName != string(hooks.EventStop) || gateCompleted.Source != "builtin" || !gateCompleted.BlockStop || gateCompleted.ContinuePromptLen == 0 {
		t.Fatalf("gate completed payload = %+v", gateCompleted)
	}
}

func TestTurn_StopHookFailureFailsOnceAndEmitsHookErrored(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, false)
	runner, err := hooks.NewRunner(hooks.Config{Commands: []hooks.CommandHook{{
		Name:    "stop-fails",
		Events:  []hooks.EventName{hooks.EventStop},
		Command: runtimeHookCommand("fail"),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	eng.Hooks = runner

	var errored HookErroredPayload
	bus.Subscribe("hook.errored", func(e events.Event) {
		payload, _ := e.Payload.(HookErroredPayload)
		if payload.Name == "stop-fails" {
			errored = payload
		}
	})

	_, err = eng.Turn(context.Background(), "finish")
	if err == nil || !strings.Contains(err.Error(), "hooks: stop-fails failed") {
		t.Fatalf("err = %v, want stop hook failure", err)
	}
	if prov.called != 1 {
		t.Fatalf("provider calls = %d, want no retry loop", prov.called)
	}
	if errored.EventName != string(hooks.EventStop) || !strings.Contains(errored.Error, "stop hook failed") {
		t.Fatalf("hook errored payload = %+v", errored)
	}
}

func TestTurn_SuccessfulCheckResolvesToolFailure(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "check_1", ToolName: "check_ready", Input: map[string]any{"path": "artifact.txt"}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "check_2", ToolName: "check_ready", Input: map[string]any{"path": "artifact.txt"}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "verified"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, false)
	var attempts int
	eng.Tools.MustRegister(tools.Tool{
		Name:   "check_ready",
		Schema: map[string]any{"type": "object"},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			attempts++
			if attempts == 1 {
				return "artifact is not ready", fmt.Errorf("check failed")
			}
			return "artifact is ready", nil
		},
	})

	var resolved ToolFailureResolvedPayload
	bus.Subscribe("tool.failure.resolved", func(e events.Event) {
		resolved, _ = e.Payload.(ToolFailureResolvedPayload)
	})

	out, err := eng.Turn(context.Background(), "verify the artifact")
	if err != nil {
		t.Fatal(err)
	}
	if out != "verified" {
		t.Fatalf("out = %q", out)
	}
	if resolved.Status != ToolFailureStatusResolved || resolved.Fingerprint == "" || resolved.Reason == "" {
		t.Fatalf("resolved payload = %+v", resolved)
	}
}

func TestTurn_FileMutationMarksToolFailureStale(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "artifact.txt")
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "check_1", ToolName: "check_ready", Input: map[string]any{"path": target}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "write_1", ToolName: "write", Input: map[string]any{"path": target, "content": "ready\n"}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "updated"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, true)
	eng.Tools.MustRegister(tools.Tool{
		Name:   "check_ready",
		Schema: map[string]any{"type": "object"},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			return "artifact is not ready", fmt.Errorf("check failed")
		},
	})

	var stale ToolFailureStalePayload
	bus.Subscribe("tool.failure.stale", func(e events.Event) {
		stale, _ = e.Payload.(ToolFailureStalePayload)
	})

	out, err := eng.Turn(context.Background(), "update the artifact")
	if err != nil {
		t.Fatal(err)
	}
	if out != "updated" {
		t.Fatalf("out = %q", out)
	}
	if stale.Status != ToolFailureStatusStale || stale.Fingerprint == "" || stale.Reason == "" {
		t.Fatalf("stale payload = %+v", stale)
	}
}

func TestToolFailureLedgerReopensStaleFingerprintOnNewFailure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "artifact.txt")
	ledger := newToolFailureLedger("")
	fail := toolFailureObservation{
		ToolName:  "check_ready",
		ToolUseID: "check_1",
		Input:     map[string]any{"path": target},
		Content:   "artifact is not ready\n[tool error]\ncheck failed",
		Error:     "check failed",
	}
	recorded := ledger.recordFailure(fail)
	if recorded.Status != ToolFailureStatusUnresolved || recorded.Occurrences != 1 {
		t.Fatalf("recorded = %+v", recorded)
	}
	if err := os.WriteFile(target, []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, stale := ledger.recordSuccess(toolFailureObservation{
		ToolName:  "write",
		ToolUseID: "write_1",
		Input:     map[string]any{"path": target},
		Content:   "ok",
	})
	if len(stale) != 1 || stale[0].Status != ToolFailureStatusStale {
		t.Fatalf("stale = %+v", stale)
	}

	reopened := ledger.recordFailure(fail)
	if reopened.Status != ToolFailureStatusUnresolved || reopened.Occurrences != 1 {
		t.Fatalf("reopened = %+v", reopened)
	}
}

func TestToolFailureLedgerResolvesRelativePathsFromSessionDir(t *testing.T) {
	workDir := t.TempDir()
	sessionDir := filepath.Join(workDir, ".juex", "sessions", "session-1")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(workDir, "artifact.txt")
	if err := os.WriteFile(target, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ledger := newToolFailureLedger(sessionDir)
	recorded := ledger.recordFailure(toolFailureObservation{
		ToolName:  "check_ready",
		ToolUseID: "check_1",
		Input:     map[string]any{"path": "artifact.txt"},
		Content:   "artifact is not ready\n[tool error]\ncheck failed",
		Error:     "check failed",
	})
	if len(recorded.RelatedPaths) != 1 || recorded.RelatedPaths[0] != target {
		t.Fatalf("related paths = %+v, want %q", recorded.RelatedPaths, target)
	}
	if recorded.LatestModUnixMS == 0 {
		t.Fatalf("latest mod time not captured: %+v", recorded)
	}
	if err := os.WriteFile(target, []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, stale := ledger.recordSuccess(toolFailureObservation{
		ToolName:  "write",
		ToolUseID: "write_1",
		Input:     map[string]any{"path": "artifact.txt"},
		Content:   "ok",
	})
	if len(stale) != 1 || len(stale[0].RelatedPaths) != 1 || stale[0].RelatedPaths[0] != target {
		t.Fatalf("stale payload = %+v", stale)
	}
}

func TestTurn_RepeatedFailureUsesRepeatedStuckContinuation(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "check_1", ToolName: "check_ready", Input: map[string]any{"path": "artifact.txt"}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "check_2", ToolName: "check_ready", Input: map[string]any{"path": "artifact.txt"}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "done too early"), StopReason: llm.StopEndTurn},
		{Message: llm.TextMessage(llm.RoleAssistant, "changed approach or blocked"), StopReason: llm.StopEndTurn},
		{Message: llm.TextMessage(llm.RoleAssistant, "blocked reason explained"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, false)
	eng.Tools.MustRegister(tools.Tool{
		Name:   "check_ready",
		Schema: map[string]any{"type": "object"},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			return "artifact is not ready", fmt.Errorf("check failed")
		},
	})

	var lastRecorded ToolFailureRecordedPayload
	bus.Subscribe("tool.failure.recorded", func(e events.Event) {
		lastRecorded, _ = e.Payload.(ToolFailureRecordedPayload)
	})

	out, err := eng.Turn(context.Background(), "finish the artifact")
	if err != nil {
		t.Fatal(err)
	}
	if out != "blocked reason explained" {
		t.Fatalf("out = %q", out)
	}
	if lastRecorded.Classification != ToolFailureRepeatedStuck || lastRecorded.Occurrences != 2 {
		t.Fatalf("last recorded payload = %+v", lastRecorded)
	}
	observation := prov.histories[3][len(prov.histories[3])-1].FirstText()
	for _, want := range []string{"repeated_stuck", "different approach"} {
		if !strings.Contains(observation, want) {
			t.Fatalf("repeated continuation missing %q:\n%s", want, observation)
		}
	}
}

func TestTurn_FinishGateRequestsBlockedReasonOnRepeatedFinishAttempt(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "check_1", ToolName: "check_ready", Input: map[string]any{"path": "artifact.txt"}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "done too early"), StopReason: llm.StopEndTurn},
		{Message: llm.TextMessage(llm.RoleAssistant, "still done too early"), StopReason: llm.StopEndTurn},
		{Message: llm.TextMessage(llm.RoleAssistant, "blocked reason explained"), StopReason: llm.StopEndTurn},
	}}
	eng, _ := newEngine(t, prov, false)
	eng.Tools.MustRegister(tools.Tool{
		Name:   "check_ready",
		Schema: map[string]any{"type": "object"},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			return "artifact is not ready", fmt.Errorf("check failed")
		},
	})

	out, err := eng.Turn(context.Background(), "finish the artifact")
	if err != nil {
		t.Fatal(err)
	}
	if out != "blocked reason explained" {
		t.Fatalf("out = %q", out)
	}
	if len(prov.histories) != 4 {
		t.Fatalf("provider calls = %d, want second finish blocker continuation", len(prov.histories))
	}
	observation := prov.histories[3][len(prov.histories[3])-1].FirstText()
	for _, want := range []string{"Runtime observation", "same unresolved failure", "explicitly explain why the task is blocked"} {
		if !strings.Contains(observation, want) {
			t.Fatalf("blocked-reason observation missing %q:\n%s", want, observation)
		}
	}
}

func TestTurn_FinishGateBlocksRuntimeFatalToolFailure(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "missing_1", ToolName: "does_not_exist", Input: map[string]any{}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "done too early"), StopReason: llm.StopEndTurn},
		{Message: llm.TextMessage(llm.RoleAssistant, "blocked reason explained"), StopReason: llm.StopEndTurn},
	}}
	eng, _ := newEngine(t, prov, false)

	out, err := eng.Turn(context.Background(), "finish with tool")
	if err != nil {
		t.Fatal(err)
	}
	if out != "blocked reason explained" {
		t.Fatalf("out = %q", out)
	}
	if len(prov.histories) != 3 {
		t.Fatalf("provider calls = %d, want runtime fatal blocker continuation", len(prov.histories))
	}
	observation := prov.histories[2][len(prov.histories[2])-1].FirstText()
	for _, want := range []string{"runtime_fatal", "explicitly explain why the task is blocked", "does_not_exist"} {
		if !strings.Contains(observation, want) {
			t.Fatalf("runtime-fatal observation missing %q:\n%s", want, observation)
		}
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
