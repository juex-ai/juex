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
	"syscall"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/artifact"
	"github.com/juex-ai/juex/internal/cancellation"
	"github.com/juex-ai/juex/internal/chunkedwrite"
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

type captureOptionsProvider struct {
	opts llm.CompleteOptions
}

func (p *captureOptionsProvider) Name() string { return "capture" }

func (p *captureOptionsProvider) Complete(ctx context.Context, sys string, history []llm.Message, tools []llm.ToolSpec) (llm.Response, error) {
	return p.CompleteWithOptions(ctx, sys, history, tools, llm.CompleteOptions{})
}

func (p *captureOptionsProvider) CompleteWithOptions(ctx context.Context, sys string, history []llm.Message, tools []llm.ToolSpec, opts llm.CompleteOptions) (llm.Response, error) {
	p.opts = opts
	return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn}, nil
}

type streamDeltaProvider struct{}

func (p streamDeltaProvider) Name() string { return "streaming-mock:model" }

func (p streamDeltaProvider) Complete(ctx context.Context, sys string, history []llm.Message, tools []llm.ToolSpec) (llm.Response, error) {
	return p.CompleteWithOptions(ctx, sys, history, tools, llm.CompleteOptions{})
}

func (p streamDeltaProvider) CompleteWithOptions(ctx context.Context, sys string, history []llm.Message, tools []llm.ToolSpec, opts llm.CompleteOptions) (llm.Response, error) {
	if opts.OnDelta != nil {
		opts.OnDelta(llm.StreamDelta{Kind: "reasoning", Index: 0, Text: "thinking "})
		opts.OnDelta(llm.StreamDelta{Kind: "text", Index: 1, Text: "hello"})
	}
	return llm.Response{
		Message:    llm.TextMessage(llm.RoleAssistant, "hello"),
		StopReason: llm.StopEndTurn,
	}, nil
}

type retryDiagnosticProvider struct{}

func (p retryDiagnosticProvider) Name() string { return "retry-diagnostic" }

func (p retryDiagnosticProvider) Complete(ctx context.Context, sys string, history []llm.Message, tools []llm.ToolSpec) (llm.Response, error) {
	return p.CompleteWithOptions(ctx, sys, history, tools, llm.CompleteOptions{})
}

func (p retryDiagnosticProvider) CompleteWithOptions(ctx context.Context, sys string, history []llm.Message, tools []llm.ToolSpec, opts llm.CompleteOptions) (llm.Response, error) {
	if opts.RetryObserver != nil {
		opts.RetryObserver(llm.ProviderRetryDiagnostic{
			Provider:    "openai-codex",
			Model:       "gpt-5.5",
			Protocol:    llm.ProtocolOpenAICodexResponses,
			Transport:   llm.CodexTransportSSE,
			Operation:   "responses.sse",
			Attempt:     1,
			MaxAttempts: 11,
			DelayMS:     100,
			RetryReason: "codex_sse_read",
			RawError:    "codex SSE read: stream error",
			WillRetry:   true,
		})
	}
	return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn}, nil
}

type cancelBeforeToolProvider struct {
	cancel context.CancelFunc
}

func (p *cancelBeforeToolProvider) Name() string { return "cancel-before-tool" }

func (p *cancelBeforeToolProvider) Complete(ctx context.Context, sys string, history []llm.Message, tools []llm.ToolSpec) (llm.Response, error) {
	if p.cancel != nil {
		p.cancel()
	}
	return llm.Response{
		Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "after_cancel", ToolName: "should_not_run", Input: map[string]any{}},
		}},
		StopReason: llm.StopToolUse,
	}, nil
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
	case "goal-output":
		_, _ = os.Stdout.WriteString(`{"goal_state":{"description":"hook should not write","status":"success"}}`)
		os.Exit(0)
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
	return newEngineWithToolTimeout(t, prov, builtinTools, 0)
}

func newEngineWithToolTimeout(t *testing.T, prov llm.Provider, builtinTools bool, toolTimeoutSeconds int) (*Engine, *events.Bus) {
	t.Helper()
	reg := tools.NewRegistryWithOptions(tools.RegistryOptions{DefaultTimeoutSeconds: toolTimeoutSeconds})
	if builtinTools {
		tools.RegisterBuiltins(reg, tools.BuiltinOptions{
			Shell:              tools.DefaultShellProfile(),
			ToolTimeoutSeconds: toolTimeoutSeconds,
		})
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

func TestTurn_PassesMaxOutputTokensToProvider(t *testing.T) {
	prov := &captureOptionsProvider{}
	eng, _ := newEngine(t, prov, false)
	eng.MaxOutputTokens = 8192

	if out, err := eng.Turn(context.Background(), "hi"); err != nil || out != "done" {
		t.Fatalf("Turn() = %q, %v", out, err)
	}
	if prov.opts.Purpose != "turn" {
		t.Fatalf("purpose = %q, want turn", prov.opts.Purpose)
	}
	if prov.opts.MaxOutputTokens != 8192 {
		t.Fatalf("MaxOutputTokens = %d, want 8192", prov.opts.MaxOutputTokens)
	}
}

func TestTurn_ReturnsImagePlaceholderForImageOnlyResponse(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{
			Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
				{
					Type: llm.BlockImage,
					Media: &llm.MediaRef{
						ArtifactPath:  ".juex/artifacts/media/s/chart.png",
						MediaType:     "image/png",
						OriginalBytes: 2048,
						Width:         640,
						Height:        480,
					},
				},
			}},
			StopReason: llm.StopEndTurn,
		},
	}}
	eng, _ := newEngine(t, prov, false)

	out, err := eng.Turn(context.Background(), "show chart")
	if err != nil {
		t.Fatal(err)
	}
	if out != "[图片: chart.png (640x480, 2.0 KB)]" {
		t.Fatalf("out = %q", out)
	}
}

func TestTurn_EmitsLLMOutputDeltaEvents(t *testing.T) {
	eng, bus := newEngine(t, streamDeltaProvider{}, false)
	var got []events.Event
	bus.Subscribe("llm.output_delta", func(e events.Event) {
		got = append(got, e)
	})

	out, err := eng.Turn(context.Background(), "stream please")
	if err != nil {
		t.Fatal(err)
	}
	if out != "hello" {
		t.Fatalf("out = %q, want hello", out)
	}
	if len(got) != 2 {
		t.Fatalf("delta events = %+v, want two", got)
	}
	first, ok := got[0].Payload.(LLMOutputDeltaPayload)
	if !ok {
		t.Fatalf("first payload type = %T", got[0].Payload)
	}
	if got[0].TurnID == "" || !got[0].Transient || first.Iter != 0 || first.Model != "streaming-mock:model" || first.Kind != "reasoning" || first.Index != 0 || first.Text != "thinking " {
		t.Fatalf("first delta event = %+v payload=%+v", got[0], first)
	}
	second, ok := got[1].Payload.(LLMOutputDeltaPayload)
	if !ok {
		t.Fatalf("second payload type = %T", got[1].Payload)
	}
	if !got[1].Transient || second.Iter != 0 || second.Model != "streaming-mock:model" || second.Kind != "text" || second.Index != 1 || second.Text != "hello" {
		t.Fatalf("second delta payload = %+v", second)
	}
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
	data := readProjectedArtifact(t, eng, block.Artifact)
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
	data := readProjectedArtifact(t, eng, block.Artifact)
	if !strings.Contains(string(data), "TOOL-SECRET") {
		t.Fatalf("artifact lost original tool output")
	}
}

