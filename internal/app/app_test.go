package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/eventmedia"
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/mcp"
	"github.com/juex-ai/juex/internal/observable"
	"github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/runtime/workmem"
	"github.com/juex-ai/juex/internal/thread"
)

type stubProvider struct {
	replies   []llm.Response
	calls     int
	systems   []string
	histories [][]llm.Message
}

type blockingAppProvider struct {
	started     chan struct{}
	release     chan struct{}
	releaseOnce sync.Once
	mu          sync.Mutex
	calls       int
	histories   [][]llm.Message
}

type failOnceEventCommitter struct {
	delegate  events.Committer
	eventType string
	err       error
	failed    bool
}

func (c *failOnceEventCommitter) Commit(event events.Event) (events.Event, error) {
	if event.Type == c.eventType && !c.failed {
		c.failed = true
		return events.Event{}, c.err
	}
	return c.delegate.Commit(event)
}

func newBlockingAppProvider() *blockingAppProvider {
	return &blockingAppProvider{started: make(chan struct{}), release: make(chan struct{})}
}

func (p *blockingAppProvider) Release() {
	p.releaseOnce.Do(func() { close(p.release) })
}

func (p *blockingAppProvider) Name() string { return "blocking" }

func (p *blockingAppProvider) Complete(ctx context.Context, _ string, history []llm.Message, _ []llm.ToolSpec) (llm.Response, error) {
	p.mu.Lock()
	index := p.calls
	p.calls++
	p.histories = append(p.histories, append([]llm.Message(nil), history...))
	p.mu.Unlock()
	if index == 0 {
		close(p.started)
		select {
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		case <-p.release:
		}
		return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "first"), StopReason: llm.StopEndTurn}, nil
	}
	return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "handled queued event"), StopReason: llm.StopEndTurn}, nil
}

func testObservationRecord(id string) observable.ObservationRecord {
	now := time.Date(2026, 7, 6, 10, 0, 0, 0, time.UTC)
	return observable.ObservationRecord{
		ID: id, ObservableID: "test-events", Kind: "test_notification", Severity: "info",
		WindowStart: now, WindowEnd: now.Add(10 * time.Second), Content: "hello",
		State: observable.ObservationStateRecorded, CreatedAt: now,
	}
}

func mustWriteAppTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (s *stubProvider) Name() string { return "stub" }

func (s *stubProvider) Complete(_ context.Context, system string, history []llm.Message, _ []llm.ToolSpec) (llm.Response, error) {
	if s.calls >= len(s.replies) {
		return llm.Response{}, errors.New("stub exhausted")
	}
	s.systems = append(s.systems, system)
	s.histories = append(s.histories, append([]llm.Message(nil), history...))
	response := s.replies[s.calls]
	s.calls++
	return response, nil
}

func newStubApp(t *testing.T, replies ...llm.Response) (*App, *stubProvider) {
	t.Helper()
	workDir := t.TempDir()
	provider := &stubProvider{replies: replies}
	app, err := New(Options{
		Config: config.Config{
			ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: workDir,
			AgentStateDir: filepath.Join(workDir, ".juex"),
		},
		Provider: provider,
		WorkDir:  workDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })
	return app, provider
}

func TestAppClosePausesAndResumesDeferredCleanup(t *testing.T) {
	closeCalls := 0
	laterCleanupCalls := 0
	a := &App{cleanup: []func() error{
		func() error {
			closeCalls++
			if closeCalls == 1 {
				return &observable.CloseDeferredError{}
			}
			return nil
		},
		func() error {
			laterCleanupCalls++
			return nil
		},
	}}
	var deferred *observable.CloseDeferredError
	if err := a.Close(); !errors.As(err, &deferred) {
		t.Fatalf("first Close error = %v, want CloseDeferredError", err)
	}
	if laterCleanupCalls != 0 {
		t.Fatalf("later cleanup calls after deferred Close = %d, want 0", laterCleanupCalls)
	}
	if err := a.CloseAndWait(); err != nil {
		t.Fatalf("CloseAndWait = %v", err)
	}
	if closeCalls != 2 || laterCleanupCalls != 1 {
		t.Fatalf("cleanup calls = first:%d later:%d", closeCalls, laterCleanupCalls)
	}
}

