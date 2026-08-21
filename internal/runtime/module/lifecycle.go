package module

import (
	"context"
	"errors"
	"fmt"
)

type Scope string

const (
	ScopeRuntime Scope = "runtime"
	ScopeSession Scope = "session"
)

type RuntimeResource interface {
	StartRuntime(context.Context, RuntimeContext) error
	CloseRuntime(context.Context) error
}

type RuntimeQuiescer interface {
	QuiesceRuntime(context.Context) error
}

type SessionResource interface {
	StartSession(context.Context, SessionContext) error
	CloseSession(context.Context) error
}

type SessionQuiescer interface {
	QuiesceSession(context.Context) error
}

type RuntimeFactorySpec struct {
	ID      ID
	Enabled bool
	New     func(context.Context, RuntimeContext) (Module, error)
}

type SessionFactorySpec struct {
	ID      ID
	Enabled bool
	New     func(context.Context, SessionContext) (Module, error)
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

func BuildSessionSet(ctx context.Context, specs []SessionFactorySpec, sessionContext SessionContext, toolContext ToolContext) (*Set, error) {
	registry, err := constructSessionRegistry(ctx, specs, sessionContext)
	if err != nil {
		return nil, err
	}
	set, err := registry.Seal(nonNilContext(ctx), toolContext)
	if err != nil {
		return nil, err
	}
	set.scope = ScopeSession
	return set, nil
}

// BuildAndStartSessionSet is the Session-scoped counterpart to
// BuildAndStartRuntimeSet.
func BuildAndStartSessionSet(ctx context.Context, specs []SessionFactorySpec, sessionContext SessionContext, toolContext ToolContext) (*Set, error) {
	registry, err := constructSessionRegistry(ctx, specs, sessionContext)
	if err != nil {
		return nil, err
	}
	set, err := registry.freeze()
	if err != nil {
		return nil, err
	}
	set.scope = ScopeSession
	if err := set.StartSession(ctx, sessionContext); err != nil {
		return nil, err
	}
	if err := set.materializeToolCatalog(ctx, toolContext); err != nil {
		catalogErr := fmt.Errorf("runtime modules: materialize session tool catalog: %w", err)
		rollbackErr := set.rollbackStartedSession(nonNilContext(ctx), "catalog rollback session")
		return nil, errors.Join(catalogErr, rollbackErr)
	}
	return set, nil
}

func constructSessionRegistry(ctx context.Context, specs []SessionFactorySpec, sessionContext SessionContext) (*Registry, error) {
	registry := NewRegistry()
	for _, spec := range specs {
		if !spec.Enabled {
			continue
		}
		id := normalizeID(spec.ID)
		if id == "" {
			return nil, fmt.Errorf("runtime modules: session factory has empty id")
		}
		if spec.New == nil {
			return nil, fmt.Errorf("runtime modules: session factory %q has nil constructor", id)
		}
		mod, err := spec.New(nonNilContext(ctx), sessionContext)
		if err != nil {
			return nil, fmt.Errorf("runtime modules: construct session module %q: %w", id, err)
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

func (s *Set) StartSession(ctx context.Context, sessionContext SessionContext) error {
	if s == nil {
		return nil
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.scope != ScopeSession {
		return fmt.Errorf("runtime modules: cannot start %q set as session", s.scope)
	}
	if s.state.closed {
		return fmt.Errorf("runtime modules: session set is closed")
	}
	if s.state.started != nil {
		return nil
	}
	for _, registered := range s.modules {
		resource, ok := registered.module.(SessionResource)
		if !ok {
			continue
		}
		if err := resource.StartSession(nonNilContext(ctx), sessionContext); err != nil {
			startErr := fmt.Errorf("runtime module %q start session: %w", registered.id, err)
			rollbackErr := closeStartedSession(nonNilContext(ctx), s.state.started, "rollback session")
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

func (s *Set) QuiesceSession(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	return s.quiesceSession(nonNilContext(ctx))
}

func (s *Set) CloseSession(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()

	quiesceErr := s.quiesceSession(nonNilContext(ctx))
	if isDeferredLifecycleError(quiesceErr) {
		return quiesceErr
	}

	s.mu.Lock()
	if s.scope != ScopeSession {
		s.mu.Unlock()
		return fmt.Errorf("runtime modules: cannot close %q set as session", s.scope)
	}
	if s.state.closed {
		err := s.state.closeErr
		s.mu.Unlock()
		return err
	}
	s.state.closed = true
	started := append([]registeredModule(nil), s.state.started...)
	s.mu.Unlock()

	closeErr := closeStartedSession(nonNilContext(ctx), started, "close session")
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

func (s *Set) quiesceSession(ctx context.Context) error {
	s.mu.RLock()
	if s.scope != ScopeSession {
		scope := s.scope
		s.mu.RUnlock()
		return fmt.Errorf("runtime modules: cannot quiesce %q set as session", scope)
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

	result := quiesceStartedSession(ctx, started)
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

func (s *Set) rollbackStartedSession(ctx context.Context, phase string) error {
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
	err := closeStartedSession(ctx, started, phase)
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

func closeStartedSession(ctx context.Context, started []registeredModule, phase string) error {
	var result error
	for i := len(started) - 1; i >= 0; i-- {
		registered := started[i]
		if err := registered.module.(SessionResource).CloseSession(ctx); err != nil {
			result = errors.Join(result, fmt.Errorf("runtime module %q %s: %w", registered.id, phase, err))
		}
	}
	return result
}

func quiesceStartedSession(ctx context.Context, started []registeredModule) error {
	var result error
	for i := len(started) - 1; i >= 0; i-- {
		registered := started[i]
		if quiescer, ok := registered.module.(SessionQuiescer); ok {
			if err := quiescer.QuiesceSession(ctx); err != nil {
				result = errors.Join(result, fmt.Errorf("runtime module %q quiesce session: %w", registered.id, err))
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
