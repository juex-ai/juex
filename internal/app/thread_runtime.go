package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/juex-ai/juex/internal/cancellation"
	"github.com/juex-ai/juex/internal/errorclass"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/runtime/workmem"
	"github.com/juex-ai/juex/internal/thread"
)

var ErrThreadUnavailable = errors.New("app: Thread is unavailable")
var ErrThreadChanged = errors.New("app: Thread changed")
var ErrThreadStopped = errorclass.WithKind(errorclass.KindTerminated, errors.New("app: Thread stopped"))

type ThreadIdentitySnapshot struct {
	ID             string
	Dir            string
	Alias          string
	ParentThreadID string
	ScratchpadDir  string
}

func (a *App) ReadThread(read func(*thread.Thread) error) error {
	return a.readThread("", read)
}

func (a *App) ReadThreadID(id string, read func(*thread.Thread) error) error {
	return a.readThread(id, read)
}

func (a *App) readThread(expectedID string, read func(*thread.Thread) error) error {
	if a == nil || read == nil {
		return ErrThreadUnavailable
	}
	a.threadMu.RLock()
	defer a.threadMu.RUnlock()
	if a.Thread == nil {
		return ErrThreadUnavailable
	}
	if expectedID != "" && a.Thread.ID != expectedID {
		return ErrThreadChanged
	}
	return read(a.Thread)
}

func (a *App) ThreadIdentity() (ThreadIdentitySnapshot, bool) {
	var snapshot ThreadIdentitySnapshot
	err := a.ReadThread(func(target *thread.Thread) error {
		info := target.Info()
		snapshot = ThreadIdentitySnapshot{
			ID: info.ID, Dir: info.Dir, Alias: info.Alias,
			ParentThreadID: info.ParentThreadID,
			ScratchpadDir:  target.ScratchpadDir(),
		}
		return nil
	})
	return snapshot, err == nil
}

func (a *App) ThreadInfo() (thread.Info, bool) {
	var info thread.Info
	err := a.ReadThread(func(target *thread.Thread) error {
		info = target.Info()
		return nil
	})
	return info, err == nil
}

func (a *App) ThreadSnapshot() (thread.Info, []llm.Message, bool) {
	var info thread.Info
	var history []llm.Message
	err := a.ReadThread(func(target *thread.Thread) error {
		info = target.Info()
		history = target.ReplaySnapshot().Messages
		return nil
	})
	return info, history, err == nil
}

func (a *App) ThreadTimeline(before string, limit int) (thread.TimelinePage, error) {
	var page thread.TimelinePage
	err := a.ReadThread(func(target *thread.Thread) error {
		var err error
		page, err = target.Timeline(before, limit)
		return err
	})
	return page, err
}

func (a *App) ThreadTokenUsage() llm.Usage {
	var usage llm.Usage
	_ = a.ReadThread(func(target *thread.Thread) error {
		usage = target.TokenUsageSnapshot()
		return nil
	})
	return usage
}

func (a *App) ActiveContext() runtime.ActiveContextSnapshot {
	if a == nil || a.Engine == nil {
		return runtime.ActiveContextSnapshot{}
	}
	a.threadMu.RLock()
	defer a.threadMu.RUnlock()
	return a.Engine.ActiveContext()
}

func (a *App) ActiveContextForThread(id string) (runtime.ActiveContextSnapshot, bool) {
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

func (a *App) ThreadStateStatus() (*workmem.GoalStatusSnapshot, *workmem.NotesSnapshot) {
	if a == nil || a.Engine == nil {
		return nil, nil
	}
	a.threadMu.RLock()
	defer a.threadMu.RUnlock()
	return a.Engine.ThreadStateStatus()
}

func (a *App) PendingInputStatus() runtime.PendingInputStatus {
	if a == nil || a.Engine == nil {
		return runtime.PendingInputStatus{}
	}
	a.threadMu.RLock()
	defer a.threadMu.RUnlock()
	return a.Engine.PendingInputStatus()
}

func (a *App) CancelActiveTurn(cause error) bool {
	if a == nil || a.Engine == nil {
		return false
	}
	a.threadMu.RLock()
	defer a.threadMu.RUnlock()
	return a.Engine.CancelActiveTurn(cause)
}

func (a *App) RunAdmittedTurn(ctx context.Context, turnID string, message llm.Message) (string, error) {
	if a == nil || a.Engine == nil {
		return "", errors.New("app: admitted turn requires an initialized engine")
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