func readProjectedArtifact(t *testing.T, eng *Engine, projection *llm.ContextArtifactProjection) []byte {
	t.Helper()
	if projection == nil {
		t.Fatal("missing context artifact projection")
	}
	if filepath.IsAbs(projection.StoredPath) {
		t.Fatalf("stored path = %q, want workspace-relative artifact reference", projection.StoredPath)
	}
	store, err := eng.projectedArtifactStore()
	if err != nil {
		t.Fatal(err)
	}
	data, err := store.Read(artifact.Ref{
		Path:   projection.StoredPath,
		SHA256: projection.SHA256,
		Bytes:  projection.OriginalBytes,
	})
	if err != nil {
		t.Fatalf("read artifact: %v", err)
	}
	return data
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

func TestTurn_CalibratesFallbackContextUsageFromPreviousProviderUsage(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{
			Message:    llm.TextMessage(llm.RoleAssistant, "calibrated"),
			StopReason: llm.StopEndTurn,
			Usage:      llm.Usage{InputTokens: 300, OutputTokens: 1},
		},
		{
			Message:    llm.TextMessage(llm.RoleAssistant, "estimated"),
			StopReason: llm.StopEndTurn,
			Usage:      llm.Usage{OutputTokens: 1},
		},
	}}
	eng, _ := newEngine(t, prov, false)
	eng.ContextWindow = 5000

	if _, err := eng.Turn(context.Background(), strings.Repeat("calibrate ", 8)); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Turn(context.Background(), "second"); err != nil {
		t.Fatal(err)
	}

	got := eng.Session.Info(time.Now()).ContextUsage
	if got == nil {
		t.Fatal("context usage is nil")
	}
	staticEstimate := estimateContextTokens(prompt.JoinSections(eng.Prompt.Sections()), eng.Tools.Specs(), prov.histories[1])
	if got.InputTokens <= staticEstimate {
		t.Fatalf("fallback input tokens = %d, want calibrated above static estimate %d", got.InputTokens, staticEstimate)
	}
	if got.InputTokens > staticEstimate*3 {
		t.Fatalf("fallback input tokens = %d, want clamp at 3x static estimate %d", got.InputTokens, staticEstimate)
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

func TestCompactStartedIncludesToolSchemaBudget(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "summary of old work"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, false)
	eng.Compaction = DefaultCompactionPolicy()
	eng.Compaction.KeepRecentTokens = 1
	eng.Compaction.TailTurns = 0
	eng.Tools.MustRegister(tools.Tool{
		Name:        "large_schema_tool",
		Description: strings.Repeat("tool schema description ", 80),
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"payload": map[string]any{"type": "string", "description": strings.Repeat("payload ", 120)},
			},
		},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			return "ok", nil
		},
	})
	if err := eng.Session.Append(llm.TextMessage(llm.RoleUser, strings.Repeat("old ", 80))); err != nil {
		t.Fatal(err)
	}
	if err := eng.Session.Append(llm.TextMessage(llm.RoleAssistant, strings.Repeat("reply ", 80))); err != nil {
		t.Fatal(err)
	}
	withoutTools := eng.estimateContextTokens("system", nil, eng.activeContextLocked().Messages)
	var started ContextCompactStartedPayload
	bus.Subscribe("context.compact.started", func(e events.Event) {
		started = e.Payload.(ContextCompactStartedPayload)
	})

	if _, err := eng.Compact(context.Background(), "compact-turn", "system", "manual", false); err != nil {
		t.Fatal(err)
	}
	if started.TokensBefore <= withoutTools {
		t.Fatalf("tokens_before = %d, want above message-only estimate %d", started.TokensBefore, withoutTools)
	}
}

func TestCompactUsesSummaryProviderWhenConfigured(t *testing.T) {
	main := &namedCompactionProvider{name: "main:model", text: "main summary"}
	summary := &namedCompactionProvider{name: "summary:model", text: "custom summary"}
	eng, _ := newEngine(t, main, false)
	eng.SummaryProvider = summary
	eng.Compaction = DefaultCompactionPolicy()
	eng.Compaction.KeepRecentTokens = 1
	eng.Compaction.TailTurns = 0
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
	if summary.calls != 1 || main.calls != 0 {
		t.Fatalf("provider calls: summary=%d main=%d", summary.calls, main.calls)
	}
	if result.SummaryModel != "summary:model" {
		t.Fatalf("summary model = %q", result.SummaryModel)
	}
}

func TestCompactFallsBackToMainProviderWhenSummaryProviderFails(t *testing.T) {
	main := &namedCompactionProvider{name: "main:model", text: "main summary"}
	summary := &namedCompactionProvider{name: "summary:model", err: errors.New("summary model unavailable")}
	eng, bus := newEngine(t, main, false)
	eng.SummaryProvider = summary
	eng.Compaction = DefaultCompactionPolicy()
	eng.Compaction.SummaryModel = "summary:model"
	eng.Compaction.KeepRecentTokens = 1
	eng.Compaction.TailTurns = 0
	if err := eng.Session.Append(llm.TextMessage(llm.RoleUser, strings.Repeat("old ", 80))); err != nil {
		t.Fatal(err)
	}
	if err := eng.Session.Append(llm.TextMessage(llm.RoleAssistant, strings.Repeat("reply ", 80))); err != nil {
		t.Fatal(err)
	}
	var fallback ContextCompactSummaryFallbackPayload
	var retries int
	bus.Subscribe("context.compact.summary_model_fallback", func(e events.Event) {
		fallback = e.Payload.(ContextCompactSummaryFallbackPayload)
	})
	bus.Subscribe("context.compact.summary_retry", func(e events.Event) {
		retries++
	})

	result, err := eng.Compact(context.Background(), "compact-turn", "system", "manual", false)
	if err != nil {
		t.Fatal(err)
	}
	if summary.calls != 1 || main.calls != 1 {
		t.Fatalf("provider calls: summary=%d main=%d", summary.calls, main.calls)
	}
	if result.SummaryModel != "main:model" {
		t.Fatalf("summary model = %q", result.SummaryModel)
	}
	if fallback.ConfiguredModel != "summary:model" || fallback.FallbackModel != "main:model" || !strings.Contains(fallback.Error, "unavailable") {
		t.Fatalf("fallback payload = %+v", fallback)
	}
	if retries != 0 {
		t.Fatalf("summary retries = %d, want no semantic retry for transport error", retries)
	}
}

