package module

import (
	"context"
	"errors"
	"fmt"
)

type Scope string

const (
	ScopeRuntime Scope = "runtime"
	ScopeThread  Scope = "thread"
)

type RuntimeResource interface {
	StartRuntime(context.Context, RuntimeContext) error
	CloseRuntime(context.Context) error
}

type RuntimeQuiescer interface {
	QuiesceRuntime(context.Context) error
}

type ThreadResource interface {
	StartThread(context.Context, ThreadContext) error
	CloseThread(context.Context) error
}

type ThreadQuiescer interface {
	QuiesceThread(context.Context) error
}

// ContextRenewalClear is a staged module-state clear. Finalize is used after
// the Generation boundary commits; Rollback is used when it does not.
type ContextRenewalClear struct {
	Finalize func() error
	Rollback func() error
}

// ContextRenewalCleaner owns module state that must be cleared around a new
// Context Generation commit. Only enabled modules exist in the Set, so
// disabled module files remain untouched.
type ContextRenewalCleaner interface {
	ClearContextForRenewal(context.Context, string) (ContextRenewalClear, error)
}

// ClearContextForRenewal invokes enabled Thread modules in registration order
// and stops before the Generation boundary if any owner cannot stage its clear.
func ClearContextForRenewal(ctx context.Context, set *Set, generationID string) (ContextRenewalClear, error) {
	if set == nil {
		return noOpContextRenewalClear(), nil
	}
	ctx = nonNilContext(ctx)
	finalizers := make([]func() error, 0)
	rollbacks := make([]func() error, 0)
	for _, mod := range set.Modules() {
		cleaner, ok := mod.(ContextRenewalCleaner)
		if !ok {
			continue
		}
		clear, err := cleaner.ClearContextForRenewal(ctx, generationID)
		if err != nil {
			return ContextRenewalClear{}, errors.Join(
				fmt.Errorf("runtime module %q clear context state: %w", mod.ID(), err),
				runContextRenewalActionsReverse(rollbacks),
			)
		}
		finalizers = append(finalizers, clear.Finalize)
		rollbacks = append(rollbacks, clear.Rollback)
	}
	return ContextRenewalClear{
		Finalize: func() error { return runContextRenewalActions(finalizers) },
		Rollback: func() error { return runContextRenewalActionsReverse(rollbacks) },
	}, nil
}

func noOpContextRenewalClear() ContextRenewalClear {
	return ContextRenewalClear{
		Finalize: func() error { return nil },
		Rollback: func() error { return nil },
	}
}

func runContextRenewalActions(actions []func() error) error {
	var err error
	for _, action := range actions {
		if action != nil {
			err = errors.Join(err, action())
		}
	}
	return err
}

func runContextRenewalActionsReverse(actions []func() error) error {
	var err error
	for index := len(actions) - 1; index >= 0; index-- {
		if actions[index] != nil {
			err = errors.Join(err, actions[index]())
		}
	}
	return err
}

// ContextRenewalObserver receives the post-commit boundary created by New.
// It is intentionally notification-only: the Journal transition is already
// durable, so observers must update derived or transient state without trying
// to veto or re-enter the Engine.
type ContextRenewalObserver interface {
	ContextRenewed(context.Context)
}

// NotifyContextRenewed invokes observers in set and registration order. A
// module registered in more than one set is responsible for idempotence.
func NotifyContextRenewed(ctx context.Context, sets ...*Set) {
	ctx = nonNilContext(ctx)
	for _, set := range sets {
		if set == nil {
			continue
		}
		for _, mod := range set.Modules() {
			if observer, ok := mod.(ContextRenewalObserver); ok {
				observer.ContextRenewed(ctx)
			}
		}
	}
}

type RuntimeFactorySpec struct {
	ID      ID
	Enabled bool
	New     func(context.Context, RuntimeContext) (Module, error)
}

type ThreadFactorySpec struct {
	ID      ID
	Enabled bool
	New     func(context.Context, ThreadContext) (Module, error)
}

func BuildRuntimeSet(ctx context.Context, specs []RuntimeFactorySpec, runtimeContext RuntimeContext, toolContext ToolContext) (*Set, error) {
	registry, err := constructRuntimeRegistry(ctx, specs, runtimeContext)
	if err != nil {
		return nil, err
	}
	set, err := registry.Seal(nonNilContext(ctx), toolContext)
	if err != nil {
		return nil, err
	}
	set.scope = ScopeRuntime
	return set, nil
}