func TestAppConcurrentCloseReturnsWaitableResult(t *testing.T) {
	cleanupStarted := make(chan struct{})
	releaseCleanup := make(chan struct{})
	a := &App{cleanup: []func() error{func() error {
		close(cleanupStarted)
		<-releaseCleanup
		return nil
	}}}
	activeResult := make(chan error, 1)
	go func() { activeResult <- a.CloseAndWait() }()
	<-cleanupStarted
	concurrentResult := make(chan error, 1)
	go func() { concurrentResult <- a.Close() }()
	select {
	case err := <-concurrentResult:
		var deferred interface{ Wait() error }
		if !errors.As(err, &deferred) {
			t.Fatalf("concurrent Close error = %v, want waitable result", err)
		}
	case <-time.After(time.Second):
		t.Fatal("concurrent Close blocked behind active cleanup")
	}
	close(releaseCleanup)
	if err := <-activeResult; err != nil {
		t.Fatalf("CloseAndWait = %v", err)
	}
}

func TestDurationSecondsCeilsAndCaps(t *testing.T) {
	if got := durationSeconds(1500 * time.Millisecond); got != 2 {
		t.Fatalf("durationSeconds(1.5s) = %d, want 2", got)
	}
	if got := durationSeconds(10 * time.Minute); got != 300 {
		t.Fatalf("durationSeconds(10m) = %d, want 300", got)
	}
}

