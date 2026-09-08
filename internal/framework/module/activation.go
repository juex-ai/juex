package module

import (
	"context"
	"fmt"
)

// RuntimeActivator admits external input after the host publishes its Main
// recovery barrier. StartRuntime may prepare resources but must not deliver
// input. QuiesceRuntime stops input before CloseRuntime releases resources.
type RuntimeActivator interface {
	RuntimeResource
	RuntimeQuiescer
	ActivateRuntime(context.Context) error
}

type runtimeActivation struct {
	done   chan struct{}
	cancel context.CancelFunc
	err    error
}

func (*runtimeActivation) Error() string { return "runtime modules: activation cleanup deferred" }

func (a *runtimeActivation) Wait() error {
	<-a.done
	return a.err
}

// ActivateRuntime calls input owners once, in registration order. Callbacks
// run without Set locks: they may inspect the published set or request Close.
// A failed pass is not retried; the host must close the prepared runtime.
func (s *Set) ActivateRuntime(ctx context.Context) (result error) {
	if s == nil {
		return nil
	}
	s.lifecycleMu.Lock()
	s.mu.Lock()
	if s.scope != ScopeRuntime || s.state.started == nil || s.state.closed || s.state.quiesced || s.state.stopping {
		s.mu.Unlock()
		s.lifecycleMu.Unlock()
		return fmt.Errorf("runtime modules: runtime set is not ready for activation")
	}
	if activation := s.state.activation; activation != nil {
		s.mu.Unlock()
		s.lifecycleMu.Unlock()
		select {
		case <-activation.done:
			return activation.err
		default:
			return activation
		}
	}
	ctx, cancel := context.WithCancel(nonNilContext(ctx))
	activation := &runtimeActivation{done: make(chan struct{}), cancel: cancel}
	s.state.activation = activation
	started := append([]registeredModule(nil), s.state.started...)
	s.mu.Unlock()
	s.lifecycleMu.Unlock()
	defer func() {
		cancel()
		activation.err = result
		close(activation.done)
	}()
	for _, registered := range started {
		if err := ctx.Err(); err != nil {
			return err
		}
		if activator, ok := registered.module.(RuntimeActivator); ok {
			if err := activator.ActivateRuntime(ctx); err != nil {
				return fmt.Errorf("runtime module %q activate runtime: %w", registered.id, err)
			}
		}
	}
	return ctx.Err()
}
