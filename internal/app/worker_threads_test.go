package app

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/thread"
)

type workerProvider struct {
	mu       sync.Mutex
	response string
	err      error
	started  chan struct{}
	release  chan struct{}
	calls    int
}

func (p *workerProvider) Name() string { return "worker-test" }

func (p *workerProvider) Complete(ctx context.Context, _ string, _ []llm.Message, _ []llm.ToolSpec) (llm.Response, error) {
	p.mu.Lock()
	p.calls++
	first := p.calls == 1
	p.mu.Unlock()
	if first && p.started != nil {
		close(p.started)
	}
	if first && p.release != nil {
		select {
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		case <-p.release:
		}
	}
	if p.err != nil {
		return llm.Response{}, p.err
	}
	return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, p.response), StopReason: llm.StopEndTurn}, nil
}

func newWorkerTestApp(t *testing.T, parentProvider llm.Provider, children ...llm.Provider) *App {
	t.Helper()
	workDir := t.TempDir()
	stateDir := filepath.Join(workDir, ".juex")
	cfg := config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: workDir, AgentStateDir: stateDir}
	var mu sync.Mutex
	next := 0
	app, err := New(Options{
		Config: cfg, Provider: parentProvider, DisableMCP: true,
		workerThreadFactory: func(options workerThreadChildOptions) (*App, error) {
			mu.Lock()
			defer mu.Unlock()
			if next >= len(children) {
				return nil, errors.New("no Worker provider")
			}
			provider := children[next]
			next++
			return New(Options{
				Config: options.Config, Provider: provider, DisableMCP: true,
				parentThreadID: thread.MainID, Alias: options.Alias,
				disableObservables: true, startupContext: options.Context,
			})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.CloseAndWait() })
	return app
}

func waitWorkerState(t *testing.T, app *App, id string, want WorkerThreadState) WorkerThreadStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, err := app.workers.Status(id)
		if err == nil && status.State == want {
			return status
		}
		time.Sleep(10 * time.Millisecond)
	}
	status, err := app.workers.Status(id)
	t.Fatalf("Worker %s = %+v, %v; want %s", id, status, err, want)
	return WorkerThreadStatus{}
}

