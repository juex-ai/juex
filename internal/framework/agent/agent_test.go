package agent

import (
	"errors"
	"testing"
	"time"
)

func TestAppClosePausesAndResumesDeferredCleanup(t *testing.T) {
	closeCalls := 0
	laterCleanupCalls := 0
	a := &Agent{cleanup: []func() error{
		func() error {
			closeCalls++
			if closeCalls == 1 {
				return &testDeferredCleanupError{}
			}
			return nil
		},
		func() error {
			laterCleanupCalls++
			return nil
		},
	}}
	var deferred *testDeferredCleanupError
	if err := a.Close(); !errors.As(err, &deferred) {
		t.Fatalf("first Close error = %v, want CloseDeferredError", err)
	}
	if laterCleanupCalls != 0 {
		t.Fatalf("later cleanup calls after deferred Close = %d, want 0", laterCleanupCalls)
	}
	if err := a.CloseAndWait(); err != nil {
		t.Fatalf("CloseAndWait = %v", err)
	}
	if closeCalls != 2 || laterCleanupCalls != 1 {
		t.Fatalf("cleanup calls = first:%d later:%d", closeCalls, laterCleanupCalls)
	}
}

func TestAppConcurrentCloseReturnsWaitableResult(t *testing.T) {
	cleanupStarted := make(chan struct{})
	releaseCleanup := make(chan struct{})
	a := &Agent{cleanup: []func() error{func() error {
		close(cleanupStarted)
		<-releaseCleanup
		return nil
	}}}
	activeResult := make(chan error, 1)
	go func() { activeResult <- a.CloseAndWait() }()
	<-cleanupStarted
	concurrentResult := make(chan error, 1)
	go func() { concurrentResult <- a.Close() }()
	select {
	case err := <-concurrentResult:
		var deferred interface{ Wait() error }
		if !errors.As(err, &deferred) {
			t.Fatalf("concurrent Close error = %v, want waitable result", err)
		}
	case <-time.After(time.Second):
		t.Fatal("concurrent Close blocked behind active cleanup")
	}
	close(releaseCleanup)
	if err := <-activeResult; err != nil {
		t.Fatalf("CloseAndWait = %v", err)
	}
}

type testDeferredCleanupError struct{}

func (*testDeferredCleanupError) Error() string { return "test cleanup deferred" }
func (*testDeferredCleanupError) Wait() error   { return nil }