func TestCompactRetriesReasoningOnlySummaryWithLargerBudget(t *testing.T) {
	provider := &scriptedCompactionProvider{
		name: "thinking:model",
		attempts: []scriptedCompactionAttempt{
			{
				response: llm.Response{
					Message:    llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockReasoning, Text: "spent the first budget"}}},
					StopReason: llm.StopMaxTokens,
					Usage:      llm.Usage{InputTokens: 10, OutputTokens: 2},
				},
			},
			{
				response: llm.Response{
					Message:    llm.TextMessage(llm.RoleAssistant, "recovered summary"),
					StopReason: llm.StopEndTurn,
					Usage:      llm.Usage{InputTokens: 11, OutputTokens: 3},
				},
			},
		},
	}
	eng, bus := newEngine(t, provider, false)
	configureCompactionRetryTest(t, eng, 30, 2000)
	eng.ContextWindow = 5000
	eng.Compaction.ReserveTokens = 1000
	eng.Compaction.SummaryMaxTokens = 1000
	var retry ContextCompactSummaryRetryPayload
	bus.Subscribe("context.compact.summary_retry", func(e events.Event) {
		retry = e.Payload.(ContextCompactSummaryRetryPayload)
	})

	result, err := eng.Compact(context.Background(), "compact-turn", "system", "manual", false)
	if err != nil {
		t.Fatal(err)
	}
	if result.SummaryChars != len("recovered summary") || provider.calls != 2 {
		t.Fatalf("result/calls = %+v, %d", result, provider.calls)
	}
	if len(provider.options) != 2 || provider.options[0].MaxOutputTokens != 1000 || provider.options[1].MaxOutputTokens != 2000 {
		t.Fatalf("max output tokens = %+v, want [1000 2000]", compactionOptionBudgets(provider.options))
	}
	if retry.Attempt != 2 || retry.Reason != "empty_summary" || retry.StopReason != llm.StopMaxTokens || !retry.ReasoningOnly || retry.PreviousMaxOutputTokens != 1000 || retry.MaxOutputTokens != 2000 {
		t.Fatalf("retry payload = %+v", retry)
	}
	if len(provider.histories) != 2 || provider.histories[0][0].FirstText() == provider.histories[1][0].FirstText() {
		t.Fatalf("retry summary request was not rebuilt for the larger budget")
	}
	usage := eng.Session.TokenUsageSnapshot()
	if usage != (llm.Usage{InputTokens: 21, OutputTokens: 5}) {
		t.Fatalf("token usage = %+v, want aggregate retry usage", usage)
	}
}

func TestCompactCapsSummaryRetryToBoundedRequest(t *testing.T) {
	provider := &scriptedCompactionProvider{
		name: "thinking:model",
		attempts: []scriptedCompactionAttempt{
			{response: llm.Response{Message: llm.Message{Role: llm.RoleAssistant}}},
			{response: llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "bounded retry summary")}},
		},
	}
	eng, bus := newEngine(t, provider, false)
	configureCompactionRetryTest(t, eng, 30, 2000)
	eng.ContextWindow = 1200
	eng.Compaction.ReserveTokens = 300
	eng.Compaction.SummaryMaxTokens = 600
	var retry ContextCompactSummaryRetryPayload
	bus.Subscribe("context.compact.summary_retry", func(e events.Event) {
		retry = e.Payload.(ContextCompactSummaryRetryPayload)
	})

	if _, err := eng.Compact(context.Background(), "compact-turn", "system", "manual", false); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 2 || len(provider.options) != 2 || len(provider.systems) != 2 || len(provider.histories) != 2 {
		t.Fatalf("calls/options/systems/histories = %d/%d/%d/%d", provider.calls, len(provider.options), len(provider.systems), len(provider.histories))
	}
	retryBudget := provider.options[1].MaxOutputTokens
	if retryBudget >= 1200 {
		t.Fatalf("retry budget = %d, want less than uncapped double 1200", retryBudget)
	}
	policy := effectiveCompactionPolicy(eng.Compaction, eng.ContextWindow)
	retryInputTokens := estimateContextTokens(provider.systems[1], nil, provider.histories[1])
	if retryInputTokens+retryBudget > policy.TriggerTokens {
		t.Fatalf("retry input + output = %d + %d, want <= trigger %d", retryInputTokens, retryBudget, policy.TriggerTokens)
	}
	if retry.PreviousMaxOutputTokens != 600 || retry.MaxOutputTokens != retryBudget {
		t.Fatalf("retry payload = %+v, want bounded budget %d", retry, retryBudget)
	}
}

func TestCompactReturnsEmptySummaryAfterSingleRetry(t *testing.T) {
	provider := &scriptedCompactionProvider{
		name: "thinking:model",
		attempts: []scriptedCompactionAttempt{
			{response: llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockReasoning, Text: "first thought"}}}, StopReason: llm.StopMaxTokens, Usage: llm.Usage{InputTokens: 3, OutputTokens: 1}}},
			{response: llm.Response{Message: llm.Message{Role: llm.RoleAssistant}, StopReason: llm.StopEndTurn, Usage: llm.Usage{InputTokens: 4, OutputTokens: 1}}},
		},
	}
	eng, bus := newEngine(t, provider, false)
	configureCompactionRetryTest(t, eng, 2, 200)
	eng.Compaction.SummaryMaxTokens = 100
	var retries int
	bus.Subscribe("context.compact.summary_retry", func(e events.Event) {
		retries++
	})

	_, err := eng.Compact(context.Background(), "compact-turn", "system", "manual", false)
	if err == nil || !strings.Contains(err.Error(), "compact context: empty summary") {
		t.Fatalf("error = %v, want empty summary", err)
	}
	if provider.calls != 2 || retries != 1 {
		t.Fatalf("calls/retries = %d/%d, want 2/1", provider.calls, retries)
	}
	if usage := eng.Session.TokenUsageSnapshot(); usage != (llm.Usage{InputTokens: 7, OutputTokens: 2}) {
		t.Fatalf("token usage = %+v, want failed-attempt usage", usage)
	}
	for _, msg := range eng.Session.History {
		if msg.Kind == llm.MessageKindCompact {
			t.Fatalf("unexpected compact marker after exhausted retry: %+v", msg)
		}
	}
}

func TestCompactFallsBackAfterEmptySummaryRetry(t *testing.T) {
	main := &scriptedCompactionProvider{
		name: "main:model",
		attempts: []scriptedCompactionAttempt{{response: llm.Response{
			Message:    llm.TextMessage(llm.RoleAssistant, "main recovered summary"),
			StopReason: llm.StopEndTurn,
			Usage:      llm.Usage{InputTokens: 7, OutputTokens: 2},
		}}},
	}
	summary := &scriptedCompactionProvider{
		name: "summary:model",
		attempts: []scriptedCompactionAttempt{
			{response: llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockReasoning, Text: "first thought"}}}, StopReason: llm.StopMaxTokens, Usage: llm.Usage{InputTokens: 5, OutputTokens: 1}}},
			{response: llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockReasoning, Text: "second thought"}}}, StopReason: llm.StopMaxTokens, Usage: llm.Usage{InputTokens: 6, OutputTokens: 1}}},
		},
	}
	eng, bus := newEngine(t, main, false)
	eng.SummaryProvider = summary
	configureCompactionRetryTest(t, eng, 2, 200)
	eng.Compaction.SummaryModel = "summary:model"
	eng.Compaction.SummaryMaxTokens = 100
	var fallback ContextCompactSummaryFallbackPayload
	bus.Subscribe("context.compact.summary_model_fallback", func(e events.Event) {
		fallback = e.Payload.(ContextCompactSummaryFallbackPayload)
	})

	result, err := eng.Compact(context.Background(), "compact-turn", "system", "manual", false)
	if err != nil {
		t.Fatal(err)
	}
	if summary.calls != 2 || main.calls != 1 || result.SummaryModel != "main:model" {
		t.Fatalf("summary/main/result = %d/%d/%+v", summary.calls, main.calls, result)
	}
	if len(main.options) != 1 || main.options[0].MaxOutputTokens != 200 {
		t.Fatalf("fallback max output tokens = %+v, want 200", compactionOptionBudgets(main.options))
	}
	if fallback.ConfiguredModel != "summary:model" || fallback.FallbackModel != "main:model" || !strings.Contains(fallback.Error, "empty summary") {
		t.Fatalf("fallback payload = %+v", fallback)
	}
	usage := eng.Session.TokenUsageSnapshot()
	if usage != (llm.Usage{InputTokens: 18, OutputTokens: 4}) {
		t.Fatalf("token usage = %+v, want all summary attempts", usage)
	}
}

