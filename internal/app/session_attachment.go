package app

import (
	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/session"
)

type SessionAttachmentRequest struct {
	ResumeDir string
	Mode      SessionMode
	Alias     string
	Lazy      bool
}

// SessionAttachment is the result of applying workspace session attachment
// policy. The caller owns Session and must acquire LockMode for its lifetime.
type SessionAttachment struct {
	Session  *session.Session
	LockMode string
}

// AttachAndLockWorkspaceSession keeps discovery, load/creation, and lifetime
// lock acquisition in one root-guarded operation so deletion cannot remove the
// selected directory between those steps.
func AttachAndLockWorkspaceSession(cfg config.Config, req SessionAttachmentRequest) (SessionAttachment, *session.Lock, error) {
	var attachment SessionAttachment
	var sessLock *session.Lock
	err := session.WithSessionRootGuard(cfg.SessionsDir(), func() error {
		var err error
		attachment, err = AttachWorkspaceSession(cfg, req)
		if err != nil {
			return err
		}
		sessLock, err = session.AcquireSessionLock(attachment.Session.Dir, attachment.LockMode)
		if err != nil {
			_ = attachment.Session.Close()
			attachment = SessionAttachment{}
		}
		return err
	})
	return attachment, sessLock, err
}

// AttachWorkspaceSession opens or creates the session requested by CLI/web
// inputs and returns the lock mode that matches that attachment decision.
func AttachWorkspaceSession(cfg config.Config, req SessionAttachmentRequest) (SessionAttachment, error) {
	if req.ResumeDir != "" {
		return resumeWorkspaceSession(cfg, req)
	}

	switch normalizeSessionMode(req.Mode) {
	case SessionModeNewPrimary:
		return newPrimaryWorkspaceSession(cfg, req, SessionModeNewPrimary)
	case SessionModeNewSide:
		return newSideWorkspaceSession(cfg, req)
	default:
		return attachActiveWorkspaceSession(cfg, req)
	}
}

// EnsureActivePrimarySessionRecord makes history.active point at an attachable
// primary session, creating one when the workspace has no usable primary.
func EnsureActivePrimarySessionRecord(cfg config.Config) error {
	return session.WithSessionRootGuard(cfg.SessionsDir(), func() error {
		if info, ok, err := findAttachablePrimarySession(cfg); err != nil {
			return err
		} else if ok {
			return session.SetActive(cfg.HistoryPath(), info)
		}

		attachment, err := newPrimaryWorkspaceSession(cfg, SessionAttachmentRequest{}, SessionModeAttachActive)
		if err != nil {
			return err
		}
		return attachment.Session.Close()
	})
}

// ActivePrimarySessionID returns the recorded active primary session id.
// SetActive is the write boundary that guarantees only primary sessions can
// occupy this slot; the session may still be lazy and have no files on disk.
func ActivePrimarySessionID(cfg config.Config) (string, bool, error) {
	h, err := session.LoadHistory(cfg.HistoryPath())
	if err != nil {
		return "", false, err
	}
	if h.Active == nil || h.Active.ID == "" {
		return "", false, nil
	}
	return h.Active.ID, true, nil
}

func normalizeSessionMode(mode SessionMode) SessionMode {
	switch mode {
	case SessionModeNewPrimary, SessionModeNewSide:
		return mode
	default:
		return SessionModeAttachActive
	}
}

func resumeWorkspaceSession(cfg config.Config, req SessionAttachmentRequest) (SessionAttachment, error) {
	kind, err := session.LoadKind(req.ResumeDir)
	if err != nil {
		return SessionAttachment{}, err
	}
	active := session.NormalizeKind(kind) == session.KindPrimary
	sess, err := session.LoadWithOptions(req.ResumeDir, session.Options{
		Alias:            req.Alias,
		Active:           active,
		HistoryPath:      cfg.HistoryPath(),
		RepairTranscript: true,
	})
	if err != nil {
		return SessionAttachment{}, err
	}
	info := sess.Info()
	if active {
		if err := session.SetActive(cfg.HistoryPath(), info); err != nil {
			sess.Close()
			return SessionAttachment{}, err
		}
	} else if err := session.RecordSession(cfg.HistoryPath(), info); err != nil {
		sess.Close()
		return SessionAttachment{}, err
	}
	return SessionAttachment{Session: sess, LockMode: "resume"}, nil
}

func attachActiveWorkspaceSession(cfg config.Config, req SessionAttachmentRequest) (SessionAttachment, error) {
	info, ok, err := findAttachablePrimarySession(cfg)
	if err != nil {
		return SessionAttachment{}, err
	}
	if !ok {
		return newPrimaryWorkspaceSession(cfg, req, SessionModeAttachActive)
	}
	sess, err := session.LoadWithOptions(session.InfoDir(cfg.SessionsDir(), info), session.Options{
		Alias:            req.Alias,
		Active:           true,
		HistoryPath:      cfg.HistoryPath(),
		RepairTranscript: true,
	})
	if err != nil {
		return SessionAttachment{}, err
	}
	if err := session.SetActive(cfg.HistoryPath(), sess.Info()); err != nil {
		sess.Close()
		return SessionAttachment{}, err
	}
	return SessionAttachment{Session: sess, LockMode: string(SessionModeAttachActive)}, nil
}

func newPrimaryWorkspaceSession(cfg config.Config, req SessionAttachmentRequest, lockMode SessionMode) (SessionAttachment, error) {
	sess, err := session.NewWithOptions(cfg.SessionsDir(), session.Options{
		Alias:       req.Alias,
		Kind:        session.KindPrimary,
		Active:      true,
		HistoryPath: cfg.HistoryPath(),
		Lazy:        req.Lazy,
	})
	if err != nil {
		return SessionAttachment{}, err
	}
	if err := session.SetActive(cfg.HistoryPath(), sess.Info()); err != nil {
		sess.Close()
		return SessionAttachment{}, err
	}
	return SessionAttachment{Session: sess, LockMode: string(lockMode)}, nil
}

func newSideWorkspaceSession(cfg config.Config, req SessionAttachmentRequest) (SessionAttachment, error) {
	sess, err := session.NewWithOptions(cfg.SessionsDir(), session.Options{
		Alias:       req.Alias,
		Kind:        session.KindSide,
		HistoryPath: cfg.HistoryPath(),
		Lazy:        req.Lazy,
	})
	if err != nil {
		return SessionAttachment{}, err
	}
	if err := session.RecordSession(cfg.HistoryPath(), sess.Info()); err != nil {
		sess.Close()
		return SessionAttachment{}, err
	}
	return SessionAttachment{Session: sess, LockMode: string(SessionModeNewSide)}, nil
}

func findAttachablePrimarySession(cfg config.Config) (session.Info, bool, error) {
	h, err := session.LoadHistory(cfg.HistoryPath())
	if err != nil {
		return session.Info{}, false, err
	}
	infos, err := session.ListWithHistory(cfg.SessionsDir(), cfg.HistoryPath())
	if err != nil {
		return session.Info{}, false, err
	}
	if h.Active != nil {
		for _, info := range infos {
			if info.ID == h.Active.ID && attachablePrimaryInfo(cfg, info) {
				return info, true, nil
			}
		}
	}
	for _, info := range infos {
		if attachablePrimaryInfo(cfg, info) {
			return info, true, nil
		}
	}
	return session.Info{}, false, nil
}

func attachablePrimaryInfo(cfg config.Config, info session.Info) bool {
	if session.NormalizeKind(info.Kind) != session.KindPrimary || info.ID == "" {
		return false
	}
	return session.HasConversation(session.InfoDir(cfg.SessionsDir(), info))
}
