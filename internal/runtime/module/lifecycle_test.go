package module

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/tools"
)

type lifecycleModule struct {
	id         ID
	log        *[]string
	startErr   error
	quiesceErr error
	closeErr   error
}

type startDiscoveredToolModule struct {
	id      ID
	name    string
	started bool
	log     *[]string
}

func (m *startDiscoveredToolModule) ID() ID { return m.id }

func (m *startDiscoveredToolModule) StartRuntime(context.Context, RuntimeContext) error {
	m.started = true
	*m.log = append(*m.log, "start:"+string(m.id))
	return nil
}

func (m *startDiscoveredToolModule) CloseRuntime(context.Context) error {
	*m.log = append(*m.log, "close:"+string(m.id))
	return nil
}

func (m *startDiscoveredToolModule) Tools(context.Context, ToolContext) ([]tools.Tool, error) {
	if !m.started {
		return nil, errors.New("resource has not started")
	}
	return []tools.Tool{{Name: m.name, Handler: func(context.Context, map[string]any) (string, error) { return "ok", nil }}}, nil
}

type deferredLifecycleError struct{}

func (*deferredLifecycleError) Error() string { return "cleanup deferred" }
func (*deferredLifecycleError) Wait() error   { return nil }

type deferredQuiesceModule struct {
	log      *[]string
	deferred bool
}

