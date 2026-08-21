package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/artifact"
	"github.com/juex-ai/juex/internal/cancellation"
	"github.com/juex-ai/juex/internal/chunkedwrite"
	"github.com/juex-ai/juex/internal/errorclass"
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/hooks"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/prompt"
	"github.com/juex-ai/juex/internal/provenance"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/runtime/workmem"
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

type cancellableProvider struct {
	started chan struct{}
}

func (p *cancellableProvider) Name() string { return "cancellable" }

func (p *cancellableProvider) Complete(ctx context.Context, sys string, h []llm.Message, t []llm.ToolSpec) (llm.Response, error) {
	signal(p.started)
	<-ctx.Done()
	return llm.Response{}, ctx.Err()
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

type queuedFailureProvider struct {
	started   chan struct{}
	release   chan struct{}
	firstErr  error
	recovery  llm.Response
	called    int
	histories [][]llm.Message
}

func (p *queuedFailureProvider) Name() string { return "queued-failure" }

func (p *queuedFailureProvider) Complete(ctx context.Context, sys string, history []llm.Message, tools []llm.ToolSpec) (llm.Response, error) {
	historyCopy := append([]llm.Message(nil), history...)
	p.histories = append(p.histories, historyCopy)
	p.called++
	if p.called == 1 {
		signal(p.started)
		select {
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		case <-p.release:
		}
		return llm.Response{}, p.firstErr
	}
	return p.recovery, nil
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
	responses map[hooks.EventName][]fakeHookResponse
	errors    map[hooks.EventName]error
	requests  []hooks.Request
}

type fakeHookResponse struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

type hookRunnerFunc func(context.Context, hooks.Request) ([]hooks.Result, error)

func (f hookRunnerFunc) Run(ctx context.Context, req hooks.Request) ([]hooks.Result, error) {
	return f(ctx, req)
}

type runtimeToolPolicyModule struct {
	id    runtimemodule.ID
	apply func(runtimemodule.ToolPolicyRequest) (runtimemodule.ToolPolicyDecision, error)
}

type runtimeTurnInputPolicyModule struct {
	id    runtimemodule.ID
	apply func(runtimemodule.TurnInputRequest) (runtimemodule.TurnInputDecision, error)
}

type continuationFailureFinishModule struct {
	committed bool
	observed  int
}

func (*continuationFailureFinishModule) ID() runtimemodule.ID {
	return "continuation-failure-finish"
}

func (*continuationFailureFinishModule) EvaluateFinish(_ context.Context, _ runtimemodule.FinishRequest) (runtimemodule.FinishDecision, error) {
	return runtimemodule.FinishDecision{
		Action:       runtimemodule.FinishContinue,
		Continuation: "continue after the durable owner checkpoint",
	}, nil
}

func (m *continuationFailureFinishModule) CommitFinishDecision(_ context.Context, _ runtimemodule.FinishRequest, _ runtimemodule.FinishDecision) (bool, error) {
	m.committed = true
	return true, nil
}

func (m *continuationFailureFinishModule) FinishContinuationCommitted(_ context.Context, _ runtimemodule.FinishRequest, _ runtimemodule.FinishDecision) {
	m.observed++
}

func (m *runtimeTurnInputPolicyModule) ID() runtimemodule.ID { return m.id }

func (m *runtimeTurnInputPolicyModule) ApplyTurnInput(_ context.Context, request runtimemodule.TurnInputRequest) (runtimemodule.TurnInputDecision, error) {
	if m.apply == nil {
		return runtimemodule.TurnInputDecision{Action: runtimemodule.TurnInputAllow}, nil
	}
	return m.apply(request)
}

func (m *runtimeToolPolicyModule) ID() runtimemodule.ID { return m.id }

func (m *runtimeToolPolicyModule) ApplyTool(_ context.Context, request runtimemodule.ToolPolicyRequest) (runtimemodule.ToolPolicyDecision, error) {
	if m.apply == nil {
		return runtimemodule.ToolPolicyDecision{Action: runtimemodule.ToolPolicyAllow}, nil
	}
	return m.apply(request)
}

func (r *fakeHookRunner) Run(ctx context.Context, req hooks.Request) ([]hooks.Result, error) {
	r.requests = append(r.requests, req)
	if err := r.errors[req.EventName]; err != nil {
		return nil, err
	}
	responses := r.responses[req.EventName]
	if len(responses) == 0 {
		return nil, nil
	}
	response := responses[0]
	r.responses[req.EventName] = responses[1:]
	return []hooks.Result{{
		Hook:      hooks.CommandHook{Name: "fake", Events: []hooks.EventName{req.EventName}},
		EventName: req.EventName,
		ToolName:  req.ToolName,
		ExitCode:  response.ExitCode,
		Stdout:    response.Stdout,
		Stderr:    response.Stderr,
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
	case "block":
		_, _ = os.Stdout.WriteString("compaction cannot be blocked")
		_, _ = os.Stderr.WriteString("compact guard diagnostic")
		os.Exit(2)
	case "goal-output":
		_, _ = os.Stdout.WriteString(`{"goal_state":{"description":"hook should not write","status":"success"}}`)
		os.Exit(0)
	}
	os.Exit(0)
}

func TestAppendHookAdditionalContextDoesNotMutateInputBlocks(t *testing.T) {
	blocks := make([]llm.Block, 1, 2)
	blocks[0] = llm.Block{Type: llm.BlockText, Text: "original"}
	msg := llm.Message{Role: llm.RoleUser, Blocks: blocks}

	out := appendPolicyAdditionalContext(msg, []hooks.Result{{
		Hook:   hooks.CommandHook{Name: "context"},
		Stdout: "extra",
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

func TestRunSessionStartHooksQueuesStdoutForNextProviderRequest(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "first"), StopReason: llm.StopEndTurn},
		{Message: llm.TextMessage(llm.RoleAssistant, "second"), StopReason: llm.StopEndTurn},
	}}
	eng, _ := newEngine(t, prov, false)
	installHookRunner(t, eng, &fakeHookRunner{responses: map[hooks.EventName][]fakeHookResponse{
		hooks.EventSessionStart: {{Stdout: "load project policy"}},
	}})

	if err := eng.RunSessionStartHooks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(eng.Session.History) != 0 {
		t.Fatalf("session policy context persisted in history: %+v", eng.Session.History)
	}
	if _, err := eng.Turn(context.Background(), "first turn"); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Turn(context.Background(), "second turn"); err != nil {
		t.Fatal(err)
	}
	if got := messagesText(prov.histories[0]); !strings.Contains(got, "load project policy") {
		t.Fatalf("first provider request missing session policy context:\n%s", got)
	}
	if got := messagesText(prov.histories[1]); strings.Contains(got, "load project policy") {
		t.Fatalf("session policy context repeated in second provider request:\n%s", got)
	}
	for _, msg := range eng.Session.History {
		if msg.Kind == llm.MessageKindRuntimeContext {
			t.Fatalf("runtime context persisted in history: %+v", msg)
		}
	}
}

func TestRunSessionStartHooksUsesStderrFallbackForExitTwo(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	installHookRunner(t, eng, &fakeHookRunner{responses: map[hooks.EventName][]fakeHookResponse{
		hooks.EventSessionStart: {{ExitCode: 2, Stderr: "workspace is not trusted"}},
	}})

	err := eng.RunSessionStartHooks(context.Background())
	if err == nil || !strings.Contains(err.Error(), "workspace is not trusted") {
		t.Fatalf("err = %v", err)
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
	pb := newTestPromptBuilder("", func() time.Time { return time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC) })
	artifactState := t.TempDir()
	return &Engine{
		Provider:    prov,
		Tools:       reg,
		Bus:         bus,
		Session:     sess,
		Prompt:      pb,
		WorkDir:     t.TempDir(),
		ArtifactDir: filepath.Join(artifactState, "artifacts"),
	}, bus
}

func TestTurnStopsBeforeProviderWhenModulePromptContextFails(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{{
		Message: llm.TextMessage(llm.RoleAssistant, "should not run"), StopReason: llm.StopEndTurn,
	}}}
	eng, _ := newEngine(t, prov, false)
	eng.Prompt.ModulePromptContext = func() ([]runtimemodule.ContextSection, error) {
		return nil, errors.New("memory unavailable")
	}

	_, err := eng.Turn(context.Background(), "hello")
	if err == nil || !strings.Contains(err.Error(), "runtime: build prompt context") ||
		!strings.Contains(err.Error(), "memory unavailable") {
		t.Fatalf("Turn() error = %v", err)
	}
	if prov.called != 0 {
		t.Fatalf("provider calls = %d, want 0", prov.called)
	}
	reloaded, err := session.Load(eng.Session.Dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reloaded.Close() })
	if len(reloaded.History) != 1 || reloaded.History[0].Role != llm.RoleUser ||
		reloaded.History[0].FirstText() != "hello" {
		t.Fatalf("durable history = %#v, want accepted user input", reloaded.History)
	}
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

func TestAdmitTurnMessage_DurableAdmissionEventFailureDropsAcceptedInput(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{{
		Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn,
	}}}
	eng, bus := newEngine(t, prov, false)
	want := errors.New("admission sync failed")
	bus.SetCommitter(selectiveFailCommitter{eventType: TurnAdmittedType, err: want})

	if _, err := eng.AdmitTurnMessage("failed-turn", llm.TextMessage(llm.RoleUser, "abandoned input")); !errors.Is(err, want) {
		t.Fatalf("AdmitTurnMessage() error = %v, want %v", err, want)
	}
	records, err := eng.PendingInputQueue.Records()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("pending records = %+v, want one dropped admission", records)
	}
	for _, record := range records {
		if record.State != PendingInputStateDropped {
			t.Fatalf("failed admission record = %+v, want state %q", record, PendingInputStateDropped)
		}
	}

	eng.Session.SubscribeBus(bus)
	if out, err := eng.Turn(context.Background(), "later input"); err != nil || out != "done" {
		t.Fatalf("later Turn() = %q, %v", out, err)
	}
	if prov.called != 1 || len(prov.histories) != 1 {
		t.Fatalf("provider calls = %d, histories = %d, want one", prov.called, len(prov.histories))
	}
	for _, message := range prov.histories[0] {
		if message.FirstText() == "abandoned input" {
			t.Fatalf("failed admission replayed in later provider history: %+v", prov.histories[0])
		}
	}
}

func TestAdmitTurnMessage_RepeatedAdmissionKeepsCommittedRecord(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	first, err := eng.AdmitTurnMessage("turn-1", llm.TextMessage(llm.RoleUser, "accepted once"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := eng.AdmitTurnMessage("turn-1", first)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Fatalf("repeated admission message id = %q, want %q", second.ID, first.ID)
	}
	records, err := eng.PendingInputQueue.Records()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("pending records = %+v, want one committed admission", records)
	}
	for _, record := range records {
		if record.State != PendingInputStateAdmitted {
			t.Fatalf("repeated admission record = %+v, want state %q", record, PendingInputStateAdmitted)
		}
	}
}

func TestAdmitTurnMessage_AdmissionAndCompensationFailureCannotReplayInput(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{{
		Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn,
	}}}
	eng, bus := newEngine(t, prov, false)
	queue := eng.currentPendingInputQueue()
	pendingPath := queue.path
	backupPath := pendingPath + ".before-failed-compensation"
	restored := false
	restoreJournal := func() error {
		if restored {
			return nil
		}
		if err := os.Remove(pendingPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.Rename(backupPath, pendingPath); err != nil {
			return err
		}
		restored = true
		return nil
	}
	t.Cleanup(func() { _ = restoreJournal() })
	want := errors.New("admission sync failed")
	bus.SetCommitter(selectiveFailCommitter{
		eventType: TurnAdmittedType,
		err:       want,
		beforeFail: func() error {
			if err := os.Rename(pendingPath, backupPath); err != nil {
				return err
			}
			return os.Mkdir(pendingPath, 0o700)
		},
	})

	_, admissionErr := eng.AdmitTurnMessage("failed-turn", llm.TextMessage(llm.RoleUser, "abandoned input"))
	if !errors.Is(admissionErr, want) || !strings.Contains(admissionErr.Error(), "drop rejected turn admission") {
		t.Fatalf("AdmitTurnMessage() error = %v, want event and compensation failures", admissionErr)
	}
	if err := restoreJournal(); err != nil {
		t.Fatal(err)
	}
	reloadedQueue := NewPendingInputQueue(eng.Session.Dir, PendingInputQueueOptions{})
	records, err := reloadedQueue.Records()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("pending records = %+v, want one abandoned admission intent", records)
	}
	for _, record := range records {
		if record.State != PendingInputState("accepting") {
			t.Fatalf("failed admission record = %+v, want non-replayable state %q", record, PendingInputState("accepting"))
		}
	}
	replayable, err := reloadedQueue.Replayable("later-turn", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayable) != 0 {
		t.Fatalf("failed admission remained replayable: %+v", replayable)
	}

	eng.PendingInputQueue = reloadedQueue
	eng.Session.SubscribeBus(bus)
	if out, err := eng.Turn(context.Background(), "later input"); err != nil || out != "done" {
		t.Fatalf("later Turn() = %q, %v", out, err)
	}
	for _, message := range prov.histories[0] {
		if message.FirstText() == "abandoned input" {
			t.Fatalf("failed admission replayed in later provider history: %+v", prov.histories[0])
		}
	}
}

func TestAdmitTurnMessage_FailedAdmissionKeepsPersistedInputReplayable(t *testing.T) {
	eng, bus := newEngine(t, &mockProvider{}, false)
	record, err := eng.PersistPendingMessageWithOptions(
		context.Background(),
		llm.TextMessage(llm.RoleUser, "persisted input"),
		PendingInputOptions{ID: "persisted", TTL: time.Hour},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := errors.New("admission sync failed")
	bus.SetCommitter(selectiveFailCommitter{eventType: TurnAdmittedType, err: want})

	_, admissionErr := eng.AdmitTurnMessage("failed-turn", record.Message)
	if !errors.Is(admissionErr, want) {
		t.Fatalf("AdmitTurnMessage() error = %v, want %v", admissionErr, want)
	}
	reloaded := NewPendingInputQueue(eng.Session.Dir, PendingInputQueueOptions{})
	replayable, err := reloaded.Replayable("later-turn", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayable) != 1 || replayable[0].ID != record.ID || replayable[0].State != PendingInputStatePending {
		t.Fatalf("replayable persisted input = %+v, want pending record %q", replayable, record.ID)
	}
}

func TestAdmitTurnMessage_FailedPromotionKeepsPersistedInputReplayable(t *testing.T) {
	eng, bus := newEngine(t, &mockProvider{}, false)
	record, err := eng.PersistPendingMessageWithOptions(
		context.Background(),
		llm.TextMessage(llm.RoleUser, "persisted input"),
		PendingInputOptions{ID: "persisted", TTL: time.Hour},
	)
	if err != nil {
		t.Fatal(err)
	}
	queue := eng.currentPendingInputQueue()
	pendingPath := queue.path
	backupPath := pendingPath + ".before-failed-persisted-promotion"
	restored := false
	restoreJournal := func() error {
		if restored {
			return nil
		}
		if err := os.Remove(pendingPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.Rename(backupPath, pendingPath); err != nil {
			return err
		}
		restored = true
		return nil
	}
	t.Cleanup(func() { _ = restoreJournal() })
	bus.SetCommitter(interceptCommitter{
		eventType: TurnAdmittedType,
		beforeCommit: func() error {
			if err := os.Rename(pendingPath, backupPath); err != nil {
				return err
			}
			return os.Mkdir(pendingPath, 0o700)
		},
	})

	_, admissionErr := eng.AdmitTurnMessage("failed-turn", record.Message)
	if admissionErr == nil || !strings.Contains(admissionErr.Error(), "persist committed turn admission") {
		t.Fatalf("AdmitTurnMessage() error = %v, want promotion failure", admissionErr)
	}
	if strings.Contains(admissionErr.Error(), "drop uncommitted turn admission") {
		t.Fatalf("AdmitTurnMessage() dropped previously persisted input: %v", admissionErr)
	}
	if err := restoreJournal(); err != nil {
		t.Fatal(err)
	}
	reloaded := NewPendingInputQueue(eng.Session.Dir, PendingInputQueueOptions{})
	replayable, err := reloaded.Replayable("later-turn", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayable) != 1 || replayable[0].ID != record.ID || replayable[0].State != PendingInputStatePending {
		t.Fatalf("replayable persisted input = %+v, want pending record %q", replayable, record.ID)
	}
}

func TestAdmitTurnMessage_IntentPromotionFailureCannotReplayInput(t *testing.T) {
	eng, bus := newEngine(t, &mockProvider{}, false)
	queue := eng.currentPendingInputQueue()
	pendingPath := queue.path
	backupPath := pendingPath + ".before-failed-promotion"
	restored := false
	restoreJournal := func() error {
		if restored {
			return nil
		}
		if err := os.Remove(pendingPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.Rename(backupPath, pendingPath); err != nil {
			return err
		}
		restored = true
		return nil
	}
	t.Cleanup(func() { _ = restoreJournal() })
	bus.SetCommitter(interceptCommitter{
		eventType: TurnAdmittedType,
		beforeCommit: func() error {
			if err := os.Rename(pendingPath, backupPath); err != nil {
				return err
			}
			return os.Mkdir(pendingPath, 0o700)
		},
	})
	var turnErrors int
	bus.Subscribe("turn.errored", func(events.Event) { turnErrors++ })

	_, err := eng.AdmitTurnMessage("failed-turn", llm.TextMessage(llm.RoleUser, "uncommitted input"))
	if err == nil || !strings.Contains(err.Error(), "persist committed turn admission") || !strings.Contains(err.Error(), "drop uncommitted turn admission") {
		t.Fatalf("AdmitTurnMessage() error = %v, want promotion and compensation failures", err)
	}
	if turnErrors != 1 {
		t.Fatalf("turn.errored events = %d, want 1", turnErrors)
	}
	if err := restoreJournal(); err != nil {
		t.Fatal(err)
	}
	reloadedQueue := NewPendingInputQueue(eng.Session.Dir, PendingInputQueueOptions{})
	replayable, err := reloadedQueue.Replayable("later-turn", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayable) != 0 {
		t.Fatalf("uncommitted admission remained replayable: %+v", replayable)
	}
}

func TestAdmitTurnMessage_IntentAppendFailureLeavesNoActiveTurn(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{{
		Message: llm.TextMessage(llm.RoleAssistant, "recovered"), StopReason: llm.StopEndTurn,
	}}}
	eng, bus := newEngine(t, prov, false)
	queue := NewPendingInputQueue(eng.Session.Dir, PendingInputQueueOptions{})
	eng.PendingInputQueue = queue
	want := errors.New("injected intent append failure")
	queue.fileOps.write = func(file *os.File, body []byte) (int, error) {
		partial := len(body) / 2
		n, writeErr := file.Write(body[:partial])
		return n, errors.Join(want, writeErr)
	}
	var admitted int
	bus.Subscribe(TurnAdmittedType, func(events.Event) { admitted++ })

	if _, err := eng.AdmitTurnMessage("failed-turn", llm.TextMessage(llm.RoleUser, "not accepted")); !errors.Is(err, want) {
		t.Fatalf("AdmitTurnMessage() error = %v, want %v", err, want)
	}
	if eng.activeTurnID != "" {
		t.Fatalf("active turn id = %q, want none", eng.activeTurnID)
	}
	if admitted != 0 || prov.called != 0 {
		t.Fatalf("admitted events/provider calls = %d/%d, want 0/0", admitted, prov.called)
	}
	if len(queue.records) != 0 {
		t.Fatalf("failed intent entered live queue: %+v", queue.records)
	}
	if _, err := NewPendingInputQueue(eng.Session.Dir, PendingInputQueueOptions{}).Records(); err == nil {
		t.Fatal("reload accepted an invalid partial intent tail")
	}

	if err := os.Truncate(queue.path, 0); err != nil {
		t.Fatal(err)
	}
	eng.PendingInputQueue = NewPendingInputQueue(eng.Session.Dir, PendingInputQueueOptions{})
	if out, err := eng.TurnMessageWithID(context.Background(), llm.TextMessage(llm.RoleUser, "retry after repair"), "recovery-turn"); err != nil || out != "recovered" {
		t.Fatalf("recovery TurnMessageWithID() = %q, %v", out, err)
	}
}

func TestTurn_DurableProviderRequestFailurePreventsProviderCall(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{{
		Message: llm.TextMessage(llm.RoleAssistant, "should not run"), StopReason: llm.StopEndTurn,
	}}}
	eng, bus := newEngine(t, prov, false)
	want := errors.New("journal sync failed")
	bus.SetCommitter(selectiveFailCommitter{eventType: "llm.requested", err: want})
	if _, err := eng.Turn(context.Background(), "hello"); !errors.Is(err, want) {
		t.Fatalf("Turn() error = %v, want %v", err, want)
	}
	if prov.called != 0 {
		t.Fatalf("provider calls = %d, want 0", prov.called)
	}
}

func TestTurn_DurableRequestEpochFailurePreventsProviderCallAndHookConsumption(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{{
		Message: llm.TextMessage(llm.RoleAssistant, "should not run"), StopReason: llm.StopEndTurn,
	}}}
	eng, bus := newEngine(t, prov, false)
	if err := eng.queuePolicyRuntimeContextFromHookResults([]hooks.Result{{
		Hook: hooks.CommandHook{Name: "policy"}, Stdout: "one-shot context",
	}}); err != nil {
		t.Fatal(err)
	}
	want := errors.New("epoch sync failed")
	bus.SetCommitter(selectiveSessionCommitter{session: eng.Session, eventType: provenance.RequestEpochType, err: want})
	if _, err := eng.Turn(context.Background(), "hello"); !errors.Is(err, want) {
		t.Fatalf("Turn() error = %v, want %v", err, want)
	}
	if prov.called != 0 {
		t.Fatalf("provider calls = %d, want 0", prov.called)
	}
	if pending := eng.pendingPolicyRuntimeContextSnapshot(); len(pending) != 1 || pending[0].ID == "" {
		t.Fatalf("pending policy context = %+v", pending)
	}
	journal, err := session.ReadEvents(eng.Session.Dir)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := provenance.Recover(journal)
	if err != nil {
		t.Fatal(err)
	}
	if pending := recovered.PendingPolicyContext(); len(pending) != 1 {
		t.Fatalf("recovered policy context before epoch checkpoint = %+v", pending)
	}
}

func TestTurn_RequestEpochCheckpointConsumesPolicyContextAndLinksResponse(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{{
		Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn,
	}}}
	eng, bus := newEngine(t, prov, false)
	notesStore := workmem.NewNotesStore(eng.Session.Dir)
	if _, err := notesStore.Update("- [ ] retain the exact runtime note"); err != nil {
		t.Fatal(err)
	}
	installSessionStateModulesWithStores(t, eng, nil, notesStore)
	if err := eng.queuePolicyRuntimeContextFromHookResults([]hooks.Result{{
		Hook: hooks.CommandHook{Name: "policy"}, Stdout: "one-shot context",
	}}); err != nil {
		t.Fatal(err)
	}
	var epoch provenance.RequestEpochPayload
	var requested LLMRequestedPayload
	var responded LLMRespondedPayload
	bus.Subscribe(provenance.RequestEpochType, func(event events.Event) { epoch = event.Payload.(provenance.RequestEpochPayload) })
	bus.Subscribe("llm.requested", func(event events.Event) { requested = event.Payload.(LLMRequestedPayload) })
	bus.Subscribe("llm.responded", func(event events.Event) { responded = event.Payload.(LLMRespondedPayload) })

	if out, err := eng.Turn(context.Background(), "hello"); err != nil || out != "done" {
		t.Fatalf("Turn() = %q, %v", out, err)
	}
	if epoch.Epoch.EpochID == "" || epoch.Epoch.RequestDigest == "" {
		t.Fatalf("epoch = %+v", epoch)
	}
	if epoch.Epoch.CachePolicy.StablePrefixKeyDigest == "" {
		t.Fatalf("epoch cache policy = %+v", epoch.Epoch.CachePolicy)
	}
	if len(epoch.Epoch.SystemPromptSnapshot.Parts) == 0 {
		t.Fatalf("system prompt was not captured as section snapshots: %+v", epoch.Epoch.SystemPromptSnapshot)
	}
	derivedBodies := map[string]string{}
	for _, message := range epoch.Epoch.Messages {
		if message.Snapshot == nil {
			continue
		}
		var body llm.Message
		if err := json.Unmarshal(message.Snapshot.Content, &body); err != nil {
			t.Fatal(err)
		}
		derivedBodies[message.ID] = body.FirstText()
	}
	if !strings.Contains(derivedBodies["runtime-notes"], "retain the exact runtime note") {
		t.Fatalf("derived runtime context bodies = %+v", derivedBodies)
	}
	rawEpoch, err := json.Marshal(epoch)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rawEpoch), "juex:"+eng.Session.ID) {
		t.Fatalf("epoch leaked raw cache key: %s", rawEpoch)
	}
	if requested.EpochID != epoch.Epoch.EpochID || requested.RequestDigest != epoch.Epoch.RequestDigest {
		t.Fatalf("requested = %+v, epoch = %+v", requested, epoch.Epoch)
	}
	if responded.EpochID != epoch.Epoch.EpochID || responded.RequestDigest != epoch.Epoch.RequestDigest {
		t.Fatalf("responded = %+v, epoch = %+v", responded, epoch.Epoch)
	}
	if pending := eng.pendingPolicyRuntimeContextSnapshot(); len(pending) != 0 {
		t.Fatalf("policy context after checkpoint = %+v", pending)
	}
}

func TestTurn_FailedProviderDoesNotReplayCheckpointedPolicyContext(t *testing.T) {
	eng, _ := newEngine(t, errorProvider{}, false)
	if err := eng.queuePolicyRuntimeContextFromHookResults([]hooks.Result{{
		Hook: hooks.CommandHook{Name: "policy"}, Stdout: "one-shot context",
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Turn(context.Background(), "hello"); err == nil {
		t.Fatal("Turn() error = nil")
	}
	if pending := eng.pendingPolicyRuntimeContextSnapshot(); len(pending) != 0 {
		t.Fatalf("policy context after provider attempt = %+v", pending)
	}
}

func TestTurn_DispatchCommitFailureKeepsEpochConsumptionAfterRecovery(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{{Message: llm.TextMessage(llm.RoleAssistant, "unused"), StopReason: llm.StopEndTurn}}}
	eng, bus := newEngine(t, prov, false)
	if err := eng.queuePolicyRuntimeContextFromHookResults([]hooks.Result{{Hook: hooks.CommandHook{Name: "policy"}, Stdout: "one-shot context"}}); err != nil {
		t.Fatal(err)
	}
	want := errors.New("dispatch sync failed")
	bus.SetCommitter(selectiveSessionCommitter{session: eng.Session, eventType: "llm.requested", err: want})
	if _, err := eng.Turn(context.Background(), "hello"); !errors.Is(err, want) {
		t.Fatalf("Turn() error = %v, want %v", err, want)
	}
	if prov.called != 0 {
		t.Fatalf("provider calls = %d, want 0", prov.called)
	}
	assertRecoveredPolicyContextCount(t, eng.Session, 0)
}

func TestTurn_ResponseCommitFailureKeepsEpochConsumptionAfterRecovery(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{{Message: llm.TextMessage(llm.RoleAssistant, "returned"), StopReason: llm.StopEndTurn}}}
	eng, bus := newEngine(t, prov, false)
	if err := eng.queuePolicyRuntimeContextFromHookResults([]hooks.Result{{Hook: hooks.CommandHook{Name: "policy"}, Stdout: "one-shot context"}}); err != nil {
		t.Fatal(err)
	}
	want := errors.New("response sync failed")
	bus.SetCommitter(selectiveSessionCommitter{session: eng.Session, eventType: "llm.responded", err: want})
	if _, err := eng.Turn(context.Background(), "hello"); !errors.Is(err, want) {
		t.Fatalf("Turn() error = %v, want %v", err, want)
	}
	if prov.called != 1 {
		t.Fatalf("provider calls = %d, want 1", prov.called)
	}
	assertRecoveredPolicyContextCount(t, eng.Session, 0)
}

func assertRecoveredPolicyContextCount(t *testing.T, sess *session.Session, want int) {
	t.Helper()
	journal, err := session.ReadEvents(sess.Dir)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := provenance.Recover(journal)
	if err != nil {
		t.Fatal(err)
	}
	if pending := recovered.PendingPolicyContext(); len(pending) != want {
		t.Fatalf("recovered policy context = %+v, want %d", pending, want)
	}
}

func TestTurn_DurableToolRequestFailurePreventsToolCall(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{{
		Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{
			Type: llm.BlockToolUse, ToolUseID: "call-1", ToolName: "side_effect",
		}}},
		StopReason: llm.StopToolUse,
	}}}
	eng, bus := newEngine(t, prov, false)
	toolCalls := 0
	eng.Tools.MustRegister(tools.Tool{
		Name: "side_effect",
		Handler: func(context.Context, map[string]any) (string, error) {
			toolCalls++
			return "done", nil
		},
	})
	want := errors.New("journal sync failed")
	bus.SetCommitter(selectiveFailCommitter{eventType: toolevents.RequestedType, err: want})
	if _, err := eng.Turn(context.Background(), "run it"); !errors.Is(err, want) {
		t.Fatalf("Turn() error = %v, want %v", err, want)
	}
	if toolCalls != 0 {
		t.Fatalf("tool calls = %d, want 0", toolCalls)
	}
	if prov.called != 1 {
		t.Fatalf("provider calls = %d, want 1", prov.called)
	}
}

