package app

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/juex-ai/juex/internal/artifact"
	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/session"
)

// SessionDeleteOptions controls application-level Session deletion policy.
type SessionDeleteOptions struct {
	AllowMissingSession bool
}

// PartialSessionDeleteError reports that the Session was removed but its
// Agent-owned Artifact namespace could not be cleaned.
type PartialSessionDeleteError struct {
	SessionID string
	Err       error
}

func (e *PartialSessionDeleteError) Error() string {
	return fmt.Sprintf("session %q was deleted but Artifact cleanup failed: %v", e.SessionID, e.Err)
}

func (e *PartialSessionDeleteError) Unwrap() error { return e.Err }

// SessionDeletePlan composes Session persistence deletion with Agent Artifact
// cleanup without coupling the session package to Artifact storage.
type SessionDeletePlan struct {
	id                string
	sessionPlan       *session.DeletePlan
	missingSessionDir string
	artifactStore     artifact.Store
	artifactEnabled   bool
	artifactExists    bool
}

// PrepareSessionDelete validates both persistence targets before callers stop
// a live runtime or mutate either target.
func PrepareSessionDelete(cfg config.Config, id string, opts SessionDeleteOptions) (*SessionDeletePlan, error) {
	if !safeSessionDeleteID(id) {
		return nil, fmt.Errorf("app: unsafe Session id %q", id)
	}
	plan := &SessionDeletePlan{id: id}
	sessionPlan, sessionErr := session.PrepareDelete(cfg.SessionsDir(), cfg.HistoryPath(), id)
	if sessionErr == nil {
		plan.sessionPlan = sessionPlan
	} else if !errors.Is(sessionErr, os.ErrNotExist) {
		return nil, sessionErr
	} else if info, statErr := os.Lstat(filepath.Join(cfg.SessionsDir(), id)); statErr == nil {
		// A directory is present but failed Session validation. Treat that as a
		// preflight error unless the caller owns a live lazy Session or a
		// just-created rollback target.
		if !opts.AllowMissingSession || !info.IsDir() {
			return nil, sessionErr
		}
		plan.missingSessionDir = filepath.Join(cfg.SessionsDir(), id)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, statErr
	}
	if artifactDir := cfg.ArtifactDir(); artifactDir != "" {
		store, err := artifact.NewStore(artifactDir)
		if err != nil {
			return nil, err
		}
		exists, err := store.HasNamespace(path.Join("sessions", id))
		if err != nil {
			return nil, err
		}
		plan.artifactStore = store
		plan.artifactEnabled = true
		plan.artifactExists = exists
	}
	if plan.sessionPlan == nil && !plan.artifactExists && !opts.AllowMissingSession {
		return nil, os.ErrNotExist
	}
	return plan, nil
}

func safeSessionDeleteID(id string) bool {
	id = strings.TrimSpace(id)
	return id != "" && id != "." && id != ".." && !strings.ContainsAny(id, `/\:`)
}

// Commit removes the Session first and its Artifact namespace second.
func (p *SessionDeletePlan) Commit() error {
	if p == nil {
		return os.ErrInvalid
	}
	sessionRemoved := false
	if p.sessionPlan != nil {
		if err := p.sessionPlan.Commit(); err != nil {
			return err
		}
		sessionRemoved = true
	} else if p.missingSessionDir != "" {
		if err := session.WithSessionRootGuard(filepath.Dir(p.missingSessionDir), func() error {
			lock, err := session.AcquireSessionDeleteLock(p.missingSessionDir, "delete")
			if err != nil {
				return err
			}
			if lock == nil {
				return nil
			}
			defer func() { _ = lock.Close() }()
			return os.RemoveAll(p.missingSessionDir)
		}); err != nil {
			return err
		}
		sessionRemoved = true
	}
	if p.artifactEnabled {
		if err := p.artifactStore.RemoveNamespace(path.Join("sessions", p.id)); err != nil {
			if sessionRemoved {
				return &PartialSessionDeleteError{SessionID: p.id, Err: err}
			}
			return err
		}
	}
	return nil
}

// commitIfInactive removes the Session and its Artifact namespace only when
// the Session is not the selected active Session at the commit point.
func (p *SessionDeletePlan) commitIfInactive() (bool, error) {
	if p == nil {
		return false, os.ErrInvalid
	}
	if p.sessionPlan == nil {
		// A replacement candidate is expected to have a complete on-disk
		// Session. Preserve any incomplete target rather than deleting it
		// without an atomic active-history check.
		return false, nil
	}
	deleted, err := p.sessionPlan.CommitIfInactive()
	if err != nil || !deleted {
		return deleted, err
	}
	if p.artifactEnabled {
		if err := p.artifactStore.RemoveNamespace(path.Join("sessions", p.id)); err != nil {
			return true, &PartialSessionDeleteError{SessionID: p.id, Err: err}
		}
	}
	return true, nil
}

// DeleteSession prepares and commits application-level Session deletion.
func DeleteSession(cfg config.Config, id string, opts SessionDeleteOptions) error {
	plan, err := PrepareSessionDelete(cfg, id, opts)
	if err != nil {
		return err
	}
	return plan.Commit()
}

// deleteSessionIfInactive conditionally removes an application Session and its
// Artifact namespace without racing with external Session activation.
func deleteSessionIfInactive(cfg config.Config, id string, opts SessionDeleteOptions) (bool, error) {
	plan, err := PrepareSessionDelete(cfg, id, opts)
	if err != nil {
		return false, err
	}
	return plan.commitIfInactive()
}