// BuildAndStartRuntimeSet freezes structural capability indexes before
// starting resources, then materializes and validates resource-dependent Tool
// contributions before returning a publishable set.
func BuildAndStartRuntimeSet(ctx context.Context, specs []RuntimeFactorySpec, runtimeContext RuntimeContext, toolContext ToolContext) (*Set, error) {
	registry, err := constructRuntimeRegistry(ctx, specs, runtimeContext)
	if err != nil {
		return nil, err
	}
	set, err := registry.freeze()
	if err != nil {
		return nil, err
	}
	set.scope = ScopeRuntime
	if err := set.StartRuntime(ctx, runtimeContext); err != nil {
		return nil, err
	}
	if err := set.materializeToolCatalog(ctx, toolContext); err != nil {
		catalogErr := fmt.Errorf("runtime modules: materialize runtime tool catalog: %w", err)
		rollbackErr := set.rollbackStartedRuntime(nonNilContext(ctx), "catalog rollback runtime")
		return nil, errors.Join(catalogErr, rollbackErr)
	}
	return set, nil
}

func constructRuntimeRegistry(ctx context.Context, specs []RuntimeFactorySpec, runtimeContext RuntimeContext) (*Registry, error) {
	registry := NewRegistry()
	for _, spec := range specs {
		if !spec.Enabled {
			continue
		}
		id := normalizeID(spec.ID)
		if id == "" {
			return nil, fmt.Errorf("runtime modules: runtime factory has empty id")
		}
		if spec.New == nil {
			return nil, fmt.Errorf("runtime modules: runtime factory %q has nil constructor", id)
		}
		mod, err := spec.New(nonNilContext(ctx), runtimeContext)
		if err != nil {
			return nil, fmt.Errorf("runtime modules: construct runtime module %q: %w", id, err)
		}
		if err := validateFactoryModule(id, mod); err != nil {
			return nil, err
		}
		if err := registry.Register(mod); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

func BuildThreadSet(ctx context.Context, specs []ThreadFactorySpec, threadContext ThreadContext, toolContext ToolContext) (*Set, error) {
	registry, err := constructThreadRegistry(ctx, specs, threadContext)
	if err != nil {
		return nil, err
	}
	set, err := registry.Seal(nonNilContext(ctx), toolContext)
	if err != nil {
		return nil, err
	}
	set.scope = ScopeThread
	return set, nil
}

// BuildAndStartThreadSet is the Thread-scoped counterpart to
// BuildAndStartRuntimeSet.
func BuildAndStartThreadSet(ctx context.Context, specs []ThreadFactorySpec, threadContext ThreadContext, toolContext ToolContext) (*Set, error) {
	registry, err := constructThreadRegistry(ctx, specs, threadContext)
	if err != nil {
		return nil, err
	}
	set, err := registry.freeze()
	if err != nil {
		return nil, err
	}
	set.scope = ScopeThread
	if err := set.StartThread(ctx, threadContext); err != nil {
		return nil, err
	}
	if err := set.materializeToolCatalog(ctx, toolContext); err != nil {
		catalogErr := fmt.Errorf("runtime modules: materialize thread tool catalog: %w", err)
		rollbackErr := set.rollbackStartedThread(nonNilContext(ctx), "catalog rollback thread")
		return nil, errors.Join(catalogErr, rollbackErr)
	}
	return set, nil
}

func constructThreadRegistry(ctx context.Context, specs []ThreadFactorySpec, threadContext ThreadContext) (*Registry, error) {
	registry := NewRegistry()
	for _, spec := range specs {
		if !spec.Enabled {
			continue
		}
		id := normalizeID(spec.ID)
		if id == "" {
			return nil, fmt.Errorf("runtime modules: thread factory has empty id")
		}
		if spec.New == nil {
			return nil, fmt.Errorf("runtime modules: thread factory %q has nil constructor", id)
		}
		mod, err := spec.New(nonNilContext(ctx), threadContext)
		if err != nil {
			return nil, fmt.Errorf("runtime modules: construct thread module %q: %w", id, err)
		}
		if err := validateFactoryModule(id, mod); err != nil {
			return nil, err
		}
		if err := registry.Register(mod); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

func validateFactoryModule(factoryID ID, mod Module) error {
	if mod == nil {
		return fmt.Errorf("runtime modules: factory %q returned nil module", factoryID)
	}
	moduleID := normalizeID(mod.ID())
	if moduleID != factoryID {
		return fmt.Errorf("runtime modules: factory %q returned module %q", factoryID, moduleID)
	}
	return nil
}

type lifecycleState struct {
	started    []registeredModule
	quiesced   bool
	quiesceErr error
	closed     bool
	closeErr   error
}

func (s *Set) StartRuntime(ctx context.Context, runtimeContext RuntimeContext) error {
	if s == nil {
		return nil
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.scope != ScopeRuntime {
		return fmt.Errorf("runtime modules: cannot start %q set as runtime", s.scope)
	}
	if s.state.closed {
		return fmt.Errorf("runtime modules: runtime set is closed")
	}
	if s.state.started != nil {
		return nil
	}
	for _, registered := range s.modules {
		resource, ok := registered.module.(RuntimeResource)
		if !ok {
			continue
		}
		if err := resource.StartRuntime(nonNilContext(ctx), runtimeContext); err != nil {
			startErr := fmt.Errorf("runtime module %q start runtime: %w", registered.id, err)
			rollbackErr := closeStartedRuntime(nonNilContext(ctx), s.state.started, "rollback runtime")
			s.state.closed = true
			s.state.closeErr = rollbackErr
			return errors.Join(startErr, rollbackErr)
		}
		s.state.started = append(s.state.started, registered)
	}
	if s.state.started == nil {
		s.state.started = []registeredModule{}
	}
	return nil
}

// QuiesceRuntime stops new asynchronous work while the Set remains readable.
// A deferred cleanup error is deliberately not cached so callers can wait and
// retry the same quiesce boundary before closing resources.
func (s *Set) QuiesceRuntime(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	return s.quiesceRuntime(nonNilContext(ctx))
}

func (s *Set) CloseRuntime(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()

	quiesceErr := s.quiesceRuntime(nonNilContext(ctx))
	if isDeferredLifecycleError(quiesceErr) {
		return quiesceErr
	}

	s.mu.Lock()
	if s.scope != ScopeRuntime {
		s.mu.Unlock()
		return fmt.Errorf("runtime modules: cannot close %q set as runtime", s.scope)
	}
	if s.state.closed {
		err := s.state.closeErr
		s.mu.Unlock()
		return err
	}
	s.state.closed = true
	started := append([]registeredModule(nil), s.state.started...)
	s.mu.Unlock()

	closeErr := closeStartedRuntime(nonNilContext(ctx), started, "close runtime")
	result := errors.Join(quiesceErr, closeErr)
	s.mu.Lock()
	s.state.closeErr = result
	s.mu.Unlock()
	return result
}

func (s *Set) StartThread(ctx context.Context, threadContext ThreadContext) error {
	if s == nil {
		return nil
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.scope != ScopeThread {
		return fmt.Errorf("runtime modules: cannot start %q set as thread", s.scope)
	}
	if s.state.closed {
		return fmt.Errorf("runtime modules: thread set is closed")
	}
	if s.state.started != nil {
		return nil
	}
	for _, registered := range s.modules {
		resource, ok := registered.module.(ThreadResource)
		if !ok {
			continue
		}
		if err := resource.StartThread(nonNilContext(ctx), threadContext); err != nil {
			startErr := fmt.Errorf("runtime module %q start thread: %w", registered.id, err)
			rollbackErr := closeStartedThread(nonNilContext(ctx), s.state.started, "rollback thread")
			s.state.closed = true
			s.state.closeErr = rollbackErr
			return errors.Join(startErr, rollbackErr)
		}
		s.state.started = append(s.state.started, registered)
	}
	if s.state.started == nil {
		s.state.started = []registeredModule{}
	}
	return nil
}

func (s *Set) QuiesceThread(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	return s.quiesceThread(nonNilContext(ctx))
}

func (s *Set) CloseThread(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()

	quiesceErr := s.quiesceThread(nonNilContext(ctx))
	if isDeferredLifecycleError(quiesceErr) {
		return quiesceErr
	}

	s.mu.Lock()
	if s.scope != ScopeThread {
		s.mu.Unlock()
		return fmt.Errorf("runtime modules: cannot close %q set as thread", s.scope)
	}
	if s.state.closed {
		err := s.state.closeErr
		s.mu.Unlock()
		return err
	}
	s.state.closed = true
	started := append([]registeredModule(nil), s.state.started...)
	s.mu.Unlock()

	closeErr := closeStartedThread(nonNilContext(ctx), started, "close thread")
	result := errors.Join(quiesceErr, closeErr)
	s.mu.Lock()
	s.state.closeErr = result
	s.mu.Unlock()
	return result
}

func (s *Set) quiesceRuntime(ctx context.Context) error {
	s.mu.RLock()
	if s.scope != ScopeRuntime {
		scope := s.scope
		s.mu.RUnlock()
		return fmt.Errorf("runtime modules: cannot quiesce %q set as runtime", scope)
	}
	if s.state.closed {
		err := s.state.closeErr
		s.mu.RUnlock()
		return err
	}
	if s.state.quiesced {
		err := s.state.quiesceErr
		s.mu.RUnlock()
		return err
	}
	started := append([]registeredModule(nil), s.state.started...)
	s.mu.RUnlock()

	result := quiesceStartedRuntime(ctx, started)
	if isDeferredLifecycleError(result) {
		return result
	}
	s.mu.Lock()
	s.state.quiesced = true
	s.state.quiesceErr = result
	s.mu.Unlock()
	return result
}

func (s *Set) quiesceThread(ctx context.Context) error {
	s.mu.RLock()
	if s.scope != ScopeThread {
		scope := s.scope
		s.mu.RUnlock()
		return fmt.Errorf("runtime modules: cannot quiesce %q set as thread", scope)
	}
	if s.state.closed {
		err := s.state.closeErr
		s.mu.RUnlock()
		return err
	}
	if s.state.quiesced {
		err := s.state.quiesceErr
		s.mu.RUnlock()
		return err
	}
	started := append([]registeredModule(nil), s.state.started...)
	s.mu.RUnlock()

	result := quiesceStartedThread(ctx, started)
	if isDeferredLifecycleError(result) {
		return result
	}
	s.mu.Lock()
	s.state.quiesced = true
	s.state.quiesceErr = result
	s.mu.Unlock()
	return result
}

func (s *Set) rollbackStartedRuntime(ctx context.Context, phase string) error {
	if s == nil {
		return nil
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.mu.Lock()
	if s.state.closed {
		err := s.state.closeErr
		s.mu.Unlock()
		return err
	}
	s.state.closed = true
	started := append([]registeredModule(nil), s.state.started...)
	s.mu.Unlock()
	err := closeStartedRuntime(ctx, started, phase)
	s.mu.Lock()
	s.state.closeErr = err
	s.mu.Unlock()
	return err
}

func (s *Set) rollbackStartedThread(ctx context.Context, phase string) error {
	if s == nil {
		return nil
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.mu.Lock()
	if s.state.closed {
		err := s.state.closeErr
		s.mu.Unlock()
		return err
	}
	s.state.closed = true
	started := append([]registeredModule(nil), s.state.started...)
	s.mu.Unlock()
	err := closeStartedThread(ctx, started, phase)
	s.mu.Lock()
	s.state.closeErr = err
	s.mu.Unlock()
	return err
}

func closeStartedRuntime(ctx context.Context, started []registeredModule, phase string) error {
	var result error
	for i := len(started) - 1; i >= 0; i-- {
		registered := started[i]
		if err := registered.module.(RuntimeResource).CloseRuntime(ctx); err != nil {
			result = errors.Join(result, fmt.Errorf("runtime module %q %s: %w", registered.id, phase, err))
		}
	}
	return result
}

func quiesceStartedRuntime(ctx context.Context, started []registeredModule) error {
	var result error
	for i := len(started) - 1; i >= 0; i-- {
		registered := started[i]
		if quiescer, ok := registered.module.(RuntimeQuiescer); ok {
			if err := quiescer.QuiesceRuntime(ctx); err != nil {
				result = errors.Join(result, fmt.Errorf("runtime module %q quiesce runtime: %w", registered.id, err))
			}
		}
	}
	return result
}

func closeStartedThread(ctx context.Context, started []registeredModule, phase string) error {
	var result error
	for i := len(started) - 1; i >= 0; i-- {
		registered := started[i]
		if err := registered.module.(ThreadResource).CloseThread(ctx); err != nil {
			result = errors.Join(result, fmt.Errorf("runtime module %q %s: %w", registered.id, phase, err))
		}
	}
	return result
}

func quiesceStartedThread(ctx context.Context, started []registeredModule) error {
	var result error
	for i := len(started) - 1; i >= 0; i-- {
		registered := started[i]
		if quiescer, ok := registered.module.(ThreadQuiescer); ok {
			if err := quiescer.QuiesceThread(ctx); err != nil {
				result = errors.Join(result, fmt.Errorf("runtime module %q quiesce thread: %w", registered.id, err))
			}
		}
	}
	return result
}

func isDeferredLifecycleError(err error) bool {
	if err == nil {
		return false
	}
	var deferred interface{ Wait() error }
	return errors.As(err, &deferred)
}

func nonNilContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
