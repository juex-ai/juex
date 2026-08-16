package module

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

type lifecycleModule struct {
	id         ID
	log        *[]string
	startErr   error
	quiesceErr error
	closeErr   error
}

func (m *lifecycleModule) ID() ID { return m.id }

func (m *lifecycleModule) StartRuntime(context.Context, RuntimeContext) error {
	*m.log = append(*m.log, "start:"+string(m.id))
	return m.startErr
}

func (m *lifecycleModule) QuiesceRuntime(context.Context) error {
	*m.log = append(*m.log, "quiesce:"+string(m.id))
	return m.quiesceErr
}

func (m *lifecycleModule) CloseRuntime(context.Context) error {
	*m.log = append(*m.log, "close:"+string(m.id))
	return m.closeErr
}

type sessionLifecycleModule struct{ lifecycleModule }

type leasedContextSessionModule struct {
	contextEntered chan struct{}
	releaseContext chan struct{}
	closeEntered   chan struct{}
}

func (*leasedContextSessionModule) ID() ID { return "leased-context" }

func (m *leasedContextSessionModule) Context(context.Context, ContextRequest) ([]ContextSection, error) {
	close(m.contextEntered)
	<-m.releaseContext
	return []ContextSection{{
		Key:        "leased",
		Source:     "test",
		Text:       "context",
		Projection: ContextProjectionSystemPrompt,
		Budget:     UnboundedContextBudget(),
	}}, nil
}

func (*leasedContextSessionModule) StartSession(context.Context, SessionContext) error { return nil }

func (m *leasedContextSessionModule) CloseSession(context.Context) error {
	close(m.closeEntered)
	return nil
}

func (m *sessionLifecycleModule) StartSession(context.Context, SessionContext) error {
	*m.log = append(*m.log, "start:"+string(m.id))
	return m.startErr
}

func (m *sessionLifecycleModule) QuiesceSession(context.Context) error {
	*m.log = append(*m.log, "quiesce:"+string(m.id))
	return m.quiesceErr
}

func (m *sessionLifecycleModule) CloseSession(context.Context) error {
	*m.log = append(*m.log, "close:"+string(m.id))
	return m.closeErr
}