func TestTurn_DurableToolStartedFailurePreventsToolCall(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{{
		Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{
			Type: llm.BlockToolUse, ToolUseID: "call-1", ToolName: "side_effect",
		}}},
		StopReason: llm.StopToolUse,
	}}}
	eng, bus := newEngine(t, prov, false)
	hookRunner := &fakeHookRunner{responses: map[hooks.EventName][]fakeHookResponse{
		hooks.EventPreToolUse: {{Stdout: "pre-hook ran"}},
	}}
	installHookRunner(t, eng, hookRunner)
	toolCalls := 0
	eng.Tools.MustRegister(tools.Tool{
		Name: "side_effect",
		Handler: func(context.Context, map[string]any) (string, error) {
			toolCalls++
			return "done", nil
		},
	})
	want := errors.New("journal sync failed")
	bus.SetCommitter(selectiveFailCommitter{eventType: toolevents.RunningType, err: want})
	if _, err := eng.Turn(context.Background(), "run it"); !errors.Is(err, want) {
		t.Fatalf("Turn() error = %v, want %v", err, want)
	}
	if toolCalls != 0 {
		t.Fatalf("tool calls = %d, want 0", toolCalls)
	}
	for _, request := range hookRunner.requests {
		if request.EventName == hooks.EventPreToolUse {
			t.Fatalf("pre-tool hook ran before durable started checkpoint: %+v", hookRunner.requests)
		}
	}
}

func TestTurn_ToolExecutionEventsCarryStableIdentityAndRecoverableOutcome(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{
			Type: llm.BlockToolUse, ToolUseID: "call-identity", ToolName: "side_effect",
		}}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, false)
	eng.Tools.MustRegister(tools.Tool{Name: "side_effect", Handler: func(context.Context, map[string]any) (string, error) {
		return "recorded once", nil
	}})
	var requested toolevents.RequestedPayload
	var running toolevents.RunningPayload
	var completed toolevents.CompletedPayload
	bus.Subscribe(toolevents.RequestedType, func(event events.Event) { requested, _ = event.Payload.(toolevents.RequestedPayload) })
	bus.Subscribe(toolevents.RunningType, func(event events.Event) { running, _ = event.Payload.(toolevents.RunningPayload) })
	bus.Subscribe(toolevents.CompletedType, func(event events.Event) { completed, _ = event.Payload.(toolevents.CompletedPayload) })

	if out, err := eng.Turn(context.Background(), "run once"); err != nil || out != "done" {
		t.Fatalf("Turn() = %q, %v", out, err)
	}
	assistant := eng.Session.History[1]
	for name, identity := range map[string]struct {
		iter      int
		callIndex int
		messageID string
		toolUseID string
	}{
		"requested": {requested.Iter, requested.CallIndex, requested.MessageID, requested.ToolUseID},
		"running":   {running.Iter, running.CallIndex, running.MessageID, running.ToolUseID},
		"completed": {completed.Iter, completed.CallIndex, completed.MessageID, completed.ToolUseID},
	} {
		if identity.iter != 0 || identity.callIndex != 0 || identity.messageID != assistant.ID || identity.toolUseID != "call-identity" {
			t.Fatalf("%s identity = %+v, assistant=%s", name, identity, assistant.ID)
		}
	}
	if completed.Outcome == nil || completed.Outcome.MessageID != eng.Session.History[2].ID || completed.Outcome.Block.Content != "recorded once" {
		t.Fatalf("completed outcome = %+v, history=%+v", completed.Outcome, eng.Session.History)
	}
}

func TestTurn_TransformedToolInputIsDurableBeforeHandlerExecution(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{
			Type: llm.BlockToolUse, ToolUseID: "call-transform", ToolName: "side_effect", Input: map[string]any{"path": "provider.txt"},
		}}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, false)
	var durableEffectiveInput atomic.Bool
	bus.Subscribe(toolevents.InputResolvedType, func(event events.Event) {
		payload, ok := event.Payload.(toolevents.InputResolvedPayload)
		if ok && payload.Input["path"] == "effective.txt" {
			durableEffectiveInput.Store(true)
		}
	})
	handlerSawCheckpoint := false
	eng.Tools.MustRegister(tools.Tool{Name: "side_effect", Handler: func(_ context.Context, input map[string]any) (string, error) {
		handlerSawCheckpoint = durableEffectiveInput.Load()
		if input["path"] != "effective.txt" {
			return "", fmt.Errorf("handler input = %#v", input)
		}
		return "recorded once", nil
	}})
	installRuntimeTestModules(t, eng, &runtimeToolPolicyModule{id: "transform-input", apply: func(request runtimemodule.ToolPolicyRequest) (runtimemodule.ToolPolicyDecision, error) {
		if request.Stage == runtimemodule.ToolPolicyBeforeExecution {
			return runtimemodule.ToolPolicyDecision{Action: runtimemodule.ToolPolicyTransform, Input: map[string]any{"path": "effective.txt"}}, nil
		}
		return runtimemodule.ToolPolicyDecision{Action: runtimemodule.ToolPolicyAllow}, nil
	}})

	if out, err := eng.Turn(context.Background(), "run transformed call"); err != nil || out != "done" {
		t.Fatalf("Turn() = %q, %v", out, err)
	}
	if !handlerSawCheckpoint {
		t.Fatal("effective tool input was not durably checkpointed before handler execution")
	}
}

func TestTurn_TransformedToolInputIsDurableBeforeLaterPolicyExecution(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{
			Type: llm.BlockToolUse, ToolUseID: "call-transform", ToolName: "side_effect", Input: map[string]any{"path": "provider.txt"},
		}}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, false)
	var durableEffectiveInput atomic.Bool
	bus.Subscribe(toolevents.InputResolvedType, func(event events.Event) {
		payload, ok := event.Payload.(toolevents.InputResolvedPayload)
		if ok && payload.Input["path"] == "effective.txt" {
			durableEffectiveInput.Store(true)
		}
	})
	laterPolicySawCheckpoint := false
	eng.Tools.MustRegister(tools.Tool{Name: "side_effect", Handler: func(context.Context, map[string]any) (string, error) {
		return "recorded once", nil
	}})
	installRuntimeTestModules(t, eng,
		&runtimeToolPolicyModule{id: "transform-input", apply: func(request runtimemodule.ToolPolicyRequest) (runtimemodule.ToolPolicyDecision, error) {
			if request.Stage == runtimemodule.ToolPolicyBeforeExecution {
				return runtimemodule.ToolPolicyDecision{Action: runtimemodule.ToolPolicyTransform, Input: map[string]any{"path": "effective.txt"}}, nil
			}
			return runtimemodule.ToolPolicyDecision{Action: runtimemodule.ToolPolicyAllow}, nil
		}},
		&runtimeToolPolicyModule{id: "later-policy", apply: func(request runtimemodule.ToolPolicyRequest) (runtimemodule.ToolPolicyDecision, error) {
			if request.Stage == runtimemodule.ToolPolicyBeforeExecution {
				laterPolicySawCheckpoint = durableEffectiveInput.Load()
			}
			return runtimemodule.ToolPolicyDecision{Action: runtimemodule.ToolPolicyAllow}, nil
		}},
	)

	if out, err := eng.Turn(context.Background(), "run transformed call"); err != nil || out != "done" {
		t.Fatalf("Turn() = %q, %v", out, err)
	}
	if !laterPolicySawCheckpoint {
		t.Fatal("effective tool input was not durably checkpointed before the later policy")
	}
}

func TestTurn_DurableTransformedToolInputFailurePreventsHandlerExecution(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{{
		Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{
			Type: llm.BlockToolUse, ToolUseID: "call-transform", ToolName: "side_effect", Input: map[string]any{"path": "provider.txt"},
		}}},
		StopReason: llm.StopToolUse,
	}}}
	eng, bus := newEngine(t, prov, false)
	toolCalls := 0
	laterPolicyCalls := 0
	eng.Tools.MustRegister(tools.Tool{Name: "side_effect", Handler: func(context.Context, map[string]any) (string, error) {
		toolCalls++
		return "must not run", nil
	}})
	installRuntimeTestModules(t, eng,
		&runtimeToolPolicyModule{id: "transform-input", apply: func(request runtimemodule.ToolPolicyRequest) (runtimemodule.ToolPolicyDecision, error) {
			if request.Stage == runtimemodule.ToolPolicyBeforeExecution {
				return runtimemodule.ToolPolicyDecision{Action: runtimemodule.ToolPolicyTransform, Input: map[string]any{"path": "effective.txt"}}, nil
			}
			return runtimemodule.ToolPolicyDecision{Action: runtimemodule.ToolPolicyAllow}, nil
		}},
		&runtimeToolPolicyModule{id: "later-policy", apply: func(runtimemodule.ToolPolicyRequest) (runtimemodule.ToolPolicyDecision, error) {
			laterPolicyCalls++
			return runtimemodule.ToolPolicyDecision{Action: runtimemodule.ToolPolicyAllow}, nil
		}},
	)
	want := errors.New("resolved input sync failed")
	bus.SetCommitter(selectiveFailCommitter{eventType: toolevents.InputResolvedType, err: want})

	if _, err := eng.Turn(context.Background(), "run transformed call"); !errors.Is(err, want) {
		t.Fatalf("Turn() error = %v, want %v", err, want)
	}
	if toolCalls != 0 {
		t.Fatalf("tool calls = %d, want 0", toolCalls)
	}
	if laterPolicyCalls != 0 {
		t.Fatalf("later policy calls = %d, want 0", laterPolicyCalls)
	}
}

func TestTurn_DeclaresWholeToolBatchBeforeAnyToolStarts(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "call-1", ToolName: "effect_one"},
			{Type: llm.BlockToolUse, ToolUseID: "call-2", ToolName: "effect_two"},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, false)
	for _, name := range []string{"effect_one", "effect_two"} {
		eng.Tools.MustRegister(tools.Tool{Name: name, Handler: func(context.Context, map[string]any) (string, error) {
			return "ok", nil
		}})
	}
	type observedToolEvent struct {
		typeName  string
		toolUseID string
	}
	var observedMu sync.Mutex
	var observed []observedToolEvent
	for _, eventType := range []string{toolevents.RequestedType, toolevents.RunningType} {
		bus.Subscribe(eventType, func(event events.Event) {
			observedMu.Lock()
			defer observedMu.Unlock()
			toolUseID := ""
			switch payload := event.Payload.(type) {
			case toolevents.RequestedPayload:
				toolUseID = payload.ToolUseID
			case toolevents.RunningPayload:
				toolUseID = payload.ToolUseID
			}
			observed = append(observed, observedToolEvent{typeName: event.Type, toolUseID: toolUseID})
		})
	}

	if out, err := eng.Turn(context.Background(), "run both"); err != nil || out != "done" {
		t.Fatalf("Turn() = %q, %v", out, err)
	}
	observedMu.Lock()
	defer observedMu.Unlock()
	if len(observed) != 4 {
		t.Fatalf("observed events = %+v", observed)
	}
	if observed[0] != (observedToolEvent{typeName: toolevents.RequestedType, toolUseID: "call-1"}) ||
		observed[1] != (observedToolEvent{typeName: toolevents.RequestedType, toolUseID: "call-2"}) {
		t.Fatalf("tool batch was not fully declared before execution: %+v", observed)
	}
}

func TestTurn_DurableToolResultFailurePreventsNextProviderCall(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{
			Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{
				Type: llm.BlockToolUse, ToolUseID: "call-1", ToolName: "side_effect",
			}}},
			StopReason: llm.StopToolUse,
		},
		{Message: llm.TextMessage(llm.RoleAssistant, "must not run"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, false)
	toolCalls := 0
	eng.Tools.MustRegister(tools.Tool{
		Name: "side_effect",
		Handler: func(context.Context, map[string]any) (string, error) {
			toolCalls++
			return "done", nil
		},
	})
	want := errors.New("journal sync failed")
	bus.SetCommitter(selectiveFailCommitter{eventType: toolevents.CompletedType, err: want})
	if _, err := eng.Turn(context.Background(), "run it"); !errors.Is(err, want) {
		t.Fatalf("Turn() error = %v, want %v", err, want)
	}
	if toolCalls != 1 {
		t.Fatalf("tool calls = %d, want 1", toolCalls)
	}
	if prov.called != 1 {
		t.Fatalf("provider calls = %d, want 1", prov.called)
	}
	history := eng.currentSession().History
	if len(history) != 2 || history[1].Role != llm.RoleAssistant {
		t.Fatalf("history = %+v, want unresolved assistant tool call", history)
	}
}

func TestTurn_DurableToolErrorFailurePersistsActualResult(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{
			Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{
				Type: llm.BlockToolUse, ToolUseID: "call-1", ToolName: "side_effect",
			}}},
			StopReason: llm.StopToolUse,
		},
		{Message: llm.TextMessage(llm.RoleAssistant, "must not run"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, false)
	eng.Tools.MustRegister(tools.Tool{
		Name: "side_effect",
		Handler: func(context.Context, map[string]any) (string, error) {
			return "partial output", errors.New("side effect failed")
		},
	})
	want := errors.New("journal sync failed")
	bus.SetCommitter(selectiveFailCommitter{eventType: toolevents.ErroredType, err: want})
	if _, err := eng.Turn(context.Background(), "run it"); !errors.Is(err, want) {
		t.Fatalf("Turn() error = %v, want %v", err, want)
	}
	if prov.called != 1 {
		t.Fatalf("provider calls = %d, want 1", prov.called)
	}
	history := eng.currentSession().History
	if len(history) != 2 || history[1].Role != llm.RoleAssistant {
		t.Fatalf("history = %+v, want unresolved assistant tool call", history)
	}
}

func TestTurn_DurableToolProjectionFailurePersistsProjectedResult(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{
			Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{
				Type: llm.BlockToolUse, ToolUseID: "call-1", ToolName: "large_result",
			}}},
			StopReason: llm.StopToolUse,
		},
		{Message: llm.TextMessage(llm.RoleAssistant, "must not run"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, false)
	eng.ToolOutput = ToolOutputPolicy{InlineMaxBytes: 8, PreviewHeadBytes: 4, PreviewTailBytes: 4}
	eng.Tools.MustRegister(tools.Tool{
		Name: "large_result",
		Handler: func(context.Context, map[string]any) (string, error) {
			return "head-" + strings.Repeat("payload-", 20) + "tail", nil
		},
	})
	want := errors.New("journal sync failed")
	bus.SetCommitter(selectiveFailCommitter{eventType: "context.projection.applied", err: want})
	if _, err := eng.Turn(context.Background(), "run it"); !errors.Is(err, want) {
		t.Fatalf("Turn() error = %v, want %v", err, want)
	}
	if prov.called != 1 {
		t.Fatalf("provider calls = %d, want 1", prov.called)
	}
	result := eng.currentSession().History[len(eng.currentSession().History)-1]
	if result.Kind != llm.MessageKindToolResult || len(result.Blocks) != 1 || result.Blocks[0].Artifact == nil {
		t.Fatalf("last history message = %+v, want persisted projected tool result", result)
	}
}

func TestTurn_DurableHookRequestFailurePreventsHookRun(t *testing.T) {
	eng, bus := newEngine(t, &mockProvider{}, false)
	runner := &fakeHookRunner{}
	installHookRunner(t, eng, runner)
	want := errors.New("journal sync failed")
	bus.SetCommitter(selectiveFailCommitter{eventType: "policy.requested", err: want})
	if _, err := eng.Turn(context.Background(), "hello"); !errors.Is(err, want) {
		t.Fatalf("Turn() error = %v, want %v", err, want)
	}
	if len(runner.requests) != 0 {
		t.Fatalf("hook requests = %d, want 0", len(runner.requests))
	}
}

type selectiveFailCommitter struct {
	eventType  string
	err        error
	beforeFail func() error
}

type interceptCommitter struct {
	eventType    string
	beforeCommit func() error
}

func (c interceptCommitter) Commit(event events.Event) (events.Event, error) {
	if event.Type == c.eventType && c.beforeCommit != nil {
		if err := c.beforeCommit(); err != nil {
			return events.Event{}, err
		}
	}
	return events.Normalize(event), nil
}

func (c selectiveFailCommitter) Commit(event events.Event) (events.Event, error) {
	if event.Type == c.eventType {
		if c.beforeFail != nil {
			if err := c.beforeFail(); err != nil {
				return events.Event{}, errors.Join(c.err, err)
			}
		}
		return events.Event{}, c.err
	}
	return events.Normalize(event), nil
}

type selectiveSessionCommitter struct {
	session   *session.Session
	eventType string
	err       error
}

func (c selectiveSessionCommitter) Commit(event events.Event) (events.Event, error) {
	event = events.Normalize(event)
	if event.Type == c.eventType {
		return events.Event{}, c.err
	}
	if !event.Transient {
		if err := c.session.AppendEvent(event); err != nil {
			return events.Event{}, err
		}
	}
	return event, nil
}

func TestTurn_ReturnsImagePlaceholderForImageOnlyResponse(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{
			Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
				{
					Type: llm.BlockImage,
					Media: &llm.MediaRef{
						ArtifactPath:  "sessions/s/media/chart.png",
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
	pb := newTestPromptBuilder("", func() time.Time { return time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC) })
	artifactState := t.TempDir()
	return &Engine{
		Provider:          prov,
		Tools:             reg,
		Bus:               bus,
		Session:           sess,
		Prompt:            pb,
		PendingInputQueue: NewPendingInputQueue(sess.Dir, PendingInputQueueOptions{Now: func() time.Time { return time.Date(2026, 6, 14, 8, 0, 0, 0, time.UTC) }}),
		PendingInputTTL:   time.Hour,
		ExternalEventTTL:  24 * time.Hour,
		WorkDir:           t.TempDir(),
		ArtifactDir:       filepath.Join(artifactState, "artifacts"),
	}
}

func TestTurn_CompactionKeepsRecentRealInputInProviderContext(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "summary"), StopReason: llm.StopEndTurn, Usage: llm.Usage{InputTokens: 10, OutputTokens: 2}},
		{Message: llm.TextMessage(llm.RoleAssistant, "answer"), StopReason: llm.StopEndTurn, Usage: llm.Usage{InputTokens: 20, OutputTokens: 3}},
	}}
	eng, _ := newEngine(t, prov, false)
	eng.ContextWindow = 200
	eng.Compaction = DefaultCompactionPolicy()
	eng.Compaction.KeepRecentTokens = 80
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
	if len(second) < 3 {
		t.Fatalf("second provider history too short: %+v", second)
	}
	if second[0].Kind != llm.MessageKindCompact {
		t.Fatalf("first active message kind = %q", second[0].Kind)
	}
	if !strings.Contains(messagesText(second), "recent question") || !strings.Contains(messagesText(second), "latest") {
		t.Fatalf("active context missing retained tail or latest: %+v", second)
	}
}

func TestTurn_CompactionSummarizesRealInputThatExceedsRetentionBudget(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "summary of oversized request"), StopReason: llm.StopEndTurn},
		{Message: llm.TextMessage(llm.RoleAssistant, "answer"), StopReason: llm.StopEndTurn},
	}}
	eng, _ := newEngine(t, prov, false)
	eng.ContextWindow = 4000
	eng.Compaction = DefaultCompactionPolicy()
	eng.Compaction.KeepRecentTokens = 200
	eng.Compaction.ReserveTokens = 1000
	eng.Compaction.SummaryMaxTokens = 500
	eng.Compaction.UserInputInlineMaxBytes = 1 << 20
	oversized := "oversized-request " + strings.Repeat("private-detail ", 1200) + "TAIL-SAFETY-GUARD"
	if err := eng.Session.Append(llm.TextMessage(llm.RoleUser, oversized)); err != nil {
		t.Fatal(err)
	}
	if err := eng.Session.Append(llm.TextMessage(llm.RoleAssistant, "working")); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.Turn(context.Background(), "latest"); err != nil {
		t.Fatal(err)
	}
	if len(prov.histories) != 2 {
		t.Fatalf("provider histories = %d, want summary and answer requests", len(prov.histories))
	}
	active := prov.histories[1]
	if strings.Contains(messagesText(active), strings.Repeat("private-detail ", 20)) {
		t.Fatalf("oversized retained input leaked past compact budget:\n%s", messagesText(active))
	}
	if !strings.Contains(messagesText(prov.histories[0]), "TAIL-SAFETY-GUARD") {
		t.Fatalf("summary request lost the oversized input tail:\n%s", messagesText(prov.histories[0]))
	}
	if !strings.Contains(messagesText(active), "summary of oversized request") || !strings.Contains(messagesText(active), "TAIL-SAFETY-GUARD") || !strings.Contains(messagesText(active), "User input stored outside context.") || !strings.Contains(messagesText(active), "path:") || !strings.Contains(messagesText(active), "latest") {
		t.Fatalf("active context missing summary or incoming request: %+v", active)
	}
	activeText := messagesText(active)
	pathStart := strings.Index(activeText, "path: ")
	if pathStart < 0 {
		t.Fatalf("active context missing recoverable artifact path:\n%s", activeText)
	}
	pathEnd := strings.IndexByte(activeText[pathStart:], '\n')
	if pathEnd < 0 {
		t.Fatalf("active context has unterminated artifact path:\n%s", activeText)
	}
	artifactPath := strings.TrimSpace(activeText[pathStart+len("path: ") : pathStart+pathEnd])
	artifactData, err := os.ReadFile(filepath.Join(eng.ArtifactDir, filepath.FromSlash(artifactPath)))
	if err != nil {
		t.Fatalf("read retained input artifact: %v", err)
	}
	if string(artifactData) != oversized {
		t.Fatalf("retained input artifact length = %d, want original %d", len(artifactData), len(oversized))
	}
	compact := eng.Session.History[len(eng.Session.History)-3]
	if compact.Kind != llm.MessageKindCompact || compact.Compaction == nil {
		t.Fatalf("compaction marker = %+v", compact)
	}
	if len(compact.Compaction.RetainedMessageIDs) != 0 {
		t.Fatalf("retained ids = %v, want oversized input summarized", compact.Compaction.RetainedMessageIDs)
	}
	if compact.Compaction.TokensAfter >= eng.ContextWindow-eng.Compaction.ReserveTokens {
		t.Fatalf("tokens after = %d, want below trigger %d", compact.Compaction.TokensAfter, eng.ContextWindow-eng.Compaction.ReserveTokens)
	}
}

func TestTurn_CompactionRetightensPreviouslyProjectedOversizedInput(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "working"), StopReason: llm.StopEndTurn},
		{Message: llm.TextMessage(llm.RoleAssistant, "summary of projected request"), StopReason: llm.StopEndTurn},
		{Message: llm.TextMessage(llm.RoleAssistant, "answer"), StopReason: llm.StopEndTurn},
	}}
	eng, _ := newEngine(t, prov, false)
	eng.ContextWindow = 4000
	eng.Compaction = DefaultCompactionPolicy()
	eng.Compaction.KeepRecentTokens = 200
	eng.Compaction.ReserveTokens = 1000
	eng.Compaction.SummaryMaxTokens = 500
	eng.Compaction.UserInputInlineMaxBytes = 1
	eng.Compaction.UserInputPreviewHeadBytes = 8192
	eng.Compaction.UserInputPreviewTailBytes = 8192
	oversized := "oversized-request " + strings.Repeat("private-detail ", 2000) + "TAIL-SAFETY-GUARD"

	if _, err := eng.Turn(context.Background(), oversized); err != nil {
		t.Fatal(err)
	}
	projected := eng.Session.History[0].Blocks[0]
	if projected.Artifact == nil || projected.Artifact.HeadBytes+projected.Artifact.TailBytes <= eng.Compaction.KeepRecentTokens {
		t.Fatalf("initial projection = %+v, want preview larger than compact retention budget", projected.Artifact)
	}
	if _, err := eng.Turn(context.Background(), "latest"); err != nil {
		t.Fatal(err)
	}
	if len(prov.histories) != 3 {
		t.Fatalf("provider histories = %d, want first answer, summary, and final answer requests", len(prov.histories))
	}
	activeText := messagesText(prov.histories[2])
	if strings.Contains(activeText, strings.Repeat("private-detail ", 20)) {
		t.Fatalf("active context kept the original large preview:\n%s", activeText)
	}
	for _, want := range []string{"summary of projected request", "TAIL-SAFETY-GUARD", projected.Artifact.StoredPath, "latest"} {
		if !strings.Contains(activeText, want) {
			t.Fatalf("active context missing %q:\n%s", want, activeText)
		}
	}
	var compact llm.Message
	for _, msg := range eng.Session.History {
		if msg.Kind == llm.MessageKindCompact {
			compact = msg
		}
	}
	if compact.Compaction == nil {
		t.Fatalf("missing compaction marker: %+v", eng.Session.History)
	}
	if compact.Compaction.TokensAfter >= eng.ContextWindow-eng.Compaction.ReserveTokens {
		t.Fatalf("tokens after = %d, want below trigger %d", compact.Compaction.TokensAfter, eng.ContextWindow-eng.Compaction.ReserveTokens)
	}
	if got := string(readProjectedArtifact(t, eng, projected.Artifact)); got != oversized {
		t.Fatalf("stored artifact changed: got %d bytes, want %d", len(got), len(oversized))
	}
}

func TestTurn_CompactionBoundsSharedPreviewForMultipleProjectedBlocks(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "summary of multi-block request"), StopReason: llm.StopEndTurn},
		{Message: llm.TextMessage(llm.RoleAssistant, "answer"), StopReason: llm.StopEndTurn},
	}}
	eng, _ := newEngine(t, prov, false)
	eng.ContextWindow = 4000
	eng.Compaction = DefaultCompactionPolicy()
	eng.Compaction.KeepRecentTokens = 200
	eng.Compaction.ReserveTokens = 1000
	eng.Compaction.SummaryMaxTokens = 500
	eng.Compaction.UserInputInlineMaxBytes = 1
	eng.Compaction.UserInputPreviewHeadBytes = 2048
	eng.Compaction.UserInputPreviewTailBytes = 2048
	msg := llm.Message{ID: "multi-block", Role: llm.RoleUser, Kind: llm.MessageKindDirect}
	for i := range 6 {
		msg.Blocks = append(msg.Blocks, llm.Block{Type: llm.BlockText, Text: fmt.Sprintf("BLOCK-%d-HEAD ", i) + strings.Repeat(fmt.Sprintf("private-%d ", i), 600) + fmt.Sprintf(" BLOCK-%d-TAIL", i)})
	}
	projected, _, err := eng.projectMessageLocked(msg, effectiveCompactionPolicy(eng.Compaction, eng.ContextWindow))
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.Session.Append(projected); err != nil {
		t.Fatal(err)
	}
	if err := eng.Session.Append(llm.TextMessage(llm.RoleAssistant, "working")); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.Turn(context.Background(), "latest"); err != nil {
		t.Fatal(err)
	}
	if len(prov.histories) != 2 {
		t.Fatalf("provider histories = %d, want summary and final answer requests", len(prov.histories))
	}
	activeText := messagesText(prov.histories[1])
	if strings.Contains(activeText, strings.Repeat("private-0 ", 20)) {
		t.Fatalf("active context kept a per-block full preview:\n%s", activeText)
	}
	for i, block := range projected.Blocks {
		if block.Artifact == nil || !strings.Contains(activeText, block.Artifact.StoredPath) {
			t.Fatalf("active context missing artifact path for block %d: %+v\n%s", i, block.Artifact, activeText)
		}
	}
	var compact llm.Message
	for _, history := range eng.Session.History {
		if history.Kind == llm.MessageKindCompact {
			compact = history
		}
	}
	if compact.Compaction == nil || compact.Compaction.TokensAfter >= eng.ContextWindow-eng.Compaction.ReserveTokens {
		t.Fatalf("compaction marker = %+v, want tokens after below trigger %d", compact.Compaction, eng.ContextWindow-eng.Compaction.ReserveTokens)
	}
}