func TestCompactFallsBackWhenEmptySummaryRetryFails(t *testing.T) {
	main := &scriptedCompactionProvider{
		name: "main:model",
		attempts: []scriptedCompactionAttempt{{response: llm.Response{
			Message: llm.TextMessage(llm.RoleAssistant, "main recovered summary"),
			Usage:   llm.Usage{InputTokens: 7, OutputTokens: 2},
		}}},
	}
	summary := &scriptedCompactionProvider{
		name: "summary:model",
		attempts: []scriptedCompactionAttempt{
			{response: llm.Response{
				Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockReasoning, Text: "first thought"}}},
				Usage:   llm.Usage{InputTokens: 5, OutputTokens: 1},
			}},
			{err: errors.New("retry unavailable")},
		},
	}
	eng, bus := newEngine(t, main, false)
	eng.SummaryProvider = summary
	configureCompactionRetryTest(t, eng, 2, 200)
	eng.Compaction.SummaryModel = "summary:model"
	eng.Compaction.SummaryMaxTokens = 100
	var fallback ContextCompactSummaryFallbackPayload
	bus.Subscribe("context.compact.summary_model_fallback", func(e events.Event) {
		fallback = e.Payload.(ContextCompactSummaryFallbackPayload)
	})

	result, err := eng.Compact(context.Background(), "compact-turn", "system", "manual", false)
	if err != nil {
		t.Fatal(err)
	}
	if summary.calls != 2 || main.calls != 1 || result.SummaryModel != "main:model" {
		t.Fatalf("summary/main/result = %d/%d/%+v", summary.calls, main.calls, result)
	}
	if !strings.Contains(fallback.Error, "retry unavailable") {
		t.Fatalf("fallback payload = %+v", fallback)
	}
	if usage := eng.Session.TokenUsageSnapshot(); usage != (llm.Usage{InputTokens: 12, OutputTokens: 3}) {
		t.Fatalf("token usage = %+v, want successful attempts only", usage)
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

type namedCompactionProvider struct {
	name  string
	text  string
	err   error
	calls int
}

type scriptedCompactionAttempt struct {
	response llm.Response
	err      error
}

type scriptedCompactionProvider struct {
	name      string
	attempts  []scriptedCompactionAttempt
	calls     int
	options   []llm.CompleteOptions
	systems   []string
	histories [][]llm.Message
}

func (p *scriptedCompactionProvider) Name() string { return p.name }

func (p *scriptedCompactionProvider) Complete(ctx context.Context, sys string, history []llm.Message, tools []llm.ToolSpec) (llm.Response, error) {
	return p.CompleteWithOptions(ctx, sys, history, tools, llm.CompleteOptions{})
}

func (p *scriptedCompactionProvider) CompleteWithOptions(ctx context.Context, sys string, history []llm.Message, tools []llm.ToolSpec, opts llm.CompleteOptions) (llm.Response, error) {
	if p.calls >= len(p.attempts) {
		return llm.Response{}, fmt.Errorf("scriptedCompactionProvider: out of attempts (called=%d)", p.calls)
	}
	p.options = append(p.options, opts)
	p.systems = append(p.systems, sys)
	p.histories = append(p.histories, append([]llm.Message(nil), history...))
	attempt := p.attempts[p.calls]
	p.calls++
	return attempt.response, attempt.err
}

func configureCompactionRetryTest(t *testing.T, eng *Engine, messageCount, messageChars int) {
	t.Helper()
	eng.Compaction = DefaultCompactionPolicy()
	eng.Compaction.KeepRecentTokens = 1
	eng.Compaction.TailTurns = 0
	for i := 0; i < messageCount; i++ {
		msg := llm.TextMessage(llm.RoleUser, fmt.Sprintf("message-%02d %s", i, strings.Repeat("x", messageChars)))
		if err := eng.Session.Append(msg); err != nil {
			t.Fatal(err)
		}
	}
}

func compactionOptionBudgets(options []llm.CompleteOptions) []int {
	out := make([]int, len(options))
	for i, opts := range options {
		out[i] = opts.MaxOutputTokens
	}
	return out
}

func (p *namedCompactionProvider) Name() string { return p.name }

func (p *namedCompactionProvider) Complete(ctx context.Context, sys string, history []llm.Message, tools []llm.ToolSpec) (llm.Response, error) {
	return p.CompleteWithOptions(ctx, sys, history, tools, llm.CompleteOptions{})
}

func (p *namedCompactionProvider) CompleteWithOptions(ctx context.Context, sys string, history []llm.Message, tools []llm.ToolSpec, opts llm.CompleteOptions) (llm.Response, error) {
	p.calls++
	if p.err != nil {
		return llm.Response{}, p.err
	}
	text := p.text
	if text == "" {
		text = "summary"
	}
	return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, text), StopReason: llm.StopEndTurn}, nil
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

func TestTurn_ToolStructuredMediaBecomesToolResultMedia(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "tu_image", ToolName: "read_image", Input: map[string]any{"path": "shot.png"}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "saw it"), StopReason: llm.StopEndTurn},
	}}
	eng, _ := newEngine(t, prov, false)
	media := llm.MediaRef{
		ArtifactPath:  ".juex/artifacts/media/read/test.png",
		MediaType:     "image/png",
		SHA256:        strings.Repeat("a", 64),
		OriginalBytes: 12,
		Width:         2,
		Height:        1,
	}
	eng.Tools.MustRegister(tools.Tool{
		Name:   "read_image",
		Schema: map[string]any{"type": "object"},
		ResultHandler: func(ctx context.Context, in map[string]any) (tools.Result, error) {
			return tools.Result{
				Text:       "[image 2x1, 12 bytes, image/png]",
				Structured: tools.MediaResult{Media: media},
			}, nil
		},
	})

	out, err := eng.Turn(context.Background(), "read the image")
	if err != nil {
		t.Fatal(err)
	}
	if out != "saw it" {
		t.Fatalf("out = %q", out)
	}
	result := eng.Session.History[2]
	if result.Role != llm.RoleUser || len(result.Blocks) != 1 {
		t.Fatalf("tool result message = %+v", result)
	}
	block := result.Blocks[0]
	if block.Type != llm.BlockToolResult || block.Media == nil {
		t.Fatalf("tool result block = %+v, want media tool result", block)
	}
	if block.Media.ArtifactPath != media.ArtifactPath || block.Content != "[image 2x1, 12 bytes, image/png]" {
		t.Fatalf("tool result block = %+v, want media and content preserved", block)
	}
}

