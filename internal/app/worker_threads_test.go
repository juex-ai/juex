package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/app/modulecatalog"
	workerthreadsmodule "github.com/juex-ai/juex/internal/features/workerthreads"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/framework/agent"
	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
	"github.com/juex-ai/juex/internal/framework/thread"
	"github.com/juex-ai/juex/tests/testsupport/modulestate"
)

type workerProvider struct {
	mu       sync.Mutex
	response string
	err      error
	started  chan struct{}
	release  chan struct{}
	calls    int
	specs    []llm.ToolSpec
	history  []llm.Message
}

func (p *workerProvider) Name() string { return "worker-test" }

func (p *workerProvider) Complete(ctx context.Context, _ string, history []llm.Message, specs []llm.ToolSpec) (llm.Response, error) {
	p.mu.Lock()
	p.calls++
	p.specs = append([]llm.ToolSpec(nil), specs...)
	p.history = append([]llm.Message(nil), history...)
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
	return newWorkerDepthTestApp(t, 1, parentProvider, children...)
}

func newWorkerDepthTestApp(t *testing.T, maxDepth int, parentProvider llm.Provider, children ...llm.Provider) *App {
	t.Helper()
	workDir := t.TempDir()
	stateDir := filepath.Join(workDir, ".juex")
	cfg := config.Config{ModuleInventory: modulecatalog.Inventory(), ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: workDir, AgentStateDir: stateDir}
	cfg.WorkerThreadMaxDepth = maxDepth
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
				ThreadID: options.ThreadID, Alias: options.Alias,
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

func waitWorkerState(t *testing.T, app interface{ Workers() *agent.WorkerManager }, id string, want agent.WorkerThreadState) agent.WorkerThreadStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, err := app.Workers().Status(id)
		if err == nil && status.State == want {
			return status
		}
		time.Sleep(10 * time.Millisecond)
	}
	status, err := app.Workers().Status(id)
	t.Fatalf("Worker %s = %+v, %v; want %s", id, status, err, want)
	return agent.WorkerThreadStatus{}
}

