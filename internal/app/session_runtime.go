package app

import (
	"context"
	"errors"
	"time"

	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/session"
)

var ErrSessionUnavailable = errors.New("app: session is unavailable")
var ErrSessionChanged = errors.New("app: session changed")

type SessionIdentitySnapshot struct {
	ID            string
	Dir           string
	Alias         string
	Kind          string
	Active        bool
	ScratchpadDir string
}

// ReadSession keeps the App session lifecycle read boundary held for the
// callback. The session pointer must not be retained after the callback
// returns; this boundary keeps the old session and lock alive during a switch.
func (a *App) ReadSession(read func(*session.Session) error) error {
	return a.readSession("", read)
}

// ReadSessionID is the route-safe form of ReadSession. It verifies the
// requested id while holding the lifecycle lock, so a stale Web registry key
// cannot expose the newly switched session through an old URL.
func (a *App) ReadSessionID(id string, read func(*session.Session) error) error {
	return a.readSession(id, read)
}

func (a *App) readSession(expectedID string, read func(*session.Session) error) error {
	if a == nil || read == nil {
		return ErrSessionUnavailable
	}
	a.sessionMu.RLock()
	defer a.sessionMu.RUnlock()
	if a.Session == nil {
		return ErrSessionUnavailable
	}
	if expectedID != "" && a.Session.ID != expectedID {
		return ErrSessionChanged
	}
	return read(a.Session)
}

func (a *App) SessionIdentity() (SessionIdentitySnapshot, bool) {
	var snapshot SessionIdentitySnapshot
	err := a.ReadSession(func(sess *session.Session) error {
		snapshot = SessionIdentitySnapshot{
			ID:            sess.ID,
			Dir:           sess.Dir,
			Alias:         sess.Alias,
			Kind:          sess.Kind,
			Active:        sess.Active,
			ScratchpadDir: sess.ScratchpadDir(),
		}
		return nil
	})
	return snapshot, err == nil
}

func (a *App) SessionInfo(now time.Time) (session.Info, bool) {
	var info session.Info
	err := a.ReadSession(func(sess *session.Session) error {
		info = sess.Info(now)
		return nil
	})
	return info, err == nil
}

func (a *App) SessionSnapshot(now time.Time) (session.Info, []llm.Message, bool) {
	var (
		info    session.Info
		history []llm.Message
	)
	err := a.ReadSession(func(sess *session.Session) error {
		info, history = sess.Snapshot(now)
		return nil
	})
	return info, history, err == nil
}

func (a *App) SessionTranscriptMessagePage(before string, limit int) (session.MessagePage, error) {
	var page session.MessagePage
	err := a.ReadSession(func(sess *session.Session) error {
		var err error
		page, err = sess.TranscriptMessagePage(before, limit)
		return err
	})
	return page, err
}

func (a *App) SessionRuntimeStats() session.RuntimeStats {
	var stats session.RuntimeStats
	_ = a.ReadSession(func(sess *session.Session) error {
		stats = sess.RuntimeStats()
		return nil
	})
	return stats
}

func (a *App) SessionTokenUsage() llm.Usage {
	var usage llm.Usage
	_ = a.ReadSession(func(sess *session.Session) error {
		usage = sess.TokenUsageSnapshot()
		return nil
	})
	return usage
}

func (a *App) ActiveContext() runtime.ActiveContextSnapshot {
	if a == nil || a.Engine == nil {
		return runtime.ActiveContextSnapshot{}
	}
	a.sessionMu.RLock()
	defer a.sessionMu.RUnlock()
	return a.Engine.ActiveContext()
}

func (a *App) ActiveContextForSession(id string) (runtime.ActiveContextSnapshot, bool) {
	if a == nil || a.Engine == nil {
		return runtime.ActiveContextSnapshot{}, false
	}
	var snapshot runtime.ActiveContextSnapshot
	err := a.ReadSessionID(id, func(*session.Session) error {
		snapshot = a.Engine.ActiveContext()
		return nil
	})
	return snapshot, err == nil
}

func (a *App) SessionStateStatus() (*runtime.GoalStatusSnapshot, *runtime.NotesSnapshot) {
	if a == nil || a.Engine == nil {
		return nil, nil
	}
	a.sessionMu.RLock()
	defer a.sessionMu.RUnlock()
	return a.Engine.SessionStateStatus()
}

func (a *App) PendingInputStatus() runtime.PendingInputStatus {
	if a == nil || a.Engine == nil {
		return runtime.PendingInputStatus{}
	}
	a.sessionMu.RLock()
	defer a.sessionMu.RUnlock()
	return a.Engine.PendingInputStatus()
}

// RunAdmittedTurn keeps the App lifecycle stable for a transport-reserved
// turn while preserving the caller's turn id.
func (a *App) RunAdmittedTurn(ctx context.Context, turnID string, message llm.Message) (string, error) {
	if a == nil || a.Engine == nil {
		return "", errors.New("app: admitted turn requires an initialized engine")
	}
	a.sessionMu.RLock()
	defer a.sessionMu.RUnlock()
	return a.Engine.TurnMessageWithID(ctx, message, turnID)
}