func TestTurn_ToolStructuredChunkedWriteBecomesToolResultLifecycleFact(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "cw_begin", ToolName: "chunked_fact", Input: map[string]any{"path": "long.md"}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
	}}
	eng, _ := newEngine(t, prov, false)
	eng.Tools.MustRegister(tools.Tool{
		Name:   "chunked_fact",
		Schema: map[string]any{"type": "object"},
		ResultHandler: func(ctx context.Context, in map[string]any) (tools.Result, error) {
			return tools.Result{
				Text: "write_begin presentation text without machine parsing contract",
				Structured: chunkedwrite.Event{
					Kind:    chunkedwrite.EventBegin,
					WriteID: "w_runtime",
					Path:    "long.md",
					Mode:    chunkedwrite.ModeOverwrite,
				},
			}, nil
		},
	})

	out, err := eng.Turn(context.Background(), "begin chunked write")
	if err != nil {
		t.Fatal(err)
	}
	if out != "done" {
		t.Fatalf("out = %q", out)
	}
	result := eng.Session.History[2]
	if result.Role != llm.RoleUser || len(result.Blocks) != 1 {
		t.Fatalf("tool result message = %+v", result)
	}
	fact := result.Blocks[0].ChunkedWrite
	if fact == nil || fact.Kind != chunkedwrite.EventBegin || fact.WriteID != "w_runtime" {
		t.Fatalf("chunked write fact = %+v", fact)
	}
}

func TestTurn_PostToolUseHookDenyPreservesChunkedWriteLifecycleFact(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "cw_commit", ToolName: "chunked_fact", Input: map[string]any{"write_id": "w_runtime"}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
	}}
	eng, _ := newEngine(t, prov, false)
	eng.Tools.MustRegister(tools.Tool{
		Name:   "chunked_fact",
		Schema: map[string]any{"type": "object"},
		ResultHandler: func(ctx context.Context, in map[string]any) (tools.Result, error) {
			return tools.Result{
				Text: "write_commit presentation text",
				Structured: chunkedwrite.Event{
					Kind:    chunkedwrite.EventCommit,
					WriteID: "w_runtime",
					Path:    "long.md",
					Chunks:  1,
				},
			}, nil
		},
	})
	eng.Hooks = &fakeHookRunner{responses: map[hooks.EventName][]hooks.Output{
		hooks.EventPostToolUse: {{Decision: hooks.DecisionDeny, AdditionalContext: "redaction required"}},
	}}

	out, err := eng.Turn(context.Background(), "commit chunked write")
	if err != nil {
		t.Fatal(err)
	}
	if out != "done" {
		t.Fatalf("out = %q", out)
	}
	result := eng.Session.History[2]
	if result.Role != llm.RoleUser || len(result.Blocks) != 1 {
		t.Fatalf("tool result message = %+v", result)
	}
	block := result.Blocks[0]
	if !block.IsError || !strings.Contains(block.Content, "redaction required") {
		t.Fatalf("tool result block = %+v, want post hook error", block)
	}
	if block.ChunkedWrite == nil || block.ChunkedWrite.Kind != chunkedwrite.EventCommit || block.ChunkedWrite.WriteID != "w_runtime" {
		t.Fatalf("chunked write fact = %+v", block.ChunkedWrite)
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
	if out != "done too early" {
		t.Fatalf("out = %q", out)
	}
	if len(prov.histories) != 2 {
		t.Fatalf("provider calls = %d, want no failure-ledger continuation", len(prov.histories))
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
	if out != "done too early" {
		t.Fatalf("out = %q", out)
	}
	if len(prov.histories) != 2 {
		t.Fatalf("provider calls = %d, want no failure-ledger continuation", len(prov.histories))
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
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "goal_create_1", ToolName: GoalToolCreate, Input: map[string]any{
				"description":             "ship this",
				"acceptance_criteria":     []any{"tests pass"},
				"required_artifacts":      []any{"artifact.txt"},
				"validation_requirements": []any{"go test ./..."},
				"verification_method":     "tests pass",
			}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "too early"), StopReason: llm.StopEndTurn},
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "goal_update_1", ToolName: GoalToolUpdate, Input: map[string]any{
				"status":        string(GoalStatusSuccess),
				"status_reason": "tests passed",
			}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "final"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, false)
	eng.GoalState = NewGoalStateStore(eng.Session.Dir, GoalStateOptions{})
	if err := RegisterGoalTools(eng.Tools, eng); err != nil {
		t.Fatal(err)
	}
	var continued int32
	bus.Subscribe("goal.continued", func(e events.Event) { atomic.AddInt32(&continued, 1) })

	out, err := eng.Turn(context.Background(), "ship this")
	if err != nil {
		t.Fatal(err)
	}
	if out != "final" {
		t.Fatalf("out = %q", out)
	}
	if len(prov.histories) != 4 {
		t.Fatalf("provider calls = %d", len(prov.histories))
	}
	continuationHistory := prov.histories[2]
	if len(continuationHistory) < 2 {
		t.Fatalf("continuation history = %+v", continuationHistory)
	}
	if got := continuationHistory[len(continuationHistory)-2].FirstText(); !strings.Contains(got, "current session goal is still in progress") {
		t.Fatalf("goal continuation = %q", got)
	}
	goalContext := continuationHistory[len(continuationHistory)-1]
	if goalContext.Kind != llm.MessageKindRuntimeContext ||
		!strings.Contains(goalContext.FirstText(), "Current goal contract") ||
		!strings.Contains(goalContext.FirstText(), "artifact.txt") {
		t.Fatalf("goal runtime context = %+v", goalContext)
	}
	if atomic.LoadInt32(&continued) != 1 {
		t.Fatalf("goal.continued events = %d", continued)
	}
	state, err := eng.GoalState.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != GoalStatusSuccess || state.StatusReason != "tests passed" || state.ContinuationCount != 1 || len(state.RequiredArtifacts) != 1 {
		t.Fatalf("goal state = %+v", state)
	}
}

func TestTurn_UserMessageDoesNotCreateGoalState(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn},
	}}
	eng, _ := newEngine(t, prov, false)
	eng.GoalState = NewGoalStateStore(eng.Session.Dir, GoalStateOptions{})

	out, err := eng.Turn(context.Background(), "this is normal context, not a goal")
	if err != nil {
		t.Fatal(err)
	}
	if out != "ok" {
		t.Fatalf("out = %q", out)
	}
	snapshot, err := eng.GoalState.StatusSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot != nil {
		t.Fatalf("ordinary turn created goal: %+v", snapshot)
	}
}

