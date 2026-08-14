package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/hooks"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/session"
)

type scriptedSideProvider struct {
	mu      sync.Mutex
	started chan string
	release chan struct{}
	calls   int
}

type failingSideProvider struct{ err error }

type barrierSideProvider struct {
	started chan struct{}
	release chan struct{}
}

type stubbornSideProvider struct {
	started chan struct{}
	release chan struct{}
}

type sideHookRunnerFunc func(context.Context, hooks.Request) ([]hooks.Result, error)

func (f sideHookRunnerFunc) Run(ctx context.Context, req hooks.Request) ([]hooks.Result, error) {
	return f(ctx, req)
}

func (p *stubbornSideProvider) Name() string { return "side-stubborn" }

func (p *stubbornSideProvider) Complete(context.Context, string, []llm.Message, []llm.ToolSpec) (llm.Response, error) {
	select {
	case p.started <- struct{}{}:
	default:
	}
	<-p.release
	return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "released"), StopReason: llm.StopEndTurn}, nil
}

type sideToolDuringDeliveryProvider struct {
	started chan struct{}
	release chan struct{}
	app     *App
}

type goalSideQueueProvider struct {
	mu      sync.Mutex
	started chan struct{}
	release chan struct{}
	app     *App
	calls   int
}

func (p *sideToolDuringDeliveryProvider) Name() string { return "side-delivery-tool" }

func (p *sideToolDuringDeliveryProvider) Complete(_ context.Context, _ string, history []llm.Message, _ []llm.ToolSpec) (llm.Response, error) {
	for _, message := range history {
		if message.Kind != llm.MessageKindSideSession {
			continue
		}
		select {
		case p.started <- struct{}{}:
		default:
		}
		<-p.release
		_, _ = p.app.Engine.Tools.Call(context.Background(), SideSessionToolList, map[string]any{})
		return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "delivery finished"), StopReason: llm.StopEndTurn}, nil
	}
	return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "primary"), StopReason: llm.StopEndTurn}, nil
}

func (p *goalSideQueueProvider) Name() string { return "goal-side-queue" }

func (p *goalSideQueueProvider) Complete(ctx context.Context, _ string, history []llm.Message, _ []llm.ToolSpec) (llm.Response, error) {
	p.mu.Lock()
	p.calls++
	call := p.calls
	p.mu.Unlock()
	if call == 1 {
		close(p.started)
		select {
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		case <-p.release:
		}
		return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "waiting for subscribed result"), StopReason: llm.StopEndTurn}, nil
	}
	for _, message := range history {
		if message.Kind != llm.MessageKindSideSession {
			continue
		}
		reason := "subscribed result incorporated"
		if _, err := p.app.Engine.GoalState.Update(runtime.GoalStateUpdate{
			Status:       runtime.GoalStatusSuccess,
			StatusReason: &reason,
		}); err != nil {
			return llm.Response{}, err
		}
		return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "subscribed result incorporated"), StopReason: llm.StopEndTurn}, nil
	}
	return llm.Response{}, errors.New("queued Side Session result missing from provider history")
}

func (p *barrierSideProvider) Name() string { return "side-barrier" }

func (p *barrierSideProvider) Complete(ctx context.Context, _ string, history []llm.Message, _ []llm.ToolSpec) (llm.Response, error) {
	p.started <- struct{}{}
	select {
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	case <-p.release:
	}
	return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "answer: "+history[len(history)-1].FirstText()), StopReason: llm.StopEndTurn}, nil
}

func (p *failingSideProvider) Name() string { return "side-failure" }

func (p *failingSideProvider) Complete(context.Context, string, []llm.Message, []llm.ToolSpec) (llm.Response, error) {
	return llm.Response{}, p.err
}

func (p *scriptedSideProvider) Name() string { return "side-test" }

func (p *scriptedSideProvider) Complete(ctx context.Context, _ string, history []llm.Message, _ []llm.ToolSpec) (llm.Response, error) {
	input := history[len(history)-1].FirstText()
	p.mu.Lock()
	p.calls++
	call := p.calls
	p.mu.Unlock()
	if p.started != nil {
		select {
		case p.started <- input:
		default:
		}
	}
	if call == 1 && p.release != nil {
		select {
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		case <-p.release:
		}
	}
	return llm.Response{
		Message:    llm.TextMessage(llm.RoleAssistant, "answer: "+input),
		StopReason: llm.StopEndTurn,
	}, nil
}

func newSideSessionTestApp(t *testing.T, parentProvider llm.Provider, childProviders ...llm.Provider) *App {
	t.Helper()
	workDir := t.TempDir()
	stateDir := filepath.Join(workDir, ".juex")
	var providerMu sync.Mutex
	nextProvider := 0
	a, err := New(Options{
		Config: config.Config{
			ProviderID:    "openai",
			APIKey:        "test",
			Model:         "primary",
			WorkDir:       workDir,
			AgentStateDir: stateDir,
		},
		Provider:   parentProvider,
		WorkDir:    workDir,
		DisableMCP: true,
		sideSessionFactory: func(opts sideSessionChildOptions) (*App, error) {
			providerMu.Lock()
			defer providerMu.Unlock()
			if nextProvider >= len(childProviders) {
				return nil, errors.New("no child provider available")
			}
			provider := childProviders[nextProvider]
			nextProvider++
			return New(Options{
				Config:                  opts.Config,
				Provider:                provider,
				WorkDir:                 opts.Config.WorkDir,
				DisableMCP:              true,
				SessionMode:             SessionModeNewSide,
				disableSideSessionTools: true,
				sharedGoalState:         opts.GoalState,
				sharedNotes:             opts.Notes,
				startupContext:          opts.Context,
			})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.CloseAndWait() })
	return a
}

func callSideTool(t *testing.T, a *App, name string, input map[string]any) map[string]any {
	t.Helper()
	result, err := a.Engine.Tools.Call(context.Background(), name, input)
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(result), &decoded); err != nil {
		t.Fatalf("decode %s result %q: %v", name, result, err)
	}
	return decoded
}

func waitForSideState(t *testing.T, a *App, id string, want SideSessionState) SideSessionStatus {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		status, err := a.sideSessions.Status(id)
		if err == nil && status.State == want {
			return status
		}
		time.Sleep(10 * time.Millisecond)
	}
	status, err := a.sideSessions.Status(id)
	t.Fatalf("side session %s state = %+v, err=%v; want %s", id, status, err, want)
	return SideSessionStatus{}
}

func waitForGoalContinuationDeferral(t *testing.T, a *App, want bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if a.sideSessions.shouldDeferGoalContinuation() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("goal continuation deferral = %t, want %t", a.sideSessions.shouldDeferGoalContinuation(), want)
}

func TestSideSessionToolsRegisterOnlyForActivePrimary(t *testing.T) {
	primary := newSideSessionTestApp(t, &scriptedSideProvider{})
	for _, name := range []string{
		SideSessionToolCreate,
		SideSessionToolList,
		SideSessionToolStatus,
		SideSessionToolSend,
		SideSessionToolSubscribe,
		SideSessionToolStop,
	} {
		if _, ok := primary.Engine.Tools.Get(name); !ok {
			t.Errorf("primary missing tool %q", name)
		}
	}

	workDir := t.TempDir()
	side, err := New(Options{
		Config: config.Config{
			ProviderID:    "openai",
			APIKey:        "test",
			Model:         "side",
			WorkDir:       workDir,
			AgentStateDir: filepath.Join(workDir, ".juex"),
		},
		Provider:    &scriptedSideProvider{},
		WorkDir:     workDir,
		DisableMCP:  true,
		SessionMode: SessionModeNewSide,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := side.CloseAndWait(); err != nil {
			t.Errorf("close side app: %v", err)
		}
	})
	for _, name := range []string{SideSessionToolCreate, SideSessionToolList, SideSessionToolStatus} {
		if _, ok := side.Engine.Tools.Get(name); ok {
			t.Errorf("side session unexpectedly registered tool %q", name)
		}
	}
}

