package runtime

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/hooks"
	"github.com/juex-ai/juex/internal/prompt"
	"github.com/juex-ai/juex/internal/session"
)

func TestReplaceSessionRuntimePublishesCoherentBundle(t *testing.T) {
	root := t.TempDir()
	first := newSessionRuntimeTestSession(t, root)
	second := newSessionRuntimeTestSession(t, root)
	engine := &Engine{
		Session:           first,
		Prompt:            &prompt.Builder{WorkDir: root, ScratchpadDir: first.ScratchpadDir()},
		PendingInputQueue: NewPendingInputQueue(first.Dir, PendingInputQueueOptions{}),
		Notes:             NewNotesStore(first.Dir),
		GoalState:         NewGoalStateStore(first.Dir, GoalStateOptions{}),
		HookContext: hooks.Request{
			CWD:              root,
			WorkspaceRoots:   []string{root},
			ConversationPath: filepath.Join(first.Dir, "conversation.jsonl"),
			EventsPath:       filepath.Join(first.Dir, "events.jsonl"),
		},
	}

	if err := engine.ReplaceSessionRuntime(first); err != nil {
		t.Fatal(err)
	}
	assertSessionRuntimeBundle(t, engine.SessionRuntimeSnapshot(), first)

	if err := engine.ReplaceSessionRuntime(second); err != nil {
		t.Fatal(err)
	}
	snapshot := engine.SessionRuntimeSnapshot()
	assertSessionRuntimeBundle(t, snapshot, second)
	if got := engine.SystemPrompt(); !strings.Contains(got, second.ScratchpadDir()) ||
		strings.Contains(got, first.ScratchpadDir()) {
		t.Fatalf("system prompt did not switch scratchpad from %q to %q:\n%s", first.ScratchpadDir(), second.ScratchpadDir(), got)
	}
}

func TestReplaceSessionRuntimeRejectsBusyRuntimeAtomically(t *testing.T) {
	root := t.TempDir()
	first := newSessionRuntimeTestSession(t, root)
	second := newSessionRuntimeTestSession(t, root)
	engine := &Engine{
		Session:           first,
		Prompt:            &prompt.Builder{ScratchpadDir: first.ScratchpadDir()},
		PendingInputQueue: NewPendingInputQueue(first.Dir, PendingInputQueueOptions{}),
		Notes:             NewNotesStore(first.Dir),
		GoalState:         NewGoalStateStore(first.Dir, GoalStateOptions{}),
	}
	if err := engine.ReplaceSessionRuntime(first); err != nil {
		t.Fatal(err)
	}
	if err := engine.ReserveTurnID("turn-busy"); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.EnqueuePendingInput(t.Context(), "keep this input"); err != nil {
		t.Fatal(err)
	}

	err := engine.ReplaceSessionRuntime(second)
	if !errors.Is(err, ErrSessionRuntimeBusy) {
		t.Fatalf("ReplaceSessionRuntime() error = %v, want ErrSessionRuntimeBusy", err)
	}
	assertSessionRuntimeBundle(t, engine.SessionRuntimeSnapshot(), first)
	if status := engine.PendingInputStatus(); status.TurnID != "turn-busy" || status.PendingCount != 1 {
		t.Fatalf("pending status after rejected replacement = %+v", status)
	}
	records, err := engine.SessionRuntimeSnapshot().PendingInputQueue.Records()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("pending records after rejected replacement = %+v, want one", records)
	}
}

func newSessionRuntimeTestSession(t *testing.T, root string) *session.Session {
	t.Helper()
	sess, err := session.NewWithOptions(root, session.Options{
		Kind:   session.KindPrimary,
		Active: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

func assertSessionRuntimeBundle(t *testing.T, snapshot SessionRuntimeSnapshot, want *session.Session) {
	t.Helper()
	if snapshot.Session != want {
		t.Fatalf("session = %p (%v), want %p (%v)", snapshot.Session, sessionID(snapshot.Session), want, want.ID)
	}
	if snapshot.ScratchpadDir != want.ScratchpadDir() {
		t.Fatalf("scratchpad = %q, want %q", snapshot.ScratchpadDir, want.ScratchpadDir())
	}
	if snapshot.PendingInputQueue == nil || filepath.Dir(snapshot.PendingInputQueue.path) != want.Dir {
		t.Fatalf("pending queue = %+v, want session dir %q", snapshot.PendingInputQueue, want.Dir)
	}
	if snapshot.Notes == nil || snapshot.Notes.SessionDir != want.Dir {
		t.Fatalf("notes = %+v, want session dir %q", snapshot.Notes, want.Dir)
	}
	if snapshot.GoalState == nil || snapshot.GoalState.SessionDir != want.Dir {
		t.Fatalf("goal state = %+v, want session dir %q", snapshot.GoalState, want.Dir)
	}
	if snapshot.HookContext.SessionID != want.ID {
		t.Fatalf("hook session id = %q, want %q", snapshot.HookContext.SessionID, want.ID)
	}
	if snapshot.HookContext.ConversationPath != filepath.Join(want.Dir, "conversation.jsonl") {
		t.Fatalf("hook conversation path = %q", snapshot.HookContext.ConversationPath)
	}
	if snapshot.HookContext.EventsPath != filepath.Join(want.Dir, "events.jsonl") {
		t.Fatalf("hook events path = %q", snapshot.HookContext.EventsPath)
	}
}

func sessionID(sess *session.Session) string {
	if sess == nil {
		return ""
	}
	return sess.ID
}
