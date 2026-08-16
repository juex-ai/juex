package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/eventcatalog"
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/runtime"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/session"
	"github.com/juex-ai/juex/internal/toolevents"
	"github.com/juex-ai/juex/internal/tools"
)

func TestSwitchToNewPrimarySessionWaitsForLifecycleReaders(t *testing.T) {
	a, _ := newStubApp(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	readDone := make(chan error, 1)
	go func() {
		readDone <- a.ReadSession(func(sess *session.Session) error {
			close(entered)
			<-release
			return sess.Append(llm.TextMessage(llm.RoleUser, "reader completed on old session"))
		})
	}()
	<-entered

	switchDone := make(chan error, 1)
	go func() {
		switchDone <- a.SwitchToNewPrimarySession()
	}()
	select {
	case err := <-switchDone:
		t.Fatalf("session switch completed before lifecycle reader: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	if err := <-readDone; err != nil {
		t.Fatal(err)
	}
	if err := <-switchDone; err != nil {
		t.Fatal(err)
	}
}

func TestSwitchToNewPrimarySessionBusyRestoresHistory(t *testing.T) {
	a, _ := newStubApp(t)
	oldIdentity, ok := a.SessionIdentity()
	if !ok {
		t.Fatal("missing initial session")
	}
	if err := a.Engine.ReserveTurnID("turn-busy"); err != nil {
		t.Fatal(err)
	}

	err := a.SwitchToNewPrimarySession()
	if !errors.Is(err, runtime.ErrSessionRuntimeBusy) {
		t.Fatalf("SwitchToNewPrimarySession() error = %v, want ErrSessionRuntimeBusy", err)
	}
	identity, ok := a.SessionIdentity()
	if !ok || identity.ID != oldIdentity.ID {
		t.Fatalf("active app session = %+v, want %q", identity, oldIdentity.ID)
	}
	history, err := session.LoadHistory(a.cfg.HistoryPath())
	if err != nil {
		t.Fatal(err)
	}
	if history.Active == nil || history.Active.ID != oldIdentity.ID {
		t.Fatalf("history active = %+v, want %q", history.Active, oldIdentity.ID)
	}
	if len(history.Sessions) != 1 || history.Sessions[0].ID != oldIdentity.ID {
		t.Fatalf("history sessions = %+v, want only original session", history.Sessions)
	}
}

type testSessionModule struct {
	id         runtimemodule.ID
	closeErr   error
	closeCount *int
}

type sessionStartPolicyTracker struct {
	mu                 sync.Mutex
	sessionIDs         []string
	rejectCall         int
	replacementStarted chan struct{}
	releaseReplacement chan struct{}
}

type trackedSessionStartPolicy struct {
	tracker *sessionStartPolicyTracker
}

func (*trackedSessionStartPolicy) ID() runtimemodule.ID { return "tracked-session-start" }

func (m *trackedSessionStartPolicy) ApplySessionStart(ctx context.Context, request runtimemodule.SessionStartRequest) (runtimemodule.SessionStartDecision, error) {
	m.tracker.mu.Lock()
	m.tracker.sessionIDs = append(m.tracker.sessionIDs, request.Session.ID)
	call := len(m.tracker.sessionIDs)
	m.tracker.mu.Unlock()
	if call == 2 && m.tracker.replacementStarted != nil {
		close(m.tracker.replacementStarted)
		select {
		case <-ctx.Done():
			return runtimemodule.SessionStartDecision{}, ctx.Err()
		case <-m.tracker.releaseReplacement:
		}
	}
	if call == m.tracker.rejectCall {
		return runtimemodule.SessionStartDecision{Reject: true, Reason: "replacement blocked"}, nil
	}
	return runtimemodule.SessionStartDecision{Context: []runtimemodule.PolicyContext{{
		Label: "Session startup context:\n",
		Text:  request.Session.ID,
	}}}, nil
}

func (t *sessionStartPolicyTracker) snapshot() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.sessionIDs...)
}