func TestSideSessionCreateSendAndStatus(t *testing.T) {
	child := &scriptedSideProvider{started: make(chan string, 2), release: make(chan struct{})}
	parent := newSideSessionTestApp(t, &scriptedSideProvider{}, child)

	created := callSideTool(t, parent, SideSessionToolCreate, map[string]any{"query": "research one"})
	id, _ := created["session_id"].(string)
	if id == "" || created["subscribed"] != true || created["state"] != string(SideSessionStateRunning) {
		t.Fatalf("create result = %#v", created)
	}
	select {
	case got := <-child.started:
		if got != "research one" {
			t.Fatalf("first input = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("side turn did not start")
	}

	queued := callSideTool(t, parent, SideSessionToolSend, map[string]any{
		"session_id": id,
		"message":    "also inspect two",
	})
	if queued["queued"] != true || queued["pending_count"].(float64) != 1 {
		t.Fatalf("send result = %#v", queued)
	}
	close(child.release)
	status := waitForSideState(t, parent, id, SideSessionStateIdle)
	if status.PendingCount != 0 || status.LastResult != "answer: also inspect two" || status.LastError != "" {
		t.Fatalf("idle status = %+v", status)
	}

	listed := callSideTool(t, parent, SideSessionToolList, nil)
	sessions, _ := listed["sessions"].([]any)
	if len(sessions) != 1 {
		t.Fatalf("list result = %#v", listed)
	}
}

func TestSideSessionSendTreatsSlashPrefixAsDirectMessage(t *testing.T) {
	child := &scriptedSideProvider{}
	parent := newSideSessionTestApp(t, &scriptedSideProvider{}, child)
	created := callSideTool(t, parent, SideSessionToolCreate, map[string]any{"query": "first", "subscribe": false})
	id := created["session_id"].(string)
	waitForSideState(t, parent, id, SideSessionStateIdle)

	result := callSideTool(t, parent, SideSessionToolSend, map[string]any{"session_id": id, "message": "/status"})
	if result["queued"] != false {
		t.Fatalf("send result = %#v", result)
	}
	status := waitForSideState(t, parent, id, SideSessionStateIdle)
	if status.LastResult != "answer: /status" {
		t.Fatalf("last result = %q, want slash text delivered to model", status.LastResult)
	}
}

func TestSideSessionCreateRunsThreeChildrenConcurrently(t *testing.T) {
	release := make(chan struct{})
	providers := []llm.Provider{
		&barrierSideProvider{started: make(chan struct{}, 1), release: release},
		&barrierSideProvider{started: make(chan struct{}, 1), release: release},
		&barrierSideProvider{started: make(chan struct{}, 1), release: release},
	}
	parent := newSideSessionTestApp(t, &scriptedSideProvider{}, providers...)
	ids := make([]string, 0, 3)
	for i := 0; i < 3; i++ {
		created := callSideTool(t, parent, SideSessionToolCreate, map[string]any{
			"query":     "parallel task " + string(rune('A'+i)),
			"subscribe": false,
		})
		ids = append(ids, created["session_id"].(string))
	}
	for i, provider := range providers {
		select {
		case <-provider.(*barrierSideProvider).started:
		case <-time.After(time.Second):
			t.Fatalf("child %d did not start before shared release", i)
		}
	}
	close(release)
	for _, id := range ids {
		waitForSideState(t, parent, id, SideSessionStateIdle)
	}
}

func TestManagedSideSessionSharesPrimaryGoalAndNotes(t *testing.T) {
	child := &scriptedSideProvider{started: make(chan string, 1), release: make(chan struct{})}
	parent := newSideSessionTestApp(t, &scriptedSideProvider{}, child)
	if _, err := parent.Engine.Notes.Update("primary notes"); err != nil {
		t.Fatal(err)
	}

	created := callSideTool(t, parent, SideSessionToolCreate, map[string]any{"query": "inspect shared state", "subscribe": false})
	id := created["session_id"].(string)
	select {
	case <-child.started:
	case <-time.After(time.Second):
		t.Fatal("side turn did not start")
	}

	parent.sideSessions.mu.Lock()
	managed := parent.sideSessions.sessions[id]
	parent.sideSessions.mu.Unlock()
	if managed == nil {
		t.Fatal("managed side session missing")
	}
	childRuntime := managed.app.Engine.SessionRuntimeSnapshot()
	parentRuntime := parent.Engine.SessionRuntimeSnapshot()
	if childRuntime.GoalState != parentRuntime.GoalState || childRuntime.Notes != parentRuntime.Notes {
		t.Fatalf("shared stores differ: child=%p/%p parent=%p/%p", childRuntime.GoalState, childRuntime.Notes, parentRuntime.GoalState, parentRuntime.Notes)
	}
	if childRuntime.Session.Dir == parentRuntime.Session.Dir {
		t.Fatal("side session reused primary transcript directory")
	}
	if _, err := childRuntime.Notes.Update("updated by side"); err != nil {
		t.Fatal(err)
	}
	got, err := parentRuntime.Notes.Snapshot()
	if err != nil || got.Content != "updated by side" {
		t.Fatalf("primary notes = %+v, err=%v", got, err)
	}

	callSideTool(t, parent, SideSessionToolStop, map[string]any{"session_id": id})
	if _, err := parent.sideSessions.Status(id); !errors.Is(err, ErrSideSessionNotActive) {
		t.Fatalf("status after stop error = %v", err)
	}
	if _, err := filepath.Glob(filepath.Join(parent.cfg.SessionsDir(), id, "conversation.jsonl")); err != nil {
		t.Fatal(err)
	}
}

func TestSideSessionSubscriptionDeliversTypedParentMessage(t *testing.T) {
	parentProvider := &scriptedSideProvider{}
	childProvider := &scriptedSideProvider{}
	parent := newSideSessionTestApp(t, parentProvider, childProvider)

	created := callSideTool(t, parent, SideSessionToolCreate, map[string]any{"query": "finish quickly"})
	id := created["session_id"].(string)
	waitForSideState(t, parent, id, SideSessionStateIdle)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, history, ok := parent.SessionSnapshot()
		if ok {
			for _, message := range history {
				if message.Kind == llm.MessageKindSideSession && strings.Contains(message.FirstText(), id) && strings.Contains(message.FirstText(), "answer: finish quickly") {
					return
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, history, _ := parent.SessionSnapshot()
	t.Fatalf("typed side-session notification missing: %+v", history)
}

func TestSideSessionSubscriptionQueuesWhileParentTurnIsBusy(t *testing.T) {
	parentProvider := &scriptedSideProvider{started: make(chan string, 1), release: make(chan struct{})}
	childProvider := &scriptedSideProvider{}
	parent := newSideSessionTestApp(t, parentProvider, childProvider)

	parentTurnDone := make(chan error, 1)
	go func() {
		_, err := parent.Run(context.Background(), "keep primary busy")
		parentTurnDone <- err
	}()
	select {
	case <-parentProvider.started:
	case <-time.After(time.Second):
		t.Fatal("parent turn did not start")
	}

	created := callSideTool(t, parent, SideSessionToolCreate, map[string]any{"query": "finish while busy"})
	id := created["session_id"].(string)
	waitForSideState(t, parent, id, SideSessionStateIdle)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		records, err := parent.Engine.PendingInputQueue.Records()
		if err != nil {
			t.Fatal(err)
		}
		for _, record := range records {
			if record.State == runtime.PendingInputStatePending &&
				record.Message.Kind == llm.MessageKindSideSession &&
				strings.Contains(record.Message.FirstText(), id) {
				close(parentProvider.release)
				if err := <-parentTurnDone; err != nil {
					t.Fatal(err)
				}
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("side-session result was not durably queued for busy parent")
}

func TestSideSessionNotificationPersistsBeforeParentQueueHasCapacity(t *testing.T) {
	parentProvider := &scriptedSideProvider{started: make(chan string, 1), release: make(chan struct{})}
	childProvider := &scriptedSideProvider{}
	parent := newSideSessionTestApp(t, parentProvider, childProvider)
	parent.Engine.MaxPendingInputs = 1

	parentTurnDone := make(chan error, 1)
	go func() {
		_, err := parent.Run(context.Background(), "keep primary busy")
		parentTurnDone <- err
	}()
	select {
	case <-parentProvider.started:
	case <-time.After(time.Second):
		t.Fatal("parent turn did not start")
	}
	if _, err := parent.Engine.EnqueuePendingMessage(context.Background(), llm.TextMessage(llm.RoleUser, "occupy capacity")); err != nil {
		t.Fatal(err)
	}
	created := callSideTool(t, parent, SideSessionToolCreate, map[string]any{"query": "finish while full"})
	id := created["session_id"].(string)
	status := waitForSideState(t, parent, id, SideSessionStateIdle)
	pendingID := "side-session-result:" + id + ":" + status.LastTurnID

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		records, err := parent.Engine.PendingInputQueue.Records()
		if err != nil {
			t.Fatal(err)
		}
		if record, ok := records[pendingID]; ok {
			if record.State != runtime.PendingInputStatePending || record.ExpiresAt.Sub(record.CreatedAt) != parent.Engine.ExternalEventTTL {
				t.Fatalf("durable notification = %+v", record)
			}
			close(parentProvider.release)
			if err := <-parentTurnDone; err != nil {
				t.Fatal(err)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("side notification was not persisted while parent queue was full")
}

func TestSideSessionExpiredNotificationFailureIsObservable(t *testing.T) {
	parentProvider := &scriptedSideProvider{started: make(chan string, 1), release: make(chan struct{})}
	parent := newSideSessionTestApp(t, parentProvider, &scriptedSideProvider{})
	parent.Engine.MaxPendingInputs = 1
	parent.Engine.ExternalEventTTL = 50 * time.Millisecond
	failed := make(chan events.Event, 1)
	unsubscribe := parent.Bus.Subscribe("side_session.notification_failed", func(event events.Event) { failed <- event })
	defer unsubscribe()

	parentTurnDone := make(chan error, 1)
	go func() {
		_, err := parent.Run(context.Background(), "keep primary busy past result TTL")
		parentTurnDone <- err
	}()
	select {
	case <-parentProvider.started:
	case <-time.After(time.Second):
		t.Fatal("parent turn did not start")
	}
	if _, err := parent.Engine.EnqueuePendingMessage(context.Background(), llm.TextMessage(llm.RoleUser, "occupy capacity")); err != nil {
		t.Fatal(err)
	}
	created := callSideTool(t, parent, SideSessionToolCreate, map[string]any{"query": "expire while full"})
	id := created["session_id"].(string)
	waitForSideState(t, parent, id, SideSessionStateIdle)
	select {
	case event := <-failed:
		if event.TurnID == "" || !strings.Contains(fmt.Sprint(event.Payload), runtime.ErrPendingInputExpired.Error()) {
			t.Fatalf("expired notification event = %+v", event)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("expired Side Session notification failure was not observable")
	}
	status, err := parent.sideSessions.Status(id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status.NotificationError, runtime.ErrPendingInputExpired.Error()) {
		t.Fatalf("notification error = %q, want pending-input expiry", status.NotificationError)
	}
	close(parentProvider.release)
	if err := <-parentTurnDone; err != nil {
		t.Fatal(err)
	}
}

func TestSideSessionPersistedAdmissionStorageFailureIsBoundedAndObservable(t *testing.T) {
	parentProvider := &scriptedSideProvider{started: make(chan string, 1), release: make(chan struct{})}
	parent := newSideSessionTestApp(t, parentProvider, &scriptedSideProvider{})
	parent.Engine.MaxPendingInputs = 1
	failed := make(chan events.Event, 1)
	unsubscribe := parent.Bus.Subscribe("side_session.notification_failed", func(event events.Event) { failed <- event })
	defer unsubscribe()

	parentTurnDone := make(chan error, 1)
	go func() {
		_, err := parent.Run(context.Background(), "keep primary busy during admission failure")
		parentTurnDone <- err
	}()
	select {
	case <-parentProvider.started:
	case <-time.After(time.Second):
		t.Fatal("parent turn did not start")
	}
	if _, err := parent.Engine.EnqueuePendingMessage(context.Background(), llm.TextMessage(llm.RoleUser, "occupy capacity")); err != nil {
		t.Fatal(err)
	}
	created := callSideTool(t, parent, SideSessionToolCreate, map[string]any{"query": "persist before admission storage failure"})
	id := created["session_id"].(string)
	status := waitForSideState(t, parent, id, SideSessionStateIdle)
	pendingID := "side-session-result:" + id + ":" + status.LastTurnID
	identity, ok := parent.SessionIdentity()
	if !ok {
		t.Fatal("parent session identity unavailable")
	}
	pendingPath := filepath.Join(identity.Dir, "pending_input.jsonl")
	backupPath := pendingPath + ".admission-backup"

	deadline := time.Now().Add(3 * time.Second)
	for {
		records, err := parent.Engine.PendingInputQueue.Records()
		if err != nil {
			t.Fatal(err)
		}
		if _, exists := records[pendingID]; exists {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("side notification was not persisted before admission failure")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := os.Rename(pendingPath, backupPath); err != nil {
		t.Fatal(err)
	}
	restored := false
	restore := func() error {
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
	t.Cleanup(func() {
		if err := restore(); err != nil {
			t.Errorf("restore pending journal: %v", err)
		}
	})
	if err := os.Mkdir(pendingPath, 0o700); err != nil {
		t.Fatal(err)
	}

	select {
	case event := <-failed:
		if !strings.Contains(fmt.Sprint(event.Payload), "admit persisted side session notification") {
			t.Fatalf("admission failure event = %+v", event)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("persisted admission storage failure did not terminate")
	}
	status, err := parent.sideSessions.Status(id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status.NotificationError, "admit persisted side session notification") {
		t.Fatalf("notification error = %q, want bounded admission failure", status.NotificationError)
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	close(parentProvider.release)
	if err := <-parentTurnDone; err != nil {
		t.Fatal(err)
	}
}

func TestSideSessionTerminalSubscriptionSurvivesTransientPersistenceFailureAndLaterUnsubscribe(t *testing.T) {
	child := &barrierSideProvider{started: make(chan struct{}, 1), release: make(chan struct{})}
	parent := newSideSessionTestApp(t, &scriptedSideProvider{}, child)
	identity, ok := parent.SessionIdentity()
	if !ok {
		t.Fatal("parent session identity unavailable")
	}
	pendingPath := filepath.Join(parent.cfg.SessionsDir(), identity.ID, "pending_input.jsonl")
	if err := os.Mkdir(pendingPath, 0o700); err != nil {
		t.Fatal(err)
	}

	created := callSideTool(t, parent, SideSessionToolCreate, map[string]any{"query": "persist after recovery"})
	id := created["session_id"].(string)
	select {
	case <-child.started:
	case <-time.After(time.Second):
		t.Fatal("side turn did not start")
	}
	close(child.release)
	waitForSideState(t, parent, id, SideSessionStateIdle)
	callSideTool(t, parent, SideSessionToolSubscribe, map[string]any{"session_id": id, "subscribed": false})
	time.Sleep(150 * time.Millisecond)
	if err := os.Remove(pendingPath); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, history, ok := parent.SessionSnapshot()
		if ok {
			for _, message := range history {
				if message.Kind == llm.MessageKindSideSession && strings.Contains(message.FirstText(), id) {
					return
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("terminal side notification was lost after persistence recovered")
}

func TestSideSessionDropsPersistedNotificationAfterPrimaryLosesOwnership(t *testing.T) {
	primary := &scriptedSideProvider{}
	child := &barrierSideProvider{started: make(chan struct{}, 1), release: make(chan struct{})}
	parent := newSideSessionTestApp(t, primary, child)
	identity, ok := parent.SessionIdentity()
	if !ok {
		t.Fatal("parent session identity unavailable")
	}
	pendingPath := filepath.Join(parent.cfg.SessionsDir(), identity.ID, "pending_input.jsonl")
	if err := os.Mkdir(pendingPath, 0o700); err != nil {
		t.Fatal(err)
	}

	created := callSideTool(t, parent, SideSessionToolCreate, map[string]any{"query": "persist after ownership changes"})
	id := created["session_id"].(string)
	select {
	case <-child.started:
	case <-time.After(time.Second):
		t.Fatal("side turn did not start")
	}
	close(child.release)
	status := waitForSideState(t, parent, id, SideSessionStateIdle)
	pendingID := "side-session-result:" + id + ":" + status.LastTurnID
	time.Sleep(150 * time.Millisecond)

	replacement, err := AttachWorkspaceSession(parent.cfg, SessionAttachmentRequest{Mode: SessionModeNewPrimary})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = replacement.Session.Close() })
	if err := os.Remove(pendingPath); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		records, err := parent.Engine.PendingInputQueue.Records()
		if err != nil {
			t.Fatal(err)
		}
		if record, ok := records[pendingID]; ok && record.State == runtime.PendingInputStateDropped {
			primary.mu.Lock()
			calls := primary.calls
			primary.mu.Unlock()
			if calls != 0 {
				t.Fatalf("inactive primary provider calls = %d, want 0", calls)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("stale Side Session notification %q was not dropped", pendingID)
}

func TestSideSessionRetriesTransientStaleNotificationDropFailure(t *testing.T) {
	parent := newSideSessionTestApp(t, &scriptedSideProvider{})
	identity, ok := parent.SessionIdentity()
	if !ok {
		t.Fatal("parent session identity unavailable")
	}
	record, err := parent.Engine.PersistPendingMessageWithOptions(
		context.Background(),
		llm.TextMessage(llm.RoleUser, "stale side result"),
		runtime.PendingInputOptions{ID: "stale-side-result", TTL: time.Hour},
	)
	if err != nil {
		t.Fatal(err)
	}
	pendingPath := filepath.Join(parent.cfg.SessionsDir(), identity.ID, "pending_input.jsonl")
	backupPath := pendingPath + ".backup"
	if err := os.Rename(pendingPath, backupPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(pendingPath, 0o700); err != nil {
		t.Fatal(err)
	}

	dropped := make(chan error, 1)
	go func() { dropped <- parent.sideSessions.dropPersistedNotification(record.ID) }()
	time.Sleep(75 * time.Millisecond)
	if err := os.Remove(pendingPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(backupPath, pendingPath); err != nil {
		t.Fatal(err)
	}
	if err := <-dropped; err != nil {
		t.Fatal(err)
	}
	records, err := parent.Engine.PendingInputQueue.Records()
	if err != nil {
		t.Fatal(err)
	}
	if got := records[record.ID].State; got != runtime.PendingInputStateDropped {
		t.Fatalf("record state = %q, want %q", got, runtime.PendingInputStateDropped)
	}
}

func TestSideSessionHookDenialDropsNotificationBeforeTranscriptAppend(t *testing.T) {
	primary := &scriptedSideProvider{}
	child := &barrierSideProvider{started: make(chan struct{}, 1), release: make(chan struct{})}
	parent := newSideSessionTestApp(t, primary, child)
	failed := make(chan events.Event, 1)
	unsubscribe := parent.Bus.Subscribe("side_session.notification_failed", func(event events.Event) { failed <- event })
	defer unsubscribe()

	created := callSideTool(t, parent, SideSessionToolCreate, map[string]any{"query": "finish before hook denial"})
	id := created["session_id"].(string)
	select {
	case <-child.started:
	case <-time.After(time.Second):
		t.Fatal("side turn did not start")
	}
	parent.Engine.Hooks = sideHookRunnerFunc(func(_ context.Context, req hooks.Request) ([]hooks.Result, error) {
		if req.EventName != hooks.EventUserPromptSubmit || !strings.HasPrefix(req.UserInput, "Side Session result:") {
			return nil, nil
		}
		return []hooks.Result{{
			Hook:      hooks.CommandHook{Name: "deny-side-result", Events: []hooks.EventName{hooks.EventUserPromptSubmit}},
			EventName: hooks.EventUserPromptSubmit,
			ExitCode:  2,
			Stdout:    "side result denied",
		}}, nil
	})
	close(child.release)
	status := waitForSideState(t, parent, id, SideSessionStateIdle)

	select {
	case event := <-failed:
		if !strings.Contains(fmt.Sprint(event.Payload), "side result denied") {
			t.Fatalf("notification failure = %+v", event)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("hook-denied Side Session notification failure was not observable")
	}
	pendingID := "side-session-result:" + id + ":" + status.LastTurnID
	records, err := parent.Engine.PendingInputQueue.Records()
	if err != nil {
		t.Fatal(err)
	}
	if got := records[pendingID].State; got != runtime.PendingInputStateDropped {
		t.Fatalf("record state = %q, want %q", got, runtime.PendingInputStateDropped)
	}
	primary.mu.Lock()
	calls := primary.calls
	primary.mu.Unlock()
	if calls != 0 {
		t.Fatalf("primary provider calls = %d, want 0 after hook denial", calls)
	}
}

func TestSideSessionPermanentNotificationFailureIsObservableAndBounded(t *testing.T) {
	child := &scriptedSideProvider{}
	parent := newSideSessionTestApp(t, &scriptedSideProvider{}, child)
	identity, ok := parent.SessionIdentity()
	if !ok {
		t.Fatal("parent session identity unavailable")
	}
	pendingPath := filepath.Join(parent.cfg.SessionsDir(), identity.ID, "pending_input.jsonl")
	if err := os.Mkdir(pendingPath, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(pendingPath) })
	failed := make(chan events.Event, 1)
	unsubscribe := parent.Bus.Subscribe("side_session.notification_failed", func(event events.Event) { failed <- event })
	defer unsubscribe()

	created := callSideTool(t, parent, SideSessionToolCreate, map[string]any{"query": "cannot persist"})
	id := created["session_id"].(string)
	waitForSideState(t, parent, id, SideSessionStateIdle)
	select {
	case event := <-failed:
		if event.TurnID == "" {
			t.Fatalf("notification failure event = %+v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("permanent notification failure was not bounded")
	}
	status, err := parent.sideSessions.Status(id)
	if err != nil {
		t.Fatal(err)
	}
	if status.NotificationError == "" {
		t.Fatalf("notification failure missing from status: %+v", status)
	}
}

func TestSideSessionStopAllDoesNotDeadlockWhenDeliveryCallsSideTool(t *testing.T) {
	provider := &sideToolDuringDeliveryProvider{started: make(chan struct{}, 1), release: make(chan struct{})}
	parent := newSideSessionTestApp(t, provider, &scriptedSideProvider{})
	provider.app = parent
	created := callSideTool(t, parent, SideSessionToolCreate, map[string]any{"query": "complete"})
	id := created["session_id"].(string)
	waitForSideState(t, parent, id, SideSessionStateIdle)
	select {
	case <-provider.started:
	case <-time.After(time.Second):
		t.Fatal("delivery turn did not reach provider")
	}
	stopped := make(chan error, 1)
	go func() { stopped <- parent.sideSessions.StopAll() }()
	close(provider.release)
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("StopAll deadlocked with a delivery-side tool call")
	}
}

func TestSideSessionFailureNotifiesParent(t *testing.T) {
	parent := newSideSessionTestApp(t, &scriptedSideProvider{}, &failingSideProvider{err: errors.New("provider unavailable")})
	created := callSideTool(t, parent, SideSessionToolCreate, map[string]any{"query": "fail this task"})
	id := created["session_id"].(string)
	status := waitForSideState(t, parent, id, SideSessionStateIdle)
	if !strings.Contains(status.LastError, "provider unavailable") || status.LastResult != "" {
		t.Fatalf("failed side status = %+v", status)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, history, ok := parent.SessionSnapshot()
		if ok {
			for _, message := range history {
				if message.Kind == llm.MessageKindSideSession &&
					strings.Contains(message.FirstText(), `"status":"failed"`) &&
					strings.Contains(message.FirstText(), "provider unavailable") {
					return
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("failed Side Session notification missing")
}

func TestSideSessionDoesNotDeliverAfterPrimaryLosesWorkspaceOwnership(t *testing.T) {
	primary := &scriptedSideProvider{}
	child := &scriptedSideProvider{started: make(chan string, 1), release: make(chan struct{})}
	parent := newSideSessionTestApp(t, primary, child)
	owner, ok := parent.SessionIdentity()
	if !ok {
		t.Fatal("parent session identity unavailable")
	}
	created := callSideTool(t, parent, SideSessionToolCreate, map[string]any{"query": "finish after activation"})
	id := created["session_id"].(string)
	select {
	case <-child.started:
	case <-time.After(time.Second):
		t.Fatal("side turn did not start")
	}

	replacement, err := AttachWorkspaceSession(parent.cfg, SessionAttachmentRequest{Mode: SessionModeNewPrimary})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = replacement.Session.Close() })
	close(child.release)

	deadline := time.Now().Add(3 * time.Second)
	idle := false
	for time.Now().Before(deadline) {
		parent.sideSessions.mu.Lock()
		managed := parent.sideSessions.sessions[id]
		idle = managed != nil && managed.status.State == SideSessionStateIdle
		parent.sideSessions.mu.Unlock()
		if idle {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !idle {
		t.Fatal("side turn did not complete after the parent lost workspace ownership")
	}
	time.Sleep(100 * time.Millisecond)
	primary.mu.Lock()
	calls := primary.calls
	primary.mu.Unlock()
	if calls != 0 {
		t.Fatalf("inactive primary provider calls = %d, want 0", calls)
	}
	_, history, ok := parent.SessionSnapshot()
	if !ok {
		t.Fatal("parent session snapshot unavailable")
	}
	for _, message := range history {
		if message.Kind == llm.MessageKindSideSession {
			t.Fatalf("inactive primary %s received side result: %+v", owner.ID, message)
		}
	}
}

func TestManagedSideSessionSkipsSharedGoalCompletionGate(t *testing.T) {
	child := &scriptedSideProvider{}
	parent := newSideSessionTestApp(t, &scriptedSideProvider{}, child)
	if _, err := parent.Engine.GoalState.Create("primary owns this goal", "primary completes it"); err != nil {
		t.Fatal(err)
	}

	created := callSideTool(t, parent, SideSessionToolCreate, map[string]any{
		"query":     "one bounded side task",
		"subscribe": false,
	})
	id := created["session_id"].(string)
	status := waitForSideState(t, parent, id, SideSessionStateIdle)
	if !strings.HasPrefix(status.LastResult, "answer:") || status.LastError != "" {
		t.Fatalf("side status = %+v", status)
	}
	child.mu.Lock()
	calls := child.calls
	child.mu.Unlock()
	if calls != 1 {
		t.Fatalf("side provider calls = %d, want one without shared goal continuation", calls)
	}
}

func TestPrimaryGoalContinuationDefersDuringSubscribedResultHandoff(t *testing.T) {
	primaryProvider := &scriptedSideProvider{started: make(chan string, 1), release: make(chan struct{})}
	parent := newSideSessionTestApp(t, primaryProvider, &scriptedSideProvider{})
	parent.Engine.MaxPendingInputs = 1

	primaryDone := make(chan error, 1)
	go func() {
		_, err := parent.Run(context.Background(), "keep primary busy during result handoff")
		primaryDone <- err
	}()
	select {
	case <-primaryProvider.started:
	case <-time.After(time.Second):
		t.Fatal("primary turn did not start")
	}
	if _, err := parent.Engine.EnqueuePendingMessage(context.Background(), llm.TextMessage(llm.RoleUser, "occupy capacity")); err != nil {
		t.Fatal(err)
	}

	created := callSideTool(t, parent, SideSessionToolCreate, map[string]any{
		"query":     "finish while primary queue is full",
		"subscribe": true,
	})
	id := created["session_id"].(string)
	status := waitForSideState(t, parent, id, SideSessionStateIdle)
	pendingID := "side-session-result:" + id + ":" + status.LastTurnID

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		records, err := parent.Engine.PendingInputQueue.Records()
		if err != nil {
			t.Fatal(err)
		}
		if record, ok := records[pendingID]; ok && record.State == runtime.PendingInputStatePending {
			if !parent.sideSessions.shouldDeferGoalContinuation() {
				t.Fatal("subscribed result awaiting admission did not defer Goal continuation")
			}
			close(primaryProvider.release)
			if err := <-primaryDone; err != nil {
				t.Fatal(err)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("side-session result was not persisted during the handoff window")
}

func TestPrimaryGoalContinuationDefersWhileSubscribedResultIsQueued(t *testing.T) {
	primaryProvider := &goalSideQueueProvider{started: make(chan struct{}), release: make(chan struct{})}
	parent := newSideSessionTestApp(t, primaryProvider, &scriptedSideProvider{})
	primaryProvider.app = parent
	if _, err := parent.Engine.GoalState.Create("finish delegated work", "incorporate the subscribed result"); err != nil {
		t.Fatal(err)
	}

	primaryDone := make(chan error, 1)
	go func() {
		_, err := parent.Run(context.Background(), "delegate while primary is busy")
		primaryDone <- err
	}()
	select {
	case <-primaryProvider.started:
	case <-time.After(time.Second):
		t.Fatal("primary turn did not start")
	}

	created := callSideTool(t, parent, SideSessionToolCreate, map[string]any{
		"query":     "finish while primary provider is active",
		"subscribe": true,
	})
	id := created["session_id"].(string)
	waitForSideState(t, parent, id, SideSessionStateIdle)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if parent.Engine.PendingInputStatus().PendingCount > 0 {
			if !parent.sideSessions.shouldDeferGoalContinuation() {
				t.Fatal("queued subscribed result did not defer Goal continuation")
			}
			close(primaryProvider.release)
			if err := <-primaryDone; err != nil {
				t.Fatal(err)
			}
			goal, err := parent.Engine.GoalState.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			if goal.Status != runtime.GoalStatusSuccess || goal.ContinuationCount != 0 {
				t.Fatalf("goal state = %+v, want success without synthetic continuation", goal)
			}
			if parent.sideSessions.shouldDeferGoalContinuation() {
				t.Fatal("admitted subscribed result kept Goal continuation deferred")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("side-session result was not queued while the primary provider was active")
}

func TestPrimaryGoalContinuationDefersForSubscribedRunningSideSessions(t *testing.T) {
	primaryProvider := &scriptedSideProvider{}
	firstChild := &scriptedSideProvider{started: make(chan string, 1), release: make(chan struct{})}
	secondChild := &scriptedSideProvider{started: make(chan string, 1), release: make(chan struct{})}
	parent := newSideSessionTestApp(t, primaryProvider, firstChild, secondChild)
	if _, err := parent.Engine.GoalState.Create("finish delegated work", "both worker results are incorporated"); err != nil {
		t.Fatal(err)
	}

	first := callSideTool(t, parent, SideSessionToolCreate, map[string]any{
		"query":     "first worker",
		"subscribe": true,
	})["session_id"].(string)
	second := callSideTool(t, parent, SideSessionToolCreate, map[string]any{
		"query":     "second worker",
		"subscribe": true,
	})["session_id"].(string)
	for name, started := range map[string]<-chan string{"first": firstChild.started, "second": secondChild.started} {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatalf("%s side turn did not start", name)
		}
	}

	if !parent.sideSessions.shouldDeferGoalContinuation() {
		t.Fatal("subscribed running Side Sessions did not defer Goal continuation")
	}
	out, err := parent.Engine.Turn(context.Background(), "wait for both workers")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "finish delegated work") {
		t.Fatalf("primary output = %q", out)
	}
	primaryProvider.mu.Lock()
	primaryCalls := primaryProvider.calls
	primaryProvider.mu.Unlock()
	if primaryCalls != 1 {
		t.Fatalf("primary provider calls = %d, want 1 without Goal continuation", primaryCalls)
	}
	goal, err := parent.Engine.GoalState.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if goal.Status != runtime.GoalStatusInProgress || goal.ContinuationCount != 0 {
		t.Fatalf("goal state = %+v", goal)
	}

	close(firstChild.release)
	waitForSideState(t, parent, first, SideSessionStateIdle)
	if !parent.sideSessions.shouldDeferGoalContinuation() {
		t.Fatal("idle subscribed worker hid another subscribed running worker")
	}
	if _, err := parent.sideSessions.Subscribe(second, false); err != nil {
		t.Fatal(err)
	}
	waitForGoalContinuationDeferral(t, parent, false)
	if _, err := parent.sideSessions.Subscribe(second, true); err != nil {
		t.Fatal(err)
	}
	if !parent.sideSessions.shouldDeferGoalContinuation() {
		t.Fatal("resubscribed running Side Session did not defer Goal continuation")
	}
	close(secondChild.release)
	waitForSideState(t, parent, second, SideSessionStateIdle)
	waitForGoalContinuationDeferral(t, parent, false)
}

func TestSideSessionCreateRejectsUnknownModel(t *testing.T) {
	parent := newSideSessionTestApp(t, &scriptedSideProvider{})
	_, err := parent.Engine.Tools.Call(context.Background(), SideSessionToolCreate, map[string]any{
		"query": "research",
		"model": "missing:model",
	})
	if err == nil || !strings.Contains(err.Error(), "unknown provider") {
		t.Fatalf("create error = %v, want unknown configured provider", err)
	}
}

func TestSideSessionCreateAppliesConfiguredModelOverride(t *testing.T) {
	testHome := t.TempDir()
	t.Setenv("JUEX_HOME", testHome)
	workDir := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "juex.yaml")
	if err := os.WriteFile(configPath, []byte(`model: openai:primary
providers:
  - id: openai
    base_url: https://openai.example
    api_key: test
    models:
      - id: primary
      - id: specialist
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadWithOptions(config.LoadOptions{
		WorkDir:    workDir,
		ConfigPath: configPath,
		AgentState: config.AgentStateNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(testHome, "agents")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("loading test config created an Agent in JUEX_HOME: %v", err)
	}
	cfg.AgentStateDir = filepath.Join(workDir, ".juex")
	var captured sideSessionChildOptions
	parent, err := New(Options{
		Config:     cfg,
		Provider:   &scriptedSideProvider{},
		WorkDir:    workDir,
		DisableMCP: true,
		sideSessionFactory: func(opts sideSessionChildOptions) (*App, error) {
			captured = opts
			return New(Options{
				Config: opts.Config, Provider: &scriptedSideProvider{}, WorkDir: opts.Config.WorkDir,
				DisableMCP: true, SessionMode: SessionModeNewSide, disableSideSessionTools: true,
				sharedGoalState: opts.GoalState, sharedNotes: opts.Notes, startupContext: opts.Context,
			})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := parent.CloseAndWait(); err != nil {
			t.Errorf("close parent app: %v", err)
		}
	})
	created := callSideTool(t, parent, SideSessionToolCreate, map[string]any{
		"query": "specialized work", "model": "openai:specialist", "subscribe": false,
	})
	if captured.Config.ProviderID != "openai" || captured.Config.Model != "specialist" || captured.UseParentProvider {
		t.Fatalf("captured child options = %+v", captured)
	}
	if created["model"] != "openai:specialist" {
		t.Fatalf("create result = %#v", created)
	}
}

func TestManagedSideSessionCannotBeDeletedWhileActive(t *testing.T) {
	child := &scriptedSideProvider{started: make(chan string, 1), release: make(chan struct{})}
	parent := newSideSessionTestApp(t, &scriptedSideProvider{}, child)
	created := callSideTool(t, parent, SideSessionToolCreate, map[string]any{
		"query":     "hold the session lock",
		"subscribe": false,
	})
	id := created["session_id"].(string)
	select {
	case <-child.started:
	case <-time.After(time.Second):
		t.Fatal("side turn did not start")
	}

	err := DeleteSession(parent.cfg, id, SessionDeleteOptions{})
	var lockErr *session.LockError
	if !errors.As(err, &lockErr) {
		t.Fatalf("delete active side error = %v, want session lock error", err)
	}
	if _, statErr := os.Stat(filepath.Join(parent.cfg.SessionsDir(), id)); statErr != nil {
		t.Fatalf("active side directory was removed: %v", statErr)
	}
	callSideTool(t, parent, SideSessionToolStop, map[string]any{"session_id": id})
	if err := DeleteSession(parent.cfg, id, SessionDeleteOptions{}); err != nil {
		t.Fatalf("delete stopped side: %v", err)
	}
}

func TestManagedSideSessionProjectsSharedStateEventsToPrimary(t *testing.T) {
	child := &scriptedSideProvider{started: make(chan string, 1), release: make(chan struct{})}
	parent := newSideSessionTestApp(t, &scriptedSideProvider{}, child)
	created := callSideTool(t, parent, SideSessionToolCreate, map[string]any{
		"query":     "wait",
		"subscribe": false,
	})
	id := created["session_id"].(string)
	select {
	case <-child.started:
	case <-time.After(time.Second):
		t.Fatal("side turn did not start")
	}

	parent.sideSessions.mu.Lock()
	managed := parent.sideSessions.sessions[id]
	parent.sideSessions.mu.Unlock()
	updated := make(chan events.Event, 1)
	unsubscribe := parent.Bus.Subscribe("notes.updated", func(event events.Event) { updated <- event })
	defer unsubscribe()
	if _, err := managed.app.Engine.Tools.Call(context.Background(), runtime.NotesToolUpdate, map[string]any{"content": "shared from side"}); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-updated:
		if event.Type != "notes.updated" {
			t.Fatalf("event = %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("primary did not receive shared Notes projection")
	}
}

func TestSideSessionParentCloseCancelsChildren(t *testing.T) {
	child := &scriptedSideProvider{started: make(chan string, 1), release: make(chan struct{})}
	parent := newSideSessionTestApp(t, &scriptedSideProvider{}, child)
	created := callSideTool(t, parent, SideSessionToolCreate, map[string]any{
		"query": "keep running",
	})
	id := created["session_id"].(string)
	select {
	case <-child.started:
	case <-time.After(time.Second):
		t.Fatal("side turn did not start")
	}

	closed := make(chan error, 1)
	go func() { closed <- parent.CloseAndWait() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("parent close did not cancel managed child")
	}
	if _, err := parent.sideSessions.Status(id); !errors.Is(err, ErrSideSessionManagerClosed) {
		t.Fatalf("status after parent close error = %v", err)
	}
}

func TestSideSessionStopCancelsNewlySentTurnWithoutUserAttribution(t *testing.T) {
	child := &scriptedSideProvider{started: make(chan string, 2), release: make(chan struct{})}
	parent := newSideSessionTestApp(t, &scriptedSideProvider{}, child)
	created := callSideTool(t, parent, SideSessionToolCreate, map[string]any{"query": "first", "subscribe": false})
	id := created["session_id"].(string)
	select {
	case <-child.started:
	case <-time.After(time.Second):
		t.Fatal("first side turn did not start")
	}
	close(child.release)
	waitForSideState(t, parent, id, SideSessionStateIdle)

	second := &scriptedSideProvider{started: child.started, release: make(chan struct{})}
	parent.sideSessions.mu.Lock()
	managed := parent.sideSessions.sessions[id]
	managed.app.Engine.Provider = second
	parent.sideSessions.mu.Unlock()
	callSideTool(t, parent, SideSessionToolSend, map[string]any{"session_id": id, "message": "second"})

	stopped := make(chan error, 1)
	go func() { stopped <- parent.sideSessions.Stop(context.Background(), id) }()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("stop blocked waiting for a newly sent turn")
	}
	data, err := os.ReadFile(filepath.Join(parent.cfg.SessionsDir(), id, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "cancelled by user") || !strings.Contains(string(data), "side session stopped") {
		t.Fatalf("stop attribution in events = %s", data)
	}
}

func TestSideSessionStopHonorsToolCancellation(t *testing.T) {
	child := &stubbornSideProvider{started: make(chan struct{}, 1), release: make(chan struct{})}
	parent := newSideSessionTestApp(t, &scriptedSideProvider{}, child)
	created := callSideTool(t, parent, SideSessionToolCreate, map[string]any{"query": "stubborn work", "subscribe": false})
	id := created["session_id"].(string)
	select {
	case <-child.started:
	case <-time.After(time.Second):
		t.Fatal("side child did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := parent.Engine.Tools.Call(ctx, SideSessionToolStop, map[string]any{"session_id": id})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stop error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("stop returned after %s, want prompt context cancellation", elapsed)
	}
	if _, err := parent.sideSessions.Status(id); !errors.Is(err, ErrSideSessionNotActive) {
		t.Fatalf("status after cancelled stop = %v, want inactive", err)
	}
	close(child.release)
}

func TestSideSessionCreateHonorsToolCancellationDuringStartup(t *testing.T) {
	workDir := t.TempDir()
	stateDir := filepath.Join(workDir, ".juex")
	childProvider := &scriptedSideProvider{}
	factoryDone := make(chan struct{})
	parent, err := New(Options{
		Config: config.Config{
			ProviderID:    "openai",
			APIKey:        "test",
			Model:         "primary",
			WorkDir:       workDir,
			AgentStateDir: stateDir,
		},
		Provider:   &scriptedSideProvider{},
		WorkDir:    workDir,
		DisableMCP: true,
		sideSessionFactory: func(opts sideSessionChildOptions) (*App, error) {
			defer close(factoryDone)
			cfg := opts.Config
			cfg.Hooks = hooks.Config{Commands: []hooks.CommandHook{{
				Name:    "wait-for-cancellation",
				Events:  []hooks.EventName{hooks.EventSessionStart},
				Command: appHookCommand("wait"),
			}}}
			return New(Options{
				Config:                  cfg,
				Provider:                childProvider,
				WorkDir:                 cfg.WorkDir,
				DisableMCP:              true,
				SessionMode:             SessionModeNewSide,
				disableSideSessionTools: true,
				sharedGoalState:         opts.GoalState,
				sharedNotes:             opts.Notes,
				startupContext:          opts.Context,
			})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := parent.CloseAndWait(); err != nil {
			t.Errorf("close parent app: %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = parent.Engine.Tools.Call(ctx, SideSessionToolCreate, map[string]any{
		"query": "must not outlive create timeout",
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("create error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("create returned after %s, want prompt context cancellation", elapsed)
	}
	select {
	case <-factoryDone:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled child factory did not finish")
	}
	parent.sideSessions.mu.Lock()
	active := len(parent.sideSessions.sessions)
	parent.sideSessions.mu.Unlock()
	if active != 0 {
		t.Fatalf("active side sessions after cancelled create = %d, want 0", active)
	}
	childProvider.mu.Lock()
	calls := childProvider.calls
	childProvider.mu.Unlock()
	if calls != 0 {
		t.Fatalf("child provider calls after cancelled create = %d, want 0", calls)
	}
}

func TestAppBeginCloseCancelsInFlightSideCreationBeforeFactoryReturns(t *testing.T) {
	parent := newSideSessionTestApp(t, &scriptedSideProvider{}, &scriptedSideProvider{})
	originalFactory := parent.sideSessions.factory
	factoryEntered := make(chan struct{})
	releaseFactory := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseFactory) }) }
	t.Cleanup(release)
	parent.sideSessions.factory = func(opts sideSessionChildOptions) (*App, error) {
		close(factoryEntered)
		<-releaseFactory
		return originalFactory(opts)
	}

	createDone := make(chan error, 1)
	go func() {
		_, err := parent.sideSessions.Create(context.Background(), "cancel with parent close", "", false)
		createDone <- err
	}()
	<-factoryEntered
	closeDone := make(chan error, 1)
	go func() { closeDone <- parent.BeginClose() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("BeginClose = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("BeginClose waited for the in-flight Side Session factory")
	}
	select {
	case err := <-createDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Create error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Side Session create did not observe parent close")
	}
	select {
	case <-releaseFactory:
		t.Fatal("factory was released before close completed")
	default:
	}
	release()
}

func TestSideSessionCreateRetainsCallDeadlineAfterFactoryReturns(t *testing.T) {
	childProvider := &scriptedSideProvider{}
	parent := newSideSessionTestApp(t, &scriptedSideProvider{}, childProvider)
	originalFactory := parent.sideSessions.factory
	childReady := make(chan *App, 1)
	parent.sideSessions.factory = func(opts sideSessionChildOptions) (*App, error) {
		child, err := originalFactory(opts)
		if err != nil {
			return nil, err
		}
		child.sessionMu.Lock()
		childReady <- child
		return child, nil
	}

	ctx := newControlledDeadlineContext()
	createDone := make(chan error, 1)
	go func() {
		_, err := parent.sideSessions.Create(ctx, "deadline after factory", "", false)
		createDone <- err
	}()
	var child *App
	select {
	case child = <-childReady:
	case err := <-createDone:
		t.Fatalf("Create returned before factory completed: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("Side Session factory did not return")
	}
	ctx.expire()
	child.sessionMu.Unlock()

	select {
	case err := <-createDone:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Create error = %v, want context deadline exceeded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Side Session create did not return after the identity lock was released")
	}
	statuses, err := parent.sideSessions.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 0 {
		t.Fatalf("managed Side Sessions = %+v, want none", statuses)
	}
	childProvider.mu.Lock()
	calls := childProvider.calls
	childProvider.mu.Unlock()
	if calls != 0 {
		t.Fatalf("child provider calls = %d, want 0", calls)
	}
}

type controlledDeadlineContext struct {
	context.Context
	done chan struct{}
	once sync.Once
}

func newControlledDeadlineContext() *controlledDeadlineContext {
	return &controlledDeadlineContext{Context: context.Background(), done: make(chan struct{})}
}

func (c *controlledDeadlineContext) Deadline() (time.Time, bool) {
	return time.Now().Add(time.Hour), true
}

func (c *controlledDeadlineContext) Done() <-chan struct{} {
	return c.done
}

func (c *controlledDeadlineContext) Err() error {
	select {
	case <-c.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

func (c *controlledDeadlineContext) expire() {
	c.once.Do(func() { close(c.done) })
}

func TestSwitchToNewPrimaryStopsChildrenAndKeepsManagerUsable(t *testing.T) {
	first := &scriptedSideProvider{started: make(chan string, 1), release: make(chan struct{})}
	second := &scriptedSideProvider{}
	parent := newSideSessionTestApp(t, &scriptedSideProvider{}, first, second)
	created := callSideTool(t, parent, SideSessionToolCreate, map[string]any{
		"query":     "old primary work",
		"subscribe": false,
	})
	oldID := created["session_id"].(string)
	select {
	case <-first.started:
	case <-time.After(time.Second):
		t.Fatal("first side turn did not start")
	}
	if err := parent.SwitchToNewPrimarySession(); err != nil {
		t.Fatal(err)
	}
	if _, err := parent.sideSessions.Status(oldID); !errors.Is(err, ErrSideSessionNotActive) {
		t.Fatalf("old side status error = %v", err)
	}
	created = callSideTool(t, parent, SideSessionToolCreate, map[string]any{
		"query":     "new primary work",
		"subscribe": false,
	})
	newID := created["session_id"].(string)
	if newID == "" || newID == oldID {
		t.Fatalf("new side id = %q, old = %q", newID, oldID)
	}
	waitForSideState(t, parent, newID, SideSessionStateIdle)
}

func TestNewSlashHonorsCancellationWhileStoppingStubbornChild(t *testing.T) {
	child := &stubbornSideProvider{started: make(chan struct{}, 1), release: make(chan struct{})}
	parent := newSideSessionTestApp(t, &scriptedSideProvider{}, child)
	oldIdentity, ok := parent.SessionIdentity()
	if !ok {
		t.Fatal("missing primary identity")
	}
	created := callSideTool(t, parent, SideSessionToolCreate, map[string]any{
		"query": "hold primary switch", "subscribe": false,
	})
	id := created["session_id"].(string)
	select {
	case <-child.started:
	case <-time.After(time.Second):
		t.Fatal("side child did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, handled, err := parent.ExecuteSlashCommand(ctx, SlashNew)
	if !handled {
		t.Fatal("/new was not handled")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("/new error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("/new returned after %s, want prompt context cancellation", elapsed)
	}
	identity, ok := parent.SessionIdentity()
	if !ok || identity.ID != oldIdentity.ID {
		t.Fatalf("primary after cancelled /new = %+v, want %s", identity, oldIdentity.ID)
	}
	if _, err := parent.sideSessions.Status(id); !errors.Is(err, ErrSideSessionNotActive) {
		t.Fatalf("old side status after cancelled /new = %v, want inactive", err)
	}
	closed := make(chan error, 1)
	go func() { closed <- parent.CloseAndWait() }()
	select {
	case err := <-closed:
		t.Fatalf("parent closed before deferred child cleanup: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(child.release)
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("parent did not finish after deferred child cleanup")
	}
}

func TestSwitchToNewPrimaryWaitsForConcurrentSideCreationAndThenStopsIt(t *testing.T) {
	parent := newSideSessionTestApp(t, &scriptedSideProvider{}, &scriptedSideProvider{})
	originalFactory := parent.sideSessions.factory
	factoryEntered := make(chan struct{})
	releaseFactory := make(chan struct{})
	parent.sideSessions.factory = func(opts sideSessionChildOptions) (*App, error) {
		close(factoryEntered)
		<-releaseFactory
		return originalFactory(opts)
	}

	type createResult struct {
		status SideSessionStatus
		err    error
	}
	created := make(chan createResult, 1)
	go func() {
		status, err := parent.sideSessions.Create(context.Background(), "concurrent child", "", false)
		created <- createResult{status: status, err: err}
	}()
	<-factoryEntered
	switched := make(chan error, 1)
	go func() { switched <- parent.SwitchToNewPrimarySession() }()
	select {
	case err := <-switched:
		t.Fatalf("primary switch passed an in-flight side create: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseFactory)
	result := <-created
	if result.err != nil {
		t.Fatal(result.err)
	}
	if err := <-switched; err != nil {
		t.Fatal(err)
	}
	if _, err := parent.sideSessions.Status(result.status.SessionID); !errors.Is(err, ErrSideSessionNotActive) {
		t.Fatalf("side created during switch remained active: %v", err)
	}
}

func TestAppCloseWaitsForInFlightPrimarySwitchAndClosesReplacement(t *testing.T) {
	child := &stubbornSideProvider{started: make(chan struct{}, 1), release: make(chan struct{})}
	parent := newSideSessionTestApp(t, &scriptedSideProvider{}, child)
	callSideTool(t, parent, SideSessionToolCreate, map[string]any{"query": "hold switch", "subscribe": false})
	select {
	case <-child.started:
	case <-time.After(time.Second):
		t.Fatal("side child did not start")
	}
	switched := make(chan error, 1)
	go func() { switched <- parent.SwitchToNewPrimarySession() }()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		parent.sideSessions.mu.Lock()
		transitioning := parent.sideSessions.transitioning
		parent.sideSessions.mu.Unlock()
		if transitioning {
			break
		}
		time.Sleep(time.Millisecond)
	}
	parent.sideSessions.mu.Lock()
	transitioning := parent.sideSessions.transitioning
	parent.sideSessions.mu.Unlock()
	if !transitioning {
		t.Fatal("primary switch did not enter manager transition")
	}
	closed := make(chan error, 1)
	go func() { closed <- parent.CloseAndWait() }()
	select {
	case err := <-closed:
		t.Fatalf("Close completed before the in-flight primary switch: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(child.release)
	if err := <-switched; err != nil {
		t.Fatal(err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if _, ok := parent.SessionIdentity(); ok {
		t.Fatal("closed App retained the replacement Primary Session")
	}
	matches, err := filepath.Glob(filepath.Join(parent.cfg.SessionsDir(), "*", "session.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("closed App leaked session locks: %v", matches)
	}
}

func TestSideSessionDefaultChildInheritsSummaryProvider(t *testing.T) {
	workDir := t.TempDir()
	provider := &scriptedSideProvider{}
	summary := &scriptedSideProvider{}
	parent, err := New(Options{
		Config: config.Config{
			ProviderID: "openai", APIKey: "test", Model: "primary",
			WorkDir: workDir, AgentStateDir: filepath.Join(workDir, ".juex"),
		},
		Provider: provider, SummaryProvider: summary, WorkDir: workDir, DisableMCP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := parent.CloseAndWait(); err != nil {
			t.Errorf("close parent app: %v", err)
		}
	})
	state := parent.Engine.SessionRuntimeSnapshot()
	child, err := parent.sideSessions.newChildApp(sideSessionChildOptions{
		Config: parent.cfg, Model: "openai:primary", UseParentProvider: true,
		GoalState: state.GoalState, Notes: state.Notes, Observables: parent.obsv,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := child.CloseAndWait(); err != nil {
			t.Errorf("close child app: %v", err)
		}
	})
	if child.Engine.SummaryProvider != summary {
		t.Fatalf("child SummaryProvider = %T %p, want parent provider %p", child.Engine.SummaryProvider, child.Engine.SummaryProvider, summary)
	}
}

func TestSideSessionUnsubscribeAndStopPreserveDurableSession(t *testing.T) {
	child := &scriptedSideProvider{started: make(chan string, 1), release: make(chan struct{})}
	parent := newSideSessionTestApp(t, &scriptedSideProvider{}, child)

	created := callSideTool(t, parent, SideSessionToolCreate, map[string]any{"query": "wait"})
	id := created["session_id"].(string)
	select {
	case <-child.started:
	case <-time.After(time.Second):
		t.Fatal("side turn did not start")
	}
	updated := callSideTool(t, parent, SideSessionToolSubscribe, map[string]any{
		"session_id": id,
		"subscribed": false,
	})
	if updated["subscribed"] != false {
		t.Fatalf("subscribe result = %#v", updated)
	}
	stopped := callSideTool(t, parent, SideSessionToolStop, map[string]any{"session_id": id})
	if stopped["stopped"] != true {
		t.Fatalf("stop result = %#v", stopped)
	}
	if _, err := parent.sideSessions.Status(id); !errors.Is(err, ErrSideSessionNotActive) {
		t.Fatalf("status after stop error = %v", err)
	}
	if _, err := runtime.NewPendingInputQueue(filepath.Join(parent.cfg.SessionsDir(), id), runtime.PendingInputQueueOptions{}).Records(); err != nil {
		t.Fatalf("durable side session state missing: %v", err)
	}
}
