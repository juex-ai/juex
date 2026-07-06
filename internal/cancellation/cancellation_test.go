package cancellation

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
)

func TestSignalErrorPreservesSignalIdentity(t *testing.T) {
	err := NewSignalError(syscall.SIGTERM)
	if err == nil {
		t.Fatal("NewSignalError returned nil")
	}
	if err.Kind != SignalKindTerminated {
		t.Fatalf("Kind = %q, want %q", err.Kind, SignalKindTerminated)
	}
	if err.Signal != "SIGTERM" {
		t.Fatalf("Signal = %q, want SIGTERM", err.Signal)
	}
	if err.SignalNumber != 15 {
		t.Fatalf("SignalNumber = %d, want 15", err.SignalNumber)
	}
	if err.Error() != "run terminated by signal SIGTERM (15)" {
		t.Fatalf("Error() = %q", err.Error())
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatal("signal error should unwrap to context.Canceled")
	}
}

func TestSignalErrorInterruptMessage(t *testing.T) {
	err := NewSignalError(os.Interrupt)
	if err.Kind != SignalKindInterrupted {
		t.Fatalf("Kind = %q, want %q", err.Kind, SignalKindInterrupted)
	}
	if err.Signal != "SIGINT" || err.SignalNumber != 2 {
		t.Fatalf("signal identity = %s/%d, want SIGINT/2", err.Signal, err.SignalNumber)
	}
	if err.Error() != "run interrupted by signal SIGINT (2)" {
		t.Fatalf("Error() = %q", err.Error())
	}
}

func TestNormalizeErrorPreservesSignalCancellation(t *testing.T) {
	err := NewSignalError(syscall.SIGTERM)
	normalized := NormalizeError(err)
	if normalized != err {
		t.Fatalf("NormalizeError(signal) = %#v, want original signal error", normalized)
	}
	if NormalizeError(context.Canceled) != ErrUserCancelled {
		t.Fatal("plain context.Canceled should still normalize to ErrUserCancelled")
	}
}

func TestContextErrorReturnsSignalCause(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	signalErr := NewSignalError(syscall.SIGTERM)
	cancel(signalErr)

	got := ContextError(ctx)
	if got != signalErr {
		t.Fatalf("ContextError = %#v, want signal cause", got)
	}
}

func TestNotifyContextPreservesParentCancelCause(t *testing.T) {
	parent, cancelParent := context.WithCancelCause(context.Background())
	ctx, stop := NotifyContext(parent, os.Interrupt)
	defer stop()

	signalErr := NewSignalError(syscall.SIGTERM)
	cancelParent(signalErr)
	<-ctx.Done()

	got := ContextError(ctx)
	if got != signalErr {
		t.Fatalf("ContextError = %#v, want parent signal cause", got)
	}
}