func TestSessionCloseWaitsForContextLease(t *testing.T) {
	mod := &leasedContextSessionModule{
		contextEntered: make(chan struct{}),
		releaseContext: make(chan struct{}),
		closeEntered:   make(chan struct{}),
	}
	set, err := BuildSessionSet(context.Background(), []SessionFactorySpec{{
		ID:      mod.ID(),
		Enabled: true,
		New: func(context.Context, SessionContext) (Module, error) {
			return mod, nil
		},
	}}, SessionContext{}, ToolContext{})
	if err != nil {
		t.Fatal(err)
	}
	if err := set.StartSession(context.Background(), SessionContext{}); err != nil {
		t.Fatal(err)
	}

	contextDone := make(chan error, 1)
	go func() {
		_, err := set.Context(context.Background(), ContextRequest{})
		contextDone <- err
	}()
	<-mod.contextEntered
	closeDone := make(chan error, 1)
	go func() {
		closeDone <- set.CloseSession(context.Background())
	}()

	select {
	case <-mod.closeEntered:
		t.Error("Session Module closed while its Context call was active")
	case <-time.After(100 * time.Millisecond):
	}
	close(mod.releaseContext)
	if err := <-contextDone; err != nil {
		t.Fatal(err)
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	if _, err := set.Context(context.Background(), ContextRequest{}); err == nil || !strings.Contains(err.Error(), "session set is closed") {
		t.Fatalf("Context() after close error = %v, want closed Session set", err)
	}
}

func TestBuildRuntimeSetFiltersDisabledBeforeConstruction(t *testing.T) {
	constructed := 0
	set, err := BuildRuntimeSet(context.Background(), []RuntimeFactorySpec{
		{
			ID:      "disabled",
			Enabled: false,
			New: func(context.Context, RuntimeContext) (Module, error) {
				constructed++
				return testModule{id: "disabled"}, nil
			},
		},
	}, RuntimeContext{}, ToolContext{})
	if err != nil {
		t.Fatal(err)
	}
	if constructed != 0 || len(set.Modules()) != 0 {
		t.Fatalf("constructed = %d, modules = %#v; want zero", constructed, set.Modules())
	}
}

func TestBuildRuntimeSetValidatesFactoryIdentity(t *testing.T) {
	_, err := BuildRuntimeSet(context.Background(), []RuntimeFactorySpec{{
		ID:      "declared",
		Enabled: true,
		New: func(context.Context, RuntimeContext) (Module, error) {
			return testModule{id: "actual"}, nil
		},
	}}, RuntimeContext{}, ToolContext{})
	if err == nil || !strings.Contains(err.Error(), `factory "declared"`) || !strings.Contains(err.Error(), `module "actual"`) {
		t.Fatalf("BuildRuntimeSet() error = %v", err)
	}
}

func TestRuntimeStartFailureRollsBackStartedModulesInReverseOrder(t *testing.T) {
	var log []string
	rollbackErr := errors.New("rollback failed")
	startErr := errors.New("start failed")
	set := buildRuntimeLifecycleSet(t,
		&lifecycleModule{id: "first", log: &log, closeErr: rollbackErr},
		&lifecycleModule{id: "second", log: &log, startErr: startErr},
		&lifecycleModule{id: "third", log: &log},
	)
	err := set.StartRuntime(context.Background(), RuntimeContext{})
	if !errors.Is(err, startErr) || !errors.Is(err, rollbackErr) {
		t.Fatalf("StartRuntime() error = %v, want joined start and rollback errors", err)
	}
	want := []string{"start:first", "start:second", "close:first"}
	if !reflect.DeepEqual(log, want) {
		t.Fatalf("lifecycle log = %#v, want %#v", log, want)
	}
}

func TestRuntimeCloseQuiescesAndClosesInReverseOrderAndJoinsErrors(t *testing.T) {
	var log []string
	quiesceErr := errors.New("quiesce failed")
	closeErr := errors.New("close failed")
	set := buildRuntimeLifecycleSet(t,
		&lifecycleModule{id: "first", log: &log, closeErr: closeErr},
		&lifecycleModule{id: "second", log: &log, quiesceErr: quiesceErr},
	)
	if err := set.StartRuntime(context.Background(), RuntimeContext{}); err != nil {
		t.Fatal(err)
	}
	err := set.CloseRuntime(context.Background())
	if !errors.Is(err, quiesceErr) || !errors.Is(err, closeErr) {
		t.Fatalf("CloseRuntime() error = %v, want joined cleanup errors", err)
	}
	want := []string{"start:first", "start:second", "quiesce:second", "quiesce:first", "close:second", "close:first"}
	if !reflect.DeepEqual(log, want) {
		t.Fatalf("lifecycle log = %#v, want %#v", log, want)
	}
	if err := set.CloseRuntime(context.Background()); !errors.Is(err, quiesceErr) || !errors.Is(err, closeErr) {
		t.Fatalf("second CloseRuntime() error = %v", err)
	}
	if !reflect.DeepEqual(log, want) {
		t.Fatalf("second close repeated lifecycle work: %#v", log)
	}
}

func TestSessionLifecycleUsesSessionScopeAndReverseCleanup(t *testing.T) {
	var log []string
	set, err := BuildSessionSet(context.Background(), []SessionFactorySpec{
		{
			ID:      "first",
			Enabled: true,
			New: func(context.Context, SessionContext) (Module, error) {
				return &sessionLifecycleModule{lifecycleModule: lifecycleModule{id: "first", log: &log}}, nil
			},
		},
		{
			ID:      "second",
			Enabled: true,
			New: func(context.Context, SessionContext) (Module, error) {
				return &sessionLifecycleModule{lifecycleModule: lifecycleModule{id: "second", log: &log}}, nil
			},
		},
	}, SessionContext{ID: "session-1"}, ToolContext{})
	if err != nil {
		t.Fatal(err)
	}
	if err := set.StartSession(context.Background(), SessionContext{ID: "session-1"}); err != nil {
		t.Fatal(err)
	}
	if err := set.CloseSession(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"start:first", "start:second", "quiesce:second", "quiesce:first", "close:second", "close:first"}
	if !reflect.DeepEqual(log, want) {
		t.Fatalf("session lifecycle log = %#v, want %#v", log, want)
	}
}

func TestSessionStartFailureRollsBackStartedModulesInReverseOrder(t *testing.T) {
	var log []string
	rollbackErr := errors.New("session rollback failed")
	startErr := errors.New("session start failed")
	set, err := BuildSessionSet(context.Background(), []SessionFactorySpec{
		{
			ID:      "first",
			Enabled: true,
			New: func(context.Context, SessionContext) (Module, error) {
				return &sessionLifecycleModule{lifecycleModule: lifecycleModule{id: "first", log: &log, closeErr: rollbackErr}}, nil
			},
		},
		{
			ID:      "second",
			Enabled: true,
			New: func(context.Context, SessionContext) (Module, error) {
				return &sessionLifecycleModule{lifecycleModule: lifecycleModule{id: "second", log: &log, startErr: startErr}}, nil
			},
		},
	}, SessionContext{ID: "session-1"}, ToolContext{})
	if err != nil {
		t.Fatal(err)
	}
	err = set.StartSession(context.Background(), SessionContext{ID: "session-1"})
	if !errors.Is(err, startErr) || !errors.Is(err, rollbackErr) {
		t.Fatalf("StartSession() error = %v, want joined start and rollback errors", err)
	}
	want := []string{"start:first", "start:second", "close:first"}
	if !reflect.DeepEqual(log, want) {
		t.Fatalf("session rollback log = %#v, want %#v", log, want)
	}
}

func buildRuntimeLifecycleSet(t *testing.T, modules ...Module) *Set {
	t.Helper()
	registry := NewRegistry()
	for _, mod := range modules {
		if err := registry.Register(mod); err != nil {
			t.Fatal(err)
		}
	}
	set, err := registry.Seal(context.Background(), ToolContext{})
	if err != nil {
		t.Fatal(err)
	}
	set.scope = ScopeRuntime
	return set
}
