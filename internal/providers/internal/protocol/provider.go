package protocol

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/juex-ai/juex/internal/foundation/llm"
)

func RequestThinkingEffort(profile llm.ProviderProfile, opts llm.CompleteOptions) string {
	if opts.ThinkingEffort != "" {
		return opts.ThinkingEffort
	}
	return profile.ThinkingEffort
}

const ProviderMaxRetries = 10

func StreamIdleTimeout(opts llm.CompleteOptions) time.Duration {
	if opts.StreamIdleTimeout != 0 {
		return opts.StreamIdleTimeout
	}
	return llm.DefaultStreamIdleTimeout
}

type streamIdleTimeoutError struct {
	operation string
	timeout   time.Duration
	cause     error
}

func (e *streamIdleTimeoutError) Error() string {
	if e.cause == nil {
		return fmt.Sprintf("%s idle timeout after %s", e.operation, e.timeout)
	}
	return fmt.Sprintf("%s idle timeout after %s: %v", e.operation, e.timeout, e.cause)
}

func (e *streamIdleTimeoutError) Unwrap() error {
	return context.DeadlineExceeded
}

func NewStreamIdleTimeoutError(operation string, timeout time.Duration, cause error) error {
	return &streamIdleTimeoutError{operation: operation, timeout: timeout, cause: cause}
}

func NewStreamIdleContext(ctx context.Context, timeout time.Duration) (context.Context, func(), func(), func() bool) {
	if timeout <= 0 {
		return ctx, func() {}, func() {}, func() bool { return false }
	}
	streamCtx, cancel := context.WithCancel(ctx)
	var (
		mu       sync.Mutex
		once     sync.Once
		expired  bool
		stopped  bool
		deadline = time.Now().Add(timeout)
	)
	timer := time.NewTimer(timeout)
	stopCh := make(chan struct{})
	go func() {
		defer timer.Stop()
		for {
			select {
			case <-timer.C:
				mu.Lock()
				if stopped || expired {
					mu.Unlock()
					return
				}
				if remaining := time.Until(deadline); remaining > 0 {
					timer.Reset(remaining)
					mu.Unlock()
					continue
				}
				expired = true
				mu.Unlock()
				cancel()
				return
			case <-ctx.Done():
				return
			case <-stopCh:
				return
			}
		}
	}()
	reset := func() {
		mu.Lock()
		defer mu.Unlock()
		if stopped || expired {
			return
		}
		deadline = time.Now().Add(timeout)
	}
	stop := func() {
		once.Do(func() {
			mu.Lock()
			stopped = true
			mu.Unlock()
			close(stopCh)
			cancel()
		})
	}
	isExpired := func() bool {
		mu.Lock()
		defer mu.Unlock()
		return expired
	}
	return streamCtx, reset, stop, isExpired
}
