package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/modules/promptcontext"
	"github.com/juex-ai/juex/internal/runtime"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/session"
	"github.com/juex-ai/juex/internal/tools"
)

type sessionReplacementPhase string

const (
	sessionReplacementPhasePrepare       sessionReplacementPhase = "prepare"
	sessionReplacementPhaseBuild         sessionReplacementPhase = "build"
	sessionReplacementPhaseValidate      sessionReplacementPhase = "validate"
	sessionReplacementPhasePublish       sessionReplacementPhase = "publish"
	sessionReplacementPhasePolicy        sessionReplacementPhase = "policy"
	sessionReplacementPhaseHistoryCommit sessionReplacementPhase = "history_commit"
	sessionReplacementPhaseRollback      sessionReplacementPhase = "rollback"
	sessionReplacementPhaseCleanup       sessionReplacementPhase = "cleanup"
)

// sessionReplacementError preserves the failed transaction phase while
// retaining the original error for errors.Is/errors.As callers.
type sessionReplacementError struct {
	Phase sessionReplacementPhase
	Err   error
}

func (e *sessionReplacementError) Error() string {
	if e == nil {
		return "app: active session replacement failed"
	}
	return fmt.Sprintf("app: active session replacement %s: %v", e.Phase, e.Err)
}

func (e *sessionReplacementError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// sessionReplacementResult describes the old and candidate identities plus
// diagnostic-only work that failed after the commit boundary.
type sessionReplacementResult struct {
	Old              session.Info
	New              session.Info
	ObservabilityErr error
	StatusErr        error
	CleanupErr       error
}

type activeSessionReplacementOptions struct {
	prepareCandidate func(config.Config) (SessionAttachment, *session.Lock, error)
	commitActive     func(string, string, session.Info, func()) (bool, error)
	deleteCandidate  func(config.Config, string, SessionDeleteOptions) (bool, error)
}

func (opts activeSessionReplacementOptions) withDefaults() activeSessionReplacementOptions {
	if opts.prepareCandidate == nil {
		opts.prepareCandidate = func(cfg config.Config) (SessionAttachment, *session.Lock, error) {
			return prepareAndLockNewPrimarySession(cfg, SessionAttachmentRequest{}, session.AcquireSessionLock)
		}
	}
	if opts.commitActive == nil {
		opts.commitActive = session.CompareAndSetActive
	}
	if opts.deleteCandidate == nil {
		opts.deleteCandidate = deleteSessionIfInactive
	}
	return opts
}

// activeSessionReplacementTransaction is the sole owner of Active Primary
// replacement ordering: candidate preparation, Runtime publication, policy
// admission, durable history commit, App publication, rollback, and cleanup.
type activeSessionReplacementTransaction struct {
	app    *App
	ctx    context.Context
	opts   activeSessionReplacementOptions
	result sessionReplacementResult

	candidate     SessionAttachment
	candidateLock *session.Lock
	nextModules   *runtimemodule.Set
	nextTools     *tools.Registry

	oldRuntimeCheckpoint runtime.SessionRuntimeCheckpoint
	oldRuntime           runtime.SessionRuntimeSnapshot
	oldLock              *session.Lock
	oldSession           *session.Session
	rollbackPrepared     bool
	rollbackRuntimeReady bool
	rollbackErr          error
}

func (a *App) executeActiveSessionReplacement(ctx context.Context, opts activeSessionReplacementOptions) (sessionReplacementResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	tx := &activeSessionReplacementTransaction{app: a, ctx: ctx, opts: opts.withDefaults()}
	return tx.execute()
}

func (tx *activeSessionReplacementTransaction) execute() (sessionReplacementResult, error) {
	a := tx.app
	if a == nil {
		return tx.result, replacementPhaseError(sessionReplacementPhasePrepare, ErrSessionUnavailable)
	}
	if err := tx.ctx.Err(); err != nil {
		return tx.result, replacementPhaseError(sessionReplacementPhasePrepare, err)
	}
	if err := a.waitPendingInputRecoveryContext(tx.ctx); err != nil {
		return tx.result, replacementPhaseError(sessionReplacementPhasePrepare, err)
	}

	a.sessionHandoffMu.Lock()
	defer a.sessionHandoffMu.Unlock()
	if err := tx.ctx.Err(); err != nil {
		return tx.result, replacementPhaseError(sessionReplacementPhasePrepare, err)
	}
	a.lifecycleMu.RLock()
	defer a.lifecycleMu.RUnlock()
	a.sessionReplaceMu.Lock()
	defer a.sessionReplaceMu.Unlock()

	var oldInfo session.Info
	if err := a.ReadSession(func(sess *session.Session) error {
		oldInfo = sess.Info()
		return nil
	}); err != nil {
		return tx.result, replacementPhaseError(sessionReplacementPhasePrepare, ErrSessionUnavailable)
	}
	if oldInfo.Kind == session.KindSide {
		return tx.result, replacementPhaseError(sessionReplacementPhasePrepare, errors.New("side sessions cannot switch workspace active session"))
	}
	tx.result.Old = oldInfo

	replace := tx.replace
	var err error
	if a.sideSessions != nil {
		err = a.sideSessions.replacePrimary(tx.ctx, replace)
	} else if ctxErr := tx.ctx.Err(); ctxErr != nil {
		err = ctxErr
	} else {
		err = replace()
	}
	if err != nil {
		var replacementErr *sessionReplacementError
		if !errors.As(err, &replacementErr) {
			err = replacementPhaseError(sessionReplacementPhasePrepare, err)
		}
	}
	return tx.result, err
}

func (tx *activeSessionReplacementTransaction) replace() error {
	var err error
	tx.candidate, tx.candidateLock, err = tx.opts.prepareCandidate(tx.app.cfg)
	if tx.candidate.Session != nil {
		tx.result.New = tx.candidate.Session.Info()
	}
	if err != nil {
		return tx.rejectBeforePublication(sessionReplacementPhasePrepare, err)
	}
	if tx.candidate.Session == nil {
		return tx.rejectBeforePublication(sessionReplacementPhasePrepare, errors.New("candidate session is required"))
	}

	tx.nextModules, err = buildSessionModules(
		tx.ctx,
		tx.app.cfg,
		tx.app.sessionModuleFactories,
		tx.app.runtimeModuleContext,
		tx.candidate.Session,
		tx.app.Engine,
		tx.app.cfg.WorkDir,
		promptcontext.ShellProfileFromConfig(tx.app.cfg.Shell),
		tx.app.shellSessions,
		sessionModuleOptions{
			hookRunner:               tx.app.hookRunner,
			hookBaseRequest:          tx.app.hookBaseRequest,
			goalContinuation:         true,
			goalContinuationDeferrer: tx.app.sideSessions,
		},
	)
	if err != nil {
		return tx.rejectBeforePublication(sessionReplacementPhaseBuild, err)
	}
	if err := validateSessionModuleContext(
		tx.ctx,
		tx.app.runtimeModules,
		tx.nextModules,
		tx.app.runtimeModuleContext,
		tx.candidate.Session,
	); err != nil {
		return tx.rejectBeforePublication(sessionReplacementPhaseValidate, err)
	}
	if tx.app.Engine != nil {
		tx.nextTools, err = runtimemodule.BuildToolRegistry(
			tools.RegistryOptions{DefaultTimeoutSeconds: durationSeconds(tx.app.cfg.RuntimeLimits().ToolTimeout)},
			tx.app.runtimeModules,
			tx.nextModules,
		)
		if err != nil {
			return tx.rejectBeforePublication(sessionReplacementPhaseBuild, err)
		}
	}
	if err := tx.ctx.Err(); err != nil {
		return tx.rejectBeforePublication(sessionReplacementPhasePolicy, err)
	}
	return tx.publishAndCommit()
}

func (tx *activeSessionReplacementTransaction) publishAndCommit() error {
	a := tx.app
	a.sessionMu.Lock()
	tx.oldLock = a.sessionLock
	tx.oldSession = a.sessionResource
	if tx.oldSession == nil {
		tx.oldSession = a.Session
	}
	if tx.oldSession != nil {
		tx.result.Old = tx.oldSession.Info()
	}
	if a.Engine != nil {
		tx.oldRuntimeCheckpoint = a.Engine.CaptureSessionRuntimeCheckpoint()
		tx.oldRuntime = tx.oldRuntimeCheckpoint.Snapshot()
		if err := a.Engine.ReplaceSessionRuntimeBundle(tx.candidate.Session, runtime.SessionRuntimeReplacement{
			Modules: tx.nextModules,
			Tools:   tx.nextTools,
		}); err != nil {
			a.sessionMu.Unlock()
			return tx.rejectBeforePublication(sessionReplacementPhasePublish, err)
		}
	}
	if a.eventSink != nil {
		a.eventSink.SetJournal(tx.candidate.Session)
	}
	tx.result.ObservabilityErr = a.detachObservability()
	tx.result.ObservabilityErr = errors.Join(tx.result.ObservabilityErr, a.attachObservability(tx.candidate.Session))

	if a.Engine != nil {
		if err := a.Engine.RunSessionStartPolicies(tx.ctx); err != nil {
			return tx.rollbackLocked(sessionReplacementPhasePolicy, err)
		}
	}
	if err := tx.ctx.Err(); err != nil {
		return tx.rollbackLocked(sessionReplacementPhasePolicy, err)
	}
	_, err := tx.opts.commitActive(
		a.cfg.HistoryPath(),
		tx.result.Old.ID,
		tx.candidate.Session.Info(),
		tx.prepareRuntimeRollbackLocked,
	)
	if err != nil {
		if tx.rollbackPrepared {
			return tx.finishRollbackLocked(sessionReplacementPhaseHistoryCommit, err)
		}
		return tx.rollbackLocked(sessionReplacementPhaseHistoryCommit, err)
	}

	a.Session = tx.candidate.Session
	a.sessionLock = tx.candidateLock
	a.sessionResource = tx.candidate.Session
	if a.chunkedWrites != nil {
		a.chunkedWrites.RestoreActiveFromHistory(tx.candidate.Session.History)
	}
	if a.Status != nil {
		tx.result.StatusErr = a.Status.ResetFromReplayWithRestartRecovery(
			runtimeStatusSeed(tx.candidate.Session, runtime.DefaultMaxPendingInput),
			func(visit func(events.Event)) error {
				return session.ReplayEventsWithCatalog(tx.candidate.Session.Dir, a.eventCatalog, visit)
			},
		)
	}
	a.sessionMu.Unlock()

	tx.candidate = SessionAttachment{}
	tx.candidateLock = nil
	tx.nextModules = nil
	tx.result.CleanupErr = tx.cleanupOldResources()
	return nil
}

func (tx *activeSessionReplacementTransaction) rollbackLocked(phase sessionReplacementPhase, cause error) error {
	tx.prepareRuntimeRollbackLocked()
	return tx.finishRollbackLocked(phase, cause)
}

func (tx *activeSessionReplacementTransaction) prepareRuntimeRollbackLocked() {
	if tx.rollbackPrepared {
		return
	}
	tx.rollbackPrepared = true
	a := tx.app
	if tx.result.ObservabilityErr != nil {
		tx.rollbackErr = errors.Join(tx.rollbackErr, fmt.Errorf("replace observability: %w", tx.result.ObservabilityErr))
	}
	tx.rollbackRuntimeReady = a.Engine == nil
	if err := a.detachObservability(); err != nil {
		tx.rollbackErr = errors.Join(tx.rollbackErr, fmt.Errorf("restore observability: detach replacement: %w", err))
	}
	if a.eventSink != nil {
		a.eventSink.SetJournal(tx.oldSession)
	}
	if a.Engine != nil {
		if err := a.Engine.RestoreSessionRuntimeCheckpoint(tx.oldRuntimeCheckpoint); err != nil {
			tx.rollbackErr = errors.Join(tx.rollbackErr, err)
		} else {
			tx.rollbackRuntimeReady = true
		}
	}
	if err := a.attachObservability(tx.oldSession); err != nil {
		tx.rollbackErr = errors.Join(tx.rollbackErr, fmt.Errorf("restore observability: attach previous session: %w", err))
	}
}

func (tx *activeSessionReplacementTransaction) finishRollbackLocked(phase sessionReplacementPhase, cause error) error {
	a := tx.app
	a.sessionMu.Unlock()

	primaryErr := replacementPhaseError(phase, cause)
	if !tx.rollbackRuntimeReady {
		return errors.Join(primaryErr, replacementPhaseError(sessionReplacementPhaseRollback, tx.rollbackErr))
	}
	cleanupErr := tx.cleanupCandidate()
	tx.result.CleanupErr = cleanupErr
	if tx.rollbackErr != nil {
		primaryErr = errors.Join(primaryErr, replacementPhaseError(sessionReplacementPhaseRollback, tx.rollbackErr))
	}
	if cleanupErr != nil {
		primaryErr = errors.Join(primaryErr, replacementPhaseError(sessionReplacementPhaseCleanup, cleanupErr))
	}
	return primaryErr
}

func (tx *activeSessionReplacementTransaction) rejectBeforePublication(phase sessionReplacementPhase, cause error) error {
	primaryErr := replacementPhaseError(phase, cause)
	cleanupErr := tx.cleanupCandidate()
	tx.result.CleanupErr = cleanupErr
	if cleanupErr == nil {
		return primaryErr
	}
	return errors.Join(primaryErr, replacementPhaseError(sessionReplacementPhaseCleanup, cleanupErr))
}

func (tx *activeSessionReplacementTransaction) cleanupCandidate() error {
	var cleanupErr error
	if tx.nextModules != nil {
		cleanupErr = errors.Join(cleanupErr, tx.nextModules.CloseSession(context.Background()))
		tx.nextModules = nil
	}
	if tx.candidateLock != nil {
		cleanupErr = errors.Join(cleanupErr, tx.candidateLock.Close())
		tx.candidateLock = nil
	}
	sess := tx.candidate.Session
	tx.candidate = SessionAttachment{}
	if sess == nil {
		return cleanupErr
	}
	cleanupErr = errors.Join(cleanupErr, sess.Close())
	_, deleteErr := tx.opts.deleteCandidate(tx.app.cfg, sess.ID, SessionDeleteOptions{AllowMissingSession: true})
	cleanupErr = errors.Join(cleanupErr, deleteErr)
	return cleanupErr
}

func (tx *activeSessionReplacementTransaction) cleanupOldResources() error {
	var cleanupErr error
	if tx.oldRuntime.Modules != nil {
		cleanupErr = errors.Join(cleanupErr, tx.oldRuntime.Modules.CloseSession(context.Background()))
	}
	if tx.oldLock != nil {
		cleanupErr = errors.Join(cleanupErr, tx.oldLock.Close())
	}
	if tx.oldSession != nil {
		cleanupErr = errors.Join(cleanupErr, tx.oldSession.Close())
	}
	return cleanupErr
}

func replacementPhaseError(phase sessionReplacementPhase, err error) error {
	if err == nil {
		return nil
	}
	return &sessionReplacementError{Phase: phase, Err: err}
}
