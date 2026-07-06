package cancellation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync"
)

var ErrUserCancelled = errors.New("cancelled by user")

type SignalKind string

const (
	SignalKindInterrupted SignalKind = "interrupted"
	SignalKindTerminated  SignalKind = "terminated"
)

type SignalError struct {
	Signal       string
	SignalNumber int
	Kind         SignalKind
}

func NewSignalError(sig os.Signal) *SignalError {
	if sig == nil {
		return nil
	}
	name, number := describeSignal(sig)
	kind := SignalKindTerminated
	if isInterruptSignal(sig, name, number) {
		kind = SignalKindInterrupted
	}
	return &SignalError{Signal: name, SignalNumber: number, Kind: kind}
}

func (e *SignalError) Error() string {
	if e == nil {
		return "<nil>"
	}
	action := "terminated"
	if e.Kind == SignalKindInterrupted {
		action = "interrupted"
	}
	return fmt.Sprintf("run %s by signal %s (%d)", action, e.Signal, e.SignalNumber)
}

func (e *SignalError) Unwrap() error {
	return context.Canceled
}

func AsSignalError(err error) (*SignalError, bool) {
	var signalErr *SignalError
	if errors.As(err, &signalErr) && signalErr != nil {
		return signalErr, true
	}
	return nil, false
}

func NormalizeError(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := AsSignalError(err); ok {
		return err
	}
	if IsUserCancelled(err) {
		return ErrUserCancelled
	}
	return err
}

func NormalizeErrorWithContext(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if contextErr := ContextError(ctx); contextErr != nil && errors.Is(err, context.Canceled) {
		return NormalizeError(contextErr)
	}
	return NormalizeError(err)
}

func IsUserCancelled(err error) bool {
	if _, ok := AsSignalError(err); ok {
		return false
	}
	return errors.Is(err, ErrUserCancelled) || errors.Is(err, context.Canceled)
}

func ContextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	err := ctx.Err()
	if err == nil {
		return nil
	}
	if signalErr, ok := AsSignalError(context.Cause(ctx)); ok {
		return signalErr
	}
	return err
}

func NotifyContext(parent context.Context, signals ...os.Signal) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	if len(signals) == 0 {
		signals = DefaultSignals()
	}
	ctx, cancel := context.WithCancelCause(parent)
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, signals...)
	done := make(chan struct{})
	var once sync.Once
	stop := func() {
		once.Do(func() {
			signal.Stop(ch)
			close(done)
			cancel(context.Canceled)
		})
	}
	go func() {
		select {
		case sig := <-ch:
			signal.Stop(ch)
			cancel(NewSignalError(sig))
		case <-parent.Done():
			signal.Stop(ch)
			cancel(context.Cause(parent))
		case <-done:
		}
	}()
	return ctx, stop
}
