package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/session"
)

func TestAttachWorkspaceSessionCreatesActivePrimaryWhenEmpty(t *testing.T) {
	cfg := attachmentTestConfig(t)

	attachment, err := AttachWorkspaceSession(cfg, SessionAttachmentRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer attachment.Session.Close()

	if attachment.LockMode != string(SessionModeAttachActive) {
		t.Fatalf("lock mode = %q, want %q", attachment.LockMode, SessionModeAttachActive)
	}
	if attachment.Session.Kind != session.KindPrimary || !attachment.Session.Active {
		t.Fatalf("session kind/active = %q/%v, want active primary", attachment.Session.Kind, attachment.Session.Active)
	}
	assertHistoryActive(t, cfg, attachment.Session.ID)
}

func TestAttachWorkspaceSessionAttachesActivePrimary(t *testing.T) {
	cfg := attachmentTestConfig(t)
	active := seedAttachmentSession(t, cfg, session.KindPrimary, "active", "active")

	attachment, err := AttachWorkspaceSession(cfg, SessionAttachmentRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer attachment.Session.Close()

	if attachment.Session.ID != active.ID {
		t.Fatalf("session id = %s, want %s", attachment.Session.ID, active.ID)
	}
	if attachment.LockMode != string(SessionModeAttachActive) {
		t.Fatalf("lock mode = %q", attachment.LockMode)
	}
	assertHistoryActive(t, cfg, active.ID)
}

func TestAttachWorkspaceSessionFallsBackFromStaleActive(t *testing.T) {
	cfg := attachmentTestConfig(t)
	stale := session.Info{
		ID:   "missing",
		Dir:  filepath.Join(cfg.SessionsDir(), "missing"),
		Kind: session.KindPrimary,
	}
	if err := session.SetActive(cfg.HistoryPath(), stale); err != nil {
		t.Fatal(err)
	}
	fallback := seedAttachmentSession(t, cfg, session.KindPrimary, "fallback", "record")

	attachment, err := AttachWorkspaceSession(cfg, SessionAttachmentRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer attachment.Session.Close()

	if attachment.Session.ID != fallback.ID {
		t.Fatalf("session id = %s, want fallback %s", attachment.Session.ID, fallback.ID)
	}
	assertHistoryActive(t, cfg, fallback.ID)
}

func TestAttachWorkspaceSessionFallsBackToDiskListedPrimary(t *testing.T) {
	cfg := attachmentTestConfig(t)
	fallback := seedAttachmentSession(t, cfg, session.KindPrimary, "disk fallback", "none")

	attachment, err := AttachWorkspaceSession(cfg, SessionAttachmentRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer attachment.Session.Close()

	if attachment.Session.ID != fallback.ID {
		t.Fatalf("session id = %s, want disk fallback %s", attachment.Session.ID, fallback.ID)
	}
	assertHistoryActive(t, cfg, fallback.ID)
}

func TestAttachWorkspaceSessionCreatesLazyNewPrimary(t *testing.T) {
	cfg := attachmentTestConfig(t)

	attachment, err := AttachWorkspaceSession(cfg, SessionAttachmentRequest{
		Mode: SessionModeNewPrimary,
		Lazy: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer attachment.Session.Close()

	if attachment.LockMode != string(SessionModeNewPrimary) {
		t.Fatalf("lock mode = %q, want %q", attachment.LockMode, SessionModeNewPrimary)
	}
	if _, err := os.Stat(attachment.Session.Dir); !os.IsNotExist(err) {
		t.Fatalf("lazy session dir stat err = %v, want not exist", err)
	}
	assertHistoryActive(t, cfg, attachment.Session.ID)
}

func TestAttachWorkspaceSessionCreatesSideWithoutReplacingActive(t *testing.T) {
	cfg := attachmentTestConfig(t)
	active := seedAttachmentSession(t, cfg, session.KindPrimary, "active", "active")

	attachment, err := AttachWorkspaceSession(cfg, SessionAttachmentRequest{
		Mode:  SessionModeNewSide,
		Alias: "side",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer attachment.Session.Close()

	if attachment.LockMode != string(SessionModeNewSide) {
		t.Fatalf("lock mode = %q, want %q", attachment.LockMode, SessionModeNewSide)
	}
	if attachment.Session.Kind != session.KindSide || attachment.Session.Active {
		t.Fatalf("side kind/active = %q/%v", attachment.Session.Kind, attachment.Session.Active)
	}
	assertHistoryActive(t, cfg, active.ID)
	assertHistoryContains(t, cfg, attachment.Session.ID)
}

func TestAttachWorkspaceSessionResumePrimaryActivates(t *testing.T) {
	cfg := attachmentTestConfig(t)
	seedAttachmentSession(t, cfg, session.KindPrimary, "old active", "active")
	resume := seedAttachmentSession(t, cfg, session.KindPrimary, "resume primary", "record")

	attachment, err := AttachWorkspaceSession(cfg, SessionAttachmentRequest{ResumeDir: resume.Dir})
	if err != nil {
		t.Fatal(err)
	}
	defer attachment.Session.Close()

	if attachment.LockMode != "resume" {
		t.Fatalf("lock mode = %q, want resume", attachment.LockMode)
	}
	if attachment.Session.ID != resume.ID || !attachment.Session.Active {
		t.Fatalf("resumed session = %s active=%v, want %s active", attachment.Session.ID, attachment.Session.Active, resume.ID)
	}
	assertHistoryActive(t, cfg, resume.ID)
}

func TestAttachWorkspaceSessionResumeSideDoesNotReplaceActive(t *testing.T) {
	cfg := attachmentTestConfig(t)
	active := seedAttachmentSession(t, cfg, session.KindPrimary, "active", "active")
	side := seedAttachmentSession(t, cfg, session.KindSide, "side", "record")

	attachment, err := AttachWorkspaceSession(cfg, SessionAttachmentRequest{ResumeDir: side.Dir})
	if err != nil {
		t.Fatal(err)
	}
	defer attachment.Session.Close()

	if attachment.LockMode != "resume" {
		t.Fatalf("lock mode = %q, want resume", attachment.LockMode)
	}
	if attachment.Session.ID != side.ID || attachment.Session.Active {
		t.Fatalf("resumed side = %s active=%v, want %s inactive", attachment.Session.ID, attachment.Session.Active, side.ID)
	}
	assertHistoryActive(t, cfg, active.ID)
	assertHistoryContains(t, cfg, side.ID)
}

func TestEnsureActivePrimarySessionRecordCreatesWhenEmpty(t *testing.T) {
	cfg := attachmentTestConfig(t)

	if err := EnsureActivePrimarySessionRecord(cfg); err != nil {
		t.Fatal(err)
	}

	h, err := session.LoadHistory(cfg.HistoryPath())
	if err != nil {
		t.Fatal(err)
	}
	if h.Active == nil || h.Active.ID == "" {
		t.Fatalf("active = %+v, want active primary", h.Active)
	}
	if _, err := os.Stat(filepath.Join(cfg.SessionsDir(), h.Active.ID, "conversation.jsonl")); err != nil {
		t.Fatalf("conversation stat err = %v", err)
	}
}

func TestEnsureActivePrimarySessionRecordUsesDiskFallback(t *testing.T) {
	cfg := attachmentTestConfig(t)
	fallback := seedAttachmentSession(t, cfg, session.KindPrimary, "disk fallback", "none")

	if err := EnsureActivePrimarySessionRecord(cfg); err != nil {
		t.Fatal(err)
	}

	assertHistoryActive(t, cfg, fallback.ID)
}

func attachmentTestConfig(t *testing.T) config.Config {
	t.Helper()
	return config.Config{WorkDir: t.TempDir()}
}

func seedAttachmentSession(t *testing.T, cfg config.Config, kind, text, history string) session.Info {
	t.Helper()
	opts := session.Options{
		Kind:           kind,
		NoRecordActive: true,
	}
	if history != "none" {
		opts.HistoryPath = cfg.HistoryPath()
	}
	sess, err := session.NewWithOptions(cfg.SessionsDir(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(llm.TextMessage(llm.RoleUser, text)); err != nil {
		t.Fatal(err)
	}
	info := sess.Info(time.Now().UTC())
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	switch history {
	case "active":
		if err := session.SetActive(cfg.HistoryPath(), info); err != nil {
			t.Fatal(err)
		}
	case "record":
		if err := session.RecordSession(cfg.HistoryPath(), info); err != nil {
			t.Fatal(err)
		}
	}
	return info
}

func assertHistoryActive(t *testing.T, cfg config.Config, id string) {
	t.Helper()
	h, err := session.LoadHistory(cfg.HistoryPath())
	if err != nil {
		t.Fatal(err)
	}
	if h.Active == nil || h.Active.ID != id {
		t.Fatalf("active = %+v, want %s", h.Active, id)
	}
}

func assertHistoryContains(t *testing.T, cfg config.Config, id string) {
	t.Helper()
	h, err := session.LoadHistory(cfg.HistoryPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range h.Sessions {
		if info.ID == id {
			return
		}
	}
	t.Fatalf("history sessions = %+v, missing %s", h.Sessions, id)
}