func (*deferredQuiesceModule) ID() ID                                             { return "deferred" }
func (*deferredQuiesceModule) StartRuntime(context.Context, RuntimeContext) error { return nil }
func (m *deferredQuiesceModule) QuiesceRuntime(context.Context) error {
	*m.log = append(*m.log, "quiesce")
	if !m.deferred {
		m.deferred = true
		return &deferredLifecycleError{}
	}
	return nil
}
func (m *deferredQuiesceModule) CloseRuntime(context.Context) error {
	*m.log = append(*m.log, "close")
	return nil
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

type threadLifecycleModule struct{ lifecycleModule }

type contextRenewalModule struct {
	id  ID
	log *[]string
}

type contextRenewalCleanerModule struct {
	id          ID
	log         *[]string
	err         error
	finalizeErr error
	rollbackErr error
}

func (m *contextRenewalCleanerModule) ID() ID { return m.id }

func (m *contextRenewalCleanerModule) ClearContextForRenewal(context.Context, string) (ContextRenewalClear, error) {
	*m.log = append(*m.log, "clear:"+string(m.id))
	if m.err != nil {
		return ContextRenewalClear{}, m.err
	}
	return ContextRenewalClear{
		Finalize: func() error {
			*m.log = append(*m.log, "finalize:"+string(m.id))
			return m.finalizeErr
		},
		Rollback: func() error {
			*m.log = append(*m.log, "restore:"+string(m.id))
			return m.rollbackErr
		},
	}, nil
}

func (m *contextRenewalModule) ID() ID { return m.id }

func (m *contextRenewalModule) ContextRenewed(context.Context) {
	*m.log = append(*m.log, "renew:"+string(m.id))
}

type leasedContextThreadModule struct {
	contextEntered chan struct{}
	releaseContext chan struct{}
	closeEntered   chan struct{}
}

func (*leasedContextThreadModule) ID() ID { return "leased-context" }

func (m *leasedContextThreadModule) Context(context.Context, ContextRequest) ([]ContextSection, error) {
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

func (*leasedContextThreadModule) StartThread(context.Context, ThreadContext) error { return nil }

func (m *leasedContextThreadModule) CloseThread(context.Context) error {
	close(m.closeEntered)
	return nil
}

func (m *threadLifecycleModule) StartThread(context.Context, ThreadContext) error {
	*m.log = append(*m.log, "start:"+string(m.id))
	return m.startErr
}

func (m *threadLifecycleModule) QuiesceThread(context.Context) error {
	*m.log = append(*m.log, "quiesce:"+string(m.id))
	return m.quiesceErr
}

func (m *threadLifecycleModule) CloseThread(context.Context) error {
	*m.log = append(*m.log, "close:"+string(m.id))
	return m.closeErr
}

func TestThreadCloseWaitsForContextLease(t *testing.T) {
	mod := &leasedContextThreadModule{
		contextEntered: make(chan struct{}),
		releaseContext: make(chan struct{}),
		closeEntered:   make(chan struct{}),
	}
	set, err := BuildThreadSet(context.Background(), []ThreadFactorySpec{{
		ID:      mod.ID(),
		Enabled: true,
		New: func(context.Context, ThreadContext) (Module, error) {
			return mod, nil
		},
	}}, ThreadContext{}, ToolContext{})
	if err != nil {
		t.Fatal(err)
	}
	if err := set.StartThread(context.Background(), ThreadContext{}); err != nil {
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
		closeDone <- set.CloseThread(context.Background())
	}()

	select {
	case <-mod.closeEntered:
		t.Error("Thread Module closed while its Context call was active")
	case <-time.After(100 * time.Millisecond):
	}
	close(mod.releaseContext)
	if err := <-contextDone; err != nil {
		t.Fatal(err)
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	if _, err := set.Context(context.Background(), ContextRequest{}); err == nil || !strings.Contains(err.Error(), "thread set is closed") {
		t.Fatalf("Context() after close error = %v, want closed Thread set", err)
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

func TestBuildAndStartRuntimeSetMaterializesToolsAfterResourceStart(t *testing.T) {
	var log []string
	mod := &startDiscoveredToolModule{id: "discovered", name: "discovered_tool", log: &log}
	set, err := BuildAndStartRuntimeSet(context.Background(), []RuntimeFactorySpec{{
		ID:      mod.ID(),
		Enabled: true,
		New:     func(context.Context, RuntimeContext) (Module, error) { return mod, nil },
	}}, RuntimeContext{}, ToolContext{})
	if err != nil {
		t.Fatal(err)
	}
	if got := set.ToolCatalog().Names(); !reflect.DeepEqual(got, []string{"discovered_tool"}) {
		t.Fatalf("ToolCatalog names = %#v", got)
	}
	if got := set.Descriptors(); !reflect.DeepEqual(got, []Descriptor{{ID: "discovered", Scope: ScopeRuntime}}) {
		t.Fatalf("Descriptors() = %#v", got)
	}
	if err := set.CloseRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"start:discovered", "close:discovered"}; !reflect.DeepEqual(log, want) {
		t.Fatalf("lifecycle log = %#v, want %#v", log, want)
	}
}

func TestBuildAndStartRuntimeSetRollsBackPostStartCatalogFailure(t *testing.T) {
	var log []string
	first := &startDiscoveredToolModule{id: "first", name: "duplicate", log: &log}
	second := &startDiscoveredToolModule{id: "second", name: "duplicate", log: &log}
	_, err := BuildAndStartRuntimeSet(context.Background(), []RuntimeFactorySpec{
		{ID: first.ID(), Enabled: true, New: func(context.Context, RuntimeContext) (Module, error) { return first, nil }},
		{ID: second.ID(), Enabled: true, New: func(context.Context, RuntimeContext) (Module, error) { return second, nil }},
	}, RuntimeContext{}, ToolContext{})
	if err == nil || !strings.Contains(err.Error(), `tool "duplicate" contributed by module "first" and module "second"`) {
		t.Fatalf("BuildAndStartRuntimeSet() error = %v", err)
	}
	want := []string{"start:first", "start:second", "close:second", "close:first"}
	if !reflect.DeepEqual(log, want) {
		t.Fatalf("lifecycle log = %#v, want %#v", log, want)
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

func TestRuntimeQuiesceIsExplicitIdempotentAndDeferredCleanupCanRetry(t *testing.T) {
	var log []string
	mod := &deferredQuiesceModule{log: &log}
	set := buildRuntimeLifecycleSet(t, mod)
	if err := set.StartRuntime(context.Background(), RuntimeContext{}); err != nil {
		t.Fatal(err)
	}
	err := set.QuiesceRuntime(context.Background())
	var deferred interface{ Wait() error }
	if !errors.As(err, &deferred) {
		t.Fatalf("QuiesceRuntime() error = %v, want deferred cleanup", err)
	}
	if err := deferred.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := set.QuiesceRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := set.QuiesceRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := set.CloseRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"quiesce", "quiesce", "close"}
	if !reflect.DeepEqual(log, want) {
		t.Fatalf("lifecycle log = %#v, want %#v", log, want)
	}
}

func TestThreadLifecycleUsesThreadScopeAndReverseCleanup(t *testing.T) {
	var log []string
	set, err := BuildThreadSet(context.Background(), []ThreadFactorySpec{
		{
			ID:      "first",
			Enabled: true,
			New: func(context.Context, ThreadContext) (Module, error) {
				return &threadLifecycleModule{lifecycleModule: lifecycleModule{id: "first", log: &log}}, nil
			},
		},
		{
			ID:      "second",
			Enabled: true,
			New: func(context.Context, ThreadContext) (Module, error) {
				return &threadLifecycleModule{lifecycleModule: lifecycleModule{id: "second", log: &log}}, nil
			},
		},
	}, ThreadContext{ID: "thread-1"}, ToolContext{})
	if err != nil {
		t.Fatal(err)
	}
	if err := set.StartThread(context.Background(), ThreadContext{ID: "thread-1"}); err != nil {
		t.Fatal(err)
	}
	if err := set.CloseThread(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"start:first", "start:second", "quiesce:second", "quiesce:first", "close:second", "close:first"}
	if !reflect.DeepEqual(log, want) {
		t.Fatalf("thread lifecycle log = %#v, want %#v", log, want)
	}
}

func TestThreadStartFailureRollsBackStartedModulesInReverseOrder(t *testing.T) {
	var log []string
	rollbackErr := errors.New("thread rollback failed")
	startErr := errors.New("thread start failed")
	set, err := BuildThreadSet(context.Background(), []ThreadFactorySpec{
		{
			ID:      "first",
			Enabled: true,
			New: func(context.Context, ThreadContext) (Module, error) {
				return &threadLifecycleModule{lifecycleModule: lifecycleModule{id: "first", log: &log, closeErr: rollbackErr}}, nil
			},
		},
		{
			ID:      "second",
			Enabled: true,
			New: func(context.Context, ThreadContext) (Module, error) {
				return &threadLifecycleModule{lifecycleModule: lifecycleModule{id: "second", log: &log, startErr: startErr}}, nil
			},
		},
	}, ThreadContext{ID: "thread-1"}, ToolContext{})
	if err != nil {
		t.Fatal(err)
	}
	err = set.StartThread(context.Background(), ThreadContext{ID: "thread-1"})
	if !errors.Is(err, startErr) || !errors.Is(err, rollbackErr) {
		t.Fatalf("StartThread() error = %v, want joined start and rollback errors", err)
	}
	want := []string{"start:first", "start:second", "close:first"}
	if !reflect.DeepEqual(log, want) {
		t.Fatalf("thread rollback log = %#v, want %#v", log, want)
	}
}

func TestNotifyContextRenewedUsesSetAndRegistrationOrder(t *testing.T) {
	var log []string
	first := buildRuntimeLifecycleSet(t,
		&contextRenewalModule{id: "first-a", log: &log},
		&contextRenewalModule{id: "first-b", log: &log},
	)
	second := buildRuntimeLifecycleSet(t,
		&contextRenewalModule{id: "second", log: &log},
	)

	NotifyContextRenewed(context.Background(), first, nil, second)

	want := []string{"renew:first-a", "renew:first-b", "renew:second"}
	if !reflect.DeepEqual(log, want) {
		t.Fatalf("renewal log = %#v, want %#v", log, want)
	}
}

func TestClearContextForRenewalStopsOnOwnerError(t *testing.T) {
	var log []string
	clearErr := errors.New("clear failed")
	set := buildRuntimeLifecycleSet(t,
		&contextRenewalCleanerModule{id: "goal", log: &log},
		&contextRenewalCleanerModule{id: "notes", log: &log, err: clearErr},
		&contextRenewalCleanerModule{id: "later", log: &log},
	)

	_, err := ClearContextForRenewal(context.Background(), set, "g000001")
	if !errors.Is(err, clearErr) || !strings.Contains(err.Error(), `module "notes"`) {
		t.Fatalf("ClearContextForRenewal() error = %v", err)
	}
	want := []string{"clear:goal", "clear:notes", "restore:goal"}
	if !reflect.DeepEqual(log, want) {
		t.Fatalf("clear order = %#v, want %#v", log, want)
	}
}

func TestClearContextForRenewalFinalizesEveryOwner(t *testing.T) {
	var log []string
	finalizeErr := errors.New("finalize failed")
	set := buildRuntimeLifecycleSet(t,
		&contextRenewalCleanerModule{id: "goal", log: &log, finalizeErr: finalizeErr},
		&contextRenewalCleanerModule{id: "notes", log: &log},
	)

	clear, err := ClearContextForRenewal(context.Background(), set, "g000001")
	if err != nil {
		t.Fatal(err)
	}
	if err := clear.Finalize(); !errors.Is(err, finalizeErr) {
		t.Fatalf("Finalize() error = %v", err)
	}
	want := []string{"clear:goal", "clear:notes", "finalize:goal", "finalize:notes"}
	if !reflect.DeepEqual(log, want) {
		t.Fatalf("clear order = %#v, want %#v", log, want)
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