func TestWorkerToolsRegisterOnEveryActiveThread(t *testing.T) {
	child := &workerProvider{response: "done"}
	main := newWorkerTestApp(t, &workerProvider{response: "ack"}, child)
	for _, name := range []string{WorkerThreadToolCreate, WorkerThreadToolList, WorkerThreadToolStatus, WorkerThreadToolSubscribe} {
		if _, ok := main.Engine.Tools.Get(name); !ok {
			t.Fatalf("Main missing %s", name)
		}
	}
	worker, err := New(Options{
		Config: main.cfg, Provider: child, parentThreadID: thread.MainID,
		disableObservables: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	if _, ok := worker.Engine.Tools.Get(WorkerThreadToolCreate); !ok {
		t.Fatal("Worker missing Worker creation tools")
	}
}

func TestWorkerCreatesNestedChildWithCallingThreadAsParent(t *testing.T) {
	childProvider := &workerProvider{response: "done"}
	main := newWorkerTestApp(t, &workerProvider{response: "ack"}, childProvider)
	childStatus, err := main.workers.Create(context.Background(), "first", "reviewer", "", false)
	if err != nil {
		t.Fatal(err)
	}
	waitWorkerState(t, main, childStatus.ThreadID, WorkerThreadStateIdle)

	main.workers.mu.Lock()
	childApp := main.workers.threads[childStatus.ThreadID].app
	main.workers.mu.Unlock()
	grandchildStatus, err := childApp.workers.Create(context.Background(), "second", "fact-checker", "", false)
	if err != nil {
		t.Fatal(err)
	}
	waitWorkerState(t, childApp, grandchildStatus.ThreadID, WorkerThreadStateIdle)

	grandchild, err := main.ThreadStore.OpenActive(grandchildStatus.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = grandchild.Close() }()
	if grandchild.ParentThreadID != childStatus.ThreadID {
		t.Fatalf("grandchild parent = %q, want calling Worker %q", grandchild.ParentThreadID, childStatus.ThreadID)
	}
}

func TestWorkerCreationPersistsParentAndIsolatesThreadState(t *testing.T) {
	child := &workerProvider{response: "review complete"}
	main := newWorkerTestApp(t, &workerProvider{response: "ack"}, child)
	status, err := main.workers.Create(context.Background(), "review", "reviewer", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !thread.ValidWorkerID(status.ThreadID) || status.Subscribed {
		t.Fatalf("created Worker = %+v", status)
	}
	if status.Alias != "reviewer" {
		t.Fatalf("Worker alias = %q, want reviewer", status.Alias)
	}
	status = waitWorkerState(t, main, status.ThreadID, WorkerThreadStateIdle)
	managed := main.workers.threads[status.ThreadID]
	if managed == nil || managed.app.Thread.ParentThreadID != thread.MainID {
		t.Fatalf("managed Worker = %+v", managed)
	}
	parentGoal, parentNotes := runtime.ThreadStateStoresFromModules(main.Engine.ThreadRuntimeSnapshot().Modules)
	childGoal, childNotes := runtime.ThreadStateStoresFromModules(managed.app.Engine.ThreadRuntimeSnapshot().Modules)
	if parentGoal == childGoal || parentNotes == childNotes {
		t.Fatal("Worker unexpectedly shares Goal or Notes stores with Main")
	}
	entries, err := main.ThreadStore.List()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range entries {
		if entry.ThreadID == status.ThreadID && entry.ParentThreadID == thread.MainID {
			found = true
		}
	}
	if !found {
		t.Fatalf("Worker %s missing from index: %+v", status.ThreadID, entries)
	}
	for _, message := range main.Thread.ReplaySnapshot().Messages {
		if strings.Contains(message.FirstText(), "Worker Thread result") {
			t.Fatal("unsubscribed Worker result reached Main")
		}
	}
}

func TestSubscribedWorkerPublishesTerminalResult(t *testing.T) {
	child := &workerProvider{response: "review complete"}
	main := newWorkerTestApp(t, &workerProvider{response: "ack"}, child)
	status, err := main.workers.Create(context.Background(), "review", "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	waitWorkerState(t, main, status.ThreadID, WorkerThreadStateIdle)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, message := range main.Thread.ReplaySnapshot().Messages {
			if strings.Contains(message.FirstText(), "Worker Thread result") && strings.Contains(message.FirstText(), status.ThreadID) {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("subscribed result for %s was not delivered", status.ThreadID)
}

func TestNewContextClearsWorkerResultSubscription(t *testing.T) {
	child := &workerProvider{response: "review complete", started: make(chan struct{}), release: make(chan struct{})}
	main := newWorkerTestApp(t, &workerProvider{response: "ack"}, child)
	status, err := main.workers.Create(context.Background(), "review", "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	<-child.started

	if err := main.NewContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err = main.workers.Status(status.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Subscribed {
		t.Fatal("Worker result subscription survived New")
	}

	close(child.release)
	waitWorkerState(t, main, status.ThreadID, WorkerThreadStateIdle)
	for _, message := range main.Thread.ReplaySnapshot().Messages {
		if strings.Contains(message.FirstText(), "Worker Thread result") {
			t.Fatal("Worker result reached Main after New cleared the subscription")
		}
	}
}

func TestWorkerStopClosesRuntimeButPreservesActiveThread(t *testing.T) {
	child := &workerProvider{response: "done", started: make(chan struct{}), release: make(chan struct{})}
	main := newWorkerTestApp(t, &workerProvider{response: "ack"}, child)
	status, err := main.workers.Create(context.Background(), "long work", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	<-child.started
	if err := main.workers.Stop(context.Background(), status.ThreadID); err != nil {
		t.Fatal(err)
	}
	if _, err := main.ThreadStore.OpenActive(status.ThreadID); err != nil {
		t.Fatalf("stopped Worker was not preserved: %v", err)
	}
}

func TestFailedWorkerReportsFailedAndCanBeArchived(t *testing.T) {
	wantErr := errors.New("review failed")
	main := newWorkerTestApp(t, &workerProvider{response: "ack"}, &workerProvider{err: wantErr})
	status, err := main.workers.Create(context.Background(), "review", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	status = waitWorkerState(t, main, status.ThreadID, WorkerThreadStateFailed)
	if !strings.Contains(status.LastError, wantErr.Error()) {
		t.Fatalf("Worker error = %q, want %q", status.LastError, wantErr)
	}
	if err := main.workers.Archive(context.Background(), status.ThreadID); err != nil {
		t.Fatalf("archive failed Worker: %v", err)
	}
	archived, err := main.ThreadStore.OpenArchived(status.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = archived.Close() }()
	if archived.Info().ArchivedAt == nil {
		t.Fatal("failed Worker was not archived")
	}
}

func TestWorkerArchiveRequiresSettledSubscriptionAndMovesHistory(t *testing.T) {
	child := &workerProvider{response: "done"}
	main := newWorkerTestApp(t, &workerProvider{response: "ack"}, child)
	status, err := main.workers.Create(context.Background(), "work", "archive-me", "", true)
	if err != nil {
		t.Fatal(err)
	}
	waitWorkerState(t, main, status.ThreadID, WorkerThreadStateIdle)
	if err := main.workers.Archive(context.Background(), status.ThreadID); err == nil {
		t.Fatal("subscribed Worker was archived")
	}
	if _, err := main.workers.Subscribe(status.ThreadID, false); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		main.workers.mu.Lock()
		managed := main.workers.threads[status.ThreadID]
		settled := managed != nil && managed.resultHandoffs == 0
		main.workers.mu.Unlock()
		if settled {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := main.workers.Archive(context.Background(), status.ThreadID); err != nil {
		t.Fatal(err)
	}
	archived, err := main.ThreadStore.OpenArchived(status.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = archived.Close() }()
	if archived.Info().ArchivedAt == nil || archived.Alias != "archive-me" {
		t.Fatalf("archived info = %+v", archived.Info())
	}
}
