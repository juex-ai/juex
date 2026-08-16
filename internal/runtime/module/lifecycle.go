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
	set, err := registry.Seal(nonNilContext(ctx), toolContext)
	if err != nil {
		return nil, err
	}
	set.scope = ScopeRuntime
	return set, nil
}

func BuildSessionSet(ctx context.Context, specs []SessionFactorySpec, sessionContext SessionContext, toolContext ToolContext) (*Set, error) {
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
	set, err := registry.Seal(nonNilContext(ctx), toolContext)
	if err != nil {
		return nil, err
	}
	set.scope = ScopeSession
	return set, nil
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
	started  []registeredModule
	closed   bool
	closeErr error
}

func (s *Set) StartRuntime(ctx context.Context, runtimeContext RuntimeContext) error {
	if s == nil {
		return nil
	}
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
			rollbackErr := closeStartedRuntime(nonNilContext(ctx), s.state.started)
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

func (s *Set) CloseRuntime(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.scope != ScopeRuntime {
		return fmt.Errorf("runtime modules: cannot close %q set as runtime", s.scope)
	}
	if s.state.closed {
		return s.state.closeErr
	}
	s.state.closed = true
	s.state.closeErr = quiesceAndCloseRuntime(nonNilContext(ctx), s.state.started)
	return s.state.closeErr
}

func (s *Set) StartSession(ctx context.Context, sessionContext SessionContext) error {
	if s == nil {
		return nil
	}
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
			rollbackErr := closeStartedSession(nonNilContext(ctx), s.state.started)
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

func (s *Set) CloseSession(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.scope != ScopeSession {
		return fmt.Errorf("runtime modules: cannot close %q set as session", s.scope)
	}
	if s.state.closed {
		return s.state.closeErr
	}
	s.state.closed = true
	s.state.closeErr = quiesceAndCloseSession(nonNilContext(ctx), s.state.started)
	return s.state.closeErr
}

func closeStartedRuntime(ctx context.Context, started []registeredModule) error {
	var result error
	for i := len(started) - 1; i >= 0; i-- {
		registered := started[i]
		if err := registered.module.(RuntimeResource).CloseRuntime(ctx); err != nil {
			result = errors.Join(result, fmt.Errorf("runtime module %q rollback runtime: %w", registered.id, err))
		}
	}
	return result
}

func quiesceAndCloseRuntime(ctx context.Context, started []registeredModule) error {
	var result error
	for i := len(started) - 1; i >= 0; i-- {
		registered := started[i]
		if quiescer, ok := registered.module.(RuntimeQuiescer); ok {
			if err := quiescer.QuiesceRuntime(ctx); err != nil {
				result = errors.Join(result, fmt.Errorf("runtime module %q quiesce runtime: %w", registered.id, err))
			}
		}
	}
	for i := len(started) - 1; i >= 0; i-- {
		registered := started[i]
		if err := registered.module.(RuntimeResource).CloseRuntime(ctx); err != nil {
			result = errors.Join(result, fmt.Errorf("runtime module %q close runtime: %w", registered.id, err))
		}
	}
	return result
}

func closeStartedSession(ctx context.Context, started []registeredModule) error {
	var result error
	for i := len(started) - 1; i >= 0; i-- {
		registered := started[i]
		if err := registered.module.(SessionResource).CloseSession(ctx); err != nil {
			result = errors.Join(result, fmt.Errorf("runtime module %q rollback session: %w", registered.id, err))
		}
	}
	return result
}

func quiesceAndCloseSession(ctx context.Context, started []registeredModule) error {
	var result error
	for i := len(started) - 1; i >= 0; i-- {
		registered := started[i]
		if quiescer, ok := registered.module.(SessionQuiescer); ok {
			if err := quiescer.QuiesceSession(ctx); err != nil {
				result = errors.Join(result, fmt.Errorf("runtime module %q quiesce session: %w", registered.id, err))
			}
		}
	}
	for i := len(started) - 1; i >= 0; i-- {
		registered := started[i]
		if err := registered.module.(SessionResource).CloseSession(ctx); err != nil {
			result = errors.Join(result, fmt.Errorf("runtime module %q close session: %w", registered.id, err))
		}
	}
	return result
}

func nonNilContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