func TestTurn_CompactionCarriesRetainedInputReferencesAcrossCompactions(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "first summary without references"), StopReason: llm.StopEndTurn},
		{Message: llm.TextMessage(llm.RoleAssistant, "first answer"), StopReason: llm.StopEndTurn},
		{Message: llm.TextMessage(llm.RoleAssistant, "second summary without references"), StopReason: llm.StopEndTurn},
		{Message: llm.TextMessage(llm.RoleAssistant, "second answer"), StopReason: llm.StopEndTurn},
	}}
	eng, _ := newEngine(t, prov, false)
	eng.ContextWindow = 4000
	eng.Compaction = DefaultCompactionPolicy()
	eng.Compaction.KeepRecentTokens = 1000
	eng.Compaction.ReserveTokens = 1000
	eng.Compaction.SummaryMaxTokens = 500
	eng.Compaction.UserInputInlineMaxBytes = 1 << 20
	firstInput := "first-head " + strings.Repeat("first-private ", 1600) + " FIRST-TAIL"
	if err := eng.Session.Append(llm.TextMessage(llm.RoleUser, firstInput)); err != nil {
		t.Fatal(err)
	}
	if err := eng.Session.Append(llm.TextMessage(llm.RoleAssistant, "working")); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Turn(context.Background(), "first-latest"); err != nil {
		t.Fatal(err)
	}
	var firstCompact llm.Message
	for _, msg := range eng.Session.History {
		if msg.Kind == llm.MessageKindCompact {
			firstCompact = msg
		}
	}
	if firstCompact.Compaction == nil || len(firstCompact.Compaction.RetainedInputReferences) != 1 {
		t.Fatalf("first compact references = %+v", firstCompact.Compaction)
	}
	firstReference := firstCompact.Compaction.RetainedInputReferences[0]
	firstArtifact := firstReference.Blocks[0].Artifact
	if firstArtifact == nil {
		t.Fatalf("first retained reference = %+v", firstReference)
	}

	secondInput := "second-head " + strings.Repeat("second-private ", 1600) + " SECOND-TAIL"
	if err := eng.Session.Append(llm.TextMessage(llm.RoleUser, secondInput)); err != nil {
		t.Fatal(err)
	}
	if err := eng.Session.Append(llm.TextMessage(llm.RoleAssistant, "more work")); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Turn(context.Background(), "second-latest"); err != nil {
		t.Fatal(err)
	}
	if len(prov.histories) != 4 {
		t.Fatalf("provider histories = %d, want two summary and two answer requests", len(prov.histories))
	}
	secondSummaryRequest := messagesText(prov.histories[2])
	if strings.Contains(secondSummaryRequest, "Retained Input References") || strings.Contains(secondSummaryRequest, firstArtifact.StoredPath) {
		t.Fatalf("second summary request replayed deterministic retained references:\n%s", secondSummaryRequest)
	}
	if !strings.Contains(secondSummaryRequest, "first summary without references") {
		t.Fatalf("second summary request lost first model summary:\n%s", secondSummaryRequest)
	}
	var compacts []llm.Message
	for _, msg := range eng.Session.History {
		if msg.Kind == llm.MessageKindCompact {
			compacts = append(compacts, msg)
		}
	}
	if len(compacts) != 2 {
		t.Fatalf("compact messages = %d, want 2", len(compacts))
	}
	latest := compacts[1]
	if latest.Compaction == nil || len(latest.Compaction.RetainedInputReferences) != 2 {
		t.Fatalf("latest compact references = %+v, want inherited and current", latest.Compaction)
	}
	activeText := messagesText(prov.histories[3])
	for _, want := range []string{"second summary without references", firstArtifact.StoredPath, firstArtifact.SHA256, "FIRST-TAIL", "SECOND-TAIL", "second-latest"} {
		if !strings.Contains(activeText, want) {
			t.Fatalf("active context missing carried reference %q:\n%s", want, activeText)
		}
	}
	previewBytes := 0
	for _, reference := range latest.Compaction.RetainedInputReferences {
		for _, block := range reference.Blocks {
			if block.Artifact != nil {
				previewBytes += block.Artifact.HeadBytes + block.Artifact.TailBytes
			}
		}
	}
	if previewBytes > eng.Compaction.KeepRecentTokens {
		t.Fatalf("carried preview bytes = %d, want <= %d", previewBytes, eng.Compaction.KeepRecentTokens)
	}
	if latest.Compaction.TokensAfter >= eng.ContextWindow-eng.Compaction.ReserveTokens {
		t.Fatalf("tokens after = %d, want below trigger %d", latest.Compaction.TokensAfter, eng.ContextWindow-eng.Compaction.ReserveTokens)
	}
	if got := string(readProjectedArtifact(t, eng, firstArtifact)); got != firstInput {
		t.Fatalf("first retained artifact changed: got %d bytes, want %d", len(got), len(firstInput))
	}
}

func TestTurn_CompactionProjectsOversizedInputInsideExecutionTail(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "summary without raw initiator"), StopReason: llm.StopEndTurn},
		{Message: llm.TextMessage(llm.RoleAssistant, "answer"), StopReason: llm.StopEndTurn},
	}}
	eng, _ := newEngine(t, prov, false)
	eng.ContextWindow = 4000
	eng.Compaction = DefaultCompactionPolicy()
	eng.Compaction.KeepRecentTokens = 200
	eng.Compaction.ReserveTokens = 1000
	eng.Compaction.SummaryMaxTokens = 500
	eng.Compaction.UserInputInlineMaxBytes = 1 << 20
	initiator := llm.TextMessage(llm.RoleUser, "tool-request-head "+strings.Repeat("private-tool-request ", 1600)+" TOOL-REQUEST-TAIL")
	initiator.ID = "direct-1"
	initiator.Kind = llm.MessageKindDirect
	toolUse := llm.Message{ID: "tool-use-1", Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ToolUseID: "call-1", ToolName: "read"}}}
	toolResult := llm.Message{ID: "tool-result-1", Role: llm.RoleUser, Kind: llm.MessageKindToolResult, Blocks: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "call-1", Content: "contents"}}}
	for _, msg := range []llm.Message{initiator, toolUse, toolResult} {
		if err := eng.Session.Append(msg); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := eng.Turn(context.Background(), "latest"); err != nil {
		t.Fatal(err)
	}
	if len(prov.histories) != 2 {
		t.Fatalf("provider histories = %d, want summary and answer requests", len(prov.histories))
	}
	active := prov.histories[1]
	activeText := messagesText(active)
	if strings.Contains(activeText, strings.Repeat("private-tool-request ", 20)) {
		t.Fatalf("active context retained oversized tool initiator verbatim:\n%s", activeText)
	}
	for _, want := range []string{"summary without raw initiator", "TOOL-REQUEST-TAIL", "latest"} {
		if !strings.Contains(activeText, want) {
			t.Fatalf("active context missing %q:\n%s", want, activeText)
		}
	}
	var compact llm.Message
	for _, msg := range eng.Session.History {
		if msg.Kind == llm.MessageKindCompact {
			compact = msg
		}
	}
	if compact.Compaction == nil || len(compact.Compaction.RetainedInputReferences) != 1 {
		t.Fatalf("compact metadata = %+v, want projected initiator reference", compact.Compaction)
	}
	if slices.Contains(compact.Compaction.RetainedMessageIDs, initiator.ID) || !slices.Contains(compact.Compaction.RetainedMessageIDs, toolUse.ID) || !slices.Contains(compact.Compaction.RetainedMessageIDs, toolResult.ID) {
		t.Fatalf("retained ids = %v, want tool protocol without raw initiator", compact.Compaction.RetainedMessageIDs)
	}
	if compact.Compaction.TokensAfter >= eng.ContextWindow-eng.Compaction.ReserveTokens {
		t.Fatalf("tokens after = %d, want below trigger %d", compact.Compaction.TokensAfter, eng.ContextWindow-eng.Compaction.ReserveTokens)
	}
	var sawUse, sawResult bool
	for _, msg := range active {
		for _, block := range msg.Blocks {
			sawUse = sawUse || block.Type == llm.BlockToolUse && block.ToolUseID == "call-1"
			sawResult = sawResult || block.Type == llm.BlockToolResult && block.ToolUseID == "call-1"
		}
	}
	if !sawUse || !sawResult {
		t.Fatalf("active tool protocol closed = use:%t result:%t; history=%+v", sawUse, sawResult, active)
	}
}