func TestTurn_HookGoalStateOutputDoesNotModifyGoal(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn},
	}}
	eng, _ := newEngine(t, prov, false)
	eng.GoalState = NewGoalStateStore(eng.Session.Dir, GoalStateOptions{})
	runner, err := hooks.NewRunner(hooks.Config{Commands: []hooks.CommandHook{{
		Name:    "legacy-goal-output",
		Events:  []hooks.EventName{hooks.EventStop},
		Command: runtimeHookCommand("goal-output"),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	eng.Hooks = runner

	out, err := eng.Turn(context.Background(), "finish")
	if err != nil {
		t.Fatal(err)
	}
	if out != "ok" {
		t.Fatalf("out = %q", out)
	}
	snapshot, err := eng.GoalState.StatusSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot != nil {
		t.Fatalf("hook goal_state output mutated goal: %+v", snapshot)
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

func TestTurn_RepairsDanglingToolUseBeforeAppendingNewUserInput(t *testing.T) {
	root := t.TempDir()
	sess, err := session.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(llm.TextMessage(llm.RoleUser, "first")); err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{
		Type:      llm.BlockToolUse,
		ToolUseID: "interrupted",
		ToolName:  "grep",
		Input:     map[string]any{"pattern": "needle"},
	}}}); err != nil {
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
		{Message: llm.TextMessage(llm.RoleAssistant, "recovered"), StopReason: llm.StopEndTurn},
	}}
	eng := newEngineForSession(t, reloaded, prov)
	if _, err := eng.Turn(context.Background(), "second"); err != nil {
		t.Fatal(err)
	}
	if len(prov.histories) != 1 {
		t.Fatalf("provider calls = %d, want 1", len(prov.histories))
	}
	got := prov.histories[0]
	if len(got) != 4 {
		t.Fatalf("provider history len = %d, want repaired history before new input: %+v", len(got), got)
	}
	repair := got[2]
	if repair.Role != llm.RoleUser || len(repair.Blocks) != 1 || repair.Blocks[0].Type != llm.BlockToolResult {
		t.Fatalf("repair message = %+v", repair)
	}
	if repair.Blocks[0].ToolUseID != "interrupted" || !repair.Blocks[0].IsError {
		t.Fatalf("repair block = %+v", repair.Blocks[0])
	}
	if got[3].FirstText() != "second" {
		t.Fatalf("new user message = %+v", got[3])
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

func TestTurn_CompactedAdmittedPendingInputWithExistingMessageIDIsNotReplayed(t *testing.T) {
	root := t.TempDir()
	sess, err := session.New(root)
	if err != nil {
		t.Fatal(err)
	}
	store := NewPendingInputQueue(sess.Dir, PendingInputQueueOptions{Now: func() time.Time { return time.Date(2026, 6, 14, 8, 0, 0, 0, time.UTC) }})
	record, err := store.Enqueue(llm.TextMessage(llm.RoleUser, "already appended before compact"), PendingInputOptions{ID: "event-1", TTL: time.Hour}, "turn-old")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkAdmitted([]string{record.ID}, "turn-old"); err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(record.Message); err != nil {
		t.Fatal(err)
	}
	compact := llm.TextMessage(llm.RoleUser, "summary")
	compact.ID = "compact-1"
	compact.Kind = llm.MessageKindCompact
	if err := sess.Append(compact); err != nil {
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
	if got := len(reloaded.History); got != 1 || reloaded.History[0].ID != "compact-1" {
		t.Fatalf("active history = %+v, want only compact marker", reloaded.History)
	}
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
	_, full, err := session.LoadInfo(sess.Dir)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, msg := range full {
		if msg.ID == record.MessageID {
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("persisted message %q count = %d, want 1", record.MessageID, seen)
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

func TestTurn_EmitsLLMRetryDiagnostics(t *testing.T) {
	eng, bus := newEngine(t, retryDiagnosticProvider{}, false)
	var got []LLMRetryPayload
	bus.Subscribe("llm.retry", func(e events.Event) {
		payload, ok := e.Payload.(LLMRetryPayload)
		if ok {
			got = append(got, payload)
		}
	})

	out, err := eng.Turn(context.Background(), "trigger provider retry")
	if err != nil {
		t.Fatal(err)
	}
	if out != "done" {
		t.Fatalf("out = %q, want done", out)
	}
	if len(got) != 1 {
		t.Fatalf("retry events = %+v, want one", got)
	}
	event := got[0]
	if event.Provider != "openai-codex" || event.Model != "gpt-5.5" || event.Transport != llm.CodexTransportSSE {
		t.Fatalf("retry identity = %+v", event)
	}
	if !event.WillRetry || event.Attempt != 1 || event.MaxAttempts != 11 || event.DelayMS != 100 || event.RetryReason != "codex_sse_read" {
		t.Fatalf("retry diagnostic = %+v", event)
	}
	if event.Purpose != "turn" || event.Iter == nil || *event.Iter != 0 {
		t.Fatalf("retry runtime context = %+v, want purpose turn iter 0", event)
	}
}

func TestCompact_EmitsLLMRetryDiagnostics(t *testing.T) {
	eng, bus := newEngine(t, retryDiagnosticProvider{}, false)
	eng.Compaction = DefaultCompactionPolicy()
	eng.Compaction.KeepRecentTokens = 1
	eng.Compaction.TailTurns = 0
	if err := eng.Session.Append(llm.TextMessage(llm.RoleUser, strings.Repeat("old ", 80))); err != nil {
		t.Fatal(err)
	}
	if err := eng.Session.Append(llm.TextMessage(llm.RoleAssistant, strings.Repeat("reply ", 80))); err != nil {
		t.Fatal(err)
	}
	var got []LLMRetryPayload
	bus.Subscribe("llm.retry", func(e events.Event) {
		payload, ok := e.Payload.(LLMRetryPayload)
		if ok {
			got = append(got, payload)
		}
	})

	result, err := eng.Compact(context.Background(), "compact-turn", "system", "manual", false)
	if err != nil {
		t.Fatal(err)
	}
	if result.MessageID == "" {
		t.Fatalf("result = %+v", result)
	}
	if len(got) != 1 {
		t.Fatalf("retry events = %+v, want one", got)
	}
	event := got[0]
	if event.Purpose != "compaction" || event.Iter != nil || event.Provider != "openai-codex" || !event.WillRetry {
		t.Fatalf("retry event = %+v", event)
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
	if !errors.Is(err, cancellation.ErrUserCancelled) {
		t.Fatalf("err = %v, want ErrUserCancelled", err)
	}
}

func TestTurn_SignalCancellationEventPayload(t *testing.T) {
	prov := &mockProvider{
		script: []llm.Response{{Message: llm.TextMessage(llm.RoleAssistant, "x"), StopReason: llm.StopEndTurn}},
		delay:  500 * time.Millisecond,
	}
	eng, bus := newEngine(t, prov, false)
	var payload TurnErroredPayload
	bus.Subscribe("turn.errored", func(e events.Event) {
		payload, _ = e.Payload.(TurnErroredPayload)
	})

	ctx, cancel := context.WithCancelCause(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel(cancellation.NewSignalError(syscall.SIGTERM))
	}()

	_, err := eng.Turn(ctx, "hi")
	if err == nil {
		t.Fatal("expected signal cancellation error")
	}
	if signalErr, ok := cancellation.AsSignalError(err); !ok || signalErr.Signal != "SIGTERM" {
		t.Fatalf("err = %T %v, want SIGTERM signal error", err, err)
	}
	if payload.Error != "run terminated by signal SIGTERM (15)" {
		t.Fatalf("turn.errored error = %q", payload.Error)
	}
	if payload.ErrorKind != "terminated" {
		t.Fatalf("turn.errored error_kind = %q, want terminated", payload.ErrorKind)
	}
	if payload.Signal != "SIGTERM" || payload.SignalNumber != 15 || !payload.Interrupted {
		t.Fatalf("turn.errored payload = %+v, want signal metadata", payload)
	}
	if strings.Contains(payload.Error, "by user") {
		t.Fatalf("turn.errored error should not blame user: %q", payload.Error)
	}
}

func TestTurn_DoesNotDispatchToolAfterProviderCancelsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	prov := &cancelBeforeToolProvider{cancel: cancel}
	eng, _ := newEngine(t, prov, false)
	var toolCalls atomic.Int32
	eng.Tools.MustRegister(tools.Tool{
		Name:   "should_not_run",
		Schema: map[string]any{"type": "object"},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			toolCalls.Add(1)
			return "unexpected", nil
		},
	})

	_, err := eng.Turn(ctx, "cancel before tool")
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if !errors.Is(err, cancellation.ErrUserCancelled) {
		t.Fatalf("err = %v, want ErrUserCancelled", err)
	}
	if got := toolCalls.Load(); got != 0 {
		t.Fatalf("tool calls = %d, want 0 after cancellation", got)
	}
}

func TestTurn_CancellationDuringToolPersistsToolResult(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "cancel_me", ToolName: "slow", Input: map[string]any{}},
		}}, StopReason: llm.StopToolUse},
	}}
	eng, bus := newEngine(t, prov, false)
	var erroredPayload toolevents.ErroredPayload
	bus.Subscribe(toolevents.ErroredType, func(e events.Event) {
		erroredPayload, _ = e.Payload.(toolevents.ErroredPayload)
	})
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
	if !errors.Is(err, cancellation.ErrUserCancelled) {
		t.Fatalf("err = %v, want ErrUserCancelled", err)
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
	if !strings.Contains(block.Content, "cancelled by user") {
		t.Fatalf("tool result content = %q, want cancelled by user", block.Content)
	}
	if got := erroredPayload.Error; got != "cancelled by user" {
		t.Fatalf("tool.errored error = %q, want cancelled by user", got)
	}
}

func TestTurn_ToolTimeoutPersistsErrorWithoutFailureLedgerContinuation(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "slow_1", ToolName: "slow", Input: map[string]any{}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "done too early"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, false)
	eng.Tools.MustRegister(tools.Tool{
		Name:           "slow",
		Schema:         map[string]any{"type": "object"},
		TimeoutSeconds: 1,
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
	if out != "done too early" {
		t.Fatalf("out = %q, want final answer without failure-ledger continuation", out)
	}
	if len(prov.histories) != 2 {
		t.Fatalf("provider calls = %d, want no failure-ledger continuation", len(prov.histories))
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
	if got := erroredPayload.ErrorKind; got != "timeout" {
		t.Fatalf("errored error_kind = %q, want timeout", got)
	}
	if !strings.Contains(erroredPayload.RawCause, "context deadline exceeded") {
		t.Fatalf("errored raw_cause = %q, want original deadline cause", erroredPayload.RawCause)
	}
	if got := erroredPayload.Len; got != len("partial stdout\npartial stderr\n") {
		t.Fatalf("errored len = %v, want captured output length", got)
	}
	if got := erroredPayload.Preview; got != "partial stdout\npartial stderr\n" {
		t.Fatalf("errored preview = %v, want captured output preview", got)
	}
}

func TestTurn_DirectToolDeadlineUsesTimeoutContract(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "deadline_1", ToolName: "deadline", Input: map[string]any{}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, false)
	eng.Tools.MustRegister(tools.Tool{
		Name:           "deadline",
		Schema:         map[string]any{"type": "object"},
		TimeoutSeconds: 1,
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			return "partial output", context.DeadlineExceeded
		},
	})

	var erroredPayload toolevents.ErroredPayload
	bus.Subscribe(toolevents.ErroredType, func(e events.Event) {
		erroredPayload, _ = e.Payload.(toolevents.ErroredPayload)
	})

	out, err := eng.Turn(context.Background(), "run deadline")
	if err != nil {
		t.Fatal(err)
	}
	if out != "done" {
		t.Fatalf("out = %q, want done", out)
	}
	block := eng.Session.History[2].Blocks[0]
	if !block.IsError {
		t.Fatalf("tool result block = %+v, want error", block)
	}
	if !strings.Contains(block.Content, "tools: deadline timed out after 1s") {
		t.Fatalf("tool result content = %q, want public timeout", block.Content)
	}
	if strings.Contains(block.Content, "context deadline exceeded") {
		t.Fatalf("tool result content = %q, should not expose raw deadline", block.Content)
	}
	if !erroredPayload.TimedOut || erroredPayload.ErrorKind != "timeout" {
		t.Fatalf("errored payload = %+v, want timeout classification", erroredPayload)
	}
	if !strings.Contains(erroredPayload.RawCause, "context deadline exceeded") {
		t.Fatalf("raw_cause = %q, want original deadline cause", erroredPayload.RawCause)
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
	if assistant.Blocks[0].TimeoutSeconds != 0 {
		t.Fatalf("assistant timeout = %d, want shell generic timeout disabled", assistant.Blocks[0].TimeoutSeconds)
	}
	respondedCalls := respondedPayload.ToolCalls
	if len(respondedCalls) != 1 {
		t.Fatalf("responded tool_calls = %+v, want one tool call", respondedPayload.ToolCalls)
	}
	respondedInput := respondedCalls[0].Input
	if respondedInput["cmd"] != "echo raw-ok" {
		t.Fatalf("responded tool input = %+v, want normalized command", respondedInput)
	}
	if got := respondedCalls[0].TimeoutSeconds; got != 0 {
		t.Fatalf("responded timeout = %v, want shell generic timeout disabled", got)
	}
	requestedInput := requestedPayload.Input
	if requestedInput["cmd"] != "echo raw-ok" {
		t.Fatalf("requested input = %+v, want normalized command", requestedInput)
	}
	if got := requestedPayload.TimeoutSeconds; got != 0 {
		t.Fatalf("requested timeout = %v, want shell generic timeout disabled", got)
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
	}}
	eng, bus := newEngine(t, prov, true)
	var errs int32
	bus.Subscribe(toolevents.ErroredType, func(e events.Event) { atomic.AddInt32(&errs, 1) })

	out, err := eng.Turn(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if out != "done too early" {
		t.Fatalf("got %q", out)
	}
	if len(prov.histories) != 2 {
		t.Fatalf("provider calls = %d, want no failure-ledger continuation", len(prov.histories))
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

func TestTurn_FinishPolicyAllowsCleanFinishWithoutFailureGate(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, false)

	var failureGateHooks int32
	bus.Subscribe("hook.completed", func(e events.Event) {
		payload, _ := e.Payload.(HookCompletedPayload)
		if payload.Name == "unresolved-failure-gate" {
			atomic.AddInt32(&failureGateHooks, 1)
		}
	})

	out, err := eng.Turn(context.Background(), "finish")
	if err != nil {
		t.Fatal(err)
	}
	if out != "ok" {
		t.Fatalf("out = %q", out)
	}
	if atomic.LoadInt32(&failureGateHooks) != 0 {
		t.Fatalf("unresolved-failure-gate should not run")
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

func TestTurn_FailureLedgerRecordsUnresolvedBlockingToolFailureWithoutContinuation(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "check_1", ToolName: "check_ready", Input: map[string]any{"path": "artifact.txt"}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "done too early"), StopReason: llm.StopEndTurn},
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
	var continued int32
	bus.Subscribe("tool.failure.recorded", func(e events.Event) {
		recorded, _ = e.Payload.(ToolFailureRecordedPayload)
	})
	bus.Subscribe("tool.failure.continued", func(e events.Event) {
		atomic.AddInt32(&continued, 1)
	})
	var failureGateHooks int32
	bus.Subscribe("hook.started", func(e events.Event) {
		payload, _ := e.Payload.(HookStartedPayload)
		if payload.Name == "unresolved-failure-gate" {
			atomic.AddInt32(&failureGateHooks, 1)
		}
	})
	bus.Subscribe("hook.completed", func(e events.Event) {
		payload, _ := e.Payload.(HookCompletedPayload)
		if payload.Name == "unresolved-failure-gate" {
			atomic.AddInt32(&failureGateHooks, 1)
		}
	})

	out, err := eng.Turn(context.Background(), "finish the artifact")
	if err != nil {
		t.Fatal(err)
	}
	if out != "done too early" {
		t.Fatalf("out = %q", out)
	}
	if len(prov.histories) != 2 {
		t.Fatalf("provider calls = %d, want no failure-ledger continuation", len(prov.histories))
	}
	if recorded.Classification != ToolFailureRecoverable || recorded.Fingerprint == "" || !recorded.Blocking {
		t.Fatalf("recorded payload = %+v", recorded)
	}
	if atomic.LoadInt32(&continued) != 0 {
		t.Fatalf("failure ledger should not continue finish")
	}
	if atomic.LoadInt32(&failureGateHooks) != 0 {
		t.Fatalf("unresolved-failure-gate should not emit hook events")
	}
}

type runtimeExitCodeStructuredResult struct {
	code int
}

func (r runtimeExitCodeStructuredResult) ToolCallExitCode() (int, bool) {
	return r.code, true
}

func TestTurn_FailureLedgerUsesToolObservationExitCode(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "check_1", ToolName: "check_ready", Input: map[string]any{"path": "artifact.txt"}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "done too early"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, false)
	eng.Tools.MustRegister(tools.Tool{
		Name:   "check_ready",
		Schema: map[string]any{"type": "object"},
		ResultHandler: func(ctx context.Context, in map[string]any) (tools.Result, error) {
			return tools.Result{
				Text:       "opaque failure output",
				Structured: runtimeExitCodeStructuredResult{code: 9},
			}, fmt.Errorf("check failed")
		},
	})

	var recorded ToolFailureRecordedPayload
	bus.Subscribe("tool.failure.recorded", func(e events.Event) {
		recorded, _ = e.Payload.(ToolFailureRecordedPayload)
	})
	var errored toolevents.ErroredPayload
	bus.Subscribe(toolevents.ErroredType, func(e events.Event) {
		errored, _ = e.Payload.(toolevents.ErroredPayload)
	})

	out, err := eng.Turn(context.Background(), "finish the artifact")
	if err != nil {
		t.Fatal(err)
	}
	if out != "done too early" {
		t.Fatalf("out = %q", out)
	}
	if recorded.ExitCode == nil || *recorded.ExitCode != 9 {
		t.Fatalf("recorded exit code = %+v, want 9", recorded.ExitCode)
	}
	if recorded.OutputPreview == "" || strings.Contains(recorded.OutputPreview, "Process exited with code") {
		t.Fatalf("recorded output preview = %q, want opaque text without formatted exit code", recorded.OutputPreview)
	}
	if errored.ExitCode == nil || *errored.ExitCode != 9 {
		t.Fatalf("tool.errored exit code = %+v, want 9", errored.ExitCode)
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

func TestTurn_RepeatedFailureRecordsRepeatedStuckWithoutContinuation(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "check_1", ToolName: "check_ready", Input: map[string]any{"path": "artifact.txt"}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "check_2", ToolName: "check_ready", Input: map[string]any{"path": "artifact.txt"}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "done too early"), StopReason: llm.StopEndTurn},
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
	var continued int32
	bus.Subscribe("tool.failure.recorded", func(e events.Event) {
		lastRecorded, _ = e.Payload.(ToolFailureRecordedPayload)
	})
	bus.Subscribe("tool.failure.continued", func(e events.Event) {
		atomic.AddInt32(&continued, 1)
	})

	out, err := eng.Turn(context.Background(), "finish the artifact")
	if err != nil {
		t.Fatal(err)
	}
	if out != "done too early" {
		t.Fatalf("out = %q", out)
	}
	if lastRecorded.Classification != ToolFailureRepeatedStuck || lastRecorded.Occurrences != 2 {
		t.Fatalf("last recorded payload = %+v", lastRecorded)
	}
	if len(prov.histories) != 3 {
		t.Fatalf("provider calls = %d, want no repeated-failure continuation", len(prov.histories))
	}
	if atomic.LoadInt32(&continued) != 0 {
		t.Fatalf("repeated failure should not emit continuation")
	}
}

func TestTurn_FailureLedgerDoesNotRequestBlockedReasonOnRepeatedFinishAttempt(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "check_1", ToolName: "check_ready", Input: map[string]any{"path": "artifact.txt"}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "done too early"), StopReason: llm.StopEndTurn},
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
	if out != "done too early" {
		t.Fatalf("out = %q", out)
	}
	if len(prov.histories) != 2 {
		t.Fatalf("provider calls = %d, want no blocked-reason continuation", len(prov.histories))
	}
}