func TestAppRunAdmittedTurnAfterCloseDoesNotReopenJournal(t *testing.T) {
	a, provider := newStubApp(t, llm.Response{
		Message: llm.TextMessage(llm.RoleAssistant, "must not run"), StopReason: llm.StopEndTurn,
	})
	journalPath := a.Thread.CurrentGenerationJournalPath()
	if err := a.CloseAndWait(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(journalPath); err != nil {
		t.Fatal(err)
	}
	_, err := a.RunAdmittedTurn(context.Background(), "late-turn", llm.TextMessage(llm.RoleUser, "late"))
	if !errors.Is(err, ErrThreadUnavailable) {
		t.Fatalf("RunAdmittedTurn error = %v, want ErrThreadUnavailable", err)
	}
	if provider.calls != 0 {
		t.Fatalf("provider calls after close = %d, want 0", provider.calls)
	}
	if _, err := os.Stat(journalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("closed journal was reopened: %v", err)
	}
}

func TestAppModelCandidateInjectionPrecedence(t *testing.T) {
	dir := t.TempDir()
	primary := &stubProvider{}
	backup := &stubProvider{}
	injectedSingle := &stubProvider{}
	health := llm.NewModelHealth(llm.ModelHealthOptions{})
	a, err := New(Options{
		Config: config.Config{
			ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: dir,
			AgentStateDir: filepath.Join(dir, ".juex"), Models: []string{"openai:m", "missing:model"},
			NotifyModelChanges: true,
		},
		Provider: injectedSingle,
		ModelCandidates: []runtime.ModelCandidate{
			{Ref: "primary:model", Provider: primary, ContextWindow: 128000},
			{Ref: "backup:model", Provider: backup, ContextWindow: 64000},
		},
		ModelHealth: health,
		WorkDir:     dir, DisableMCP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if len(a.Engine.ModelCandidates) != 2 || a.Engine.Provider != primary || a.Engine.ModelHealth != health || !a.Engine.NotifyModelChanges {
		t.Fatalf("engine wiring = provider:%T candidates:%+v health:%p", a.Engine.Provider, a.Engine.ModelCandidates, a.Engine.ModelHealth)
	}
}

func TestAppInjectedSingleProviderDisablesConfiguredFallback(t *testing.T) {
	dir := t.TempDir()
	provider := &stubProvider{}
	a, err := New(Options{
		Config: config.Config{
			ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: dir,
			AgentStateDir: filepath.Join(dir, ".juex"), Models: []string{"openai:m", "missing:model"},
		},
		Provider: provider, WorkDir: dir, DisableMCP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if a.Engine.Provider != provider || len(a.Engine.ModelCandidates) != 0 {
		t.Fatalf("injected provider wiring = provider:%T candidates:%+v", a.Engine.Provider, a.Engine.ModelCandidates)
	}
}

func TestAppDeliverObservationStartsTurnAndPreservesMessageKind(t *testing.T) {
	a, provider := newStubApp(t, llm.Response{
		Message: llm.TextMessage(llm.RoleAssistant, "ack"), StopReason: llm.StopEndTurn,
	})
	record := testObservationRecord("obs-delivered")
	outcome, err := a.DeliverObservation(context.Background(), record)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State != observable.ObservationStateDelivered || outcome.TargetThread != thread.MainID {
		t.Fatalf("delivery outcome = %+v", outcome)
	}
	if provider.calls != 1 || len(provider.histories) != 1 || len(provider.histories[0]) == 0 {
		t.Fatalf("provider calls/history = %d/%+v", provider.calls, provider.histories)
	}
	message := provider.histories[0][0]
	if message.Kind != llm.MessageKindObservation {
		t.Fatalf("message kind = %q, want observation", message.Kind)
	}
	for _, want := range []string{"Observable observation", "observation_id: " + record.ID, "content_bytes: 5", "content:\nhello"} {
		if !strings.Contains(message.FirstText(), want) {
			t.Fatalf("observation text missing %q:\n%s", want, message.FirstText())
		}
	}
}

func TestRenderObservationTextEncodesStoredAttachmentDisplayName(t *testing.T) {
	const displayName = "evil artifact=x (text/plain, 1 bytes,.txt"
	record := testObservationRecord("obs-attachment-name")
	report := eventmedia.ValidationReport{Valid: []eventmedia.ValidatedAttachment{{
		Ref: eventmedia.AttachmentRef{
			Path: "event-media/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.txt",
			Name: displayName,
		},
		ArtifactPath:  "event-media/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.txt",
		MediaType:     "text/plain",
		OriginalBytes: 12,
	}}}

	text := renderObservationText(record, report)
	if !strings.Contains(text, `source="evil artifact=x (text/plain, 1 bytes,.txt" artifact=event-media/`) {
		t.Fatalf("observation text did not preserve display name:\n%s", text)
	}
}

func TestRenderMCPNotificationTextDelimitsContent(t *testing.T) {
	content := "first line\nmeta:\nstill notification content\n"
	text := renderMCPNotificationText(mcp.Notification{
		ServerName: "wechat-wire",
		Content:    content,
	}, "notification", eventmedia.ValidationReport{}, nil)
	renderedContent := strings.TrimRight(content, "\n")
	want := fmt.Sprintf("content_bytes: %d\ncontent:\n%s", len(renderedContent), renderedContent)
	if !strings.Contains(text, want) {
		t.Fatalf("MCP notification text missing content boundary %q:\n%s", want, text)
	}
}

func TestAppDeliverObservationQueuesDuringActiveTurn(t *testing.T) {
	a, provider := newStubApp(t)
	if err := a.Engine.ReserveTurnID("turn-active"); err != nil {
		t.Fatal(err)
	}
	record := testObservationRecord("obs-queued")
	outcome, err := a.DeliverObservation(context.Background(), record)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State != observable.ObservationStateQueued || outcome.PendingInputID == "" {
		t.Fatalf("delivery outcome = %+v, want queued", outcome)
	}
	if provider.calls != 0 {
		t.Fatalf("provider calls = %d, want 0", provider.calls)
	}
	records, err := a.Engine.PendingInputQueue.Records()
	if err != nil {
		t.Fatal(err)
	}
	pending := records[outcome.PendingInputID]
	if pending.ID == "" || pending.Message.Kind != llm.MessageKindObservation || pending.State != runtime.PendingInputStatePending {
		t.Fatalf("pending record = %+v", pending)
	}
}

func TestAppUsesStableMainThreadAndReopensItsJournal(t *testing.T) {
	workDir := t.TempDir()
	stateDir := filepath.Join(workDir, ".juex")
	cfg := config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: workDir, AgentStateDir: stateDir}
	first, err := New(Options{
		Config: cfg,
		Provider: &stubProvider{replies: []llm.Response{{
			Message: llm.TextMessage(llm.RoleAssistant, "remembered"), StopReason: llm.StopEndTurn,
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Thread.ID != thread.MainID || first.Thread.Alias != thread.MainAlias {
		t.Fatalf("Main identity = %q/%q", first.Thread.ID, first.Thread.Alias)
	}
	if first.Thread.Dir != filepath.Join(stateDir, "threads", thread.MainID) {
		t.Fatalf("Main dir = %q", first.Thread.Dir)
	}
	if output, err := first.Run(context.Background(), "remember this"); err != nil || output != "remembered" {
		t.Fatalf("Run = %q, %v", output, err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := New(Options{Config: cfg, Provider: &stubProvider{}})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if second.Thread.ID != thread.MainID {
		t.Fatalf("reopened id = %q", second.Thread.ID)
	}
	if got := len(second.Thread.ReplaySnapshot().Messages); got != 2 {
		t.Fatalf("persisted messages = %d, want 2", got)
	}
}

func TestAppRecoversInterruptedContextRenewalBeforeBuildingModules(t *testing.T) {
	for _, test := range []struct {
		name      string
		committed bool
	}{
		{name: "before Generation commit"},
		{name: "after Generation commit", committed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			workDir := t.TempDir()
			cfg := config.Config{
				ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: workDir,
				AgentStateDir: filepath.Join(workDir, ".juex"),
			}
			first, err := New(Options{Config: cfg, Provider: &stubProvider{}, DisableMCP: true})
			if err != nil {
				t.Fatal(err)
			}
			goal := workmem.NewGoalStateStore(first.Thread.Dir, workmem.GoalStateOptions{})
			notes := workmem.NewNotesStore(first.Thread.Dir)
			if _, err := goal.Create("recover staged state", "respect the Generation boundary"); err != nil {
				t.Fatal(err)
			}
			if _, err := notes.Update("- [ ] recover staged Notes"); err != nil {
				t.Fatal(err)
			}
			generationID := first.Thread.Projection().CurrentGeneration.ID
			for _, path := range []string{goal.Path, filepath.Join(first.Thread.Dir, workmem.NotesFileName)} {
				if err := os.Rename(path, path+".context-renewal-"+generationID); err != nil {
					t.Fatal(err)
				}
			}
			if test.committed {
				if _, err := first.Thread.BeginNewGeneration(); err != nil {
					t.Fatal(err)
				}
			}
			if err := first.Close(); err != nil {
				t.Fatal(err)
			}

			cfg.Modules = config.ModulePolicy{
				string(runtime.GoalModuleID):  {Enabled: false},
				string(runtime.NotesModuleID): {Enabled: false},
			}
			restarted, err := New(Options{Config: cfg, Provider: &stubProvider{}, DisableMCP: true})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = restarted.Close() })

			goalSnapshot, goalErr := workmem.NewGoalStateStore(restarted.Thread.Dir, workmem.GoalStateOptions{}).StatusSnapshot()
			notesSnapshot, notesErr := workmem.NewNotesStore(restarted.Thread.Dir).StatusSnapshot()
			if test.committed {
				if goalErr != nil || goalSnapshot != nil || notesErr != nil || notesSnapshot != nil {
					t.Fatalf("committed clear recovered old state: Goal=%+v/%v Notes=%+v/%v", goalSnapshot, goalErr, notesSnapshot, notesErr)
				}
				return
			}
			if goalErr != nil || goalSnapshot == nil || notesErr != nil || notesSnapshot == nil {
				t.Fatalf("pre-commit clear did not restore state: Goal=%+v/%v Notes=%+v/%v", goalSnapshot, goalErr, notesSnapshot, notesErr)
			}
		})
	}
}

func TestAppNewContextPreservesJournalAndScratchpad(t *testing.T) {
	app, _ := newStubApp(t)
	if err := app.Thread.Append(llm.TextMessage(llm.RoleUser, "old context")); err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(app.Thread.ScratchpadDir(), "work.md")
	if err := os.WriteFile(scratch, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := app.NewContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	replay := app.Thread.ReplaySnapshot()
	if replay.Projection.CurrentGeneration.ID != "g000002" || len(replay.Activities) != 1 {
		t.Fatalf("generation/activity = %s/%+v", replay.Projection.CurrentGeneration.ID, replay.Activities)
	}
	if len(replay.Messages) != 0 || len(app.Thread.History) != 0 {
		t.Fatalf("current Generation messages = %d/%d, want empty", len(replay.Messages), len(app.Thread.History))
	}
	page, err := app.Thread.Timeline("", 10)
	if err != nil {
		t.Fatal(err)
	}
	foundHistorical := false
	for _, item := range page.Items {
		if item.Message != nil && item.Message.FirstText() == "old context" {
			foundHistorical = true
		}
	}
	if !foundHistorical {
		t.Fatalf("timeline omitted previous Generation message: %#v", page.Items)
	}
	if body, err := os.ReadFile(scratch); err != nil || string(body) != "keep" {
		t.Fatalf("scratchpad = %q, %v", body, err)
	}
}

func TestAppPromptUsesThreadScratchpad(t *testing.T) {
	app, provider := newStubApp(t, llm.Response{
		Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn,
	})
	if _, err := app.Run(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if len(provider.systems) != 1 || !strings.Contains(provider.systems[0], app.Thread.ScratchpadDir()) {
		t.Fatalf("system prompt missing Thread scratchpad: %q", provider.systems)
	}
}

func TestWorkerRuntimeHasOwnStateAndNoObservableManager(t *testing.T) {
	workDir := t.TempDir()
	stateDir := filepath.Join(workDir, ".juex")
	cfg := config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: workDir, AgentStateDir: stateDir}
	main, err := New(Options{Config: cfg, Provider: &stubProvider{}})
	if err != nil {
		t.Fatal(err)
	}
	defer main.Close()
	worker, err := New(Options{
		Config: cfg, Provider: &stubProvider{}, parentThreadID: thread.MainID,
		disableObservables: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	if worker.Thread.ParentThreadID != thread.MainID || worker.Thread.Dir == main.Thread.Dir {
		t.Fatalf("Worker identity = %+v", worker.Thread.Info())
	}
	if worker.Observables() != nil {
		t.Fatal("Worker unexpectedly owns Observable manager")
	}
}