func TestTurn_CompactionKeepsOversizedImageInputReferenceAndOneByteCaption(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "summary with image reference"), StopReason: llm.StopEndTurn},
		{Message: llm.TextMessage(llm.RoleAssistant, "answer"), StopReason: llm.StopEndTurn},
	}}
	eng, _ := newEngine(t, prov, false)
	eng.ContextWindow = 4000
	eng.Compaction = DefaultCompactionPolicy()
	eng.Compaction.KeepRecentTokens = 200
	eng.Compaction.ReserveTokens = 1000
	eng.Compaction.SummaryMaxTokens = 500
	image := llm.Message{
		Role: llm.RoleUser,
		Kind: llm.MessageKindDirect,
		Blocks: []llm.Block{
			{Type: llm.BlockText, Text: "A"},
			{Type: llm.BlockImage, Media: &llm.MediaRef{
				ArtifactPath:  "sessions/session/media/photo.png",
				MediaType:     "image/png",
				SHA256:        "image-sha",
				OriginalBytes: 1234,
				Width:         4000,
				Height:        4000,
			}},
		},
	}
	if err := eng.Session.Append(image); err != nil {
		t.Fatal(err)
	}
	if err := eng.Session.Append(llm.TextMessage(llm.RoleAssistant, "working")); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.Turn(context.Background(), "latest"); err != nil {
		t.Fatal(err)
	}
	if len(prov.histories) != 2 {
		t.Fatalf("provider histories = %d, want summary and answer requests", len(prov.histories))
	}
	for index, history := range prov.histories {
		text := messagesText(history)
		caption := "text: A"
		if index > 0 {
			caption = "\nA\n"
		}
		for _, want := range []string{caption, "sessions/session/media/photo.png", "image-sha", "size=4000x4000"} {
			if !strings.Contains(text, want) {
				t.Fatalf("provider history %d missing media reference %q:\n%s", index, want, text)
			}
		}
	}
	compact := eng.Session.History[len(eng.Session.History)-3]
	if compact.Kind != llm.MessageKindCompact || compact.Compaction == nil || len(compact.Compaction.RetainedMessageIDs) != 0 {
		t.Fatalf("compaction marker = %+v", compact)
	}
	if compact.Compaction.TokensAfter >= eng.ContextWindow-eng.Compaction.ReserveTokens {
		t.Fatalf("tokens after = %d, want below trigger %d", compact.Compaction.TokensAfter, eng.ContextWindow-eng.Compaction.ReserveTokens)
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
	eng.Compaction.Enabled = false
	eng.ToolOutput = ToolOutputPolicy{
		InlineMaxBytes:   64,
		PreviewHeadBytes: 10,
		PreviewTailBytes: 10,
	}
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

func TestTurn_ProjectsLargeUnprojectedHistoryBeforeProviderRequest(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "answer"), StopReason: llm.StopEndTurn},
	}}
	eng, _ := newEngine(t, prov, false)
	eng.ContextWindow = 10000
	eng.Compaction = DefaultCompactionPolicy()
	eng.Compaction.UserInputInlineMaxBytes = 64
	eng.Compaction.UserInputPreviewHeadBytes = 10
	eng.Compaction.UserInputPreviewTailBytes = 10
	original := "old-head\n" + strings.Repeat("ARCHIVED-SECRET ", 80) + "\nold-tail"
	if err := eng.Session.Append(llm.TextMessage(llm.RoleUser, original)); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.Turn(context.Background(), "latest"); err != nil {
		t.Fatal(err)
	}
	providerText := messagesText(prov.histories[0])
	if strings.Contains(providerText, "ARCHIVED-SECRET ARCHIVED-SECRET") {
		t.Fatalf("provider received unbounded historical input:\n%s", providerText)
	}
	if !strings.Contains(providerText, "User input stored outside context.") || !strings.Contains(providerText, "old-head") || !strings.Contains(providerText, "old-tail") {
		t.Fatalf("historical input projection missing:\n%s", providerText)
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

func TestCancelActiveTurnCancelsRuntimeOwnedProviderRequest(t *testing.T) {
	prov := &cancellableProvider{started: make(chan struct{}, 1)}
	eng, _ := newEngine(t, prov, false)
	done := make(chan error, 1)
	go func() {
		_, err := eng.Turn(context.Background(), "wait for cancellation")
		done <- err
	}()
	waitSignal(t, prov.started, "provider start")

	if !eng.CancelActiveTurn(cancellation.ErrUserCancelled) {
		t.Fatal("CancelActiveTurn returned false for active provider request")
	}
	if eng.CancelActiveTurn(cancellation.ErrUserCancelled) {
		t.Fatal("second CancelActiveTurn returned true")
	}
	select {
	case err := <-done:
		if !errors.Is(err, cancellation.ErrUserCancelled) {
			t.Fatalf("turn err = %v, want ErrUserCancelled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("turn did not stop after runtime cancellation")
	}
}

func TestCancelActiveTurnCancelsCompactionWithoutAppendingMarker(t *testing.T) {
	prov := &cancellableProvider{started: make(chan struct{}, 1)}
	eng, bus := newEngine(t, prov, false)
	eng.ContextWindow = 100
	eng.Compaction = DefaultCompactionPolicy()
	if err := eng.Session.Append(llm.TextMessage(llm.RoleUser, strings.Repeat("old ", 80))); err != nil {
		t.Fatal(err)
	}
	var compactError ContextCompactErroredPayload
	bus.Subscribe("context.compact.errored", func(event events.Event) {
		compactError, _ = event.Payload.(ContextCompactErroredPayload)
	})

	done := make(chan error, 1)
	go func() {
		_, err := eng.CompactWithInstructions(
			context.Background(),
			"compact-cancel",
			"system",
			"manual",
			false,
			"",
		)
		done <- err
	}()
	waitSignal(t, prov.started, "compaction provider start")

	if !eng.CancelActiveTurn(cancellation.ErrUserCancelled) {
		t.Fatal("CancelActiveTurn returned false for active compaction")
	}
	select {
	case err := <-done:
		if !errors.Is(err, cancellation.ErrUserCancelled) {
			t.Fatalf("compact err = %v, want ErrUserCancelled", err)
		}
		payload := NewTurnErroredPayload(err)
		if payload.Error != "Compaction canceled" || payload.ErrorKind != string(errorclass.KindCancelled) {
			t.Fatalf("turn error payload = %+v", payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("compaction did not stop after runtime cancellation")
	}
	if compactError.Error != "Compaction canceled" {
		t.Fatalf("compact event error = %q", compactError.Error)
	}
	for _, msg := range eng.Session.History {
		if msg.Kind == llm.MessageKindCompact {
			t.Fatalf("unexpected compact marker after cancellation: %+v", msg)
		}
	}
}

func TestCancelActiveTurnRejectsCancellationAfterCompactionCommit(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{{
		Message:    llm.TextMessage(llm.RoleAssistant, "committed summary"),
		StopReason: llm.StopEndTurn,
	}}}
	eng, bus := newEngine(t, prov, false)
	eng.ContextWindow = 100
	eng.Compaction = DefaultCompactionPolicy()
	if err := eng.Session.Append(llm.TextMessage(llm.RoleUser, strings.Repeat("old ", 80))); err != nil {
		t.Fatal(err)
	}

	postStarted := make(chan struct{}, 1)
	releasePost := make(chan struct{})
	var releaseOnce sync.Once
	releaseHook := func() { releaseOnce.Do(func() { close(releasePost) }) }
	defer releaseHook()
	installHookRunner(t, eng, hookRunnerFunc(func(ctx context.Context, req hooks.Request) ([]hooks.Result, error) {
		if req.EventName != hooks.EventPostCompact {
			return nil, nil
		}
		signal(postStarted)
		select {
		case <-releasePost:
			return nil, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}))
	completed := make(chan struct{}, 1)
	bus.Subscribe("context.compact.completed", func(events.Event) {
		signal(completed)
	})

	done := make(chan error, 1)
	go func() {
		_, err := eng.CompactWithInstructions(
			context.Background(),
			"compact-commit",
			"system",
			"manual",
			false,
			"",
		)
		done <- err
	}()
	waitSignal(t, postStarted, "post-compact hook start")
	select {
	case <-completed:
	default:
		t.Fatal("compaction completion was not published before post-commit hook")
	}

	if eng.CancelActiveTurn(cancellation.ErrUserCancelled) {
		t.Fatal("CancelActiveTurn accepted cancellation after compact marker commit")
	}
	foundMarker := false
	for _, msg := range eng.Session.History {
		if msg.Kind == llm.MessageKindCompact {
			foundMarker = true
		}
	}
	if !foundMarker {
		t.Fatal("compaction marker was not committed before post-compact hook")
	}

	releaseHook()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("compaction did not finish after post-compact hook release")
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

func TestTurnMessage_SideSessionContinuesAfterAutoCompactionFailure(t *testing.T) {
	prov := &mockProviderWithErrors{
		errs: []error{fmt.Errorf("openai codex responses: codex SSE read: context deadline exceeded")},
		responses: []llm.Response{
			{Message: llm.TextMessage(llm.RoleAssistant, "handled side result"), StopReason: llm.StopEndTurn},
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

	msg := llm.TextMessage(llm.RoleUser, "Side Session result: done")
	msg.Kind = llm.MessageKindSideSession
	out, err := eng.TurnMessage(context.Background(), msg)
	if err != nil {
		t.Fatal(err)
	}
	if out != "handled side result" {
		t.Fatalf("out = %q, want handled side result", out)
	}
	if !strings.Contains(compactErr, "codex SSE read") {
		t.Fatalf("compact error event = %q, want original failure", compactErr)
	}
	if prov.called != 2 {
		t.Fatalf("provider calls = %d, want compact attempt plus side result turn", prov.called)
	}
	if len(eng.Session.History) != 3 {
		t.Fatalf("history len = %d, want old message, side result, assistant", len(eng.Session.History))
	}
	if got := eng.Session.History[1]; got.Kind != llm.MessageKindSideSession || got.FirstText() != "Side Session result: done" {
		t.Fatalf("side result not preserved: %+v", got)
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

func TestTurn_SecondOverflowDoesNotRetryForPendingInput(t *testing.T) {
	var eng *Engine
	var enqueueErr error
	prov := &scriptedCompactionProvider{
		name: "overflow-twice",
		attempts: []scriptedCompactionAttempt{
			{err: errors.New("context_length_exceeded: first turn attempt")},
			{response: llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "summary"), StopReason: llm.StopEndTurn}},
			{
				err: errors.New("context_length_exceeded: compacted turn attempt"),
				beforeReturn: func() {
					_, enqueueErr = eng.EnqueuePendingInput(context.Background(), "queued during second overflow")
				},
			},
		},
	}
	eng, _ = newEngine(t, prov, false)
	eng.ContextWindow = 10000
	eng.Compaction = DefaultCompactionPolicy()
	if err := eng.Session.Append(llm.TextMessage(llm.RoleUser, strings.Repeat("old ", 400))); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.Turn(context.Background(), "latest"); err == nil || !strings.Contains(err.Error(), "context_length_exceeded") {
		t.Fatalf("turn error = %v, want terminal context overflow", err)
	}
	if enqueueErr != nil {
		t.Fatalf("enqueue pending input: %v", enqueueErr)
	}
	if prov.calls != 3 {
		t.Fatalf("provider calls = %d, want initial turn + compact + one retry", prov.calls)
	}
	if got := messagesText(eng.Session.History); !strings.Contains(got, "queued during second overflow") {
		t.Fatalf("terminal overflow did not preserve pending input:\n%s", got)
	}
}

func TestTurn_CompactRetryFailureConsumesPolicyContext(t *testing.T) {
	prov := &scriptedCompactionProvider{
		name: "compact-failure",
		attempts: []scriptedCompactionAttempt{
			{err: errors.New("context_length_exceeded: turn attempt")},
			{err: errors.New("compaction backend unavailable")},
			{response: llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "next turn"), StopReason: llm.StopEndTurn}},
		},
	}
	eng, _ := newEngine(t, prov, false)
	eng.ContextWindow = 10000
	eng.Compaction = DefaultCompactionPolicy()
	if err := eng.queuePolicyRuntimeContextFromHookResults([]hooks.Result{{
		Hook:   hooks.CommandHook{Name: "one-shot"},
		Stdout: "one-shot compact context",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := eng.Session.Append(llm.TextMessage(llm.RoleUser, strings.Repeat("old ", 400))); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.Turn(context.Background(), "failing turn"); err == nil || !strings.Contains(err.Error(), "compact retry failed") {
		t.Fatalf("turn error = %v, want compact retry failure", err)
	}
	if out, err := eng.Turn(context.Background(), "next request"); err != nil || out != "next turn" {
		t.Fatalf("next turn out=%q err=%v", out, err)
	}
	if prov.calls != 3 {
		t.Fatalf("provider calls = %d, want failed turn + compact + next turn", prov.calls)
	}
	if got := messagesText(prov.histories[0]); !strings.Contains(got, "one-shot compact context") {
		t.Fatalf("failed provider request missing policy context:\n%s", got)
	}
	if got := messagesText(prov.histories[2]); strings.Contains(got, "one-shot compact context") {
		t.Fatalf("next provider request repeated stale policy context:\n%s", got)
	}
	if remaining := eng.pendingPolicyRuntimeContextSnapshot(); len(remaining) != 0 {
		t.Fatalf("policy context remaining after compact retry failure = %+v", remaining)
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
	case <-time.After(5 * time.Second):
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
	eng, bus := newEngine(t, prov, false)
	eng.ContextWindow = 1000
	eng.Compaction = DefaultCompactionPolicy()
	if err := eng.Session.Append(llm.TextMessage(llm.RoleUser, strings.Repeat("old ", 80))); err != nil {
		t.Fatal(err)
	}
	var completedContextUsage *llm.ContextUsage
	bus.Subscribe("context.compact.completed", func(event events.Event) {
		data, err := json.Marshal(event.Payload)
		if err != nil {
			t.Errorf("marshal compact completed payload: %v", err)
			return
		}
		var payload struct {
			ContextUsage *llm.ContextUsage `json:"context_usage"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Errorf("decode compact completed payload: %v", err)
			return
		}
		completedContextUsage = payload.ContextUsage
	})

	result, err := eng.Compact(context.Background(), "turn-1", "system", "manual", false)
	if err != nil {
		t.Fatal(err)
	}
	info := eng.Session.Info()
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
	if completedContextUsage == nil ||
		completedContextUsage.TotalTokens != result.TokensAfter ||
		completedContextUsage.ContextWindow != 1000 {
		t.Fatalf("completed event context usage = %+v", completedContextUsage)
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
	var responded LLMRespondedPayload
	bus.Subscribe("llm.responded", func(e events.Event) {
		responded = e.Payload.(LLMRespondedPayload)
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
	if got := eng.Session.Info().TokenUsage; got != (llm.Usage{InputTokens: 10, OutputTokens: 5}) {
		t.Fatalf("session token usage = %+v", got)
	}
	if responded.TokenUsage != (llm.Usage{InputTokens: 10, OutputTokens: 5}) {
		t.Fatalf("event usage = %+v", responded.TokenUsage)
	}
	if responded.MessageID == "" || responded.MessageID != eng.Session.History[1].ID {
		t.Fatalf("responded message id = %q, history id = %q", responded.MessageID, eng.Session.History[1].ID)
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

	info := eng.Session.Info()
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
	for _, key := range []string{"system_prompt", "system_tools", "mcp_tools", "skills", "messages", "response"} {
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

	got := eng.Session.Info().ContextUsage
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
	var started TurnStartedPayload
	bus.Subscribe("turn.started", func(e events.Event) {
		started = e.Payload.(TurnStartedPayload)
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
	if started.Kind != llm.MessageKindMCPEvent {
		t.Fatalf("turn.started kind = %q", started.Kind)
	}
	if started.MessageID == "" || started.MessageID != eng.Session.History[0].ID {
		t.Fatalf("turn.started message id = %q, history id = %q", started.MessageID, eng.Session.History[0].ID)
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
	runner := &fakeHookRunner{responses: map[hooks.EventName][]fakeHookResponse{
		hooks.EventPreCompact:  {{}},
		hooks.EventPostCompact: {{}},
	}}
	installHookRunner(t, eng, runner)
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
	got := []hooks.EventName{runner.requests[0].EventName, runner.requests[1].EventName}
	want := []hooks.EventName{hooks.EventPreCompact, hooks.EventPostCompact}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("hook order = %+v, want %+v", got, want)
		}
	}
}

func TestCompactPreHookStdoutExtendsSummaryInstructions(t *testing.T) {
	prov := &scriptedCompactionProvider{
		name: "summary",
		attempts: []scriptedCompactionAttempt{{
			response: llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "summary of old work"), StopReason: llm.StopEndTurn},
		}},
	}
	eng, _ := newEngine(t, prov, false)
	eng.Compaction = DefaultCompactionPolicy()
	eng.Compaction.KeepRecentTokens = 1
	installHookRunner(t, eng, &fakeHookRunner{responses: map[hooks.EventName][]fakeHookResponse{
		hooks.EventPreCompact: {{Stdout: "Preserve deployment command exactly."}},
	}})
	if err := eng.Session.Append(llm.TextMessage(llm.RoleUser, strings.Repeat("old ", 80))); err != nil {
		t.Fatal(err)
	}
	if err := eng.Session.Append(llm.TextMessage(llm.RoleAssistant, strings.Repeat("reply ", 80))); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.Compact(context.Background(), "compact-turn", "system", "manual", false); err != nil {
		t.Fatal(err)
	}
	if len(prov.systems) != 1 || !strings.Contains(prov.systems[0], "Hook compact instructions (fake):\nPreserve deployment command exactly.") {
		t.Fatalf("compaction system = %q", strings.Join(prov.systems, "\n---\n"))
	}
}

func TestCompactCarriesAuthoritativeStateAndMergesInstructionSources(t *testing.T) {
	prov := &scriptedCompactionProvider{
		name: "summary",
		attempts: []scriptedCompactionAttempt{{
			response: llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "summary of old work"), StopReason: llm.StopEndTurn},
		}},
	}
	eng, _ := newEngine(t, prov, false)
	eng.Compaction = DefaultCompactionPolicy()
	eng.Compaction.KeepRecentTokens = 1
	eng.Compaction.Instructions = "Preserve configured release criteria."
	goalState := workmem.NewGoalStateStore(eng.Session.Dir, workmem.GoalStateOptions{})
	if _, err := goalState.CreateWithContract(workmem.GoalStateCreate{
		Description: "Ship authoritative compaction state",
		Acceptance:  "The persisted goal remains exact:\n- [ ] preserve acceptance line one\n- [ ] preserve acceptance line two",
	}); err != nil {
		t.Fatal(err)
	}
	notesStore := workmem.NewNotesStore(eng.Session.Dir)
	if _, err := notesStore.Update("- [x] map the runtime\n- [ ] run the live compaction evaluation"); err != nil {
		t.Fatal(err)
	}
	installSessionStateModulesWithStores(t, eng, goalState, notesStore)
	installHookRunner(t, eng, &fakeHookRunner{responses: map[hooks.EventName][]fakeHookResponse{
		hooks.EventPreCompact: {{Stdout: "Preserve hook deployment evidence."}},
	}})
	if err := eng.Session.Append(llm.TextMessage(llm.RoleUser, strings.Repeat("old ", 80))); err != nil {
		t.Fatal(err)
	}
	if err := eng.Session.Append(llm.TextMessage(llm.RoleAssistant, strings.Repeat("reply ", 80))); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.CompactWithInstructions(context.Background(), "compact-turn", "system", "manual", false, "Focus on the requested verification command."); err != nil {
		t.Fatal(err)
	}
	if len(prov.systems) != 1 || len(prov.histories) != 1 || len(prov.histories[0]) != 1 {
		t.Fatalf("summary requests = systems:%d histories:%d", len(prov.systems), len(prov.histories))
	}
	system := prov.systems[0]
	configured := strings.Index(system, "Preserve configured release criteria.")
	requested := strings.Index(system, "Focus on the requested verification command.")
	hook := strings.Index(system, "Hook compact instructions (fake):\nPreserve hook deployment evidence.")
	if configured < 0 || requested <= configured || hook <= requested {
		t.Fatalf("compact instruction order is not config -> request -> hook:\n%s", system)
	}
	body := prov.histories[0][0].FirstText()
	for _, want := range []string{
		"<authoritative-session-state>",
		"- [x] map the runtime",
		"- [ ] run the live compaction evaluation",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("summary request missing authoritative state %q:\n%s", want, body)
		}
	}
	const goalOpen = "<goal-contract>\n"
	const goalClose = "\n</goal-contract>"
	start := strings.Index(body, goalOpen)
	if start < 0 {
		t.Fatalf("summary request missing goal contract:\n%s", body)
	}
	start += len(goalOpen)
	end := strings.Index(body[start:], goalClose)
	if end < 0 {
		t.Fatalf("summary request has unterminated goal contract:\n%s", body)
	}
	var goal struct {
		Description string `json:"description"`
		Acceptance  string `json:"acceptance"`
		Status      string `json:"status"`
	}
	if err := json.Unmarshal([]byte(body[start:start+end]), &goal); err != nil {
		t.Fatalf("decode summary goal contract: %v\n%s", err, body)
	}
	if goal.Description != "Ship authoritative compaction state" ||
		goal.Acceptance != "The persisted goal remains exact:\n- [ ] preserve acceptance line one\n- [ ] preserve acceptance line two" ||
		goal.Status != string(workmem.GoalStatusInProgress) {
		t.Fatalf("summary goal contract = %+v", goal)
	}
}

func TestCompactExitTwoEmitsHookErrorWithoutVeto(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "summary of old work"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, false)
	eng.Compaction = DefaultCompactionPolicy()
	eng.Compaction.KeepRecentTokens = 1
	runner, err := hooks.NewRunner(hooks.Config{Commands: []hooks.CommandHook{{
		Name:    "compact-guard",
		Events:  []hooks.EventName{hooks.EventPreCompact},
		Command: runtimeHookCommand("block"),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	installHookRunner(t, eng, runner)
	var hookError PolicyErroredPayload
	bus.Subscribe("policy.errored", func(event events.Event) {
		payload, _ := event.Payload.(PolicyErroredPayload)
		if payload.Name == "PreCompact/compact-guard" {
			hookError = payload
		}
	})
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
	if hookError.ExitCode != 2 || !strings.Contains(hookError.Error, "cannot block compaction") {
		t.Fatalf("hook error = %+v", hookError)
	}
}

func TestCompactPostHookStdoutQueuesRuntimeContextForNextProviderRequest(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "summary of old work"), StopReason: llm.StopEndTurn},
		{Message: llm.TextMessage(llm.RoleAssistant, "first"), StopReason: llm.StopEndTurn},
		{Message: llm.TextMessage(llm.RoleAssistant, "second"), StopReason: llm.StopEndTurn},
	}}
	eng, _ := newEngine(t, prov, false)
	eng.Compaction = DefaultCompactionPolicy()
	eng.Compaction.KeepRecentTokens = 1
	installHookRunner(t, eng, &fakeHookRunner{responses: map[hooks.EventName][]fakeHookResponse{
		hooks.EventPostCompact: {{Stdout: "Recheck the release branch on the next turn."}},
	}})
	if err := eng.Session.Append(llm.TextMessage(llm.RoleUser, strings.Repeat("old ", 80))); err != nil {
		t.Fatal(err)
	}
	if err := eng.Session.Append(llm.TextMessage(llm.RoleAssistant, strings.Repeat("reply ", 80))); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.Compact(context.Background(), "compact-turn", "system", "manual", false); err != nil {
		t.Fatal(err)
	}
	last := eng.Session.History[len(eng.Session.History)-1]
	if last.Kind != llm.MessageKindCompact {
		t.Fatalf("post-compact context persisted in history: %+v", last)
	}
	if _, err := eng.Turn(context.Background(), "first turn"); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Turn(context.Background(), "second turn"); err != nil {
		t.Fatal(err)
	}
	if got := messagesText(prov.histories[1]); !strings.Contains(got, "Recheck the release branch") {
		t.Fatalf("first provider request missing post-compact context:\n%s", got)
	}
	if got := messagesText(prov.histories[2]); strings.Contains(got, "Recheck the release branch") {
		t.Fatalf("post-compact context repeated in second provider request:\n%s", got)
	}
	for _, msg := range eng.Session.History {
		if msg.Kind == llm.MessageKindRuntimeContext {
			t.Fatalf("runtime context persisted in history: %+v", msg)
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
	eng, bus := newEngine(t, main, false)
	eng.SummaryProvider = summary
	eng.SummaryProvenance = provenance.SafeProvider{ID: "summary", Model: "summary:model", EndpointDigest: "summary-endpoint"}
	eng.Compaction = DefaultCompactionPolicy()
	eng.Compaction.KeepRecentTokens = 1
	if err := eng.Session.Append(llm.TextMessage(llm.RoleUser, strings.Repeat("old ", 80))); err != nil {
		t.Fatal(err)
	}
	if err := eng.Session.Append(llm.TextMessage(llm.RoleAssistant, strings.Repeat("reply ", 80))); err != nil {
		t.Fatal(err)
	}
	var epoch provenance.RequestEpoch
	bus.Subscribe(provenance.RequestEpochType, func(event events.Event) {
		epoch = event.Payload.(provenance.RequestEpochPayload).Epoch
	})

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
	if epoch.Provider.ID != "summary" || epoch.Provider.EndpointDigest != "summary-endpoint" {
		t.Fatalf("summary provider provenance = %+v", epoch.Provider)
	}
}

func TestCompactRequestEpochFailurePreventsSummaryProviderAndFallbackCalls(t *testing.T) {
	main := &namedCompactionProvider{name: "main:model", text: "main summary"}
	summary := &namedCompactionProvider{name: "summary:model", text: "custom summary"}
	eng, bus := newEngine(t, main, false)
	eng.SummaryProvider = summary
	eng.Compaction = DefaultCompactionPolicy()
	eng.Compaction.KeepRecentTokens = 1
	if err := eng.Session.Append(llm.TextMessage(llm.RoleUser, strings.Repeat("old ", 80))); err != nil {
		t.Fatal(err)
	}
	if err := eng.Session.Append(llm.TextMessage(llm.RoleAssistant, strings.Repeat("reply ", 80))); err != nil {
		t.Fatal(err)
	}
	want := errors.New("epoch sync failed")
	bus.SetCommitter(selectiveSessionCommitter{session: eng.Session, eventType: provenance.RequestEpochType, err: want})

	if _, err := eng.Compact(context.Background(), "compact-turn", "system", "manual", false); !errors.Is(err, want) {
		t.Fatalf("Compact() error = %v, want %v", err, want)
	}
	if summary.calls != 0 || main.calls != 0 {
		t.Fatalf("provider calls: summary=%d main=%d, want 0/0", summary.calls, main.calls)
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
	if err := eng.Session.Append(llm.TextMessage(llm.RoleUser, strings.Repeat("old ", 80))); err != nil {
		t.Fatal(err)
	}
	if err := eng.Session.Append(llm.TextMessage(llm.RoleAssistant, strings.Repeat("reply ", 80))); err != nil {
		t.Fatal(err)
	}
	var fallback ContextCompactSummaryFallbackPayload
	var retries int
	var epochs []provenance.RequestEpoch
	var summaryError ContextCompactSummaryErroredPayload
	bus.Subscribe(provenance.RequestEpochType, func(e events.Event) {
		epochs = append(epochs, e.Payload.(provenance.RequestEpochPayload).Epoch)
	})
	bus.Subscribe("context.compact.summary_errored", func(e events.Event) {
		summaryError = e.Payload.(ContextCompactSummaryErroredPayload)
	})
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
	if len(epochs) != 2 || epochs[0].Attempt != 1 || epochs[1].Attempt != 2 || epochs[0].Purpose != "compaction" || epochs[1].Purpose != "compaction" {
		t.Fatalf("fallback epochs = %+v", epochs)
	}
	if fallback.EpochID != epochs[0].EpochID || fallback.RequestDigest != epochs[0].RequestDigest {
		t.Fatalf("fallback link = %+v, failed epoch = %+v", fallback, epochs[0])
	}
	if summaryError.EpochID != epochs[0].EpochID || summaryError.RequestDigest != epochs[0].RequestDigest {
		t.Fatalf("summary error link = %+v, failed epoch = %+v", summaryError, epochs[0])
	}
}

func TestCompactFallbackEventFailureReleasesSelectedHalfOpenProbe(t *testing.T) {
	now := time.Unix(40_000, 0)
	health := llm.NewModelHealth(llm.ModelHealthOptions{Now: func() time.Time { return now }})
	backupOnly := []string{"backup:model"}
	backupFailure, ok := health.Acquire(backupOnly, nil)
	if !ok {
		t.Fatal("acquire backup health ticket")
	}
	health.Complete(backupFailure.Ticket, llm.ModelHealthEligibleFailure, "transient")
	now = now.Add(30 * time.Second)

	primary := &namedCompactionProvider{name: "primary:model", err: errors.New("status 503: primary unavailable")}
	backup := &namedCompactionProvider{name: "backup:model", text: "backup summary"}
	eng, bus := newEngine(t, primary, false)
	eng.ModelCandidates = []ModelCandidate{
		{Ref: "primary:model", Provider: primary},
		{Ref: "backup:model", Provider: backup},
	}
	eng.ModelHealth = health
	eng.Compaction = DefaultCompactionPolicy()
	eng.Compaction.KeepRecentTokens = 1
	if err := eng.Session.Append(llm.TextMessage(llm.RoleUser, strings.Repeat("old ", 80))); err != nil {
		t.Fatal(err)
	}
	want := errors.New("fallback event sync failed")
	bus.SetCommitter(selectiveFailCommitter{eventType: "context.compact.summary_model_fallback", err: want})

	if _, err := eng.Compact(context.Background(), "compact-turn", "system", "manual", false); !errors.Is(err, want) {
		t.Fatalf("Compact() error = %v, want %v", err, want)
	}
	if primary.calls != 1 || backup.calls != 0 {
		t.Fatalf("provider calls primary/backup = %d/%d, want 1/0", primary.calls, backup.calls)
	}
	retry, ok := health.Acquire(backupOnly, nil)
	if !ok || retry.Ticket.Ref != "backup:model" || !retry.Ticket.Probe {
		t.Fatalf("backup probe after journal failure = %+v, %v", retry, ok)
	}
	health.Complete(retry.Ticket, llm.ModelHealthNeutral, "")
}

func TestCompactFallsBackThroughConfiguredModelChainWithoutModelChangeNotice(t *testing.T) {
	summary := &namedCompactionProvider{name: "summary:model", err: errors.New("status 503: summary unavailable")}
	primary := &namedCompactionProvider{name: "primary:model", err: errors.New("status 401: token expired")}
	backup := &namedCompactionProvider{name: "backup:model", text: "backup summary"}
	eng, bus := newEngine(t, primary, false)
	eng.SummaryProvider = summary
	eng.SummaryProvenance = provenance.SafeProvider{ID: "summary", Model: "summary:model"}
	eng.ModelCandidates = []ModelCandidate{
		{Ref: "primary:model", Provider: primary},
		{Ref: "backup:model", Provider: backup},
	}
	eng.ModelHealth = llm.NewModelHealth(llm.ModelHealthOptions{})
	eng.NotifyModelChanges = true
	eng.Compaction = DefaultCompactionPolicy()
	eng.Compaction.SummaryModel = "summary:model"
	eng.Compaction.KeepRecentTokens = 1
	if err := eng.Session.Append(llm.TextMessage(llm.RoleUser, strings.Repeat("old ", 80))); err != nil {
		t.Fatal(err)
	}
	if err := eng.Session.Append(llm.TextMessage(llm.RoleAssistant, strings.Repeat("reply ", 80))); err != nil {
		t.Fatal(err)
	}

	var epochs []provenance.RequestEpoch
	var fallbacks []ContextCompactSummaryFallbackPayload
	bus.Subscribe(provenance.RequestEpochType, func(event events.Event) {
		epochs = append(epochs, event.Payload.(provenance.RequestEpochPayload).Epoch)
	})
	bus.Subscribe("context.compact.summary_model_fallback", func(event events.Event) {
		fallbacks = append(fallbacks, event.Payload.(ContextCompactSummaryFallbackPayload))
	})

	result, err := eng.Compact(context.Background(), "compact-turn", "system", "manual", false)
	if err != nil {
		t.Fatal(err)
	}
	if summary.calls != 1 || primary.calls != 1 || backup.calls != 1 {
		t.Fatalf("summary/primary/backup calls = %d/%d/%d, want 1/1/1", summary.calls, primary.calls, backup.calls)
	}
	if result.SummaryModel != "backup:model" {
		t.Fatalf("summary model = %q, want backup:model", result.SummaryModel)
	}
	if len(epochs) != 3 {
		t.Fatalf("epochs = %+v, want one per provider attempt", epochs)
	}
	for i, wantModel := range []string{"summary:model", "primary:model", "backup:model"} {
		if epochs[i].Attempt != i+1 || epochs[i].Purpose != "compaction" || epochs[i].Provider.Model != wantModel {
			t.Fatalf("epoch[%d] = %+v, want attempt %d model %s", i, epochs[i], i+1, wantModel)
		}
	}
	if len(fallbacks) != 2 || fallbacks[0].ConfiguredModel != "summary:model" || fallbacks[0].FallbackModel != "primary:model" || fallbacks[1].ConfiguredModel != "primary:model" || fallbacks[1].FallbackModel != "backup:model" {
		t.Fatalf("fallbacks = %+v", fallbacks)
	}
	for _, message := range eng.Session.History {
		if message.Kind == llm.MessageKindModelChange {
			t.Fatalf("compaction persisted model-change notice: %+v", message)
		}
	}
}

func TestCompactUsesConfiguredFallbackModelsWithoutDedicatedSummaryModel(t *testing.T) {
	primary := &namedCompactionProvider{name: "primary:model", err: errors.New("status 503: primary unavailable")}
	backup := &namedCompactionProvider{name: "backup:model", text: "backup summary"}
	eng, bus := newEngine(t, primary, false)
	eng.ModelCandidates = []ModelCandidate{
		{Ref: "primary:model", Provider: primary},
		{Ref: "backup:model", Provider: backup},
	}
	eng.Compaction = DefaultCompactionPolicy()
	eng.Compaction.KeepRecentTokens = 1
	if err := eng.Session.Append(llm.TextMessage(llm.RoleUser, strings.Repeat("old ", 80))); err != nil {
		t.Fatal(err)
	}
	if err := eng.Session.Append(llm.TextMessage(llm.RoleAssistant, strings.Repeat("reply ", 80))); err != nil {
		t.Fatal(err)
	}

	var fallback ContextCompactSummaryFallbackPayload
	bus.Subscribe("context.compact.summary_model_fallback", func(event events.Event) {
		fallback = event.Payload.(ContextCompactSummaryFallbackPayload)
	})

	result, err := eng.Compact(context.Background(), "compact-turn", "system", "manual", false)
	if err != nil {
		t.Fatal(err)
	}
	if primary.calls != 1 || backup.calls != 1 || result.SummaryModel != "backup:model" {
		t.Fatalf("primary/backup/result = %d/%d/%+v", primary.calls, backup.calls, result)
	}
	if fallback.ConfiguredModel != "primary:model" || fallback.FallbackModel != "backup:model" {
		t.Fatalf("fallback = %+v", fallback)
	}
}

func TestCompactRefitsSummaryRequestForFallbackContextWindow(t *testing.T) {
	primary := &namedCompactionProvider{name: "primary:model", err: errors.New("status 503: primary unavailable")}
	backup := &limitedCompactionProvider{name: "backup:model", maxInputTokens: 7500}
	eng, bus := newEngine(t, primary, false)
	eng.ContextWindow = 256000
	eng.ModelCandidates = []ModelCandidate{
		{Ref: "primary:model", Provider: primary, ContextWindow: 256000},
		{Ref: "backup:model", Provider: backup, ContextWindow: 10000},
	}
	eng.Compaction = DefaultCompactionPolicy()
	eng.Compaction.KeepRecentTokens = 1
	for i := 0; i < 100; i++ {
		message := llm.TextMessage(llm.RoleUser, fmt.Sprintf("message-%03d %s", i, strings.Repeat("x", 1000)))
		if err := eng.Session.Append(message); err != nil {
			t.Fatal(err)
		}
	}

	var epochs []provenance.RequestEpoch
	bus.Subscribe(provenance.RequestEpochType, func(event events.Event) {
		epochs = append(epochs, event.Payload.(provenance.RequestEpochPayload).Epoch)
	})

	result, err := eng.Compact(context.Background(), "compact-turn", "system", "manual", false)
	if err != nil {
		t.Fatal(err)
	}
	if result.SummaryModel != "backup:model" || backup.inputTokens > backup.maxInputTokens {
		t.Fatalf("result/input tokens = %+v/%d, want backup summary within %d", result, backup.inputTokens, backup.maxInputTokens)
	}
	if len(epochs) != 2 || epochs[1].Provider.Model != "backup:model" || epochs[1].ContextWindow != 10000 {
		t.Fatalf("epochs = %+v, want fallback context window 10000", epochs)
	}
}

func TestCompactClampsSummaryOutputForFallbackContextWindow(t *testing.T) {
	primary := &namedCompactionProvider{name: "primary:model", err: errors.New("status 503: primary unavailable")}
	backup := &scriptedCompactionProvider{
		name: "backup:model",
		attempts: []scriptedCompactionAttempt{{response: llm.Response{
			Message:    llm.TextMessage(llm.RoleAssistant, "bounded fallback summary"),
			StopReason: llm.StopEndTurn,
		}}},
	}
	eng, bus := newEngine(t, primary, false)
	eng.ContextWindow = 256000
	eng.ModelCandidates = []ModelCandidate{
		{Ref: "primary:model", Provider: primary, ContextWindow: 256000},
		{Ref: "backup:model", Provider: backup, ContextWindow: 2000},
	}
	eng.Compaction = DefaultCompactionPolicy()
	eng.Compaction.KeepRecentTokens = 1
	eng.Compaction.SummaryMaxTokens = 2048
	for i := 0; i < 20; i++ {
		if err := eng.Session.Append(llm.TextMessage(llm.RoleUser, fmt.Sprintf("message-%02d %s", i, strings.Repeat("x", 500)))); err != nil {
			t.Fatal(err)
		}
	}
	var epochs []provenance.RequestEpoch
	bus.Subscribe(provenance.RequestEpochType, func(event events.Event) {
		epochs = append(epochs, event.Payload.(provenance.RequestEpochPayload).Epoch)
	})

	result, err := eng.Compact(context.Background(), "compact-turn", "system", "manual", false)
	if err != nil {
		t.Fatal(err)
	}
	if result.SummaryModel != "backup:model" || len(backup.options) != 1 || len(backup.histories) != 1 {
		t.Fatalf("result/options/histories = %+v/%+v/%d", result, backup.options, len(backup.histories))
	}
	policy := effectiveCompactionPolicy(eng.Compaction, 2000)
	inputTokens := estimateContextTokens(backup.systems[0], nil, backup.histories[0])
	if got := backup.options[0].MaxOutputTokens; got <= 0 || got >= eng.Compaction.SummaryMaxTokens || inputTokens+got > policy.TriggerTokens {
		t.Fatalf("fallback request/output = %d/%d tokens, want positive clamped total <= trigger %d", inputTokens, got, policy.TriggerTokens)
	}
	if len(epochs) != 2 || epochs[1].Provider.Model != "backup:model" || epochs[1].ContextWindow != 2000 || epochs[1].MaxOutputTokens != backup.options[0].MaxOutputTokens {
		t.Fatalf("epochs = %+v, want clamped fallback provenance", epochs)
	}
}

func TestCompactSkipsModelAlreadyInSharedHealthCooldown(t *testing.T) {
	health := llm.NewModelHealth(llm.ModelHealthOptions{})
	primaryFailure, ok := health.Acquire([]string{"primary:model"}, nil)
	if !ok {
		t.Fatal("acquire primary health ticket")
	}
	health.Complete(primaryFailure.Ticket, llm.ModelHealthEligibleFailure, "transient")

	primary := &namedCompactionProvider{name: "primary:model", text: "unexpected primary summary"}
	backup := &namedCompactionProvider{name: "backup:model", text: "backup summary"}
	eng, bus := newEngine(t, primary, false)
	eng.ModelCandidates = []ModelCandidate{
		{Ref: "primary:model", Provider: primary},
		{Ref: "backup:model", Provider: backup},
	}
	eng.ModelHealth = health
	eng.Compaction = DefaultCompactionPolicy()
	eng.Compaction.KeepRecentTokens = 1
	if err := eng.Session.Append(llm.TextMessage(llm.RoleUser, strings.Repeat("old ", 80))); err != nil {
		t.Fatal(err)
	}
	var epochs []provenance.RequestEpoch
	bus.Subscribe(provenance.RequestEpochType, func(event events.Event) {
		epochs = append(epochs, event.Payload.(provenance.RequestEpochPayload).Epoch)
	})

	result, err := eng.Compact(context.Background(), "compact-turn", "system", "manual", false)
	if err != nil {
		t.Fatal(err)
	}
	if primary.calls != 0 || backup.calls != 1 || result.SummaryModel != "backup:model" {
		t.Fatalf("primary/backup/result = %d/%d/%+v, want cooldown skip to backup", primary.calls, backup.calls, result)
	}
	if len(epochs) != 1 || epochs[0].Provider.Model != "backup:model" {
		t.Fatalf("epochs = %+v, want only attempted backup", epochs)
	}
}

func TestCompactReportsHealthSkipsWhenNoCandidateCanBeAcquired(t *testing.T) {
	health := llm.NewModelHealth(llm.ModelHealthOptions{})
	for _, ref := range []string{"primary:model", "backup:model"} {
		selection, ok := health.Acquire([]string{ref}, nil)
		if !ok {
			t.Fatalf("acquire %s health ticket", ref)
		}
		health.Complete(selection.Ticket, llm.ModelHealthEligibleFailure, "transient")
	}

	primary := &namedCompactionProvider{name: "primary:model", text: "unexpected primary summary"}
	backup := &namedCompactionProvider{name: "backup:model", text: "unexpected backup summary"}
	eng, bus := newEngine(t, primary, false)
	eng.ModelCandidates = []ModelCandidate{
		{Ref: "primary:model", Provider: primary},
		{Ref: "backup:model", Provider: backup},
	}
	eng.ModelHealth = health
	eng.Compaction = DefaultCompactionPolicy()
	eng.Compaction.KeepRecentTokens = 1
	if err := eng.Session.Append(llm.TextMessage(llm.RoleUser, strings.Repeat("old ", 80))); err != nil {
		t.Fatal(err)
	}
	var fallbacks []LLMFallbackPayload
	bus.Subscribe("llm.fallback", func(event events.Event) {
		fallbacks = append(fallbacks, event.Payload.(LLMFallbackPayload))
	})

	_, err := eng.Compact(context.Background(), "compact-turn", "system", "manual", false)
	if err == nil || !strings.Contains(err.Error(), "primary:model: unavailable") || !strings.Contains(err.Error(), "backup:model: unavailable") {
		t.Fatalf("Compact error = %v, want exhausted cooldown candidates", err)
	}
	if primary.calls != 0 || backup.calls != 0 {
		t.Fatalf("primary/backup calls = %d/%d, want no provider calls", primary.calls, backup.calls)
	}
	want := []LLMFallbackPayload{
		{From: "primary:model", Reason: "transient"},
		{From: "backup:model", Reason: "transient"},
	}
	if len(fallbacks) != len(want) {
		t.Fatalf("fallbacks = %+v, want %+v", fallbacks, want)
	}
	for index := range want {
		if fallbacks[index].From != want[index].From || fallbacks[index].To != "" || fallbacks[index].Reason != want[index].Reason || fallbacks[index].CooldownMS <= 0 {
			t.Fatalf("fallback[%d] = %+v, want terminal health skip for %s", index, fallbacks[index], want[index].From)
		}
	}
}

func TestCompactReportsRemainingHealthSkipsAfterAttemptFailure(t *testing.T) {
	health := llm.NewModelHealth(llm.ModelHealthOptions{})
	backupSelection, ok := health.Acquire([]string{"backup:model"}, nil)
	if !ok {
		t.Fatal("acquire backup health ticket")
	}
	health.Complete(backupSelection.Ticket, llm.ModelHealthEligibleFailure, "transient")

	primary := &namedCompactionProvider{name: "primary:model", err: errors.New("status 503: primary unavailable")}
	backup := &namedCompactionProvider{name: "backup:model", text: "unexpected backup summary"}
	eng, bus := newEngine(t, primary, false)
	eng.ModelCandidates = []ModelCandidate{
		{Ref: "primary:model", Provider: primary},
		{Ref: "backup:model", Provider: backup},
	}
	eng.ModelHealth = health
	eng.Compaction = DefaultCompactionPolicy()
	eng.Compaction.KeepRecentTokens = 1
	if err := eng.Session.Append(llm.TextMessage(llm.RoleUser, strings.Repeat("old ", 80))); err != nil {
		t.Fatal(err)
	}
	var fallbacks []LLMFallbackPayload
	bus.Subscribe("llm.fallback", func(event events.Event) {
		fallbacks = append(fallbacks, event.Payload.(LLMFallbackPayload))
	})

	_, err := eng.Compact(context.Background(), "compact-turn", "system", "manual", false)
	if err == nil || !strings.Contains(err.Error(), "primary:model: status 503") || !strings.Contains(err.Error(), "backup:model: unavailable") {
		t.Fatalf("Compact error = %v, want attempt failure plus cooldown skip", err)
	}
	if primary.calls != 1 || backup.calls != 0 {
		t.Fatalf("primary/backup calls = %d/%d, want 1/0", primary.calls, backup.calls)
	}
	if len(fallbacks) != 1 || fallbacks[0].From != "backup:model" || fallbacks[0].To != "" || fallbacks[0].Reason != "transient" || fallbacks[0].CooldownMS <= 0 {
		t.Fatalf("fallbacks = %+v, want terminal backup health skip", fallbacks)
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

func TestCompactRetriesFirstIncompleteFallbackWithLargerBudget(t *testing.T) {
	primary := &namedCompactionProvider{name: "primary:model", err: errors.New("status 503: primary unavailable")}
	backup := &scriptedCompactionProvider{
		name: "backup:model",
		attempts: []scriptedCompactionAttempt{
			{response: llm.Response{
				Message:    llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockReasoning, Text: "spent the first fallback budget"}}},
				StopReason: llm.StopMaxTokens,
				Usage:      llm.Usage{InputTokens: 10, OutputTokens: 2},
			}},
			{response: llm.Response{
				Message:    llm.TextMessage(llm.RoleAssistant, "recovered fallback summary"),
				StopReason: llm.StopEndTurn,
				Usage:      llm.Usage{InputTokens: 11, OutputTokens: 3},
			}},
		},
	}
	eng, bus := newEngine(t, primary, false)
	eng.ModelCandidates = []ModelCandidate{
		{Ref: "primary:model", Provider: primary, ContextWindow: 5000},
		{Ref: "backup:model", Provider: backup, ContextWindow: 5000},
	}
	configureCompactionRetryTest(t, eng, 30, 2000)
	eng.ContextWindow = 5000
	eng.Compaction.ReserveTokens = 1000
	eng.Compaction.SummaryMaxTokens = 1000
	var retry ContextCompactSummaryRetryPayload
	var epochs []provenance.RequestEpoch
	bus.Subscribe("context.compact.summary_retry", func(event events.Event) {
		retry = event.Payload.(ContextCompactSummaryRetryPayload)
	})
	bus.Subscribe(provenance.RequestEpochType, func(event events.Event) {
		epochs = append(epochs, event.Payload.(provenance.RequestEpochPayload).Epoch)
	})

	result, err := eng.Compact(context.Background(), "compact-turn", "system", "manual", false)
	if err != nil {
		t.Fatal(err)
	}
	if result.SummaryModel != "backup:model" || primary.calls != 1 || backup.calls != 2 {
		t.Fatalf("result/primary/backup = %+v/%d/%d, want recovered backup after retry", result, primary.calls, backup.calls)
	}
	if len(backup.options) != 2 || backup.options[0].MaxOutputTokens != 1000 || backup.options[1].MaxOutputTokens != 2000 {
		t.Fatalf("fallback max output tokens = %+v, want [1000 2000]", compactionOptionBudgets(backup.options))
	}
	if len(epochs) != 3 || retry.EpochID != epochs[1].EpochID || retry.RequestDigest != epochs[1].RequestDigest {
		t.Fatalf("epochs/retry = %+v/%+v, want retry linked to first fallback attempt", epochs, retry)
	}
	if retry.Attempt != 2 || retry.Reason != "empty_summary" || !retry.ReasoningOnly {
		t.Fatalf("retry payload = %+v", retry)
	}
	if usage := eng.Session.TokenUsageSnapshot(); usage != (llm.Usage{InputTokens: 21, OutputTokens: 5}) {
		t.Fatalf("token usage = %+v, want aggregate fallback retry usage", usage)
	}
}

func TestCompactCheckpointsEachSummaryAttemptAndLinksOutcomes(t *testing.T) {
	provider := &scriptedCompactionProvider{
		name: "thinking:model",
		attempts: []scriptedCompactionAttempt{
			{response: llm.Response{
				Message:    llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockReasoning, Text: "first budget"}}},
				StopReason: llm.StopMaxTokens,
				Usage:      llm.Usage{InputTokens: 10, OutputTokens: 2},
			}},
			{response: llm.Response{
				Message:    llm.TextMessage(llm.RoleAssistant, "summary"),
				StopReason: llm.StopEndTurn,
				Usage:      llm.Usage{InputTokens: 11, OutputTokens: 3},
			}},
		},
	}
	eng, bus := newEngine(t, provider, false)
	configureCompactionRetryTest(t, eng, 30, 2000)
	eng.ContextWindow = 5000
	eng.Compaction.ReserveTokens = 1000
	eng.Compaction.SummaryMaxTokens = 1000
	var epochs []provenance.RequestEpoch
	var outcomes []ContextCompactSummaryRespondedPayload
	var retry ContextCompactSummaryRetryPayload
	bus.Subscribe(provenance.RequestEpochType, func(event events.Event) {
		epochs = append(epochs, event.Payload.(provenance.RequestEpochPayload).Epoch)
	})
	bus.Subscribe("context.compact.summary_responded", func(event events.Event) {
		outcomes = append(outcomes, event.Payload.(ContextCompactSummaryRespondedPayload))
	})
	bus.Subscribe("context.compact.summary_retry", func(event events.Event) {
		retry = event.Payload.(ContextCompactSummaryRetryPayload)
	})

	if _, err := eng.Compact(context.Background(), "compact-turn", "system", "manual", false); err != nil {
		t.Fatal(err)
	}
	if len(epochs) != 2 || len(outcomes) != 2 {
		t.Fatalf("epochs/outcomes = %d/%d, want 2/2", len(epochs), len(outcomes))
	}
	for index, epoch := range epochs {
		if epoch.Purpose != "compaction" || epoch.Attempt != index+1 || epoch.EpochID == "" || epoch.RequestDigest == "" {
			t.Fatalf("epoch %d = %+v", index, epoch)
		}
		if epoch.CachePolicy.StablePrefixKeyDigest == "" || len(epoch.HistoryMessageIDs) != 1 || epoch.HistoryMessageIDs[0] == "" {
			t.Fatalf("epoch %d cache/history = %+v/%v", index, epoch.CachePolicy, epoch.HistoryMessageIDs)
		}
		if outcomes[index].EpochID != epoch.EpochID || outcomes[index].RequestDigest != epoch.RequestDigest || outcomes[index].Attempt != index+1 {
			t.Fatalf("outcome %d = %+v, epoch = %+v", index, outcomes[index], epoch)
		}
		if len(epoch.Messages) != 1 || epoch.Messages[0].Source != "compaction_input" || epoch.Messages[0].Snapshot == nil {
			t.Fatalf("epoch %d synthesized summary history = %+v", index, epoch.Messages)
		}
		var reconstructed llm.Message
		if err := json.Unmarshal(epoch.Messages[0].Snapshot.Content, &reconstructed); err != nil {
			t.Fatal(err)
		}
		if reconstructed.FirstText() != provider.histories[index][0].FirstText() {
			t.Fatalf("epoch %d summary history body was not reconstructable", index)
		}
	}
	if epochs[0].EpochID == epochs[1].EpochID || epochs[0].RequestDigest == epochs[1].RequestDigest {
		t.Fatalf("semantic retry reused epoch/digest: %+v", epochs)
	}
	if retry.EpochID != epochs[0].EpochID || retry.RequestDigest != epochs[0].RequestDigest {
		t.Fatalf("retry link = %+v, first epoch = %+v", retry, epochs[0])
	}
}

func TestCompactRetainsProviderUsageWhenSummaryOutcomeCommitFails(t *testing.T) {
	provider := &scriptedCompactionProvider{
		name: "thinking:model",
		attempts: []scriptedCompactionAttempt{{response: llm.Response{
			Message:    llm.TextMessage(llm.RoleAssistant, "summary"),
			StopReason: llm.StopEndTurn,
			Usage:      llm.Usage{InputTokens: 13, OutputTokens: 5},
		}}},
	}
	eng, bus := newEngine(t, provider, false)
	configureCompactionRetryTest(t, eng, 30, 2000)
	want := errors.New("summary outcome sync failed")
	bus.SetCommitter(selectiveSessionCommitter{session: eng.Session, eventType: "context.compact.summary_responded", err: want})

	if _, err := eng.Compact(context.Background(), "compact-turn", "system", "manual", false); !errors.Is(err, want) {
		t.Fatalf("Compact() error = %v, want %v", err, want)
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.calls)
	}
	if usage := eng.Session.TokenUsageSnapshot(); usage != (llm.Usage{InputTokens: 13, OutputTokens: 5}) {
		t.Fatalf("token usage = %+v, want dispatched provider usage", usage)
	}
}

func TestCompactRetriesPartialSummaryStoppedAtMaxTokens(t *testing.T) {
	provider := &scriptedCompactionProvider{
		name: "thinking:model",
		attempts: []scriptedCompactionAttempt{
			{
				response: llm.Response{
					Message:    llm.TextMessage(llm.RoleAssistant, "Goal\n- partial summary"),
					StopReason: llm.StopMaxTokens,
					Usage:      llm.Usage{InputTokens: 10, OutputTokens: 1000},
				},
			},
			{
				response: llm.Response{
					Message:    llm.TextMessage(llm.RoleAssistant, "complete summary"),
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
	if result.SummaryChars != len("complete summary") || provider.calls != 2 {
		t.Fatalf("result/calls = %+v, %d", result, provider.calls)
	}
	if retry.Reason != "max_tokens" || retry.StopReason != llm.StopMaxTokens || retry.ReasoningOnly {
		t.Fatalf("retry payload = %+v", retry)
	}
	if len(provider.options) != 2 || provider.options[0].MaxOutputTokens != 1000 || provider.options[1].MaxOutputTokens != 2000 {
		t.Fatalf("max output tokens = %+v, want [1000 2000]", compactionOptionBudgets(provider.options))
	}
	if usage := eng.Session.TokenUsageSnapshot(); usage != (llm.Usage{InputTokens: 21, OutputTokens: 1003}) {
		t.Fatalf("token usage = %+v, want aggregate retry usage", usage)
	}
}

func TestCompactSummaryRetryReusesAuthoritativeStateSnapshot(t *testing.T) {
	var eng *Engine
	var goalState *workmem.GoalStateStore
	var notesStore *workmem.NotesStore
	provider := &scriptedCompactionProvider{
		name: "thinking:model",
		attempts: []scriptedCompactionAttempt{
			{
				response: llm.Response{Message: llm.Message{Role: llm.RoleAssistant}},
				beforeReturn: func() {
					updated := "Mutated after the first summary request"
					if _, err := goalState.Update(workmem.GoalStateUpdate{Description: &updated}); err != nil {
						t.Fatal(err)
					}
					if _, err := notesStore.Update("- [ ] mutated after first request"); err != nil {
						t.Fatal(err)
					}
				},
			},
			{response: llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "recovered summary")}},
		},
	}
	var bus *events.Bus
	eng, bus = newEngine(t, provider, false)
	configureCompactionRetryTest(t, eng, 30, 2000)
	eng.ContextWindow = 5000
	eng.Compaction.ReserveTokens = 1000
	eng.Compaction.SummaryMaxTokens = 1000
	goalState = workmem.NewGoalStateStore(eng.Session.Dir, workmem.GoalStateOptions{})
	if _, err := goalState.Create("Original compact goal", "Original acceptance"); err != nil {
		t.Fatal(err)
	}
	notesStore = workmem.NewNotesStore(eng.Session.Dir)
	if _, err := notesStore.Update("- [ ] original compact note"); err != nil {
		t.Fatal(err)
	}
	installSessionStateModulesWithStores(t, eng, goalState, notesStore)
	var epochs []provenance.RequestEpoch
	bus.Subscribe(provenance.RequestEpochType, func(event events.Event) {
		epochs = append(epochs, event.Payload.(provenance.RequestEpochPayload).Epoch)
	})

	if _, err := eng.Compact(context.Background(), "compact-turn", "system", "manual", false); err != nil {
		t.Fatal(err)
	}
	if len(provider.histories) != 2 {
		t.Fatalf("summary requests = %d, want 2", len(provider.histories))
	}
	if len(epochs) != len(provider.histories) {
		t.Fatalf("request epochs = %d, want %d", len(epochs), len(provider.histories))
	}
	for i, history := range provider.histories {
		body := history[0].FirstText()
		for _, want := range []string{"Original compact goal", "Original acceptance", "original compact note"} {
			if !strings.Contains(body, want) {
				t.Fatalf("request %d missing snapshot value %q:\n%s", i+1, want, body)
			}
		}
		if strings.Contains(body, "Mutated after") || strings.Contains(body, "mutated after") {
			t.Fatalf("request %d used state mutated during retry:\n%s", i+1, body)
		}
		if len(epochs[i].Messages) != 1 || epochs[i].Messages[0].Snapshot == nil {
			t.Fatalf("request %d epoch message = %+v", i+1, epochs[i].Messages)
		}
		var reconstructed llm.Message
		if err := json.Unmarshal(epochs[i].Messages[0].Snapshot.Content, &reconstructed); err != nil {
			t.Fatal(err)
		}
		if reconstructed.FirstText() != body {
			t.Fatalf("request %d epoch did not preserve authoritative summary state", i+1)
		}
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
	policy := effectiveCompactionPolicy(eng.Compaction, eng.ContextWindow)
	initialBudget := provider.options[0].MaxOutputTokens
	initialInputTokens := estimateContextTokens(provider.systems[0], nil, provider.histories[0])
	if initialBudget >= eng.Compaction.SummaryMaxTokens || initialInputTokens+initialBudget > policy.TriggerTokens {
		t.Fatalf("initial input + output = %d + %d, want clamped below configured %d and total <= trigger %d", initialInputTokens, initialBudget, eng.Compaction.SummaryMaxTokens, policy.TriggerTokens)
	}
	retryBudget := provider.options[1].MaxOutputTokens
	if retryBudget >= 1200 {
		t.Fatalf("retry budget = %d, want less than uncapped double 1200", retryBudget)
	}
	retryInputTokens := estimateContextTokens(provider.systems[1], nil, provider.histories[1])
	if retryInputTokens+retryBudget > policy.TriggerTokens {
		t.Fatalf("retry input + output = %d + %d, want <= trigger %d", retryInputTokens, retryBudget, policy.TriggerTokens)
	}
	if retry.PreviousMaxOutputTokens != initialBudget || retry.MaxOutputTokens != retryBudget {
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

func TestCompactDoesNotFallbackAfterContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	main := &scriptedCompactionProvider{name: "main:model"}
	summary := &scriptedCompactionProvider{
		name: "summary:model",
		attempts: []scriptedCompactionAttempt{{
			beforeReturn: cancel,
			err:          context.Canceled,
		}},
	}
	eng, bus := newEngine(t, main, false)
	eng.SummaryProvider = summary
	configureCompactionRetryTest(t, eng, 2, 200)
	eng.Compaction.SummaryModel = "summary:model"
	var fallbacks int
	bus.Subscribe("context.compact.summary_model_fallback", func(events.Event) {
		fallbacks++
	})

	_, err := eng.Compact(ctx, "compact-turn", "system", "manual", false)
	if !errors.Is(err, cancellation.ErrUserCancelled) || err.Error() != compactionCanceledMessage {
		t.Fatalf("error = %v, want compaction cancellation", err)
	}
	if summary.calls != 1 || main.calls != 0 || fallbacks != 0 {
		t.Fatalf("summary/main/fallbacks = %d/%d/%d, want 1/0/0", summary.calls, main.calls, fallbacks)
	}
}

func TestCompactPostHookFailuresAreObservational(t *testing.T) {
	cases := []struct {
		name              string
		runner            HookRunner
		wantQueuedContext string
	}{
		{
			name: "error",
			runner: &fakeHookRunner{
				responses: map[hooks.EventName][]fakeHookResponse{hooks.EventPreCompact: {{}}},
				errors:    map[hooks.EventName]error{hooks.EventPostCompact: errors.New("audit sink unavailable")},
			},
		},
		{
			name: "deny",
			runner: &fakeHookRunner{
				responses: map[hooks.EventName][]fakeHookResponse{
					hooks.EventPreCompact:  {{}},
					hooks.EventPostCompact: {{ExitCode: 2, Stdout: "audit failed"}},
				},
			},
		},
		{
			name: "partial success before error",
			runner: hookRunnerFunc(func(_ context.Context, req hooks.Request) ([]hooks.Result, error) {
				if req.EventName != hooks.EventPostCompact {
					return nil, nil
				}
				return []hooks.Result{{
					Hook:      hooks.CommandHook{Name: "audit", Events: []hooks.EventName{req.EventName}},
					EventName: req.EventName,
					ExitCode:  0,
					Stdout:    "retain this context",
				}}, errors.New("later audit hook unavailable")
			}),
			wantQueuedContext: "retain this context",
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
			installHookRunner(t, eng, tc.runner)
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
			if sawErrored {
				t.Fatalf("events = %+v, committed compaction must not emit compact error", eventTypes)
			}
			if !sawCompleted {
				t.Fatalf("events = %+v, want completed after committed compaction", eventTypes)
			}
			queued := eng.pendingPolicyRuntimeContextSnapshot()
			if tc.wantQueuedContext == "" {
				if len(queued) != 0 {
					t.Fatalf("queued context = %+v, want none", queued)
				}
			} else if len(queued) != 1 || !strings.Contains(queued[0].FirstText(), tc.wantQueuedContext) {
				t.Fatalf("queued context = %+v, want %q", queued, tc.wantQueuedContext)
			}
		})
	}
}

func TestCompactPreservesCommittedResultWhenPostPolicyContextCannotBeQueued(t *testing.T) {
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
	installHookRunner(t, eng, &fakeHookRunner{responses: map[hooks.EventName][]fakeHookResponse{
		hooks.EventPreCompact:  {{}},
		hooks.EventPostCompact: {{Stdout: strings.Repeat("x", provenance.MaxPolicyContextBatchBytes)}},
	}})
	if err := eng.Session.Append(llm.TextMessage(llm.RoleUser, strings.Repeat("old ", 80))); err != nil {
		t.Fatal(err)
	}
	if err := eng.Session.Append(llm.TextMessage(llm.RoleAssistant, strings.Repeat("reply ", 80))); err != nil {
		t.Fatal(err)
	}

	result, err := eng.Compact(context.Background(), "compact-turn", "system", "manual", false)
	if err == nil || !strings.Contains(err.Error(), "queued policy context exceeds") {
		t.Fatalf("error = %v, want policy context queue size failure", err)
	}
	if result.MessageID == "" {
		t.Fatalf("result = %+v, want committed compaction result", result)
	}
	if !slices.Contains(eventTypes, "context.compact.completed") {
		t.Fatalf("events = %+v, want completed event", eventTypes)
	}
	if slices.Contains(eventTypes, "context.compact.errored") {
		t.Fatalf("events = %+v, committed compaction must not emit compact error", eventTypes)
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

type limitedCompactionProvider struct {
	name           string
	maxInputTokens int
	inputTokens    int
}

func (p *limitedCompactionProvider) Name() string { return p.name }

func (p *limitedCompactionProvider) Complete(ctx context.Context, sys string, history []llm.Message, tools []llm.ToolSpec) (llm.Response, error) {
	return p.CompleteWithOptions(ctx, sys, history, tools, llm.CompleteOptions{})
}

func (p *limitedCompactionProvider) CompleteWithOptions(ctx context.Context, sys string, history []llm.Message, tools []llm.ToolSpec, opts llm.CompleteOptions) (llm.Response, error) {
	p.inputTokens = estimateContextTokens(sys, nil, history)
	if p.inputTokens > p.maxInputTokens {
		return llm.Response{}, fmt.Errorf("context_length_exceeded: compaction request has %d tokens", p.inputTokens)
	}
	return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "bounded fallback summary"), StopReason: llm.StopEndTurn}, nil
}

type scriptedCompactionAttempt struct {
	response     llm.Response
	err          error
	beforeReturn func()
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
	if attempt.beforeReturn != nil {
		attempt.beforeReturn()
	}
	return attempt.response, attempt.err
}

func configureCompactionRetryTest(t *testing.T, eng *Engine, messageCount, messageChars int) {
	t.Helper()
	eng.Compaction = DefaultCompactionPolicy()
	eng.Compaction.KeepRecentTokens = 1
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

func TestTurn_GuidedToolErrorAddsRecoveryHintAfterDiagnosticEvent(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "guided-1", ToolName: "guided_test"},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "recovered"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, false)
	eng.Tools.MustRegister(tools.Tool{
		Name:  "guided_test",
		Group: tools.ToolGroupObservable,
		Handler: func(context.Context, map[string]any) (string, error) {
			return "partial output", errors.New("guided failure")
		},
	})
	var errored toolevents.ErroredPayload
	bus.Subscribe(toolevents.ErroredType, func(event events.Event) {
		errored, _ = event.Payload.(toolevents.ErroredPayload)
	})

	out, err := eng.Turn(context.Background(), "try guided tool")
	if err != nil {
		t.Fatal(err)
	}
	if out != "recovered" {
		t.Fatalf("out = %q, want recovered", out)
	}
	const hint = `skill_load("juex-observables")`
	result := eng.Session.History[2].Blocks[0]
	if !result.IsError || !strings.Contains(result.Content, hint) {
		t.Fatalf("persisted tool result = %+v, want guided failure hint", result)
	}
	if !strings.Contains(messagesText(prov.histories[1]), hint) {
		t.Fatalf("next provider history missing guided failure hint: %+v", prov.histories[1])
	}
	if errored.Error != "guided failure" || strings.Contains(errored.Preview, "skill_load") {
		t.Fatalf("tool.errored = %+v, want original diagnostic without remediation", errored)
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
		ArtifactPath:  "read-media/test.png",
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
	installHookRunner(t, eng, &fakeHookRunner{responses: map[hooks.EventName][]fakeHookResponse{
		hooks.EventPostToolUse: {{ExitCode: 2, Stdout: "redaction required"}},
	}})

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
	if block.IsError || !strings.Contains(block.Content, "redaction required") {
		t.Fatalf("tool result block = %+v, want corrective context without error", block)
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
	installHookRunner(t, eng, &fakeHookRunner{responses: map[hooks.EventName][]fakeHookResponse{
		hooks.EventUserPromptSubmit: {{Stdout: "ticket: ABC-123"}},
	}})

	out, err := eng.Turn(context.Background(), "summarize")
	if err != nil {
		t.Fatal(err)
	}
	if out != "answer" {
		t.Fatalf("out = %q", out)
	}
	if got := messagesText(prov.histories[0]); !strings.Contains(got, "ticket: ABC-123") {
		t.Fatalf("provider history missing policy context:\n%s", got)
	}
	first := eng.Session.History[0]
	if len(first.Blocks) != 2 || !strings.Contains(first.Blocks[1].Text, "ticket: ABC-123") {
		t.Fatalf("session user message missing policy context: %+v", first)
	}
}

func TestTurn_UserPromptSubmitHookDenyStopsBeforeProvider(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "should not run"), StopReason: llm.StopEndTurn},
	}}
	eng, _ := newEngine(t, prov, false)
	installHookRunner(t, eng, &fakeHookRunner{responses: map[hooks.EventName][]fakeHookResponse{
		hooks.EventUserPromptSubmit: {{ExitCode: 2, Stdout: "missing approval"}},
	}})

	_, err := eng.Turn(context.Background(), "summarize")
	if err == nil || !strings.Contains(err.Error(), `runtime module "hooks" turn input rejected: missing approval`) {
		t.Fatalf("err = %v", err)
	}
	if len(prov.histories) != 0 {
		t.Fatalf("provider should not be called, calls = %d", len(prov.histories))
	}
	if len(eng.Session.History) != 1 || eng.Session.History[0].FirstText() != "summarize" {
		t.Fatalf("accepted input was not preserved after policy rejection: %+v", eng.Session.History)
	}
	if !eng.Session.History[0].PolicyBlocked {
		t.Fatalf("rejected input = %+v, want policy_blocked", eng.Session.History[0])
	}
	for _, message := range eng.ActiveContext().Messages {
		if message.FirstText() == "summarize" {
			t.Fatalf("policy-rejected input remained provider-visible: %+v", eng.ActiveContext().Messages)
		}
	}
}

func TestTurn_PreToolUseStdoutAddsToolResultContext(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "tu1", ToolName: "inspect", Input: map[string]any{}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
	}}
	eng, _ := newEngine(t, prov, false)
	eng.Tools.MustRegister(tools.Tool{
		Name: "inspect",
		Handler: func(context.Context, map[string]any) (string, error) {
			return "inspection complete", nil
		},
	})
	runner := &fakeHookRunner{responses: map[hooks.EventName][]fakeHookResponse{
		hooks.EventPreToolUse: {{Stdout: "compare against approved baseline"}},
	}}
	installHookRunner(t, eng, runner)

	if _, err := eng.Turn(context.Background(), "inspect"); err != nil {
		t.Fatal(err)
	}
	result := eng.Session.History[2].Blocks[0]
	if result.IsError || !strings.Contains(result.Content, "inspection complete") || !strings.Contains(result.Content, "compare against approved baseline") {
		t.Fatalf("tool result = %+v", result)
	}
	var postRequest hooks.Request
	for _, request := range runner.requests {
		if request.EventName == hooks.EventPostToolUse {
			postRequest = request
		}
	}
	if postRequest.ToolResult != "inspection complete" {
		t.Fatalf("PostToolUse tool_result = %q, want raw tool output", postRequest.ToolResult)
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
	installHookRunner(t, eng, &fakeHookRunner{responses: map[hooks.EventName][]fakeHookResponse{
		hooks.EventPreToolUse: {{ExitCode: 2, Stdout: "policy denied"}},
	}})

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

func TestTurn_PostToolUseExitTwoAddsCorrectiveContext(t *testing.T) {
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
	installHookRunner(t, eng, &fakeHookRunner{responses: map[hooks.EventName][]fakeHookResponse{
		hooks.EventPreToolUse:  {{}},
		hooks.EventPostToolUse: {{ExitCode: 2, Stdout: "redaction required"}},
	}})

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
	if len(tr.Blocks) != 1 || tr.Blocks[0].IsError || !strings.Contains(tr.Blocks[0].Content, "sensitive output") || !strings.Contains(tr.Blocks[0].Content, "redaction required") {
		t.Fatalf("tool result = %+v", tr)
	}
}

func TestTurn_PostToolUseRetainsSuccessfulContextBeforeRequiredFailure(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "tu1", ToolName: "audit", Input: map[string]any{"path": "x"}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
	}}
	eng, _ := newEngine(t, prov, false)
	eng.Tools.MustRegister(tools.Tool{
		Name: "audit",
		Handler: func(context.Context, map[string]any) (string, error) {
			return "tool output", nil
		},
	})
	installHookRunner(t, eng, hookRunnerFunc(func(_ context.Context, request hooks.Request) ([]hooks.Result, error) {
		if request.EventName != hooks.EventPostToolUse {
			return nil, nil
		}
		return []hooks.Result{{
			Hook:      hooks.CommandHook{Name: "context-hook", Events: []hooks.EventName{hooks.EventPostToolUse}},
			EventName: hooks.EventPostToolUse,
			ToolName:  request.ToolName,
			ExitCode:  0,
			Stdout:    "earlier successful context",
		}}, errors.New("required post hook failed")
	}))

	if _, err := eng.Turn(context.Background(), "run audit"); err != nil {
		t.Fatal(err)
	}
	result := eng.Session.History[2].Blocks[0]
	if !result.IsError || !strings.Contains(result.Content, "tool output") || !strings.Contains(result.Content, "earlier successful context") || !strings.Contains(result.Content, "required post hook failed") {
		t.Fatalf("tool result = %+v", result)
	}
}

func TestTurn_PostExecutionPolicyDenyBecomesToolErrorAndStopsLaterPolicies(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "tu1", ToolName: "audit", Input: map[string]any{}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
	}}
	eng, _ := newEngine(t, prov, false)
	handlerRan := false
	eng.Tools.MustRegister(tools.Tool{
		Name: "audit",
		Handler: func(context.Context, map[string]any) (string, error) {
			handlerRan = true
			return "sensitive result", nil
		},
	})
	var laterAfterCalls int
	installRuntimeTestModules(t, eng,
		&runtimeToolPolicyModule{id: "deny-after", apply: func(request runtimemodule.ToolPolicyRequest) (runtimemodule.ToolPolicyDecision, error) {
			if request.Stage == runtimemodule.ToolPolicyAfterExecution {
				return runtimemodule.ToolPolicyDecision{Action: runtimemodule.ToolPolicyDeny, Reason: "result cannot be released"}, nil
			}
			return runtimemodule.ToolPolicyDecision{Action: runtimemodule.ToolPolicyAllow}, nil
		}},
		&runtimeToolPolicyModule{id: "later", apply: func(request runtimemodule.ToolPolicyRequest) (runtimemodule.ToolPolicyDecision, error) {
			if request.Stage == runtimemodule.ToolPolicyAfterExecution {
				laterAfterCalls++
			}
			return runtimemodule.ToolPolicyDecision{Action: runtimemodule.ToolPolicyAllow}, nil
		}},
	)

	if _, err := eng.Turn(context.Background(), "run audit"); err != nil {
		t.Fatal(err)
	}
	result := eng.Session.History[2].Blocks[0]
	if !handlerRan || laterAfterCalls != 0 {
		t.Fatalf("handler ran = %t, later after calls = %d", handlerRan, laterAfterCalls)
	}
	if !result.IsError || !strings.Contains(result.Content, "sensitive result") || !strings.Contains(result.Content, "result cannot be released") {
		t.Fatalf("tool result = %+v", result)
	}
}

func TestTurn_PostExecutionTransformOwnsTerminalObservation(t *testing.T) {
	const rawResult = "sensitive raw result"
	const filteredResult = "[redacted by policy]"
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "tu-redact", ToolName: "audit", Input: map[string]any{}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, false)
	eng.Tools.MustRegister(tools.Tool{
		Name: "audit",
		Handler: func(context.Context, map[string]any) (string, error) {
			return rawResult, nil
		},
	})
	installRuntimeTestModules(t, eng, &runtimeToolPolicyModule{id: "redact-result", apply: func(request runtimemodule.ToolPolicyRequest) (runtimemodule.ToolPolicyDecision, error) {
		if request.Stage == runtimemodule.ToolPolicyAfterExecution {
			return runtimemodule.ToolPolicyDecision{
				Action: runtimemodule.ToolPolicyTransform,
				Result: runtimemodule.ToolPolicyResult{Content: filteredResult},
			}, nil
		}
		return runtimemodule.ToolPolicyDecision{Action: runtimemodule.ToolPolicyAllow}, nil
	}})

	var completed toolevents.CompletedPayload
	bus.Subscribe(toolevents.CompletedType, func(event events.Event) {
		completed, _ = event.Payload.(toolevents.CompletedPayload)
	})

	if out, err := eng.Turn(context.Background(), "run audit"); err != nil || out != "done" {
		t.Fatalf("Turn() = %q, %v", out, err)
	}
	result := eng.Session.History[2].Blocks[0]
	if result.Content != filteredResult || result.IsError {
		t.Fatalf("tool result = %+v, want filtered success", result)
	}
	if completed.Preview != filteredResult || completed.Len != len(filteredResult) {
		t.Fatalf("completed payload = %+v, want filtered terminal observation", completed)
	}
	data, err := os.ReadFile(filepath.Join(eng.Session.Dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), rawResult) || !strings.Contains(string(data), filteredResult) {
		t.Fatalf("durable events leaked pre-policy result:\n%s", data)
	}
}

func TestTurn_PostExecutionTransformSuppressesRawOutputDelta(t *testing.T) {
	const rawResult = "sensitive streamed result"
	const filteredResult = "[redacted by policy]"
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "tu-stream-redact", ToolName: "stream-audit", Input: map[string]any{}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, false)
	eng.Tools.MustRegister(tools.Tool{
		Name: "stream-audit",
		Handler: func(ctx context.Context, _ map[string]any) (string, error) {
			tools.ToolCallEventsFromContext(ctx).Emit(tools.OutputDelta{Text: rawResult})
			return rawResult, nil
		},
	})
	installRuntimeTestModules(t, eng, &runtimeToolPolicyModule{id: "redact-streamed-result", apply: func(request runtimemodule.ToolPolicyRequest) (runtimemodule.ToolPolicyDecision, error) {
		if request.Stage == runtimemodule.ToolPolicyAfterExecution {
			return runtimemodule.ToolPolicyDecision{
				Action: runtimemodule.ToolPolicyTransform,
				Result: runtimemodule.ToolPolicyResult{Content: filteredResult},
			}, nil
		}
		return runtimemodule.ToolPolicyDecision{Action: runtimemodule.ToolPolicyAllow}, nil
	}})

	var deltas []toolevents.OutputDeltaPayload
	bus.Subscribe(toolevents.OutputDeltaType, func(event events.Event) {
		payload, _ := event.Payload.(toolevents.OutputDeltaPayload)
		deltas = append(deltas, payload)
	})

	if out, err := eng.Turn(context.Background(), "stream sensitive output"); err != nil || out != "done" {
		t.Fatalf("Turn() = %q, %v", out, err)
	}
	if len(deltas) != 0 {
		t.Fatalf("tool output deltas = %+v, want raw streaming suppressed while Tool Policies are active", deltas)
	}
	result := eng.Session.History[2].Blocks[0]
	if result.Content != filteredResult || result.IsError {
		t.Fatalf("tool result = %+v, want filtered success", result)
	}
	data, err := os.ReadFile(filepath.Join(eng.Session.Dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), rawResult) || !strings.Contains(string(data), filteredResult) {
		t.Fatalf("events leaked pre-policy streamed result:\n%s", data)
	}
}

func TestTurn_StopHookBlockContinuesWithPrompt(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "first"), StopReason: llm.StopEndTurn},
		{Message: llm.TextMessage(llm.RoleAssistant, "final"), StopReason: llm.StopEndTurn},
	}}
	eng, _ := newEngine(t, prov, false)
	installHookRunner(t, eng, &fakeHookRunner{responses: map[hooks.EventName][]fakeHookResponse{
		hooks.EventStop: {
			{ExitCode: 2, Stdout: "continue until done"},
			{},
		},
	}})

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

func TestTurn_StopHookStdoutQueuesRuntimeContextForNextProviderRequest(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "first"), StopReason: llm.StopEndTurn},
		{Message: llm.TextMessage(llm.RoleAssistant, "second"), StopReason: llm.StopEndTurn},
		{Message: llm.TextMessage(llm.RoleAssistant, "third"), StopReason: llm.StopEndTurn},
	}}
	eng, _ := newEngine(t, prov, false)
	installHookRunner(t, eng, &fakeHookRunner{responses: map[hooks.EventName][]fakeHookResponse{
		hooks.EventStop: {
			{Stdout: "verify the release branch before the next response"},
			{},
			{},
		},
	}})

	if _, err := eng.Turn(context.Background(), "first turn"); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Turn(context.Background(), "second turn"); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Turn(context.Background(), "third turn"); err != nil {
		t.Fatal(err)
	}
	if got := messagesText(prov.histories[0]); strings.Contains(got, "verify the release branch") {
		t.Fatalf("stop policy context appeared before the hook ran:\n%s", got)
	}
	if got := messagesText(prov.histories[1]); !strings.Contains(got, "verify the release branch") {
		t.Fatalf("next provider request missing stop policy context:\n%s", got)
	}
	if got := messagesText(prov.histories[2]); strings.Contains(got, "verify the release branch") {
		t.Fatalf("stop policy context repeated in later provider request:\n%s", got)
	}
	for _, msg := range eng.Session.History {
		if msg.Kind == llm.MessageKindRuntimeContext {
			t.Fatalf("runtime context persisted in history: %+v", msg)
		}
	}
}

func TestTurn_GoalCompletionGateContinuesThenCompletes(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "goal_create_1", ToolName: GoalToolCreate, Input: map[string]any{
				"description": "ship this",
				"acceptance":  "artifact.txt exists and go test ./... passes",
			}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "too early"), StopReason: llm.StopEndTurn},
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "goal_update_1", ToolName: GoalToolUpdate, Input: map[string]any{
				"status":        string(workmem.GoalStatusSuccess),
				"status_reason": "tests passed",
			}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "final"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, false)
	goalState, _ := installSessionStateModules(t, eng)
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
	state, err := goalState.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != workmem.GoalStatusSuccess || state.StatusReason != "tests passed" || state.ContinuationCount != 1 || !strings.Contains(state.Acceptance, "artifact.txt") {
		t.Fatalf("goal state = %+v", state)
	}
}

func TestTurn_GoalCompletionGateAcceptsMaximumGoalContract(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "too early"), StopReason: llm.StopEndTurn},
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "goal_update_max", ToolName: GoalToolUpdate, Input: map[string]any{
				"status":        string(workmem.GoalStatusSuccess),
				"status_reason": "maximum contract preserved",
			}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "final"), StopReason: llm.StopEndTurn},
	}}
	eng, _ := newEngine(t, prov, false)
	goalState := workmem.NewGoalStateStore(eng.Session.Dir, workmem.GoalStateOptions{})
	acceptance := strings.Repeat("a", 32*1024)
	if _, err := goalState.Create("ship the maximum contract", acceptance); err != nil {
		t.Fatal(err)
	}
	installSessionStateModulesWithStores(t, eng, goalState, nil)

	out, err := eng.Turn(context.Background(), "finish the goal")
	if err != nil {
		t.Fatal(err)
	}
	if out != "final" || len(prov.histories) != 3 {
		t.Fatalf("out = %q, provider calls = %d", out, len(prov.histories))
	}
	if got := messagesText(prov.histories[1]); !strings.Contains(got, acceptance) {
		t.Fatalf("maximum acceptance missing from continuation context: length=%d", len(got))
	}
}

func TestTurn_GoalCompletionGateDefersWhileExternalWorkIsRunning(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "waiting for delegated work"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, false)
	goalState := workmem.NewGoalStateStore(eng.Session.Dir, workmem.GoalStateOptions{})
	if _, err := goalState.Create("finish delegated work", "all delegated results are incorporated"); err != nil {
		t.Fatal(err)
	}
	installSessionStateModulesWithStoresAndGoalOptions(t, eng, goalState, nil, GoalModuleOptions{
		EnableContinuation:   true,
		ContinuationDeferrer: fixedGoalContinuationDeferrer(true),
	})
	var continued int32
	bus.Subscribe("goal.continued", func(events.Event) { atomic.AddInt32(&continued, 1) })

	out, err := eng.Turn(context.Background(), "delegate the work")
	if err != nil {
		t.Fatal(err)
	}
	if out != "waiting for delegated work" || len(prov.histories) != 1 {
		t.Fatalf("out = %q, provider calls = %d", out, len(prov.histories))
	}
	if atomic.LoadInt32(&continued) != 0 {
		t.Fatalf("goal.continued events = %d", continued)
	}
	state, err := goalState.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != workmem.GoalStatusInProgress || state.ContinuationCount != 0 {
		t.Fatalf("goal state = %+v", state)
	}
}

func TestTurn_DeferredGoalStillHonorsStopHookContinuation(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "first"), StopReason: llm.StopEndTurn},
		{Message: llm.TextMessage(llm.RoleAssistant, "final"), StopReason: llm.StopEndTurn},
	}}
	eng, _ := newEngine(t, prov, false)
	goalState := workmem.NewGoalStateStore(eng.Session.Dir, workmem.GoalStateOptions{})
	if _, err := goalState.Create("finish delegated work", "all checks pass"); err != nil {
		t.Fatal(err)
	}
	installSessionStateModulesWithStoresAndGoalOptions(t, eng, goalState, nil, GoalModuleOptions{
		EnableContinuation:   true,
		ContinuationDeferrer: fixedGoalContinuationDeferrer(true),
	})
	installHookRunner(t, eng, &fakeHookRunner{responses: map[hooks.EventName][]fakeHookResponse{
		hooks.EventStop: {
			{ExitCode: 2, Stdout: "run the explicit stop check"},
			{},
		},
	}})

	out, err := eng.Turn(context.Background(), "delegate the work")
	if err != nil {
		t.Fatal(err)
	}
	if out != "final" || len(prov.histories) != 2 {
		t.Fatalf("out = %q, provider calls = %d", out, len(prov.histories))
	}
	if got := prov.histories[1][len(prov.histories[1])-2].FirstText(); got != "run the explicit stop check" {
		t.Fatalf("stop continuation = %q", got)
	}
}

func TestTurn_GoalWaitForUserAllowsFinish(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "goal_wait_1", ToolName: GoalToolUpdate, Input: map[string]any{
				"status":        string(workmem.GoalStatusWaitForUser),
				"status_reason": "waiting for the deployment choice",
			}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "Which deployment should I use?"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, false)
	goalState := workmem.NewGoalStateStore(eng.Session.Dir, workmem.GoalStateOptions{})
	if _, err := goalState.Create("deploy the service", "the chosen deployment is healthy"); err != nil {
		t.Fatal(err)
	}
	installSessionStateModulesWithStoresAndGoalOptions(t, eng, goalState, nil, GoalModuleOptions{
		EnableContinuation:   true,
		ContinuationDeferrer: panicGoalContinuationDeferrer{t: t},
	})
	var continued int32
	bus.Subscribe("goal.continued", func(events.Event) { atomic.AddInt32(&continued, 1) })

	out, err := eng.Turn(context.Background(), "deploy it")
	if err != nil {
		t.Fatal(err)
	}
	if out != "Which deployment should I use?" || len(prov.histories) != 2 {
		t.Fatalf("out = %q, provider calls = %d", out, len(prov.histories))
	}
	if atomic.LoadInt32(&continued) != 0 {
		t.Fatalf("goal.continued events = %d", continued)
	}
	state, err := goalState.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != workmem.GoalStatusWaitForUser || state.ContinuationCount != 0 {
		t.Fatalf("goal state = %+v", state)
	}
}

func TestTurn_UserMessageDoesNotCreateGoalState(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn},
	}}
	eng, _ := newEngine(t, prov, false)
	goalState := workmem.NewGoalStateStore(eng.Session.Dir, workmem.GoalStateOptions{})
	installSessionStateModulesWithStores(t, eng, goalState, nil)

	out, err := eng.Turn(context.Background(), "this is normal context, not a goal")
	if err != nil {
		t.Fatal(err)
	}
	if out != "ok" {
		t.Fatalf("out = %q", out)
	}
	snapshot, err := goalState.StatusSnapshot()
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
	goalState := workmem.NewGoalStateStore(eng.Session.Dir, workmem.GoalStateOptions{})
	installSessionStateModulesWithStores(t, eng, goalState, nil)
	runner, err := hooks.NewRunner(hooks.Config{Commands: []hooks.CommandHook{{
		Name:    "ignored-goal-output",
		Events:  []hooks.EventName{hooks.EventStop},
		Command: runtimeHookCommand("goal-output"),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	installHookRunner(t, eng, runner)

	out, err := eng.Turn(context.Background(), "finish")
	if err != nil {
		t.Fatal(err)
	}
	if out != "ok" {
		t.Fatalf("out = %q", out)
	}
	snapshot, err := goalState.StatusSnapshot()
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

func TestTurn_ProviderFailureContinuesWhenPendingInputExists(t *testing.T) {
	prov := &queuedFailureProvider{
		started:  make(chan struct{}, 1),
		release:  make(chan struct{}),
		firstErr: errors.New("connection reset by peer"),
		recovery: llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "recovered"), StopReason: llm.StopEndTurn},
	}
	eng, bus := newEngine(t, prov, false)
	if err := eng.queuePolicyRuntimeContextFromHookResults([]hooks.Result{{
		Hook:   hooks.CommandHook{Name: "provider-retry"},
		Stdout: "preserve retry context",
	}}); err != nil {
		t.Fatal(err)
	}
	var retries []LLMRetryPayload
	var turnErrors int32
	bus.Subscribe("llm.retry", func(e events.Event) {
		if payload, ok := e.Payload.(LLMRetryPayload); ok {
			retries = append(retries, payload)
		}
	})
	bus.Subscribe("turn.errored", func(e events.Event) { atomic.AddInt32(&turnErrors, 1) })

	done := make(chan error, 1)
	go func() {
		out, err := eng.Turn(context.Background(), "active")
		if err == nil && out != "recovered" {
			err = fmt.Errorf("out = %q, want recovered", out)
		}
		done <- err
	}()
	waitSignal(t, prov.started, "provider did not start")
	if _, err := eng.EnqueuePendingMessageWithOptions(context.Background(), llm.TextMessage(llm.RoleUser, "continue after failure"), PendingInputOptions{
		ID:  "pending-provider-failure",
		TTL: time.Hour,
	}); err != nil {
		t.Fatal(err)
	}
	close(prov.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	if prov.called != 2 || len(prov.histories) != 2 {
		t.Fatalf("provider calls = %d histories = %d, want 2", prov.called, len(prov.histories))
	}
	continuedHistory := messagesText(prov.histories[1])
	if !strings.Contains(continuedHistory, "continue after failure") {
		t.Fatalf("continued provider history missing pending input:\n%s", continuedHistory)
	}
	if strings.Contains(continuedHistory, "preserve retry context") {
		t.Fatalf("continued provider history repeated checkpointed policy context:\n%s", continuedHistory)
	}
	if remaining := eng.pendingPolicyRuntimeContextSnapshot(); len(remaining) != 0 {
		t.Fatalf("policy context remaining after successful provider request = %+v", remaining)
	}
	if len(retries) != 1 || retries[0].RetryReason != "pending_input_after_provider_error" || !retries[0].WillRetry {
		t.Fatalf("retry diagnostics = %+v", retries)
	}
	if got := atomic.LoadInt32(&turnErrors); got != 0 {
		t.Fatalf("turn.errored count = %d, want 0", got)
	}
	records, err := eng.PendingInputQueue.Records()
	if err != nil {
		t.Fatal(err)
	}
	if got := records["pending-provider-failure"].State; got != PendingInputStateProcessed {
		t.Fatalf("pending state = %q, want %q", got, PendingInputStateProcessed)
	}
}

func TestTurn_TerminalProviderFailureConsumesPolicyContext(t *testing.T) {
	release := make(chan struct{})
	close(release)
	prov := &queuedFailureProvider{
		started:  make(chan struct{}, 1),
		release:  release,
		firstErr: errors.New("unauthorized API key"),
		recovery: llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "recovered later"), StopReason: llm.StopEndTurn},
	}
	eng, _ := newEngine(t, prov, false)
	if err := eng.queuePolicyRuntimeContextFromHookResults([]hooks.Result{{
		Hook:   hooks.CommandHook{Name: "one-shot"},
		Stdout: "one-shot provider context",
	}}); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.Turn(context.Background(), "failing turn"); err == nil {
		t.Fatal("first turn error = nil, want provider failure")
	}
	if out, err := eng.Turn(context.Background(), "next turn"); err != nil || out != "recovered later" {
		t.Fatalf("next turn out=%q err=%v", out, err)
	}
	if len(prov.histories) != 2 {
		t.Fatalf("provider histories = %d, want 2", len(prov.histories))
	}
	if got := messagesText(prov.histories[0]); !strings.Contains(got, "one-shot provider context") {
		t.Fatalf("failed provider request missing policy context:\n%s", got)
	}
	if got := messagesText(prov.histories[1]); strings.Contains(got, "one-shot provider context") {
		t.Fatalf("next provider request repeated stale policy context:\n%s", got)
	}
	if remaining := eng.pendingPolicyRuntimeContextSnapshot(); len(remaining) != 0 {
		t.Fatalf("policy context remaining after terminal provider failure = %+v", remaining)
	}
}

func TestTurn_PreRestoreCancellationTerminallyPersistsAcceptedInput(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{{
		Message: llm.TextMessage(llm.RoleAssistant, "unused"), StopReason: llm.StopEndTurn,
	}}}
	eng, _ := newEngine(t, prov, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := eng.TurnMessageWithID(ctx, llm.TextMessage(llm.RoleUser, "cancel before restore"), "cancelled-turn")
	if !errors.Is(err, cancellation.ErrUserCancelled) {
		t.Fatalf("TurnMessageWithID() error = %v, want ErrUserCancelled", err)
	}
	if prov.called != 0 {
		t.Fatalf("provider calls = %d, want 0", prov.called)
	}
	if len(eng.Session.History) != 1 || eng.Session.History[0].FirstText() != "cancel before restore" {
		t.Fatalf("durable history = %+v, want accepted cancelled input", eng.Session.History)
	}
	records, err := eng.PendingInputQueue.Records()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("pending records = %+v, want one processed admission", records)
	}
	for _, record := range records {
		if record.State != PendingInputStateProcessed {
			t.Fatalf("cancelled admission record = %+v, want state %q", record, PendingInputStateProcessed)
		}
	}
}

func TestTurn_PendingInputRestoreFailureTerminallyPersistsAcceptedInput(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{{
		Message: llm.TextMessage(llm.RoleAssistant, "later done"), StopReason: llm.StopEndTurn,
	}}}
	eng, _ := newEngine(t, prov, false)
	accepted, err := eng.AdmitTurnMessage("failed-turn", llm.TextMessage(llm.RoleUser, "accepted before restore failure"))
	if err != nil {
		t.Fatal(err)
	}
	queue := eng.currentPendingInputQueue()
	pendingPath := queue.path
	backupPath := pendingPath + ".restore-failure"
	if err := os.Rename(pendingPath, backupPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(pendingPath, 0o700); err != nil {
		t.Fatal(err)
	}
	restored := false
	restoreJournal := func() error {
		if restored {
			return nil
		}
		if err := os.Remove(pendingPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.Rename(backupPath, pendingPath); err != nil {
			return err
		}
		restored = true
		return nil
	}
	t.Cleanup(func() { _ = restoreJournal() })

	eng.mu.Lock()
	lifecycle := turnLifecycle{
		engine:  eng,
		turnID:  "failed-turn",
		userMsg: accepted,
		start:   time.Now(),
	}
	_, turnErr := lifecycle.runLocked(context.Background())
	eng.mu.Unlock()
	if turnErr == nil || !strings.Contains(turnErr.Error(), "pending input queue") {
		t.Fatalf("turn lifecycle error = %v, want pending journal failure", turnErr)
	}
	if len(eng.Session.History) != 1 || eng.Session.History[0].ID != accepted.ID {
		t.Fatalf("durable history = %+v, want accepted input %q", eng.Session.History, accepted.ID)
	}

	if err := restoreJournal(); err != nil {
		t.Fatal(err)
	}
	eng.finishActiveTurn("failed-turn")
	if out, err := eng.Turn(context.Background(), "later input"); err != nil || out != "later done" {
		t.Fatalf("later Turn() = %q, %v", out, err)
	}
	acceptedCopies := 0
	for _, message := range eng.Session.History {
		if message.ID == accepted.ID {
			acceptedCopies++
		}
	}
	if acceptedCopies != 1 {
		t.Fatalf("accepted input copies in durable history = %d, want 1", acceptedCopies)
	}
	if prov.called != 1 {
		t.Fatalf("provider calls = %d, want 1", prov.called)
	}
}

func TestTurn_CancellationPreservesPendingInputWithoutContinuing(t *testing.T) {
	prov := &mockProvider{
		script: []llm.Response{{Message: llm.TextMessage(llm.RoleAssistant, "unused"), StopReason: llm.StopEndTurn}},
		delay:  500 * time.Millisecond,
	}
	eng, bus := newEngine(t, prov, false)
	requested := make(chan struct{}, 1)
	var drained, dropped int32
	bus.Subscribe("llm.requested", func(e events.Event) { signal(requested) })
	bus.Subscribe("pending_input.drained", func(e events.Event) { atomic.AddInt32(&drained, 1) })
	bus.Subscribe("pending_input.dropped", func(e events.Event) { atomic.AddInt32(&dropped, 1) })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := eng.Turn(ctx, "active")
		done <- err
	}()
	waitSignal(t, requested, "llm.requested")
	if _, err := eng.EnqueuePendingMessageWithOptions(context.Background(), llm.TextMessage(llm.RoleUser, "preserve me"), PendingInputOptions{
		ID:  "pending-after-cancel",
		TTL: time.Hour,
	}); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := <-done; !errors.Is(err, cancellation.ErrUserCancelled) {
		t.Fatalf("turn err = %v, want ErrUserCancelled", err)
	}

	if got := len(eng.Session.History); got != 2 {
		t.Fatalf("history len = %d, want active and preserved pending input: %+v", got, eng.Session.History)
	}
	if got := eng.Session.History[1].FirstText(); got != "preserve me" {
		t.Fatalf("preserved message = %q", got)
	}
	records, err := eng.PendingInputQueue.Records()
	if err != nil {
		t.Fatal(err)
	}
	if got := records["pending-after-cancel"].State; got != PendingInputStateProcessed {
		t.Fatalf("pending state = %q, want %q", got, PendingInputStateProcessed)
	}
	if atomic.LoadInt32(&drained) != 1 || atomic.LoadInt32(&dropped) != 0 {
		t.Fatalf("pending events drained=%d dropped=%d, want 1/0", drained, dropped)
	}
	if prov.called != 0 {
		t.Fatalf("completed provider calls = %d, want none after cancellation", prov.called)
	}
}

func TestTurn_AuthFailurePreservesPendingInputWithoutContinuing(t *testing.T) {
	prov := &queuedFailureProvider{
		started:  make(chan struct{}, 1),
		release:  make(chan struct{}),
		firstErr: errors.New("codex websocket connect: status 401: handshake failed"),
		recovery: llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "must not run"), StopReason: llm.StopEndTurn},
	}
	eng, _ := newEngine(t, prov, false)
	done := make(chan error, 1)
	go func() {
		_, err := eng.Turn(context.Background(), "active")
		done <- err
	}()
	waitSignal(t, prov.started, "provider did not start")
	if _, err := eng.EnqueuePendingMessageWithOptions(context.Background(), llm.TextMessage(llm.RoleUser, "keep after auth failure"), PendingInputOptions{
		ID:  "pending-auth-failure",
		TTL: time.Hour,
	}); err != nil {
		t.Fatal(err)
	}
	close(prov.release)
	if err := <-done; err == nil || !strings.Contains(err.Error(), "status 401") {
		t.Fatalf("turn err = %v, want status 401", err)
	}
	if prov.called != 1 {
		t.Fatalf("provider calls = %d, want 1", prov.called)
	}
	if got := eng.Session.History[len(eng.Session.History)-1].FirstText(); got != "keep after auth failure" {
		t.Fatalf("preserved message = %q", got)
	}
	records, err := eng.PendingInputQueue.Records()
	if err != nil {
		t.Fatal(err)
	}
	if got := records["pending-auth-failure"].State; got != PendingInputStateProcessed {
		t.Fatalf("pending state = %q, want %q", got, PendingInputStateProcessed)
	}
}

func TestTurn_NonRetryableProviderFailurePreservesPendingInputWithoutContinuing(t *testing.T) {
	tests := []struct {
		name        string
		providerErr error
		wantError   string
	}{
		{name: "bad-request", providerErr: errors.New("codex websocket error: status 400: bad request"), wantError: "status 400"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prov := &queuedFailureProvider{
				started:  make(chan struct{}, 1),
				release:  make(chan struct{}),
				firstErr: tt.providerErr,
				recovery: llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "must not run"), StopReason: llm.StopEndTurn},
			}
			eng, _ := newEngine(t, prov, false)
			done := make(chan error, 1)
			go func() {
				_, err := eng.Turn(context.Background(), "active")
				done <- err
			}()
			waitSignal(t, prov.started, "provider did not start")
			pendingID := "pending-" + tt.name
			pendingText := "keep after " + tt.name
			if _, err := eng.EnqueuePendingMessageWithOptions(context.Background(), llm.TextMessage(llm.RoleUser, pendingText), PendingInputOptions{
				ID:  pendingID,
				TTL: time.Hour,
			}); err != nil {
				t.Fatal(err)
			}
			close(prov.release)
			if err := <-done; err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("turn err = %v, want %q", err, tt.wantError)
			}
			if prov.called != 1 {
				t.Fatalf("provider calls = %d, want 1", prov.called)
			}
			if got := eng.Session.History[len(eng.Session.History)-1].FirstText(); got != pendingText {
				t.Fatalf("preserved message = %q, want %q", got, pendingText)
			}
			records, err := eng.PendingInputQueue.Records()
			if err != nil {
				t.Fatal(err)
			}
			if got := records[pendingID].State; got != PendingInputStateProcessed {
				t.Fatalf("pending state = %q, want %q", got, PendingInputStateProcessed)
			}
		})
	}
}

func TestPreservePendingInputAfterFailureRepairsInterruptedToolCall(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	turnID := eng.beginActiveTurn("turn-repair-before-preserve")
	if err := eng.Session.Append(llm.TextMessage(llm.RoleUser, "active")); err != nil {
		t.Fatal(err)
	}
	if err := eng.Session.Append(llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{
		Type:      llm.BlockToolUse,
		ToolUseID: "interrupted-tool",
		ToolName:  "read",
	}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.EnqueuePendingMessageWithOptions(context.Background(), llm.TextMessage(llm.RoleUser, "preserve after tool failure"), PendingInputOptions{
		ID:  "pending-after-tool-failure",
		TTL: time.Hour,
	}); err != nil {
		t.Fatal(err)
	}

	if err := eng.preservePendingInputAfterFailureLocked(turnID); err != nil {
		t.Fatal(err)
	}
	if got := len(eng.Session.History); got != 4 {
		t.Fatalf("history len = %d, want user, tool use, repair, and pending input: %+v", got, eng.Session.History)
	}
	repair := eng.Session.History[2]
	if repair.Role != llm.RoleUser || len(repair.Blocks) != 1 || repair.Blocks[0].Type != llm.BlockToolResult || repair.Blocks[0].ToolUseID != "interrupted-tool" || !repair.Blocks[0].IsError {
		t.Fatalf("transcript repair = %+v", repair)
	}
	if got := eng.Session.History[3].FirstText(); got != "preserve after tool failure" {
		t.Fatalf("preserved pending input = %q", got)
	}
	records, err := eng.PendingInputQueue.Records()
	if err != nil {
		t.Fatal(err)
	}
	if got := records["pending-after-tool-failure"].State; got != PendingInputStateProcessed {
		t.Fatalf("pending state = %q, want %q", got, PendingInputStateProcessed)
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
	if got, want := []string{
		prov.histories[0][0].FirstText(),
		prov.histories[0][1].FirstText(),
	}, []string{"replay me", "after restart"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("provider input order = %v, want %v", got, want)
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
	var admissionOrder []string
	probe := &pendingAdmissionProbe{queue: eng.PendingInputQueue, order: &admissionOrder}
	registry := runtimemodule.NewRegistry()
	if err := registry.Register(probe); err != nil {
		t.Fatal(err)
	}
	set, err := registry.Seal(context.Background(), runtimemodule.ToolContext{})
	if err != nil {
		t.Fatal(err)
	}
	eng.RuntimeModules = set
	var drained int32
	var promoted PendingInputPromotedPayload
	eng.Bus.Subscribe("pending_input.drained", func(e events.Event) {
		atomic.AddInt32(&drained, 1)
	})
	eng.Bus.Subscribe(PendingInputPromotedType, func(e events.Event) {
		promoted, _ = e.Payload.(PendingInputPromotedPayload)
		admissionOrder = append(admissionOrder, "pending_input.promoted")
	})
	eng.Bus.Subscribe(TurnAdmittedType, func(events.Event) {
		admissionOrder = append(admissionOrder, "turn.admitted")
	})

	msg, promotedStatus, ok, err := eng.PromotePendingInputTurn("compact-1", "turn-1")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("pending input was not promoted")
	}
	if promotedStatus.PendingCount != 0 ||
		promoted.PendingCount != 0 ||
		promoted.MaxPendingInputs != DefaultMaxPendingInput {
		t.Fatalf("promoted status/event = %+v / %+v, want empty queue", promotedStatus, promoted)
	}
	if probe.err != nil {
		t.Fatal(probe.err)
	}
	if want := []string{"turn.admitted", "pending_input.promoted", "observer"}; !reflect.DeepEqual(admissionOrder, want) {
		t.Fatalf("promotion admission order = %v, want %v", admissionOrder, want)
	}
	if len(probe.states) != 1 || probe.states[0] != PendingInputStateAdmitted {
		t.Fatalf("observer states = %v, want admitted", probe.states)
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

func TestTurn_PromotedPendingInputReplacementPreservesFrameworkIdentity(t *testing.T) {
	root := t.TempDir()
	sess, err := session.New(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	prov := &mockProvider{script: []llm.Response{{
		Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn,
	}}}
	eng := newEngineForSession(t, sess, prov)
	if err := eng.ReserveTurnID("compact-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.EnqueuePendingMessageWithOptions(
		context.Background(),
		llm.TextMessage(llm.RoleUser, "original pending input"),
		PendingInputOptions{ID: "event-replaced", TTL: time.Hour},
	); err != nil {
		t.Fatal(err)
	}
	records, err := eng.PendingInputQueue.Records()
	if err != nil {
		t.Fatal(err)
	}
	record := records["event-replaced"]
	installRuntimeTestModules(t, eng, &runtimeTurnInputPolicyModule{id: "replace-input", apply: func(runtimemodule.TurnInputRequest) (runtimemodule.TurnInputDecision, error) {
		return runtimemodule.TurnInputDecision{
			Action:  runtimemodule.TurnInputReplace,
			Message: llm.TextMessage(llm.RoleUser, "transformed pending input"),
		}, nil
	}})

	msg, _, promoted, err := eng.PromotePendingInputTurn("compact-1", "turn-replaced")
	if err != nil {
		t.Fatal(err)
	}
	if !promoted {
		t.Fatal("pending input was not promoted")
	}
	if _, err := eng.TurnMessageWithID(context.Background(), msg, "turn-replaced"); err != nil {
		t.Fatal(err)
	}
	if got := eng.Session.History[0]; got.ID != record.MessageID || got.Kind != record.Message.Kind || got.FirstText() != "transformed pending input" {
		t.Fatalf("persisted transformed input = %+v, want id %q kind %q", got, record.MessageID, record.Message.Kind)
	}
	records, err = eng.PendingInputQueue.Records()
	if err != nil {
		t.Fatal(err)
	}
	if records[record.ID].State != PendingInputStateProcessed {
		t.Fatalf("pending state = %q, want processed", records[record.ID].State)
	}
}

func TestTurn_AcceptedInputIsReplayableBeforeTurnInputPolicy(t *testing.T) {
	const turnID = "turn-policy-checkpoint"
	root := t.TempDir()
	sess, err := session.New(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	prov := &mockProvider{script: []llm.Response{{
		Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn,
	}}}
	eng := newEngineForSession(t, sess, prov)
	var (
		policyMessageID string
		recordsAtPolicy map[string]PendingInputRecord
		policyReadErr   error
	)
	installRuntimeTestModules(t, eng, &runtimeTurnInputPolicyModule{id: "replace-input", apply: func(request runtimemodule.TurnInputRequest) (runtimemodule.TurnInputDecision, error) {
		policyMessageID = request.Message.ID
		recordsAtPolicy, policyReadErr = NewPendingInputQueue(sess.Dir, PendingInputQueueOptions{}).Records()
		return runtimemodule.TurnInputDecision{
			Action:  runtimemodule.TurnInputReplace,
			Message: llm.TextMessage(llm.RoleUser, "transformed input"),
		}, nil
	}})

	if _, err := eng.TurnMessageWithID(context.Background(), llm.TextMessage(llm.RoleUser, "original input"), turnID); err != nil {
		t.Fatal(err)
	}
	if policyReadErr != nil {
		t.Fatal(policyReadErr)
	}
	if policyMessageID == "" {
		t.Fatal("turn input policy received a message without durable identity")
	}
	if len(recordsAtPolicy) != 1 {
		t.Fatalf("pending records during policy = %+v, want one accepted input", recordsAtPolicy)
	}
	var accepted PendingInputRecord
	for _, record := range recordsAtPolicy {
		accepted = record
	}
	if accepted.State != PendingInputStateAdmitted || accepted.TurnID != turnID || accepted.MessageID != policyMessageID || accepted.Message.FirstText() != "original input" {
		t.Fatalf("accepted input during policy = %+v", accepted)
	}
	if got := eng.Session.History[0]; got.ID != accepted.MessageID || got.FirstText() != "transformed input" {
		t.Fatalf("persisted transformed input = %+v, want message id %q", got, accepted.MessageID)
	}
	records, err := eng.PendingInputQueue.Records()
	if err != nil {
		t.Fatal(err)
	}
	if records[accepted.ID].State != PendingInputStateProcessed {
		t.Fatalf("accepted input state = %q, want processed", records[accepted.ID].State)
	}
}

func TestTurn_ProjectionFailurePersistsAcceptedInputOnce(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{{
		Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn,
	}}}
	eng, _ := newEngine(t, prov, false)
	eng.Compaction = DefaultCompactionPolicy()
	eng.Compaction.UserInputInlineMaxBytes = 8
	eng.ArtifactDir = filepath.Join(t.TempDir(), "missing-parent", "artifacts")
	input := "accepted input that requires projection"

	if _, err := eng.Turn(context.Background(), input); err == nil || !strings.Contains(err.Error(), "artifact store root") {
		t.Fatalf("Turn() error = %v, want projection failure", err)
	}
	if prov.called != 0 {
		t.Fatalf("provider calls after projection failure = %d, want none", prov.called)
	}
	if len(eng.Session.History) != 1 || eng.Session.History[0].FirstText() != input {
		t.Fatalf("history after projection failure = %+v, want accepted input once", eng.Session.History)
	}
	records, err := eng.PendingInputQueue.Records()
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.Message.FirstText() == input && record.State != PendingInputStateProcessed {
			t.Fatalf("accepted input record = %+v, want processed", record)
		}
	}

	eng.Compaction.Enabled = false
	eng.ArtifactDir = filepath.Join(t.TempDir(), "artifacts")
	if out, err := eng.Turn(context.Background(), "next input"); err != nil || out != "done" {
		t.Fatalf("second Turn() = %q, %v", out, err)
	}
	var occurrences int
	for _, message := range eng.Session.History {
		if message.FirstText() == input {
			occurrences++
		}
	}
	if occurrences != 1 {
		t.Fatalf("accepted input occurrences = %d, want one in history %+v", occurrences, eng.Session.History)
	}
}

func TestTurn_RecoveredAcceptedInputRunsTurnInputPolicy(t *testing.T) {
	sess, err := session.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	beforeCrash := newEngineForSession(t, sess, &mockProvider{})
	accepted, err := beforeCrash.AdmitTurnMessage("turn-before-crash", llm.TextMessage(llm.RoleUser, "secret before crash"))
	if err != nil {
		t.Fatal(err)
	}

	prov := &mockProvider{script: []llm.Response{{
		Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn,
	}}}
	recovered := newEngineForSession(t, sess, prov)
	var policyInputs []string
	installRuntimeTestModules(t, recovered, &runtimeTurnInputPolicyModule{id: "redact-input", apply: func(request runtimemodule.TurnInputRequest) (runtimemodule.TurnInputDecision, error) {
		policyInputs = append(policyInputs, request.Message.FirstText())
		if request.Message.ID == accepted.ID {
			return runtimemodule.TurnInputDecision{
				Action:  runtimemodule.TurnInputReplace,
				Message: llm.TextMessage(llm.RoleUser, "redacted before recovery"),
			}, nil
		}
		return runtimemodule.TurnInputDecision{Action: runtimemodule.TurnInputAllow}, nil
	}})

	if _, err := recovered.TurnMessageWithID(context.Background(), llm.TextMessage(llm.RoleUser, "new trigger"), "turn-after-crash"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"secret before crash", "new trigger"}; !reflect.DeepEqual(policyInputs, want) {
		t.Fatalf("policy inputs = %v, want %v", policyInputs, want)
	}
	if len(prov.histories) != 1 || len(prov.histories[0]) != 2 {
		t.Fatalf("provider history = %+v", prov.histories)
	}
	if got := prov.histories[0][0]; got.ID != accepted.ID || got.FirstText() != "redacted before recovery" {
		t.Fatalf("recovered provider input = %+v", got)
	}
	if got := prov.histories[0][1].FirstText(); got != "new trigger" {
		t.Fatalf("current provider input = %q", got)
	}
}

func TestTurn_RecoveryPreservesQueuedAndTurnInputAcceptanceOrder(t *testing.T) {
	sess, err := session.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	now := time.Date(2026, 6, 14, 8, 0, 0, 0, time.UTC)
	queue := NewPendingInputQueue(sess.Dir, PendingInputQueueOptions{Now: func() time.Time { return now }})
	queued, err := queue.Enqueue(
		llm.TextMessage(llm.RoleUser, "queued before crash"),
		PendingInputOptions{ID: "queued-before-crash", TTL: time.Hour},
		"turn-before-crash",
	)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	accepted, err := queue.AdmitTurnInput(
		"turn-before-crash",
		llm.TextMessage(llm.RoleUser, "turn input before crash"),
		false,
	)
	if err != nil {
		t.Fatal(err)
	}

	prov := &mockProvider{script: []llm.Response{{
		Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn,
	}}}
	recovered := newEngineForSession(t, sess, prov)
	now = now.Add(time.Second)
	recovered.PendingInputQueue = NewPendingInputQueue(sess.Dir, PendingInputQueueOptions{Now: func() time.Time { return now }})
	var (
		policyInputs   []string
		admissionOrder []string
	)
	probe := &pendingAdmissionProbe{queue: recovered.PendingInputQueue, order: &admissionOrder}
	installRuntimeTestModules(t, recovered,
		&runtimeTurnInputPolicyModule{id: "redact-input", apply: func(request runtimemodule.TurnInputRequest) (runtimemodule.TurnInputDecision, error) {
			policyInputs = append(policyInputs, request.Message.FirstText())
			if request.Message.ID == accepted.MessageID {
				return runtimemodule.TurnInputDecision{
					Action:  runtimemodule.TurnInputReplace,
					Message: llm.TextMessage(llm.RoleUser, "redacted turn input"),
				}, nil
			}
			return runtimemodule.TurnInputDecision{Action: runtimemodule.TurnInputAllow}, nil
		}},
		probe,
	)

	if _, err := recovered.TurnMessageWithID(
		context.Background(),
		llm.TextMessage(llm.RoleUser, "new trigger"),
		"turn-after-crash",
	); err != nil {
		t.Fatal(err)
	}
	if want := []string{"turn input before crash", "new trigger"}; !reflect.DeepEqual(policyInputs, want) {
		t.Fatalf("policy inputs = %v, want %v", policyInputs, want)
	}
	if len(prov.histories) != 1 || len(prov.histories[0]) != 3 {
		t.Fatalf("provider history = %+v", prov.histories)
	}
	if got, want := []string{
		prov.histories[0][0].FirstText(),
		prov.histories[0][1].FirstText(),
		prov.histories[0][2].FirstText(),
	}, []string{"queued before crash", "redacted turn input", "new trigger"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("provider input order = %v, want %v", got, want)
	}
	if probe.err != nil {
		t.Fatal(probe.err)
	}
	if want := []string{"observer"}; !reflect.DeepEqual(admissionOrder, want) {
		t.Fatalf("queued admission order = %v, want %v", admissionOrder, want)
	}
	if want := []PendingInputState{PendingInputStateAdmitted}; !reflect.DeepEqual(probe.states, want) {
		t.Fatalf("queued admission states = %v, want %v", probe.states, want)
	}
	records, err := recovered.PendingInputQueue.Records()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{queued.ID, accepted.ID} {
		if records[id].State != PendingInputStateProcessed {
			t.Fatalf("recovered record %q state = %q, want processed", id, records[id].State)
		}
	}
	if status := recovered.PendingInputStatus(); status.PendingCount != 0 {
		t.Fatalf("pending status = %+v, want empty queue", status)
	}
}

func TestTurn_CurrentTurnFollowUpStaysAfterTrigger(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{{
		Message:    llm.TextMessage(llm.RoleAssistant, "done"),
		StopReason: llm.StopEndTurn,
	}}}
	eng, bus := newEngine(t, prov, false)
	var enqueueErr error
	bus.Subscribe(TurnAdmittedType, func(events.Event) {
		_, enqueueErr = eng.EnqueuePendingInput(context.Background(), "queued during admission")
	})

	if _, err := eng.Turn(context.Background(), "current trigger"); err != nil {
		t.Fatal(err)
	}
	if enqueueErr != nil {
		t.Fatalf("enqueue during turn admission: %v", enqueueErr)
	}
	if len(prov.histories) != 1 || len(prov.histories[0]) != 2 {
		t.Fatalf("provider history = %+v", prov.histories)
	}
	if got, want := []string{
		prov.histories[0][0].FirstText(),
		prov.histories[0][1].FirstText(),
	}, []string{"current trigger", "queued during admission"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("provider input order = %v, want %v", got, want)
	}
	if status := eng.PendingInputStatus(); status.PendingCount != 0 {
		t.Fatalf("pending status = %+v, want empty queue", status)
	}
}

func TestTurn_PersistedInputsAfterCurrentTriggerRestoreInOrder(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{{
		Message:    llm.TextMessage(llm.RoleAssistant, "done"),
		StopReason: llm.StopEndTurn,
	}}}
	eng, bus := newEngine(t, prov, false)
	now := time.Date(2026, 8, 17, 8, 0, 0, 0, time.UTC)
	eng.PendingInputQueue = NewPendingInputQueue(eng.currentSession().Dir, PendingInputQueueOptions{
		Now: func() time.Time { return now },
	})
	current, err := eng.PersistPendingMessageWithOptions(
		context.Background(),
		llm.TextMessage(llm.RoleUser, "current persisted trigger"),
		PendingInputOptions{ID: "current-persisted"},
	)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	later, err := eng.PersistPendingMessageWithOptions(
		context.Background(),
		llm.TextMessage(llm.RoleUser, "later durable input"),
		PendingInputOptions{ID: "later-durable"},
	)
	if err != nil {
		t.Fatal(err)
	}
	var enqueueErr error
	bus.Subscribe(TurnAdmittedType, func(events.Event) {
		_, enqueueErr = eng.EnqueuePendingInput(context.Background(), "queued during admission")
	})

	if _, err := eng.TurnMessageWithID(context.Background(), current.Message, "turn-1"); err != nil {
		t.Fatal(err)
	}
	if enqueueErr != nil {
		t.Fatalf("enqueue during turn admission: %v", enqueueErr)
	}
	if len(prov.histories) != 1 || len(prov.histories[0]) != 3 {
		t.Fatalf("provider history = %+v", prov.histories)
	}
	if got, want := []string{
		prov.histories[0][0].FirstText(),
		prov.histories[0][1].FirstText(),
		prov.histories[0][2].FirstText(),
	}, []string{"current persisted trigger", "later durable input", "queued during admission"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("provider input order = %v, want %v", got, want)
	}
	records, err := eng.PendingInputQueue.Records()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{current.ID, later.ID} {
		if records[id].State != PendingInputStateProcessed {
			t.Fatalf("persisted record %q state = %q, want processed", id, records[id].State)
		}
	}
	if status := eng.PendingInputStatus(); status.PendingCount != 0 {
		t.Fatalf("pending status = %+v, want empty queue", status)
	}
}

func TestTurn_LaterAcceptedTurnInputStillRunsPolicy(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{{
		Message:    llm.TextMessage(llm.RoleAssistant, "done"),
		StopReason: llm.StopEndTurn,
	}}}
	eng, _ := newEngine(t, prov, false)
	now := time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC)
	eng.PendingInputQueue = NewPendingInputQueue(eng.currentSession().Dir, PendingInputQueueOptions{
		Now: func() time.Time { return now },
	})
	current, err := eng.PendingInputQueue.Enqueue(
		llm.TextMessage(llm.RoleUser, "current persisted trigger"),
		PendingInputOptions{ID: "current-persisted", TTL: time.Hour},
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	later, err := eng.PendingInputQueue.AdmitTurnInput(
		"later-turn",
		llm.TextMessage(llm.RoleUser, "later accepted turn"),
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	var policyInputs []string
	installRuntimeTestModules(t, eng, &runtimeTurnInputPolicyModule{id: "redact-later", apply: func(request runtimemodule.TurnInputRequest) (runtimemodule.TurnInputDecision, error) {
		policyInputs = append(policyInputs, request.Message.FirstText())
		if request.Message.ID == later.MessageID {
			return runtimemodule.TurnInputDecision{
				Action:  runtimemodule.TurnInputReplace,
				Message: llm.TextMessage(llm.RoleUser, "redacted later turn"),
			}, nil
		}
		return runtimemodule.TurnInputDecision{Action: runtimemodule.TurnInputAllow}, nil
	}})

	if _, err := eng.TurnMessageWithID(context.Background(), current.Message, "current-turn"); err != nil {
		t.Fatal(err)
	}
	if got, want := policyInputs, []string{"current persisted trigger", "later accepted turn"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("policy inputs = %v, want %v", got, want)
	}
	if len(prov.histories) != 1 || len(prov.histories[0]) != 2 {
		t.Fatalf("provider history = %+v", prov.histories)
	}
	if got, want := []string{
		prov.histories[0][0].FirstText(),
		prov.histories[0][1].FirstText(),
	}, []string{"current persisted trigger", "redacted later turn"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("provider input order = %v, want %v", got, want)
	}
	if status := eng.PendingInputStatus(); status.PendingCount != 0 {
		t.Fatalf("pending status = %+v, want empty queue", status)
	}
}

func TestDrainPendingTurnInputClearsPublishedStatus(t *testing.T) {
	eng, bus := newEngine(t, &mockProvider{}, false)
	store := NewStatusStore(StatusSeed{SessionID: "session-1", MaxPendingInputs: eng.effectiveMaxPendingInputs()})
	var lifecycle []string
	bus.Subscribe("*", func(event events.Event) {
		store.Publish(event)
		switch event.Type {
		case "pending_input.queued", PendingInputDrainingType, "pending_input.drained":
			lifecycle = append(lifecycle, event.Type)
		}
	})
	if err := eng.ReserveTurnID("active-turn"); err != nil {
		t.Fatal(err)
	}
	record, err := eng.currentPendingInputQueue().AdmitTurnInput(
		"later-turn",
		llm.TextMessage(llm.RoleUser, "later accepted turn"),
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.EnqueuePersistedPendingMessage(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if snapshot := store.Snapshot(); snapshot.Session.PendingCount != 1 {
		t.Fatalf("pending status before drain = %+v, want one queued input", snapshot.Session)
	}

	eng.mu.Lock()
	err = eng.drainPendingInputLocked(context.Background(), "active-turn")
	eng.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	snapshot := store.Snapshot()
	if snapshot.Session.PendingCount != 0 || snapshot.Session.State != SessionRuntimeTurnActive {
		t.Fatalf("pending status after drain = %+v, want active turn with empty queue", snapshot.Session)
	}
	if got, want := lifecycle, []string{"pending_input.queued", PendingInputDrainingType, "pending_input.drained"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("pending lifecycle = %v, want %v", got, want)
	}
}

func TestTurn_RecoveredAcceptedInputPolicyRejectsFailClosed(t *testing.T) {
	sess, err := session.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	beforeCrash := newEngineForSession(t, sess, &mockProvider{})
	acceptedRecord, err := beforeCrash.PendingInputQueue.AdmitTurnInput(
		"turn-before-crash",
		llm.TextMessage(llm.RoleUser, "blocked before crash"),
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	accepted := acceptedRecord.Message
	laterRecord, err := beforeCrash.PendingInputQueue.AdmitTurnInput(
		"turn-after-blocked",
		llm.TextMessage(llm.RoleUser, "accepted after blocked"),
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	later := laterRecord.Message

	prov := &mockProvider{}
	recovered := newEngineForSession(t, sess, prov)
	installRuntimeTestModules(t, recovered, &runtimeTurnInputPolicyModule{id: "reject-input", apply: func(request runtimemodule.TurnInputRequest) (runtimemodule.TurnInputDecision, error) {
		if request.Message.ID == accepted.ID {
			return runtimemodule.TurnInputDecision{Action: runtimemodule.TurnInputReject, Reason: "blocked"}, nil
		}
		return runtimemodule.TurnInputDecision{Action: runtimemodule.TurnInputAllow}, nil
	}})

	if _, err := recovered.TurnMessageWithID(context.Background(), llm.TextMessage(llm.RoleUser, "new trigger"), "turn-after-crash"); err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("recovered turn error = %v, want policy rejection", err)
	}
	if prov.called != 0 {
		t.Fatalf("provider calls = %d, want none after recovered input rejection", prov.called)
	}
	got := make([]string, 0, len(sess.History))
	for _, message := range sess.History {
		got = append(got, message.FirstText())
	}
	if want := []string{"blocked before crash", "accepted after blocked", "new trigger"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("persisted accepted inputs = %+v, want %v", sess.History, want)
	}
	if !sess.History[0].PolicyBlocked {
		t.Fatalf("recovered rejected input = %+v, want policy_blocked", sess.History[0])
	}
	for _, message := range recovered.ActiveContext().Messages {
		if message.FirstText() == "blocked before crash" {
			t.Fatalf("recovered policy-rejected input remained provider-visible: %+v", recovered.ActiveContext().Messages)
		}
	}
	records, err := recovered.PendingInputQueue.Records()
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if (record.MessageID == accepted.ID || record.MessageID == later.ID || record.Message.FirstText() == "new trigger") && record.State != PendingInputStateProcessed {
			t.Fatalf("accepted record after terminal policy failure = %+v", record)
		}
	}
}

func TestTurn_ReusedTurnIDGetsFreshAcceptedInputIdentity(t *testing.T) {
	sess, err := session.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "first done"), StopReason: llm.StopEndTurn},
		{Message: llm.TextMessage(llm.RoleAssistant, "second done"), StopReason: llm.StopEndTurn},
	}}
	eng := newEngineForSession(t, sess, prov)
	for _, input := range []string{"first input", "second input"} {
		if _, err := eng.TurnMessageWithID(context.Background(), llm.TextMessage(llm.RoleUser, input), "turn-1"); err != nil {
			t.Fatal(err)
		}
	}
	records, err := eng.PendingInputQueue.Records()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("accepted records = %+v, want distinct records for reused turn id", records)
	}
	messageIDs := map[string]bool{}
	for _, record := range records {
		if record.State != PendingInputStateProcessed {
			t.Fatalf("accepted record = %+v, want processed", record)
		}
		messageIDs[record.MessageID] = true
	}
	if len(messageIDs) != 2 {
		t.Fatalf("accepted message ids = %+v, want two", messageIDs)
	}
}

type pendingAdmissionProbe struct {
	queue  *PendingInputQueue
	order  *[]string
	states []PendingInputState
	err    error
}

func (*pendingAdmissionProbe) ID() runtimemodule.ID { return "pending-admission-probe" }

func (p *pendingAdmissionProbe) PendingInputsAdmitted(_ context.Context, admission runtimemodule.PendingInputAdmission) {
	if p.order != nil {
		*p.order = append(*p.order, "observer")
	}
	records, err := p.queue.Records()
	if err != nil {
		p.err = err
		return
	}
	for _, id := range admission.RecordIDs {
		p.states = append(p.states, records[id].State)
	}
}

func TestPromotePendingInputFailsBeforeObserverWhenDurableAdmissionFails(t *testing.T) {
	root := t.TempDir()
	sess, err := session.New(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })
	eng := newEngineForSession(t, sess, &mockProvider{})
	if err := eng.ReserveTurnID("compact-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.EnqueuePendingMessageWithOptions(
		context.Background(),
		llm.TextMessage(llm.RoleUser, "after compact"),
		PendingInputOptions{ID: "event-1", TTL: time.Hour},
	); err != nil {
		t.Fatal(err)
	}
	var observed []string
	probe := &pendingAdmissionProbe{queue: eng.PendingInputQueue, order: &observed}
	registry := runtimemodule.NewRegistry()
	if err := registry.Register(probe); err != nil {
		t.Fatal(err)
	}
	set, err := registry.Seal(context.Background(), runtimemodule.ToolContext{})
	if err != nil {
		t.Fatal(err)
	}
	eng.RuntimeModules = set
	eng.PendingInputQueue.path = t.TempDir()

	_, status, promoted, err := eng.PromotePendingInputTurn("compact-1", "turn-1")
	if err == nil || !strings.Contains(err.Error(), "mark promoted pending input admitted") {
		t.Fatalf("promotion error = %v", err)
	}
	if promoted {
		t.Fatal("pending input was promoted after durable admission failed")
	}
	if status.TurnID != "" || status.PendingCount != 1 || len(observed) != 0 {
		t.Fatalf("status/observer = %+v / %v, want queued and unobserved", status, observed)
	}
	if current := eng.PendingInputStatus(); current.TurnID != "" || current.PendingCount != 1 {
		t.Fatalf("engine status = %+v, want idle with queued input", current)
	}
}

func TestPromotePendingInputKeepsRecordReplayableWhenTurnAdmissionFails(t *testing.T) {
	root := t.TempDir()
	sess, err := session.New(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	eng := newEngineForSession(t, sess, &mockProvider{})
	if err := eng.ReserveTurnID("compact-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.EnqueuePendingMessageWithOptions(
		context.Background(),
		llm.TextMessage(llm.RoleUser, "after compact"),
		PendingInputOptions{ID: "event-1", TTL: time.Hour},
	); err != nil {
		t.Fatal(err)
	}

	var observed []string
	probe := &pendingAdmissionProbe{queue: eng.PendingInputQueue, order: &observed}
	registry := runtimemodule.NewRegistry()
	if err := registry.Register(probe); err != nil {
		t.Fatal(err)
	}
	set, err := registry.Seal(context.Background(), runtimemodule.ToolContext{})
	if err != nil {
		t.Fatal(err)
	}
	eng.RuntimeModules = set
	eng.Bus.Subscribe(PendingInputPromotedType, func(events.Event) {
		observed = append(observed, "pending_input.promoted")
	})
	want := errors.New("admission sync failed")
	eng.Bus.SetCommitter(selectiveFailCommitter{eventType: TurnAdmittedType, err: want})

	_, status, promoted, err := eng.PromotePendingInputTurn("compact-1", "turn-1")
	if !errors.Is(err, want) || !strings.Contains(err.Error(), "commit promoted turn admission") {
		t.Fatalf("promotion error = %v, want %v", err, want)
	}
	if promoted {
		t.Fatal("pending input was promoted after turn admission failed")
	}
	if status.TurnID != "" || status.PendingCount != 1 || len(observed) != 0 {
		t.Fatalf("status/observer = %+v / %v, want queued and unobserved", status, observed)
	}
	if current := eng.PendingInputStatus(); current.TurnID != "" || current.PendingCount != 1 {
		t.Fatalf("engine status = %+v, want idle with queued input", current)
	}
	records, err := eng.PendingInputQueue.Records()
	if err != nil {
		t.Fatal(err)
	}
	record := records["event-1"]
	if record.Origin != PendingInputOriginQueued || record.State != PendingInputStatePending || record.TurnID != "compact-1" {
		t.Fatalf("record = %+v, want queued pending input", record)
	}
	replayable, err := eng.PendingInputQueue.Replayable("later-turn", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayable) != 1 || replayable[0].ID != "event-1" {
		t.Fatalf("replayable records = %+v, want event-1", replayable)
	}
}

func TestPromotePendingInputDefersReentrantQueuedEventUntilAfterPromotion(t *testing.T) {
	eng, bus := newEngine(t, &mockProvider{}, false)
	eng.MaxPendingInputs = 4
	store := NewStatusStore(StatusSeed{SessionID: "session-1", MaxPendingInputs: 4})
	var enqueueErr error
	bus.Subscribe(PendingInputPromotedType, func(events.Event) {
		_, enqueueErr = eng.EnqueuePendingInput(context.Background(), "racing input")
	})
	bus.Subscribe("*", store.Publish)

	if err := eng.ReserveTurnID("compact-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.EnqueuePendingInput(context.Background(), "promoted input"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, err := eng.PromotePendingInputTurn("compact-1", "turn-1"); err != nil {
		t.Fatal(err)
	} else if !ok {
		t.Fatal("pending input was not promoted")
	}
	if enqueueErr != nil {
		t.Fatalf("reentrant enqueue: %v", enqueueErr)
	}

	snapshot := store.Snapshot()
	if snapshot.Session.PendingCount != 1 || snapshot.Session.MaxPendingInputs != 4 {
		t.Fatalf("status queue = %+v, want 1/4", snapshot.Session)
	}
	if status := eng.PendingInputStatus(); status.PendingCount != 1 {
		t.Fatalf("engine queue = %+v, want one pending input", status)
	}
}

func TestDrainPendingInputReportsMessagesQueuedWhileDraining(t *testing.T) {
	eng, bus := newEngine(t, &mockProvider{}, false)
	if err := eng.ReserveTurnID("turn-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.EnqueuePendingInput(context.Background(), "first"); err != nil {
		t.Fatal(err)
	}

	var (
		enqueueErr error
		eventOrder []string
	)
	bus.Subscribe("*", func(event events.Event) {
		if event.Type == PendingInputDrainingType || event.Type == "pending_input.queued" {
			eventOrder = append(eventOrder, event.Type)
		}
	})
	bus.Subscribe(PendingInputDrainingType, func(events.Event) {
		_, enqueueErr = eng.EnqueuePendingInput(context.Background(), "second")
	})
	var drained PendingInputDrainedPayload
	bus.Subscribe("pending_input.drained", func(event events.Event) {
		drained, _ = event.Payload.(PendingInputDrainedPayload)
	})

	eng.mu.Lock()
	err := eng.drainPendingInputLocked(context.Background(), "turn-1")
	eng.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if enqueueErr != nil {
		t.Fatalf("enqueue during drain: %v", enqueueErr)
	}
	if drained.Count != 1 || drained.PendingCount != 1 {
		t.Fatalf("drained payload = %+v", drained)
	}
	if status := eng.PendingInputStatus(); status.PendingCount != 1 {
		t.Fatalf("pending status = %+v", status)
	}
	if len(eventOrder) < 2 || strings.Join(eventOrder[len(eventOrder)-2:], ",") != PendingInputDrainingType+",pending_input.queued" {
		t.Fatalf("drain event order = %v", eventOrder)
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
	admitted := make(chan struct{}, 1)
	phase := make(chan struct{}, 4)
	draining := make(chan struct{}, 1)
	bus.Subscribe(TurnAdmittedType, func(e events.Event) { signal(admitted) })
	bus.Subscribe(TurnPhaseType, func(e events.Event) { signal(phase) })
	bus.Subscribe(PendingInputDrainingType, func(e events.Event) { signal(draining) })
	bus.Subscribe("llm.requested", func(e events.Event) { signal(requested) })
	bus.Subscribe("pending_input.rejected", func(e events.Event) { signal(rejected) })
	done := make(chan error, 1)
	go func() {
		_, err := eng.Turn(context.Background(), "start")
		done <- err
	}()
	waitSignal(t, admitted, TurnAdmittedType)
	waitSignal(t, requested, "llm.requested")
	waitSignal(t, phase, TurnPhaseType)
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
	waitSignal(t, draining, PendingInputDrainingType)
}

func TestEngine_EnqueuePersistedPendingMessageExpiresBeforeIdleAdmission(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	record, err := eng.PersistPendingMessageWithOptions(
		context.Background(),
		llm.TextMessage(llm.RoleUser, "expired external input"),
		PendingInputOptions{ID: "expired-event", TTL: time.Millisecond},
	)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)

	if _, err := eng.EnqueuePersistedPendingMessage(context.Background(), record); !errors.Is(err, ErrPendingInputExpired) {
		t.Fatalf("enqueue expired record error = %v, want ErrPendingInputExpired", err)
	}
	if status := eng.PendingInputStatus(); status.TurnID != "" || status.PendingCount != 0 {
		t.Fatalf("pending status after expired admission = %+v", status)
	}
	records, err := eng.PendingInputQueue.Records()
	if err != nil {
		t.Fatal(err)
	}
	if got := records[record.ID].State; got != PendingInputStateExpired {
		t.Fatalf("record state = %q, want %q", got, PendingInputStateExpired)
	}
}

func TestEngine_DropPersistedPendingMessagePreventsReplay(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	record, err := eng.PersistPendingMessageWithOptions(
		context.Background(),
		llm.TextMessage(llm.RoleUser, "stale external input"),
		PendingInputOptions{ID: "stale-event", TTL: time.Hour},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.DropPersistedPendingMessage(record.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.EnqueuePersistedPendingMessage(context.Background(), record); !errors.Is(err, ErrPendingInputHandled) {
		t.Fatalf("enqueue dropped record error = %v, want ErrPendingInputHandled", err)
	}
	records, err := eng.PendingInputQueue.Records()
	if err != nil {
		t.Fatal(err)
	}
	if got := records[record.ID].State; got != PendingInputStateDropped {
		t.Fatalf("record state = %q, want %q", got, PendingInputStateDropped)
	}
}

func TestRunToolCallEmitsRequestedRunningCompleted(t *testing.T) {
	eng, bus := newEngine(t, &mockProvider{}, false)
	eng.Tools.MustRegister(tools.Tool{
		Name: "echo_status_test",
		Handler: func(context.Context, map[string]any) (string, error) {
			return "hello", nil
		},
	})
	sequence := make(chan string, 3)
	for _, eventType := range []string{
		toolevents.RequestedType,
		toolevents.RunningType,
		toolevents.CompletedType,
	} {
		eventType := eventType
		bus.Subscribe(eventType, func(events.Event) { sequence <- eventType })
	}

	calls := []llm.Block{{
		Type:      llm.BlockToolUse,
		ToolUseID: "tool-1",
		ToolName:  "echo_status_test",
		Input:     map[string]any{"text": "hello"},
	}}
	if err := eng.recordToolBatchLocked(context.Background(), "turn-1", compactionPolicy{}, recordedProviderResponse{
		toolCalls: calls,
		iter:      0,
		messageID: "assistant-test",
	}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		toolevents.RequestedType,
		toolevents.RunningType,
		toolevents.CompletedType,
	} {
		if got := <-sequence; got != want {
			t.Fatalf("event = %q, want %q", got, want)
		}
	}
}

func TestNormalizeGuidedToolFailureResults(t *testing.T) {
	reg := tools.NewRegistry()
	for name, group := range map[string]tools.ToolGroup{
		"observe": tools.ToolGroupObservable,
		"chunk":   tools.ToolGroupChunkedWrite,
		"goal":    tools.ToolGroupSessionState,
		"read":    tools.ToolGroupFile,
	} {
		reg.MustRegister(tools.Tool{
			Name:    name,
			Group:   group,
			Handler: func(context.Context, map[string]any) (string, error) { return "", nil },
		})
	}
	eng := &Engine{Tools: reg}
	hint := func(skill string) string {
		return `For workflows, constraints, and examples, load the full guide with skill_load("` + skill + `").`
	}
	tests := []struct {
		name        string
		toolName    string
		isError     bool
		content     string
		wantContent string
	}{
		{
			name:        "observable error",
			toolName:    "observe",
			isError:     true,
			content:     "boom",
			wantContent: "boom\n\n" + hint("juex-observables"),
		},
		{
			name:        "chunked write error",
			toolName:    "chunk",
			isError:     true,
			content:     "boom",
			wantContent: "boom\n\n" + hint("juex-chunked-write"),
		},
		{
			name:        "session state error",
			toolName:    "goal",
			isError:     true,
			content:     "boom",
			wantContent: "boom\n\n" + hint("juex-session-state"),
		},
		{name: "guided success", toolName: "observe", content: "ok", wantContent: "ok"},
		{name: "unguided error", toolName: "read", isError: true, content: "boom", wantContent: "boom"},
		{name: "unknown tool error", toolName: "missing", isError: true, content: "boom", wantContent: "boom"},
		{
			name:        "existing hint is not duplicated",
			toolName:    "observe",
			isError:     true,
			content:     "boom\n\n" + hint("juex-observables"),
			wantContent: "boom\n\n" + hint("juex-observables"),
		},
		{
			name:        "trailing whitespace is normalized",
			toolName:    "observe",
			isError:     true,
			content:     "boom \n\n",
			wantContent: "boom\n\n" + hint("juex-observables"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const (
				errorText = "original error"
				errorKind = "test"
				rawCause  = "original cause"
			)
			got := eng.normalizeGuidedToolFailureResults([]toolCallResult{{
				Block: llm.Block{
					Type:     llm.BlockToolResult,
					ToolName: tt.toolName,
					Content:  tt.content,
					IsError:  tt.isError,
				},
				Observation: tools.Observation{
					ToolName:  tt.toolName,
					Content:   tt.content,
					Error:     errorText,
					ErrorKind: errorKind,
					RawCause:  rawCause,
				},
			}})
			if got[0].Block.Content != tt.wantContent {
				t.Fatalf("block content = %q, want %q", got[0].Block.Content, tt.wantContent)
			}
			if got[0].Observation.Content != tt.wantContent {
				t.Fatalf("observation content = %q, want %q", got[0].Observation.Content, tt.wantContent)
			}
			if got[0].Observation.Error != errorText ||
				got[0].Observation.ErrorKind != errorKind ||
				got[0].Observation.RawCause != rawCause {
				t.Fatalf("error metadata changed: %+v", got[0].Observation)
			}
		})
	}

	input := []toolCallResult{{
		Block:       llm.Block{Type: llm.BlockToolResult, ToolName: "observe", Content: "boom", IsError: true},
		Observation: tools.Observation{ToolName: "observe", Content: "boom"},
	}}
	got := (&Engine{}).normalizeGuidedToolFailureResults(input)
	if !reflect.DeepEqual(got, input) {
		t.Fatalf("nil registry changed results: got %+v want %+v", got, input)
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
	pb := newTestPromptBuilder("", time.Now)
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

func TestTurn_SerializesUpdateNotesCallsInProviderOrder(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "notes-1", ToolName: NotesToolUpdate, Input: map[string]any{"content": "first"}},
			{Type: llm.BlockToolUse, ToolUseID: "notes-2", ToolName: NotesToolUpdate, Input: map[string]any{"content": "second"}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
	}}, false)
	_, notesStore := installSessionStateModules(t, eng)
	installHookRunner(t, eng, hookRunnerFunc(func(ctx context.Context, req hooks.Request) ([]hooks.Result, error) {
		if req.EventName == hooks.EventPreToolUse && req.ToolName == NotesToolUpdate && req.ToolInput["content"] == "first" {
			select {
			case <-time.After(100 * time.Millisecond):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return nil, nil
	}))

	if out, err := eng.Turn(context.Background(), "rewrite notes twice"); err != nil || out != "done" {
		t.Fatalf("Turn() = %q, %v", out, err)
	}
	snapshot, err := notesStore.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Content != "second" {
		t.Fatalf("notes content = %q, want final provider-ordered rewrite", snapshot.Content)
	}
}

func TestRunToolCalls_SerializesGoalCallsInProviderOrder(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	goalState, _ := installSessionStateModules(t, eng)
	installHookRunner(t, eng, hookRunnerFunc(func(ctx context.Context, req hooks.Request) ([]hooks.Result, error) {
		if req.EventName == hooks.EventPreToolUse && req.ToolName == GoalToolCreate {
			select {
			case <-time.After(100 * time.Millisecond):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return nil, nil
	}))

	results := eng.runToolCalls(context.Background(), "turn-goal-order", testToolExecutions([]llm.Block{
		{
			Type:      llm.BlockToolUse,
			ToolUseID: "goal-create",
			ToolName:  GoalToolCreate,
			Input: map[string]any{
				"description": "ship ordered goal state",
				"acceptance":  "goal updates observe provider order",
			},
		},
		{
			Type:      llm.BlockToolUse,
			ToolUseID: "goal-update",
			ToolName:  GoalToolUpdate,
			Input: map[string]any{
				"status":        string(workmem.GoalStatusSuccess),
				"status_reason": "ordered update applied",
			},
		},
	}))
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	for i, result := range results {
		if result.Block.IsError {
			t.Fatalf("result %d unexpectedly failed: %s", i, result.Block.Content)
		}
	}
	snapshot, err := goalState.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != workmem.GoalStatusSuccess || snapshot.StatusReason != "ordered update applied" {
		t.Fatalf("goal state = %+v, want provider-ordered success update", snapshot)
	}
}

func TestRunToolCalls_SerializesSideSessionCallsInProviderOrder(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	firstStarted := make(chan struct{})
	secondStarted := make(chan struct{}, 1)
	releaseFirst := make(chan struct{})
	eng.Tools.MustRegister(tools.Tool{
		Name:  "side_first",
		Group: tools.ToolGroupSideSession,
		Handler: func(context.Context, map[string]any) (string, error) {
			close(firstStarted)
			<-releaseFirst
			return "first", nil
		},
	})
	eng.Tools.MustRegister(tools.Tool{
		Name:  "side_second",
		Group: tools.ToolGroupSideSession,
		Handler: func(context.Context, map[string]any) (string, error) {
			secondStarted <- struct{}{}
			return "second", nil
		},
	})

	done := make(chan []toolCallResult, 1)
	go func() {
		done <- eng.runToolCalls(context.Background(), "turn-side-order", testToolExecutions([]llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "side-1", ToolName: "side_first", Input: map[string]any{}},
			{Type: llm.BlockToolUse, ToolUseID: "side-2", ToolName: "side_second", Input: map[string]any{}},
		}))
	}()
	<-firstStarted
	select {
	case <-secondStarted:
		t.Fatal("second Side Session tool started before the first completed")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseFirst)
	results := <-done
	if len(results) != 2 || results[0].Block.IsError || results[1].Block.IsError {
		t.Fatalf("results = %+v", results)
	}
	select {
	case <-secondStarted:
	default:
		t.Fatal("second Side Session tool did not run")
	}
}

func TestTurn_AllowsLongToolSequence(t *testing.T) {
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
	if event.EpochID == "" || event.RequestDigest == "" {
		t.Fatalf("retry provenance = %+v", event)
	}
}

func TestCompact_EmitsLLMRetryDiagnostics(t *testing.T) {
	eng, bus := newEngine(t, retryDiagnosticProvider{}, false)
	eng.Compaction = DefaultCompactionPolicy()
	eng.Compaction.KeepRecentTokens = 1
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
	eng, bus := newEngine(t, prov, false)
	var epoch provenance.RequestEpoch
	var errored LLMErroredPayload
	bus.Subscribe(provenance.RequestEpochType, func(event events.Event) {
		epoch = event.Payload.(provenance.RequestEpochPayload).Epoch
	})
	bus.Subscribe("llm.errored", func(event events.Event) {
		errored = event.Payload.(LLMErroredPayload)
	})
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
	if errored.EpochID != epoch.EpochID || errored.RequestDigest != epoch.RequestDigest || !strings.Contains(errored.Error, "response discarded") {
		t.Fatalf("llm.errored = %+v, epoch = %+v", errored, epoch)
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
	if erroredPayload.Outcome == nil || erroredPayload.Outcome.MessageID != result.ID || !reflect.DeepEqual(erroredPayload.Outcome.Block, block) {
		t.Fatalf("cancelled durable outcome = %+v, want message %s block %+v", erroredPayload.Outcome, result.ID, block)
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
	if erroredPayload.Outcome == nil || erroredPayload.Outcome.MessageID != result.ID || !reflect.DeepEqual(erroredPayload.Outcome.Block, block) {
		t.Fatalf("timeout durable outcome = %+v, want message %s block %+v", erroredPayload.Outcome, result.ID, block)
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
	if erroredPayload.Outcome == nil || !reflect.DeepEqual(erroredPayload.Outcome.Block, block) {
		t.Fatalf("deadline durable outcome = %+v, want block %+v", erroredPayload.Outcome, block)
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
	var deltaEvent events.Event
	bus.Subscribe(toolevents.OutputDeltaType, func(e events.Event) {
		deltaEvent = e
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
	if !deltaEvent.Transient {
		t.Fatalf("tool output delta event = %+v, want transient", deltaEvent)
	}
	data, err := os.ReadFile(filepath.Join(eng.Session.Dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), toolevents.OutputDeltaType) || strings.Contains(string(data), "live bytes") {
		t.Fatalf("transient tool output persisted in events.jsonl:\n%s", data)
	}
}

func TestTurn_ToolOutputDeltaCannotAmplifyActiveJournal(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "journal_1", ToolName: "read_journal", Input: map[string]any{}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
	}}
	eng, _ := newEngine(t, prov, false)
	eng.Tools.MustRegister(tools.Tool{
		Name:   "read_journal",
		Schema: map[string]any{"type": "object"},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			path := filepath.Join(eng.Session.Dir, "events.jsonl")
			for range 20 {
				data, err := os.ReadFile(path)
				if err != nil {
					return "", err
				}
				tools.ToolCallEventsFromContext(ctx).Emit(tools.OutputDelta{Text: string(data)})
			}
			return "journal inspected", nil
		},
	})

	if _, err := eng.Turn(context.Background(), "inspect the active journal"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(eng.Session.Dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), toolevents.OutputDeltaType) {
		t.Fatalf("journal contains recursive output deltas:\n%s", data)
	}
	if len(data) > 64<<10 {
		t.Fatalf("journal grew to %d bytes, want bounded metadata-only events", len(data))
	}
}

func TestTurn_BuiltinShellCompletedEventCarriesAuthoritativeContentWithoutStructuredOutputDuplication(t *testing.T) {
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
	if result.Output != "" {
		t.Fatalf("shell event result output = %q, want metadata-only structured result", result.Output)
	}
	if completedPayload.Outcome == nil || !strings.Contains(completedPayload.Outcome.Block.Content, "structured-shell") {
		t.Fatalf("shell event outcome = %+v, want authoritative output", completedPayload.Outcome)
	}
	data, err := os.ReadFile(filepath.Join(eng.Session.Dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"content":"Chunk ID:`) || !strings.Contains(string(data), `structured-shell`) {
		t.Fatalf("shell output missing from durable completion payload:\n%s", data)
	}
	if strings.Contains(string(data), `"output":"structured-shell`) {
		t.Fatalf("shell output duplicated in structured event result:\n%s", data)
	}
}

func TestEmitToolFinishedHandlesPointerShellResult(t *testing.T) {
	bus := events.NewBus()
	eng := &Engine{Bus: bus}
	var completed toolevents.CompletedPayload
	bus.Subscribe(toolevents.CompletedType, func(event events.Event) {
		completed, _ = event.Payload.(toolevents.CompletedPayload)
	})
	original := &tools.ShellResult{Output: "duplicate raw output", OriginalBytes: 20}
	call := llm.Block{Type: llm.BlockToolUse, ToolUseID: "pointer-shell", ToolName: "exec_command"}
	block := llm.Block{Type: llm.BlockToolResult, ToolUseID: call.ToolUseID, ToolName: call.ToolName, Content: "finalized bounded output"}
	observation := tools.NewObservation(tools.ObservationOptions{Content: "earlier output", StructuredResult: original})

	if err := eng.emitToolFinished("turn-1", toolExecutionCall{
		call:    call,
		payload: toolCallPayload(call, 0, 0, "assistant-pointer"),
	}, "result-pointer", block, observation, tools.CallInfo{}); err != nil {
		t.Fatal(err)
	}

	result, ok := completed.Result.(*tools.ShellResult)
	if !ok {
		t.Fatalf("completed result = %#v, want *tools.ShellResult", completed.Result)
	}
	if completed.Outcome == nil || completed.Outcome.Block.Content != block.Content || completed.Preview != "" || result.Output != "" {
		t.Fatalf("completed event = %+v result=%+v", completed, result)
	}
	if original.Output != "duplicate raw output" {
		t.Fatalf("source pointer was mutated: %+v", original)
	}
}

func testToolExecutions(calls []llm.Block) []toolExecutionCall {
	executions := make([]toolExecutionCall, len(calls))
	for index, call := range calls {
		executions[index] = toolExecutionCall{
			call:    call,
			payload: toolCallPayload(call, 0, index, "assistant-test"),
		}
	}
	return executions
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

func TestTurn_ContinuationQueueFailurePreservesCommittedPolicyStateWithoutObservation(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{{
		Message: llm.TextMessage(llm.RoleAssistant, "checkpointed answer"), StopReason: llm.StopEndTurn,
	}}}
	eng, _ := newEngine(t, prov, false)
	policy := &continuationFailureFinishModule{}
	installRuntimeTestModules(t, eng, policy)
	queue := NewPendingInputQueue(eng.Session.Dir, PendingInputQueueOptions{})
	eng.PendingInputQueue = queue
	want := errors.New("injected continuation queue failure")
	queue.fileOps.write = func(file *os.File, body []byte) (int, error) {
		if strings.Contains(string(body), `"kind":"continuation"`) {
			return 0, want
		}
		return file.Write(body)
	}

	if _, err := eng.TurnMessageWithID(context.Background(), llm.TextMessage(llm.RoleUser, "finish once"), "continuation-failure-turn"); !errors.Is(err, want) {
		t.Fatalf("TurnMessageWithID() error = %v, want %v", err, want)
	}
	if !policy.committed || policy.observed != 0 {
		t.Fatalf("finish policy committed/observed = %t/%d, want true/0", policy.committed, policy.observed)
	}
	if prov.called != 1 {
		t.Fatalf("provider calls = %d, want 1", prov.called)
	}
	if status := eng.PendingInputStatus(); status.TurnID != "" || status.PendingCount != 0 {
		t.Fatalf("pending input status = %+v, want closed and empty", status)
	}
	records, err := queue.Records()
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.Message.Kind == llm.MessageKindContinuation {
			t.Fatalf("failed continuation became durable: %+v", record)
		}
	}
	if len(eng.Session.History) != 2 || eng.Session.History[0].FirstText() != "finish once" || eng.Session.History[1].FirstText() != "checkpointed answer" {
		t.Fatalf("transcript = %+v, want committed user and assistant messages", eng.Session.History)
	}
	journal, err := session.ReadEvents(eng.Session.Dir)
	if err != nil {
		t.Fatal(err)
	}
	var completed, errored int
	for _, event := range journal {
		if event.TurnID != "continuation-failure-turn" {
			continue
		}
		switch event.Type {
		case "turn.completed":
			completed++
		case "turn.errored":
			errored++
		}
	}
	if completed != 0 || errored != 1 {
		t.Fatalf("terminal events completed/errored = %d/%d, want 0/1", completed, errored)
	}
}

func TestTurn_CompletionCommitFailureReturnsErrorAndPreservesTranscript(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "answer before completion failure"), StopReason: llm.StopEndTurn},
		{Message: llm.TextMessage(llm.RoleAssistant, "recovered answer"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, false)
	want := errors.New("injected completion commit failure")
	bus.SetCommitter(selectiveSessionCommitter{session: eng.Session, eventType: "turn.completed", err: want})

	if _, err := eng.TurnMessageWithID(context.Background(), llm.TextMessage(llm.RoleUser, "first input"), "failed-completion"); !errors.Is(err, want) {
		t.Fatalf("TurnMessageWithID() error = %v, want %v", err, want)
	}
	if len(eng.Session.History) != 2 || eng.Session.History[0].FirstText() != "first input" || eng.Session.History[1].FirstText() != "answer before completion failure" {
		t.Fatalf("transcript after completion failure = %+v", eng.Session.History)
	}
	journal, err := session.ReadEvents(eng.Session.Dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range journal {
		if event.TurnID == "failed-completion" && event.Type == "turn.completed" {
			t.Fatalf("failed completion event became durable: %+v", event)
		}
	}

	eng.Session.SubscribeBus(bus)
	if out, err := eng.TurnMessageWithID(context.Background(), llm.TextMessage(llm.RoleUser, "later input"), "recovery-turn"); err != nil || out != "recovered answer" {
		t.Fatalf("recovery TurnMessageWithID() = %q, %v", out, err)
	}
	if prov.called != 2 || !strings.Contains(messagesText(prov.histories[1]), "answer before completion failure") {
		t.Fatalf("recovery provider history = %+v, want prior durable assistant response", prov.histories)
	}
	journal, err = session.ReadEvents(eng.Session.Dir)
	if err != nil {
		t.Fatal(err)
	}
	var failedCompleted, failedErrored, recoveryCompleted int
	for _, event := range journal {
		switch {
		case event.TurnID == "failed-completion" && event.Type == "turn.completed":
			failedCompleted++
		case event.TurnID == "failed-completion" && event.Type == "turn.errored":
			failedErrored++
		case event.TurnID == "recovery-turn" && event.Type == "turn.completed":
			recoveryCompleted++
		}
	}
	if failedCompleted != 0 || failedErrored != 1 || recoveryCompleted != 1 {
		t.Fatalf("terminal events failed-completed/failed-errored/recovery-completed = %d/%d/%d, want 0/1/1", failedCompleted, failedErrored, recoveryCompleted)
	}
}

func TestTurn_FinishPolicyAllowsCleanFinishWithoutFailureGate(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, false)

	var failureGateHooks int32
	bus.Subscribe("policy.completed", func(e events.Event) {
		payload, _ := e.Payload.(PolicyCompletedPayload)
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
	installSessionStateModules(t, eng)
	runner, err := hooks.NewRunner(hooks.Config{Commands: []hooks.CommandHook{{
		Name:    "stop-ok",
		Events:  []hooks.EventName{hooks.EventStop},
		Command: runtimeHookCommand("ok"),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	installHookRunner(t, eng, runner)

	var order []string
	bus.Subscribe("finish.attempted", func(e events.Event) {
		order = append(order, "finish.attempted")
	})
	bus.Subscribe("policy.started", func(e events.Event) {
		payload, _ := e.Payload.(PolicyStartedPayload)
		order = append(order, "start:"+payload.Name)
	})
	bus.Subscribe("policy.completed", func(e events.Event) {
		payload, _ := e.Payload.(PolicyCompletedPayload)
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
		"start:goal-completion-gate",
		"done:goal-completion-gate",
		"start:Stop/stop-ok",
		"done:Stop/stop-ok",
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
	bus.Subscribe("policy.started", func(e events.Event) {
		payload, _ := e.Payload.(PolicyStartedPayload)
		if payload.Name == "unresolved-failure-gate" {
			atomic.AddInt32(&failureGateHooks, 1)
		}
	})
	bus.Subscribe("policy.completed", func(e events.Event) {
		payload, _ := e.Payload.(PolicyCompletedPayload)
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
		t.Fatalf("unresolved-failure-gate should not emit policy facts")
	}
}

func TestTurn_FailureLedgerUsesBeforePolicyTransformedInput(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "check_1", ToolName: "check_ready", Input: map[string]any{"path": "provider.txt"}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, false)
	var handlerPath string
	eng.Tools.MustRegister(tools.Tool{
		Name:   "check_ready",
		Schema: map[string]any{"type": "object"},
		Handler: func(_ context.Context, input map[string]any) (string, error) {
			handlerPath, _ = input["path"].(string)
			return "artifact is not ready", errors.New("check failed")
		},
	})
	installRuntimeTestModules(t, eng, &runtimeToolPolicyModule{id: "transform-input", apply: func(request runtimemodule.ToolPolicyRequest) (runtimemodule.ToolPolicyDecision, error) {
		if request.Stage == runtimemodule.ToolPolicyBeforeExecution {
			return runtimemodule.ToolPolicyDecision{
				Action: runtimemodule.ToolPolicyTransform,
				Input:  map[string]any{"path": "effective.txt"},
			}, nil
		}
		return runtimemodule.ToolPolicyDecision{Action: runtimemodule.ToolPolicyAllow}, nil
	}})

	var recorded ToolFailureRecordedPayload
	bus.Subscribe("tool.failure.recorded", func(event events.Event) {
		recorded, _ = event.Payload.(ToolFailureRecordedPayload)
	})
	if _, err := eng.Turn(context.Background(), "finish the artifact"); err != nil {
		t.Fatal(err)
	}
	if handlerPath != "effective.txt" {
		t.Fatalf("handler path = %q", handlerPath)
	}
	wantPath := filepath.Join(eng.WorkDir, "effective.txt")
	if !reflect.DeepEqual(recorded.RelatedPaths, []string{wantPath}) {
		t.Fatalf("related paths = %#v, want %#v", recorded.RelatedPaths, []string{wantPath})
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

func TestTurn_StopHookOtherExitDoesNotBlockAndEmitsHookErrored(t *testing.T) {
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
	installHookRunner(t, eng, runner)

	var errored PolicyErroredPayload
	bus.Subscribe("policy.errored", func(e events.Event) {
		payload, _ := e.Payload.(PolicyErroredPayload)
		if payload.Name == "Stop/stop-fails" {
			errored = payload
		}
	})

	out, err := eng.Turn(context.Background(), "finish")
	if err != nil {
		t.Fatal(err)
	}
	if out != "done" {
		t.Fatalf("out = %q", out)
	}
	if prov.called != 1 {
		t.Fatalf("provider calls = %d, want no retry loop", prov.called)
	}
	if errored.ModuleID != hooks.ModuleID || errored.PolicyPoint != runtimemodule.PolicyPointFinish || errored.ExitCode != 1 || !strings.Contains(errored.Error, "stop hook failed") {
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

func TestToolFailureLedgerResolvesRelativePathsFromExplicitWorkDir(t *testing.T) {
	workDir := t.TempDir()
	target := filepath.Join(workDir, "artifact.txt")
	if err := os.WriteFile(target, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ledger := newToolFailureLedger(workDir)
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
