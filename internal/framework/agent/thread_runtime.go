package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/juex-ai/juex/internal/foundation/cancellation"
	"github.com/juex-ai/juex/internal/foundation/errorclass"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/framework/runtime"
	"github.com/juex-ai/juex/internal/framework/thread"
)

var ErrThreadUnavailable = errors.New("app: Thread is unavailable")

var ErrThreadChanged = errors.New("app: Thread changed")

var ErrThreadStopped = errorclass.WithKind(errorclass.KindTerminated, errors.New("app: Thread stopped"))

type ThreadIdentitySnapshot struct {
	ID             string
	Dir            string
	Alias          string
	ParentThreadID string
}

func (a *Agent) ReadThread(read func(*thread.Thread) error) error {
	return a.readThread("", read)
}

func (a *Agent) ReadThreadID(id string, read func(*thread.Thread) error) error {
	return a.readThread(id, read)
}

func (a *Agent) readThread(expectedID string, read func(*thread.Thread) error) error {
	if a == nil || read == nil {
		return ErrThreadUnavailable
	}
	return a.ReadThreadState(func(target *thread.Thread) error {
		if target == nil {
			return ErrThreadUnavailable
		}
		if expectedID != "" && target.ID != expectedID {
			return ErrThreadChanged
		}
		return read(target)
	})
}

func (a *Agent) ThreadIdentity() (ThreadIdentitySnapshot, bool) {
	var snapshot ThreadIdentitySnapshot
	err := a.ReadThread(func(target *thread.Thread) error {
		info := target.Info()
		snapshot = ThreadIdentitySnapshot{
			ID: info.ID, Dir: info.Dir, Alias: info.Alias,
			ParentThreadID: info.ParentThreadID,
		}
		return nil
	})
	return snapshot, err == nil
}

func (a *Agent) ThreadInfo() (thread.Info, bool) {
	var info thread.Info
	err := a.ReadThread(func(target *thread.Thread) error {
		info = target.Info()
		return nil
	})
	return info, err == nil
}

func (a *Agent) ThreadSnapshot() (thread.Info, []llm.Message, bool) {
	var info thread.Info
	var history []llm.Message
	err := a.ReadThread(func(target *thread.Thread) error {
		info = target.Info()
		history = target.ReplaySnapshot().Messages
		return nil
	})
	return info, history, err == nil
}

func (a *Agent) ThreadTimeline(before string, limit int) (thread.TimelinePage, error) {
	var page thread.TimelinePage
	err := a.ReadThread(func(target *thread.Thread) error {
		var err error
		page, err = target.Timeline(before, limit)
		return err
	})
	return page, err
}

func (a *Agent) ThreadTokenUsage() llm.Usage {
	var usage llm.Usage
	_ = a.ReadThread(func(target *thread.Thread) error {
		usage = target.TokenUsageSnapshot()
		return nil
	})
	return usage
}

func (a *Agent) ActiveContext() runtime.ActiveContextSnapshot {
	if a == nil || a.Engine == nil {
		return runtime.ActiveContextSnapshot{}
	}
	a.threadMu.RLock()
	defer a.threadMu.RUnlock()
	return a.Engine.ActiveContext()
}

func (a *Agent) ActiveContextForThread(id string) (runtime.ActiveContextSnapshot, bool) {
	if a == nil || a.Engine == nil {
		return runtime.ActiveContextSnapshot{}, false
	}
	var snapshot runtime.ActiveContextSnapshot
	err := a.ReadThreadID(id, func(*thread.Thread) error {
		snapshot = a.Engine.ActiveContext()
		return nil
	})
	return snapshot, err == nil
}

func (a *Agent) PendingInputStatus() runtime.PendingInputStatus {
	if a == nil || a.Engine == nil {
		return runtime.PendingInputStatus{}
	}
	a.threadMu.RLock()
	defer a.threadMu.RUnlock()
	return a.Engine.PendingInputStatus()
}

func (a *Agent) CancelActiveTurn(cause error) bool {
	if a == nil || a.Engine == nil {
		return false
	}
	a.threadMu.RLock()
	defer a.threadMu.RUnlock()
	return a.Engine.CancelActiveTurn(cause)
}

func (a *Agent) RunAdmittedTurn(ctx context.Context, turnID string, message llm.Message) (string, error) {
	if a == nil || a.Engine == nil {
		return "", errors.New("app: admitted turn requires an initialized engine")
	}
	if err := a.executionError(); err != nil {
		return "", err
	}
	a.threadMu.RLock()
	defer a.threadMu.RUnlock()
	if a.Thread == nil {
		return "", ErrThreadUnavailable
	}
	if err := a.waitPendingInputRecoveryContext(ctx); err != nil {
		cause := cancellation.ContextError(ctx)
		if cause == nil {
			cause = err
		}
		unwindCtx, cancel := context.WithCancelCause(context.Background())
		cancel(cause)
		_, unwindErr := a.Engine.TurnMessageWithID(unwindCtx, message, turnID)
		if unwindErr != nil && !errors.Is(unwindErr, cause) && !cancellation.IsUserCancelled(unwindErr) {
			return "", errors.Join(err, fmt.Errorf("release canceled admitted turn %q: %w", turnID, unwindErr))
		}
		return "", cause
	}
	return a.Engine.TurnMessageWithID(ctx, message, turnID)
}

// ReadThreadState holds the read lease even after the Thread has been released.
// A nil Thread represents that released state to application projections.
func (a *Agent) ReadThreadState(read func(*thread.Thread) error) error {
	if a == nil || read == nil {
		return ErrThreadUnavailable
	}
	a.threadMu.RLock()
	defer a.threadMu.RUnlock()
	return read(a.Thread)
}
