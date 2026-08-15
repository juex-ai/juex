package app

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/eventcatalog"
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	juexruntime "github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/session"
	"github.com/juex-ai/juex/internal/toolevents"
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

func TestAttachAndLockWorkspaceSessionRepairsThroughRuntimeBus(t *testing.T) {
	cfg := attachmentTestConfig(t)
	active := seedDanglingToolUseSession(t, cfg)

	attachment, lock, err := AttachAndLockWorkspaceSession(cfg, SessionAttachmentRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer attachment.Session.Close()
	defer func() { _ = lock.Close() }()
	sink := events.NewDurableSink(attachment.Session)
	sink.SetCatalog(eventcatalog.Default())
	defer func() { _ = sink.Close() }()
	bus := events.NewBus()
	bus.SetCommitter(sink)
	engine := &juexruntime.Engine{Bus: bus, Session: attachment.Session}
	if err := engine.RecoverTranscript("load"); err != nil {
		t.Fatal(err)
	}

	if attachment.Session.ID != active.ID {
		t.Fatalf("session id = %s, want %s", attachment.Session.ID, active.ID)
	}
	if len(attachment.Session.History) != 3 {
		t.Fatalf("history len = %d, want repaired 3-message history: %+v", len(attachment.Session.History), attachment.Session.History)
	}
	repair := attachment.Session.History[2]
	if repair.Role != llm.RoleUser || len(repair.Blocks) != 1 || repair.Blocks[0].Type != llm.BlockToolResult {
		t.Fatalf("repair message = %+v", repair)
	}
	if repair.Blocks[0].ToolUseID != "attach_missing" || !repair.Blocks[0].IsError {
		t.Fatalf("repair block = %+v", repair.Blocks[0])
	}
}

func TestAttachWorkspaceSessionDoesNotRepairBeforeLifetimeLock(t *testing.T) {
	cfg := attachmentTestConfig(t)
	active := seedDanglingToolUseSession(t, cfg)

	attachment, err := AttachWorkspaceSession(cfg, SessionAttachmentRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer attachment.Session.Close()

	if attachment.Session.ID != active.ID {
		t.Fatalf("session id = %s, want %s", attachment.Session.ID, active.ID)
	}
	if len(attachment.Session.History) != 2 {
		t.Fatalf("history len = %d, want unrepaired 2-message history", len(attachment.Session.History))
	}
}

func TestLockedRuntimeRecoverySurfacesUnknownOutcomeToCLIAndTranscript(t *testing.T) {
	cfg := attachmentTestConfig(t)
	active := seedStartedDanglingToolUseSession(t, cfg)

	attachment, lock, err := AttachAndLockWorkspaceSession(cfg, SessionAttachmentRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer attachment.Session.Close()
	defer func() { _ = lock.Close() }()
	sink := events.NewDurableSink(attachment.Session)
	sink.SetCatalog(eventcatalog.Default())
	defer func() { _ = sink.Close() }()
	bus := events.NewBus()
	bus.SetCommitter(sink)
	var stderr bytes.Buffer
	printer := newVerbosePrinter(&stderr)
	bus.Subscribe("*", printer.handle)
	engine := &juexruntime.Engine{Bus: bus, Session: attachment.Session}
	if err := engine.RecoverTranscript("load"); err != nil {
		t.Fatal(err)
	}

	if attachment.Session.ID != active.ID {
		t.Fatalf("session id = %s, want %s", attachment.Session.ID, active.ID)
	}
	result := attachment.Session.History[len(attachment.Session.History)-1].Blocks[0]
	if !result.IsError || !strings.Contains(result.Content, "TOOL_OUTCOME_UNKNOWN") {
		t.Fatalf("recovered result = %+v", result)
	}
	if output := stripANSI(stderr.String()); !strings.Contains(output, "outcome unknown: mcp__remote__send (attach_started)") {
		t.Fatalf("verbose recovery output missing unknown outcome:\n%s", output)
	}
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

func TestAttachAndLockWorkspaceSessionRacesDeleteWithoutReturningDeletedSession(t *testing.T) {
	for i := 0; i < 25; i++ {
		cfg := attachmentTestConfig(t)
		active := seedAttachmentSession(t, cfg, session.KindPrimary, "active", "active")
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		var attachment SessionAttachment
		var lock *session.Lock
		var attachErr error
		var deleteErr error
		go func() {
			defer wg.Done()
			<-start
			attachment, lock, attachErr = AttachAndLockWorkspaceSession(cfg, SessionAttachmentRequest{})
		}()
		go func() {
			defer wg.Done()
			<-start
			deleteErr = DeleteSession(cfg, active.ID, SessionDeleteOptions{})
		}()
		close(start)
		wg.Wait()

		if attachErr != nil {
			t.Fatalf("iteration %d attach error = %v", i, attachErr)
		}
		if lock == nil {
			t.Fatalf("iteration %d returned nil lifetime lock", i)
		}
		if _, err := os.Stat(attachment.Session.Dir); err != nil {
			t.Fatalf("iteration %d returned deleted session %s: %v", i, attachment.Session.ID, err)
		}
		var lockErr *session.LockError
		if deleteErr != nil && !errors.As(deleteErr, &lockErr) {
			t.Fatalf("iteration %d delete error = %v, want nil or lock conflict", i, deleteErr)
		}
		if err := lock.Close(); err != nil {
			t.Fatal(err)
		}
		if err := attachment.Session.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func attachmentTestConfig(t *testing.T) config.Config {
	t.Helper()
	return config.Config{WorkDir: t.TempDir()}
}

func seedAttachmentSession(t *testing.T, cfg config.Config, kind, text, history string) session.Info {
	t.Helper()
	opts := session.Options{Kind: kind}
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
	info := sess.Info()
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

func seedDanglingToolUseSession(t *testing.T, cfg config.Config) session.Info {
	t.Helper()
	sess, err := session.NewWithOptions(cfg.SessionsDir(), session.Options{
		Kind:        session.KindPrimary,
		HistoryPath: cfg.HistoryPath(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(llm.TextMessage(llm.RoleUser, "before")); err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{
		Type:      llm.BlockToolUse,
		ToolUseID: "attach_missing",
		ToolName:  "read",
	}}}); err != nil {
		t.Fatal(err)
	}
	info := sess.Info()
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	if err := session.SetActive(cfg.HistoryPath(), info); err != nil {
		t.Fatal(err)
	}
	return info
}

func seedStartedDanglingToolUseSession(t *testing.T, cfg config.Config) session.Info {
	t.Helper()
	sess, err := session.NewWithOptions(cfg.SessionsDir(), session.Options{
		Kind: session.KindPrimary, HistoryPath: cfg.HistoryPath(), EventCatalog: eventcatalog.Default(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(llm.TextMessage(llm.RoleUser, "before")); err != nil {
		t.Fatal(err)
	}
	assistant, err := sess.AppendAssigned(llm.Message{
		ID: "attach-assistant", Role: llm.RoleAssistant,
		Blocks: []llm.Block{{Type: llm.BlockToolUse, ToolUseID: "attach_started", ToolName: "mcp__remote__send"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	call := toolevents.ToolCallPayload{
		Name: "mcp__remote__send", ToolUseID: "attach_started", Iter: 1,
		CallIndex: 0, MessageID: assistant.ID,
	}
	for _, event := range []events.Event{
		{Type: "llm.responded", TurnID: "attach-turn", Payload: juexruntime.LLMRespondedPayload{
			Iter: 1, MessageID: assistant.ID, Blocks: assistant.Blocks,
			ToolCalls: []toolevents.ToolCallPayload{call},
		}},
		{Type: toolevents.RequestedType, TurnID: "attach-turn", Payload: toolevents.Requested(call)},
		{Type: toolevents.RunningType, TurnID: "attach-turn", Payload: toolevents.Running(call)},
	} {
		prepared, err := eventcatalog.Default().Prepare(events.Normalize(event))
		if err != nil {
			t.Fatal(err)
		}
		if err := sess.AppendEvent(prepared); err != nil {
			t.Fatal(err)
		}
	}
	info := sess.Info()
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	if err := session.SetActive(cfg.HistoryPath(), info); err != nil {
		t.Fatal(err)
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
