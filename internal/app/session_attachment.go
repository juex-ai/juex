package app

import (
	"time"

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
}

// ActivePrimarySessionID returns the history.active primary session id, if one
// is recorded. It does not validate the transcript files on disk.
func ActivePrimarySessionID(cfg config.Config) (string, bool, error) {
	h, err := session.LoadHistory(cfg.HistoryPath())
	if err != nil {
		return "", false, err
	}
	if h.Active == nil || h.Active.ID == "" || session.NormalizeKind(h.Active.Kind) != session.KindPrimary {
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
		Alias:        req.Alias,
		Active:       active,
		RecordActive: active,
		HistoryPath:  cfg.HistoryPath(),
	})
	if err != nil {
		return SessionAttachment{}, err
	}
	info := sess.Info(time.Now().UTC())
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
		Alias:        req.Alias,
		Active:       true,
		RecordActive: true,
		HistoryPath:  cfg.HistoryPath(),
	})
	if err != nil {
		return SessionAttachment{}, err
	}
	if err := session.SetActive(cfg.HistoryPath(), sess.Info(time.Now().UTC())); err != nil {
		sess.Close()
		return SessionAttachment{}, err
	}
	return SessionAttachment{Session: sess, LockMode: string(SessionModeAttachActive)}, nil
}

func newPrimaryWorkspaceSession(cfg config.Config, req SessionAttachmentRequest, lockMode SessionMode) (SessionAttachment, error) {
	sess, err := session.NewWithOptions(cfg.SessionsDir(), session.Options{
		Alias:        req.Alias,
		Kind:         session.KindPrimary,
		Active:       true,
		RecordActive: true,
		HistoryPath:  cfg.HistoryPath(),
		Lazy:         req.Lazy,
	})
	if err != nil {
		return SessionAttachment{}, err
	}
	if err := session.SetActive(cfg.HistoryPath(), sess.Info(time.Now().UTC())); err != nil {
		sess.Close()
		return SessionAttachment{}, err
	}
	return SessionAttachment{Session: sess, LockMode: string(lockMode)}, nil
}

func newSideWorkspaceSession(cfg config.Config, req SessionAttachmentRequest) (SessionAttachment, error) {
	sess, err := session.NewWithOptions(cfg.SessionsDir(), session.Options{
		Alias:          req.Alias,
		Kind:           session.KindSide,
		NoRecordActive: true,
		HistoryPath:    cfg.HistoryPath(),
		Lazy:           req.Lazy,
	})
	if err != nil {
		return SessionAttachment{}, err
	}
	if err := session.RecordSession(cfg.HistoryPath(), sess.Info(time.Now().UTC())); err != nil {
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
	if h.Active != nil && attachablePrimaryInfo(cfg, *h.Active) {
		return *h.Active, true, nil
	}
	for _, info := range h.Sessions {
		if attachablePrimaryInfo(cfg, info) {
			return info, true, nil
		}
	}
	infos, err := session.List(cfg.SessionsDir())
	if err != nil {
		return session.Info{}, false, err
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