type trackedSessionModule struct {
	index   int
	tracker *sessionModuleTracker
}

type sessionModuleTracker struct {
	mu                      sync.Mutex
	constructed             int
	closed                  map[int]int
	firstReplacementStarted chan struct{}
	releaseFirstReplacement chan struct{}
}

func newSessionModuleTracker() *sessionModuleTracker {
	return &sessionModuleTracker{
		closed:                  make(map[int]int),
		firstReplacementStarted: make(chan struct{}),
		releaseFirstReplacement: make(chan struct{}),
	}
}

func (t *sessionModuleTracker) newModule() runtimemodule.Module {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.constructed++
	return &trackedSessionModule{index: t.constructed, tracker: t}
}

func (t *sessionModuleTracker) snapshot() (int, map[int]int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	closed := make(map[int]int, len(t.closed))
	for index, count := range t.closed {
		closed[index] = count
	}
	return t.constructed, closed
}

func (*trackedSessionModule) ID() runtimemodule.ID { return "tracked-session" }

func (m *trackedSessionModule) StartSession(context.Context, runtimemodule.SessionContext) error {
	if m.index == 2 {
		close(m.tracker.firstReplacementStarted)
		<-m.tracker.releaseFirstReplacement
	}
	return nil
}

func (m *trackedSessionModule) CloseSession(context.Context) error {
	m.tracker.mu.Lock()
	defer m.tracker.mu.Unlock()
	m.tracker.closed[m.index]++
	return nil
}

type duplicateSessionToolModule struct {
	starts *int
}

func (*duplicateSessionToolModule) ID() runtimemodule.ID { return "duplicate-session-tool" }

func (*duplicateSessionToolModule) Tools(context.Context, runtimemodule.ToolContext) ([]tools.Tool, error) {
	return []tools.Tool{{
		Name:    "read",
		Handler: func(context.Context, map[string]any) (string, error) { return "duplicate", nil },
	}}, nil
}

func (m *duplicateSessionToolModule) StartSession(context.Context, runtimemodule.SessionContext) error {
	*m.starts = *m.starts + 1
	return nil
}

func (*duplicateSessionToolModule) CloseSession(context.Context) error { return nil }

type duplicateSessionContextModule struct {
	starts *int
}

func (*duplicateSessionContextModule) ID() runtimemodule.ID { return "duplicate-session-context" }

func (*duplicateSessionContextModule) Context(context.Context, runtimemodule.ContextRequest) ([]runtimemodule.ContextSection, error) {
	return []runtimemodule.ContextSection{{
		Key:        "skills",
		Source:     "test",
		Text:       "duplicate",
		Projection: runtimemodule.ContextProjectionSystemPrompt,
		Budget:     runtimemodule.UnboundedContextBudget(),
	}}, nil
}

func (m *duplicateSessionContextModule) StartSession(context.Context, runtimemodule.SessionContext) error {
	*m.starts = *m.starts + 1
	return nil
}

func (*duplicateSessionContextModule) CloseSession(context.Context) error { return nil }