func TestWorkerModuleAbsentAtDefaultDepthLimit(t *testing.T) {
	child := &workerProvider{response: "done"}
	main := newWorkerTestApp(t, &workerProvider{response: "ack"}, child)
	for _, name := range []string{workerthreadsmodule.ToolCreate, workerthreadsmodule.ToolList, workerthreadsmodule.ToolStatus, workerthreadsmodule.ToolSubscribe} {
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
	defer func() { _ = worker.Close() }()
	assertNoWorkerModule(t, worker.Agent)
	if _, err := worker.Run(context.Background(), "execute without delegation"); err != nil {
		t.Fatal(err)
	}
	child.mu.Lock()
	defer child.mu.Unlock()
	for _, spec := range child.specs {
		if strings.HasPrefix(spec.Name, "thread_") {
			t.Fatalf("provider saw Worker schema %s", spec.Name)
		}
	}
	for _, message := range child.history {
		for _, block := range message.Blocks {
			if strings.Contains(block.Text, "thread_create") {
				t.Fatal("provider saw unavailable Worker guidance")
			}
		}
	}
}

func TestWorkerInternalCreateRejectsBeforeStartup(t *testing.T) {
	main := newWorkerTestApp(t, &workerProvider{response: "ack"})
	worker, err := main.ThreadStore.CreateWorker(thread.MainID, "parent", 1)
	if err != nil {
		t.Fatal(err)
	}
	_ = worker.Close()
	before, err := os.ReadFile(main.ThreadStore.IndexPath())
	if err != nil {
		t.Fatal(err)
	}
	provider := &workerProvider{response: "unexpected"}
	if _, err := New(Options{Config: main.cfg, Provider: provider, parentThreadID: worker.ID, DisableMCP: true}); err == nil || !strings.Contains(err.Error(), "max_depth=1") {
		t.Fatalf("error = %v", err)
	}
	after, err := os.ReadFile(main.ThreadStore.IndexPath())
	if err != nil || string(before) != string(after) || provider.calls != 0 {
		t.Fatal("rejected creation had side effects")
	}
}

func assertNoWorkerModule(t *testing.T, worker *agent.Agent) {
	t.Helper()
	if worker.Workers() != nil {
		t.Fatal("capped Thread initialized WorkerManager")
	}
	for _, mod := range worker.Engine.RuntimeModules.Modules() {
		if mod.ID() == workerthreadsmodule.ModuleID {
			t.Fatal("capped Thread registered worker-threads lifecycle contributions")
		}
	}
	for _, name := range []string{workerthreadsmodule.ToolCreate, workerthreadsmodule.ToolList, workerthreadsmodule.ToolStatus, workerthreadsmodule.ToolSend, workerthreadsmodule.ToolSubscribe, workerthreadsmodule.ToolStop, workerthreadsmodule.ToolArchive} {
		if _, ok := worker.Engine.Tools.Get(name); ok {
			t.Fatalf("capped Thread exposes %s", name)
		}
	}
}

func TestWorkerCreatesNestedChildWithCallingThreadAsParent(t *testing.T) {
	childProvider := &workerProvider{response: "done"}
	main := newWorkerDepthTestApp(t, 2, &workerProvider{response: "ack"}, childProvider)
	childStatus, err := main.Workers().Create(context.Background(), "first", "reviewer", "", false)
	if err != nil {
		t.Fatal(err)
	}
	waitWorkerState(t, main, childStatus.ThreadID, agent.WorkerThreadStateIdle)

	childApp, ok := main.ManagedWorkerAgent(childStatus.ThreadID)
	if !ok {
		t.Fatal("Worker is not managed")
	}
	grandchildStatus, err := childApp.Workers().Create(context.Background(), "second", "fact-checker", "", false)
	if err != nil {
		t.Fatal(err)
	}
	waitWorkerState(t, childApp, grandchildStatus.ThreadID, agent.WorkerThreadStateIdle)
	grandchildApp, ok := childApp.ManagedWorkerAgent(grandchildStatus.ThreadID)
	if !ok {
		t.Fatal("Worker is not managed")
	}
	managedGrandchild, ok := main.ManagedWorkerAgent(grandchildStatus.ThreadID)
	if !ok || managedGrandchild != grandchildApp {
		t.Fatalf("managed grandchild = %p, %v; want %p, true", managedGrandchild, ok, grandchildApp)
	}

	if grandchildApp.Thread.ParentThreadID != childStatus.ThreadID {
		t.Fatalf("grandchild parent = %q, want calling Worker %q", grandchildApp.Thread.ParentThreadID, childStatus.ThreadID)
	}
	assertNoWorkerModule(t, grandchildApp)
	managed, err := main.ArchiveManagedWorker(context.Background(), grandchildStatus.ThreadID)
	if err != nil || !managed {
		t.Fatalf("archive managed grandchild = %v, %v; want true, nil", managed, err)
	}
	grandchild, err := main.ThreadStore.OpenArchived(grandchildStatus.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	_ = grandchild.Close()
}

func TestManagedWorkerParentArchiveKeepsRuntimeWhenChildIsActive(t *testing.T) {
	childProvider := &workerProvider{response: "done"}
	main := newWorkerDepthTestApp(t, 2, &workerProvider{response: "ack"}, childProvider)
	parentStatus, err := main.Workers().Create(context.Background(), "parent", "parent-worker", "", false)
	if err != nil {
		t.Fatal(err)
	}
	waitWorkerState(t, main, parentStatus.ThreadID, agent.WorkerThreadStateIdle)
	parentApp, ok := main.ManagedWorkerAgent(parentStatus.ThreadID)
	if !ok {
		t.Fatal("parent Worker is not managed")
	}
	childStatus, err := parentApp.Workers().Create(context.Background(), "child", "child-worker", "", false)
	if err != nil {
		t.Fatal(err)
	}
	waitWorkerState(t, parentApp, childStatus.ThreadID, agent.WorkerThreadStateIdle)
	if err := main.Workers().Archive(context.Background(), parentStatus.ThreadID); err == nil || !strings.Contains(err.Error(), childStatus.ThreadID) {
		t.Fatalf("archive parent error = %v, want active child %s", err, childStatus.ThreadID)
	}
	if managed, ok := main.ManagedWorkerAgent(parentStatus.ThreadID); !ok || managed != parentApp {
		t.Fatalf("parent runtime was lost after rejected archive: %p, %v", managed, ok)
	}
	if _, err := parentApp.Workers().Status(childStatus.ThreadID); err != nil {
		t.Fatalf("child manager remained in transition: %v", err)
	}
}

func TestWorkerCreationPersistsParentAndIsolatesThreadState(t *testing.T) {
	child := &workerProvider{response: "review complete"}
	main := newWorkerTestApp(t, &workerProvider{response: "ack"}, child)
	status, err := main.Workers().Create(context.Background(), "review", "reviewer", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !thread.ValidWorkerID(status.ThreadID) || status.Subscribed {
		t.Fatalf("created Worker = %+v", status)
	}
	if status.Alias != "reviewer" {
		t.Fatalf("Worker alias = %q, want reviewer", status.Alias)
	}
	status = waitWorkerState(t, main, status.ThreadID, agent.WorkerThreadStateIdle)
	managed, _ := main.ManagedWorkerAgent(status.ThreadID)
	if managed == nil || managed.Thread.ParentThreadID != thread.MainID {
		t.Fatalf("managed Worker = %+v", managed)
	}
	parentGoal, parentNotes := modulestate.Stores(main.Engine.ThreadRuntimeSnapshot().Modules)
	childGoal, childNotes := modulestate.Stores(managed.Engine.ThreadRuntimeSnapshot().Modules)
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

func TestManagedWorkerLookupWaitsForCreationOwnership(t *testing.T) {
	main := newWorkerTestApp(t, &workerProvider{response: "ack"})
	created := make(chan *App, 1)
	release := make(chan struct{})
	main.workerFactory = func(options workerThreadChildOptions) (*App, error) {
		child, err := New(Options{
			Config: options.Config, Provider: &workerProvider{response: "done"}, DisableMCP: true,
			ThreadID: options.ThreadID, Alias: options.Alias,
			disableObservables: true, startupContext: options.Context,
		})
		if err != nil {
			return nil, err
		}
		created <- child
		<-release
		return child, nil
	}
	createDone := make(chan error, 1)
	go func() {
		_, err := main.Workers().Create(context.Background(), "work", "owned-worker", "", false)
		createDone <- err
	}()
	child := <-created
	childIdentity, ok := child.ThreadIdentity()
	if !ok {
		t.Fatal("created child lost its Thread identity")
	}
	type lookupResult struct {
		app *agent.Agent
		ok  bool
	}
	lookupDone := make(chan lookupResult, 1)
	go func() {
		worker, found := main.ManagedWorkerAgent(childIdentity.ID)
		lookupDone <- lookupResult{app: worker, ok: found}
	}()
	select {
	case result := <-lookupDone:
		t.Fatalf("lookup escaped creation reservation: %+v", result)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-createDone; err != nil {
		t.Fatal(err)
	}
	result := <-lookupDone
	if !result.ok || result.app != child.Agent {
		t.Fatalf("managed lookup = %p, %v; want %p, true", result.app, result.ok, child)
	}
}

func TestWorkerFactoryInitializationFailureRollsBackReservedIdentity(t *testing.T) {
	wantErr := errors.New("worker initialization failed")
	main := newWorkerTestApp(t, &workerProvider{response: "ack"})
	failureFactories := []runtimemodule.ThreadFactorySpec{{
		ID:      "fail-worker-initialization",
		Enabled: true,
		New: func(context.Context, runtimemodule.ThreadContext) (runtimemodule.Module, error) {
			return nil, wantErr
		},
	}}
	main.workerFactory = func(options workerThreadChildOptions) (*App, error) {
		return New(Options{Config: options.Config, Provider: main.Engine.Provider, DisableMCP: true,
			ThreadID: options.ThreadID, Alias: options.Alias, startupContext: options.Context,
			disableObservables: true, threadModuleFactories: failureFactories,
		})
	}
	if _, err := main.Workers().Create(context.Background(), "work", "retry-worker", "", false); !errors.Is(err, wantErr) {
		t.Fatalf("create error = %v, want %v", err, wantErr)
	}
	entries, err := main.ThreadStore.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ThreadID != thread.MainID {
		t.Fatalf("failed Worker remains published: %+v", entries)
	}
	main.workerFactory = nil
	status, err := main.Workers().Create(context.Background(), "work", "retry-worker", "", false)
	if err != nil {
		t.Fatalf("retry same alias: %v", err)
	}
	if status.Alias != "retry-worker" {
		t.Fatalf("retry status = %+v", status)
	}
}

func TestNewRollsBackWorkerCreatedBeforeThreadInitializationFailure(t *testing.T) {
	wantErr := errors.New("thread initialization failed")
	main := newWorkerTestApp(t, &workerProvider{response: "ack"})
	_, err := New(Options{
		Config: main.cfg, Provider: &workerProvider{response: "unused"}, DisableMCP: true,
		parentThreadID: thread.MainID, Alias: "direct-failure",
		threadModuleFactories: []runtimemodule.ThreadFactorySpec{{
			ID:      "fail-direct-worker-initialization",
			Enabled: true,
			New: func(context.Context, runtimemodule.ThreadContext) (runtimemodule.Module, error) {
				return nil, wantErr
			},
		}},
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("New error = %v, want %v", err, wantErr)
	}
	entries, listErr := main.ThreadStore.List()
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(entries) != 1 || entries[0].ThreadID != thread.MainID {
		t.Fatalf("failed direct Worker remains published: %+v", entries)
	}
}

func TestSubscribedWorkerPublishesTerminalResult(t *testing.T) {
	child := &workerProvider{response: "review complete"}
	main := newWorkerTestApp(t, &workerProvider{response: "ack"}, child)
	status, err := main.Workers().Create(context.Background(), "review", "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	waitWorkerState(t, main, status.ThreadID, agent.WorkerThreadStateIdle)
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
	status, err := main.Workers().Create(context.Background(), "review", "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	<-child.started

	if err := main.NewContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err = main.Workers().Status(status.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Subscribed {
		t.Fatal("Worker result subscription survived New")
	}

	close(child.release)
	waitWorkerState(t, main, status.ThreadID, agent.WorkerThreadStateIdle)
	for _, message := range main.Thread.ReplaySnapshot().Messages {
		if strings.Contains(message.FirstText(), "Worker Thread result") {
			t.Fatal("Worker result reached Main after New cleared the subscription")
		}
	}
}

func TestWorkerStopClosesRuntimeButPreservesActiveThread(t *testing.T) {
	child := &workerProvider{response: "done", started: make(chan struct{}), release: make(chan struct{})}
	main := newWorkerTestApp(t, &workerProvider{response: "ack"}, child)
	status, err := main.Workers().Create(context.Background(), "long work", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	<-child.started
	if err := main.Workers().Stop(context.Background(), status.ThreadID); err != nil {
		t.Fatal(err)
	}
	if _, err := main.ThreadStore.OpenActive(status.ThreadID); err != nil {
		t.Fatalf("stopped Worker was not preserved: %v", err)
	}
}

func TestFailedWorkerReportsFailedAndCanBeArchived(t *testing.T) {
	wantErr := errors.New("review failed")
	main := newWorkerTestApp(t, &workerProvider{response: "ack"}, &workerProvider{err: wantErr})
	status, err := main.Workers().Create(context.Background(), "review", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	status = waitWorkerState(t, main, status.ThreadID, agent.WorkerThreadStateFailed)
	if !strings.Contains(status.LastError, wantErr.Error()) {
		t.Fatalf("Worker error = %q, want %q", status.LastError, wantErr)
	}
	if err := main.Workers().Archive(context.Background(), status.ThreadID); err != nil {
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
	status, err := main.Workers().Create(context.Background(), "work", "archive-me", "", true)
	if err != nil {
		t.Fatal(err)
	}
	waitWorkerState(t, main, status.ThreadID, agent.WorkerThreadStateIdle)
	if err := main.Workers().Archive(context.Background(), status.ThreadID); err == nil {
		t.Fatal("subscribed Worker was archived")
	}
	if _, err := main.Workers().Subscribe(status.ThreadID, false); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		settled := !main.Workers().ShouldDeferContinuation()
		if settled {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := main.Workers().Archive(context.Background(), status.ThreadID); err != nil {
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

func TestWorkerModelValidationPrecedesIdentityReservationAndFactory(t *testing.T) {
	main := newWorkerTestApp(t, &workerProvider{response: "ack"}, &workerProvider{response: "done"})
	original := main.workerFactory
	factoryCalls := 0
	main.workerFactory = func(options workerThreadChildOptions) (*App, error) {
		factoryCalls++
		return original(options)
	}
	if _, err := main.Workers().Create(context.Background(), "work", "model-check", "not-configured:missing", false); err == nil || !strings.Contains(err.Error(), "worker thread model") {
		t.Fatalf("invalid model error = %v", err)
	}
	if factoryCalls != 0 {
		t.Fatalf("invalid model opened %d children", factoryCalls)
	}
	entries, err := main.ThreadStore.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ThreadID != thread.MainID {
		t.Fatalf("invalid model reserved a Worker: %+v", entries)
	}
	status, err := main.Workers().Create(context.Background(), "work", "model-check", "", false)
	if err != nil {
		t.Fatal(err)
	}
	waitWorkerState(t, main, status.ThreadID, agent.WorkerThreadStateIdle)
	if factoryCalls != 1 {
		t.Fatalf("valid model factory calls = %d", factoryCalls)
	}
}