func TestTurn_RuntimeFatalToolFailureRecordsWithoutContinuation(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "missing_1", ToolName: "does_not_exist", Input: map[string]any{}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "done too early"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, false)

	var recorded ToolFailureRecordedPayload
	var continued int32
	bus.Subscribe("tool.failure.recorded", func(e events.Event) {
		recorded, _ = e.Payload.(ToolFailureRecordedPayload)
	})
	bus.Subscribe("tool.failure.continued", func(e events.Event) {
		atomic.AddInt32(&continued, 1)
	})

	out, err := eng.Turn(context.Background(), "finish with tool")
	if err != nil {
		t.Fatal(err)
	}
	if out != "done too early" {
		t.Fatalf("out = %q", out)
	}
	if len(prov.histories) != 2 {
		t.Fatalf("provider calls = %d, want no runtime fatal continuation", len(prov.histories))
	}
	if recorded.Classification != ToolFailureRuntimeFatal || !recorded.Blocking || recorded.Fingerprint == "" {
		t.Fatalf("recorded payload = %+v", recorded)
	}
	if atomic.LoadInt32(&continued) != 0 {
		t.Fatalf("runtime fatal failure should not emit continuation")
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

func TestTurn_ProviderDeadlineEmitsTimeoutContract(t *testing.T) {
	prov := &mockProviderWithErrors{
		errs: []error{fmt.Errorf("openai codex responses: codex SSE read: context deadline exceeded")},
	}
	eng, bus := newEngine(t, prov, false)
	var payload TurnErroredPayload
	bus.Subscribe("turn.errored", func(e events.Event) {
		payload, _ = e.Payload.(TurnErroredPayload)
	})

	_, err := eng.Turn(context.Background(), "x")
	if err == nil {
		t.Fatal("expected provider timeout")
	}
	if payload.ErrorKind != "timeout" || !payload.TimedOut {
		t.Fatalf("turn.errored payload = %+v, want timeout classification", payload)
	}
	if !strings.Contains(payload.Error, "timed out") {
		t.Fatalf("turn.errored error = %q, want public timeout", payload.Error)
	}
	if strings.Contains(payload.Error, "context deadline exceeded") {
		t.Fatalf("turn.errored error = %q, should not expose raw deadline", payload.Error)
	}
	if !strings.Contains(payload.RawCause, "context deadline exceeded") {
		t.Fatalf("turn.errored raw_cause = %q, want original deadline cause", payload.RawCause)
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