func TestAppValidatesCompleteModuleCatalogBeforeSessionStart(t *testing.T) {
	dir := t.TempDir()
	starts := 0
	_, err := New(Options{
		Config: config.Config{
			ProviderID:    "openai",
			APIKey:        "x",
			Model:         "m",
			WorkDir:       dir,
			AgentStateDir: filepath.Join(dir, ".juex"),
		},
		Provider:           &stubProvider{},
		WorkDir:            dir,
		DisableMCP:         true,
		disableObservables: true,
		sessionModuleFactories: []runtimemodule.SessionFactorySpec{{
			ID:      "duplicate-session-tool",
			Enabled: true,
			New: func(context.Context, runtimemodule.SessionContext) (runtimemodule.Module, error) {
				return &duplicateSessionToolModule{starts: &starts}, nil
			},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), `tool "read"`) || !strings.Contains(err.Error(), `module "builtin-tools"`) || !strings.Contains(err.Error(), `module "duplicate-session-tool"`) {
		t.Fatalf("New() error = %v", err)
	}
	if starts != 0 {
		t.Fatalf("Session Module started before catalog validation: %d", starts)
	}
}

func TestAppValidatesCompleteModuleContextBeforeSessionStart(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, ".agents", "skills", "context-test")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: context-test\ndescription: context validation fixture\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	starts := 0
	_, err := New(Options{
		Config: config.Config{
			ProviderID:    "openai",
			APIKey:        "x",
			Model:         "m",
			WorkDir:       dir,
			AgentStateDir: filepath.Join(dir, ".juex"),
			Skills:        config.DefaultSkillsConfig(),
		},
		Provider:           &stubProvider{},
		WorkDir:            dir,
		DisableMCP:         true,
		disableObservables: true,
		sessionModuleFactories: []runtimemodule.SessionFactorySpec{{
			ID:      "duplicate-session-context",
			Enabled: true,
			New: func(context.Context, runtimemodule.SessionContext) (runtimemodule.Module, error) {
				return &duplicateSessionContextModule{starts: &starts}, nil
			},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), `context key "skills"`) || !strings.Contains(err.Error(), `module "skills"`) || !strings.Contains(err.Error(), `module "duplicate-session-context"`) {
		t.Fatalf("New() error = %v", err)
	}
	if starts != 0 {
		t.Fatalf("Session Module started before context validation: %d", starts)
	}
}

func (m *testSessionModule) ID() runtimemodule.ID { return m.id }

func (*testSessionModule) StartSession(context.Context, runtimemodule.SessionContext) error {
	return nil
}

func (m *testSessionModule) CloseSession(context.Context) error {
	if m.closeCount != nil {
		(*m.closeCount)++
	}
	return m.closeErr
}

func TestSwitchToNewPrimarySessionRunsSessionStartPolicies(t *testing.T) {
	dir := t.TempDir()
	tracker := &sessionStartPolicyTracker{}
	a, err := New(Options{
		Config: config.Config{
			ProviderID:    "openai",
			APIKey:        "x",
			Model:         "m",
			WorkDir:       dir,
			AgentStateDir: filepath.Join(dir, ".juex"),
		},
		Provider:           &stubProvider{},
		WorkDir:            dir,
		DisableMCP:         true,
		disableObservables: true,
		sessionModuleFactories: []runtimemodule.SessionFactorySpec{{
			ID:      "tracked-session-start",
			Enabled: true,
			New: func(context.Context, runtimemodule.SessionContext) (runtimemodule.Module, error) {
				return &trackedSessionStartPolicy{tracker: tracker}, nil
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	oldID := a.Session.ID

	if err := a.SwitchToNewPrimarySession(); err != nil {
		t.Fatal(err)
	}
	newID := a.Session.ID
	if newID == oldID {
		t.Fatal("session replacement retained the old session")
	}
	if got, want := tracker.snapshot(), []string{oldID, newID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("session start calls = %#v, want %#v", got, want)
	}
	var startupContext []string
	for _, message := range a.ActiveContext().Messages {
		if message.Kind == llm.MessageKindRuntimeContext && strings.HasPrefix(message.FirstText(), "Session startup context:\n") {
			startupContext = append(startupContext, message.FirstText())
		}
	}
	if want := []string{"Session startup context:\n" + newID}; !reflect.DeepEqual(startupContext, want) {
		t.Fatalf("replacement startup context = %#v, want %#v", startupContext, want)
	}
}

func TestSwitchToNewPrimarySessionStartPolicyRejectsReplacement(t *testing.T) {
	dir := t.TempDir()
	tracker := &sessionStartPolicyTracker{rejectCall: 2}
	a, err := New(Options{
		Config: config.Config{
			ProviderID:    "openai",
			APIKey:        "x",
			Model:         "m",
			WorkDir:       dir,
			AgentStateDir: filepath.Join(dir, ".juex"),
		},
		Provider:           &stubProvider{},
		WorkDir:            dir,
		DisableMCP:         true,
		disableObservables: true,
		sessionModuleFactories: []runtimemodule.SessionFactorySpec{{
			ID:      "tracked-session-start",
			Enabled: true,
			New: func(context.Context, runtimemodule.SessionContext) (runtimemodule.Module, error) {
				return &trackedSessionStartPolicy{tracker: tracker}, nil
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	oldID := a.Session.ID
	oldDir := a.Session.Dir

	err = a.SwitchToNewPrimarySession()
	if err == nil || !strings.Contains(err.Error(), `runtime module "tracked-session-start" session start rejected: replacement blocked`) {
		t.Fatalf("session replacement error = %v", err)
	}
	if got := a.Session.ID; got != oldID {
		t.Fatalf("active session after rejected replacement = %q, want %q", got, oldID)
	}
	started := tracker.snapshot()
	if len(started) != 2 || started[0] != oldID || started[1] == oldID {
		t.Fatalf("session start calls = %#v", started)
	}
	history, err := session.LoadHistory(a.cfg.HistoryPath())
	if err != nil {
		t.Fatal(err)
	}
	if history.Active == nil || history.Active.ID != oldID || len(history.Sessions) != 1 {
		t.Fatalf("history after rejected replacement = %+v, want only active %q", history, oldID)
	}
	if err := a.Bus.Emit(events.Event{
		Type:          "test.observability-restored",
		SchemaVersion: 1,
		ReplayPolicy:  events.ReplayIgnorable,
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(oldDir, "logs", "juex.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "event=test.observability-restored") {
		t.Fatalf("previous session observability was not restored:\n%s", data)
	}
}

func TestSwitchToNewPrimarySessionObservabilityFailureDoesNotCorruptRuntimeStatus(t *testing.T) {
	dir := t.TempDir()
	var stderr bytes.Buffer
	a, err := New(Options{
		Config: config.Config{
			ProviderID:    "openai",
			APIKey:        "x",
			Model:         "m",
			WorkDir:       dir,
			AgentStateDir: filepath.Join(dir, ".juex"),
		},
		Provider:           &stubProvider{},
		WorkDir:            dir,
		Stderr:             &stderr,
		DisableMCP:         true,
		disableObservables: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	a.logLevel = "invalid-for-replacement"

	if err := a.SwitchToNewPrimarySession(); err != nil {
		t.Fatal(err)
	}
	snapshot := a.Status.Snapshot()
	if snapshot.Session.ID != a.Session.ID || snapshot.Session.State != runtime.SessionRuntimeIdle {
		t.Fatalf("status session = %+v, want idle replacement %q", snapshot.Session, a.Session.ID)
	}
	if snapshot.Turn != nil || snapshot.LastError != nil {
		t.Fatalf("recorder failure corrupted runtime status: %+v", snapshot)
	}
	if got := stderr.String(); !strings.Contains(got, "session observability") || !strings.Contains(got, "invalid-for-replacement") {
		t.Fatalf("observability warning missing from stderr: %q", got)
	}
}

func TestSwitchToNewPrimarySessionClosesCandidateModulesWhenObservabilityRollbackFails(t *testing.T) {
	dir := t.TempDir()
	tracker := &sessionStartPolicyTracker{rejectCall: 2}
	closed := 0
	a, err := New(Options{
		Config: config.Config{
			ProviderID:    "openai",
			APIKey:        "x",
			Model:         "m",
			WorkDir:       dir,
			AgentStateDir: filepath.Join(dir, ".juex"),
		},
		Provider:           &stubProvider{},
		WorkDir:            dir,
		DisableMCP:         true,
		disableObservables: true,
		sessionModuleFactories: []runtimemodule.SessionFactorySpec{
			{
				ID:      "close-tracked",
				Enabled: true,
				New: func(context.Context, runtimemodule.SessionContext) (runtimemodule.Module, error) {
					return &testSessionModule{id: "close-tracked", closeCount: &closed}, nil
				},
			},
			{
				ID:      "tracked-session-start",
				Enabled: true,
				New: func(context.Context, runtimemodule.SessionContext) (runtimemodule.Module, error) {
					return &trackedSessionStartPolicy{tracker: tracker}, nil
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	oldID := a.Session.ID
	a.logLevel = "invalid-for-rollback"

	err = a.SwitchToNewPrimarySession()
	if err == nil || !strings.Contains(err.Error(), "replacement blocked") || !strings.Contains(err.Error(), "invalid-for-rollback") {
		t.Fatalf("session replacement error = %v, want policy rejection and observability rollback failure", err)
	}
	if got := a.Session.ID; got != oldID {
		t.Fatalf("active session after rejected replacement = %q, want %q", got, oldID)
	}
	if closed != 1 {
		t.Fatalf("closed Session Modules = %d, want rejected candidate closed once", closed)
	}
}

func TestSwitchToNewPrimarySessionCancellationStopsSessionStartPolicy(t *testing.T) {
	dir := t.TempDir()
	tracker := &sessionStartPolicyTracker{
		replacementStarted: make(chan struct{}),
		releaseReplacement: make(chan struct{}),
	}
	a, err := New(Options{
		Config: config.Config{
			ProviderID:    "openai",
			APIKey:        "x",
			Model:         "m",
			WorkDir:       dir,
			AgentStateDir: filepath.Join(dir, ".juex"),
		},
		Provider:           &stubProvider{},
		WorkDir:            dir,
		DisableMCP:         true,
		disableObservables: true,
		sessionModuleFactories: []runtimemodule.SessionFactorySpec{{
			ID:      "tracked-session-start",
			Enabled: true,
			New: func(context.Context, runtimemodule.SessionContext) (runtimemodule.Module, error) {
				return &trackedSessionStartPolicy{tracker: tracker}, nil
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	oldID := a.Session.ID

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- a.SwitchToNewPrimarySessionContext(ctx)
	}()
	<-tracker.replacementStarted
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("session replacement error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		close(tracker.releaseReplacement)
		if err := <-done; err != nil {
			t.Fatalf("session replacement ignored cancellation, then returned %v", err)
		}
		t.Fatal("session replacement ignored cancellation while SessionStart policy was running")
	}
	if got := a.Session.ID; got != oldID {
		t.Fatalf("active session after cancelled replacement = %q, want %q", got, oldID)
	}
	history, err := session.LoadHistory(a.cfg.HistoryPath())
	if err != nil {
		t.Fatal(err)
	}
	if history.Active == nil || history.Active.ID != oldID || len(history.Sessions) != 1 {
		t.Fatalf("history after cancelled replacement = %+v, want only active %q", history, oldID)
	}
}

func TestSwitchToNewPrimarySessionSerializesModuleReplacement(t *testing.T) {
	dir := t.TempDir()
	tracker := newSessionModuleTracker()
	a, err := New(Options{
		Config: config.Config{
			ProviderID:    "openai",
			APIKey:        "x",
			Model:         "m",
			WorkDir:       dir,
			AgentStateDir: filepath.Join(dir, ".juex"),
		},
		Provider:           &stubProvider{},
		WorkDir:            dir,
		DisableMCP:         true,
		disableObservables: true,
		sessionModuleFactories: []runtimemodule.SessionFactorySpec{{
			ID:      "tracked-session",
			Enabled: true,
			New: func(context.Context, runtimemodule.SessionContext) (runtimemodule.Module, error) {
				return tracker.newModule(), nil
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- a.SwitchToNewPrimarySession()
	}()
	<-tracker.firstReplacementStarted

	secondCalled := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		close(secondCalled)
		secondDone <- a.SwitchToNewPrimarySession()
	}()
	<-secondCalled
	time.Sleep(100 * time.Millisecond)
	constructedWhileBlocked, _ := tracker.snapshot()

	close(tracker.releaseFirstReplacement)
	if err := <-firstDone; err != nil {
		t.Fatalf("first replacement: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second replacement: %v", err)
	}
	if constructedWhileBlocked != 2 {
		t.Errorf("modules constructed while first replacement blocked = %d, want 2", constructedWhileBlocked)
	}
	constructed, closed := tracker.snapshot()
	if constructed != 3 {
		t.Errorf("constructed modules = %d, want 3", constructed)
	}
	if closed[1] != 1 || closed[2] != 1 || closed[3] != 0 {
		t.Errorf("module close counts before App.Close = %v, want map[1:1 2:1]", closed)
	}

	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	_, closed = tracker.snapshot()
	if closed[3] != 1 {
		t.Errorf("active module close count after App.Close = %d, want 1", closed[3])
	}
}

func TestSwitchToNewPrimarySessionKeepsCommittedReplacementWhenOldModuleCleanupFails(t *testing.T) {
	dir := t.TempDir()
	var stderr bytes.Buffer
	constructed := 0
	a, err := New(Options{
		Config: config.Config{
			ProviderID:    "openai",
			APIKey:        "x",
			Model:         "m",
			WorkDir:       dir,
			AgentStateDir: filepath.Join(dir, ".juex"),
		},
		Provider:           &stubProvider{},
		WorkDir:            dir,
		Stderr:             &stderr,
		DisableMCP:         true,
		disableObservables: true,
		sessionModuleFactories: []runtimemodule.SessionFactorySpec{{
			ID:      "test-session",
			Enabled: true,
			New: func(context.Context, runtimemodule.SessionContext) (runtimemodule.Module, error) {
				constructed++
				mod := &testSessionModule{id: "test-session"}
				if constructed == 1 {
					mod.closeErr = errors.New("old module close failed")
				}
				return mod, nil
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	oldIdentity, ok := a.SessionIdentity()
	if !ok {
		t.Fatal("missing initial session")
	}

	if err := a.SwitchToNewPrimarySession(); err != nil {
		t.Fatalf("committed replacement returned cleanup failure: %v", err)
	}
	newIdentity, ok := a.SessionIdentity()
	if !ok || newIdentity.ID == oldIdentity.ID {
		t.Fatalf("replacement was not committed: old=%+v new=%+v", oldIdentity, newIdentity)
	}
	if !strings.Contains(stderr.String(), "committed session replacement cleanup") || !strings.Contains(stderr.String(), "old module close failed") {
		t.Fatalf("cleanup diagnostic missing from stderr: %q", stderr.String())
	}
}

func TestSwitchToNewPrimarySessionIsAtomicForConcurrentReaders(t *testing.T) {
	a, _ := newStubApp(t)

	const (
		switches = 48
		readers  = 6
	)
	start := make(chan struct{})
	done := make(chan struct{})
	errs := make(chan error, readers+1)
	var wg sync.WaitGroup

	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for {
				select {
				case <-done:
					return
				default:
				}
				if err := assertAtomicAppSessionRead(a); err != nil {
					errs <- err
					return
				}
				status := a.StatusSnapshot()
				if status.SessionID == "" || filepath.Base(status.SessionDir) != status.SessionID {
					errs <- fmt.Errorf("mixed status snapshot: id=%q dir=%q", status.SessionID, status.SessionDir)
					return
				}
				_ = a.ActiveContext()
				_, _ = a.SessionStateStatus()
				_ = a.Engine.PromptSections()
				_ = a.PendingInputStatus()
			}
		}()
	}

	close(start)
	for i := 0; i < switches; i++ {
		if err := a.SwitchToNewPrimarySession(); err != nil {
			errs <- fmt.Errorf("switch %d: %w", i, err)
			break
		}
	}
	close(done)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestSwitchToNewPrimarySessionKeepsCleanupBounded(t *testing.T) {
	a, _ := newStubApp(t)
	initialCleanup := len(a.cleanup)

	for i := 0; i < 12; i++ {
		if err := a.SwitchToNewPrimarySession(); err != nil {
			t.Fatalf("switch %d: %v", i, err)
		}
	}
	if got := len(a.cleanup); got != initialCleanup {
		t.Fatalf("cleanup entries after switches = %d, want %d", got, initialCleanup)
	}
}

func TestReplaceSessionPublishesOnlyRestartRecoveredStatus(t *testing.T) {
	a, _ := newStubApp(t)
	stream := a.Status.OpenStream(runtime.StatusStreamOptions{Follow: true})
	defer stream.Close()
	if _, ok := stream.Next(context.Background()); !ok {
		t.Fatal("stream omitted initial status")
	}
	if err := a.Bus.Emit(events.Event{
		ID:      "old-1",
		Type:    runtime.TurnAdmittedType,
		TurnID:  "turn-old",
		Payload: runtime.TurnAdmittedPayload{},
	}); err != nil {
		t.Fatal(err)
	}

	sess, err := session.New(a.cfg.SessionsDir())
	if err != nil {
		t.Fatal(err)
	}
	closeNewSession := true
	defer func() {
		if closeNewSession {
			_ = sess.Close()
		}
	}()
	for _, event := range []events.Event{
		{
			ID:      "new-1",
			Type:    runtime.TurnAdmittedType,
			TurnID:  "turn-new",
			Payload: runtime.TurnAdmittedPayload{},
		},
		{
			ID:     "new-2",
			Type:   toolevents.RunningType,
			TurnID: "turn-new",
			Payload: toolevents.RunningPayload{
				Name: "exec_command", ToolUseID: "tool-new", Iter: 0, CallIndex: 0, MessageID: "assistant-new",
			},
		},
	} {
		event, err = eventcatalog.Default().Prepare(event)
		if err != nil {
			t.Fatal(err)
		}
		if err := sess.AppendEvent(event); err != nil {
			t.Fatal(err)
		}
	}
	lock, err := session.AcquireSessionLock(sess.Dir, "test-replace")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.replaceSession(context.Background(), sess, lock); err != nil {
		_ = lock.Close()
		t.Fatal(err)
	}
	closeNewSession = false

	replaced, ok := stream.Next(context.Background())
	if !ok ||
		replaced.Session.ID != sess.ID ||
		replaced.Cursor != "new-2" ||
		replaced.Session.State != runtime.SessionRuntimeFailed ||
		replaced.Turn == nil ||
		replaced.Turn.State != runtime.TurnLifecycleCancelled ||
		len(replaced.Tools) != 0 {
		t.Fatalf("replacement status = %+v, %t", replaced, ok)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if extra, ok := stream.Next(ctx); ok {
		t.Fatalf("subscriber received intermediate replacement: %+v", extra)
	}
}

func assertAtomicAppSessionRead(a *App) error {
	return a.ReadSession(func(sess *session.Session) error {
		runtime := a.Engine.SessionRuntimeSnapshot()
		if runtime.Session != sess {
			return fmt.Errorf("app session %q and engine session %q differ", sess.ID, sessionRuntimeID(runtime.Session))
		}
		if runtime.ScratchpadDir != sess.ScratchpadDir() {
			return fmt.Errorf("session %q scratchpad = %q, want %q", sess.ID, runtime.ScratchpadDir, sess.ScratchpadDir())
		}
		if runtime.Notes == nil {
			return fmt.Errorf("session %q has no notes store", sess.ID)
		}
		if runtime.Notes.SessionDir != sess.Dir {
			return fmt.Errorf("session %q notes belong to %q", sess.ID, runtime.Notes.SessionDir)
		}
		if runtime.GoalState == nil {
			return fmt.Errorf("session %q has no goal state store", sess.ID)
		}
		if runtime.GoalState.SessionDir != sess.Dir {
			return fmt.Errorf("session %q goal state belongs to %q", sess.ID, runtime.GoalState.SessionDir)
		}
		if runtime.Modules == nil {
			return fmt.Errorf("session %q has no sealed Module set", sess.ID)
		}
		return nil
	})
}

func sessionRuntimeID(sess *session.Session) string {
	if sess == nil {
		return ""
	}
	return sess.ID
}
