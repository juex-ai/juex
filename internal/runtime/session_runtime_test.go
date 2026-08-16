package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/hooks"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/prompt"
	"github.com/juex-ai/juex/internal/provenance"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/session"
	"github.com/juex-ai/juex/internal/tools"
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
	firstModules := newSessionRuntimeTestModuleSet(t)
	secondModules := newSessionRuntimeTestModuleSet(t)
	firstTools := sessionRuntimeTestTools(t, "first_tool")
	secondTools := sessionRuntimeTestTools(t, "second_tool")

	if err := engine.ReplaceSessionRuntimeBundle(first, SessionRuntimeReplacement{Modules: firstModules, Tools: firstTools}); err != nil {
		t.Fatal(err)
	}
	assertSessionRuntimeBundle(t, engine.SessionRuntimeSnapshot(), first)
	if snapshot := engine.SessionRuntimeSnapshot(); snapshot.Modules != firstModules || snapshot.Tools != firstTools {
		t.Fatalf("initial module bundle = modules %p tools %p, want %p %p", snapshot.Modules, snapshot.Tools, firstModules, firstTools)
	}

	if err := engine.ReplaceSessionRuntimeBundle(second, SessionRuntimeReplacement{Modules: secondModules, Tools: secondTools}); err != nil {
		t.Fatal(err)
	}
	snapshot := engine.SessionRuntimeSnapshot()
	assertSessionRuntimeBundle(t, snapshot, second)
	if snapshot.Modules != secondModules || snapshot.Tools != secondTools || engine.Tools != secondTools {
		t.Fatalf("replacement module bundle = modules %p tools %p engine tools %p, want %p %p", snapshot.Modules, snapshot.Tools, engine.Tools, secondModules, secondTools)
	}
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
	firstModules := newSessionRuntimeTestModuleSet(t)
	secondModules := newSessionRuntimeTestModuleSet(t)
	firstTools := sessionRuntimeTestTools(t, "first_tool")
	secondTools := sessionRuntimeTestTools(t, "second_tool")
	if err := engine.ReplaceSessionRuntimeBundle(first, SessionRuntimeReplacement{Modules: firstModules, Tools: firstTools}); err != nil {
		t.Fatal(err)
	}
	if err := engine.ReserveTurnID("turn-busy"); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.EnqueuePendingInput(t.Context(), "keep this input"); err != nil {
		t.Fatal(err)
	}

	err := engine.ReplaceSessionRuntimeBundle(second, SessionRuntimeReplacement{Modules: secondModules, Tools: secondTools})
	if !errors.Is(err, ErrSessionRuntimeBusy) {
		t.Fatalf("ReplaceSessionRuntime() error = %v, want ErrSessionRuntimeBusy", err)
	}
	assertSessionRuntimeBundle(t, engine.SessionRuntimeSnapshot(), first)
	if snapshot := engine.SessionRuntimeSnapshot(); snapshot.Modules != firstModules || snapshot.Tools != firstTools || engine.Tools != firstTools {
		t.Fatalf("busy replacement changed module bundle: %+v", snapshot)
	}
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

func TestReplaceSessionRuntimeRecoversUnconsumedHookContext(t *testing.T) {
	root := t.TempDir()
	sess := newSessionRuntimeTestSession(t, root)
	first := provenanceRuntimeContextMessage("hook-1", "consumed")
	second := provenanceRuntimeContextMessage("hook-2", "pending")
	for _, event := range []events.Event{
		{Type: provenance.HookContextQueuedType, SchemaVersion: 1, ReplayPolicy: events.ReplayRequired, Payload: provenance.HookContextQueuedPayload{Messages: []llm.Message{first}}},
		{Type: provenance.RequestEpochType, SchemaVersion: 1, ReplayPolicy: events.ReplayRequired, Payload: provenance.RequestEpochPayload{Epoch: validRecoveryEpoch(t, first.ID)}},
		{Type: provenance.HookContextQueuedType, SchemaVersion: 1, ReplayPolicy: events.ReplayRequired, Payload: provenance.HookContextQueuedPayload{Messages: []llm.Message{second}}},
	} {
		if err := sess.AppendEvent(events.Normalize(event)); err != nil {
			t.Fatal(err)
		}
	}
	engine := &Engine{Session: sess, Prompt: &prompt.Builder{ScratchpadDir: sess.ScratchpadDir()}}
	if err := engine.ReplaceSessionRuntime(sess); err != nil {
		t.Fatal(err)
	}
	pending := engine.pendingHookRuntimeContextSnapshot()
	if len(pending) != 1 || pending[0].ID != second.ID {
		t.Fatalf("recovered hook context = %+v", pending)
	}
}

func TestRecoverSessionProvenanceDoesNotMaterializeUnrelatedEvents(t *testing.T) {
	root := t.TempDir()
	sess := newSessionRuntimeTestSession(t, root)
	queued := provenanceRuntimeContextMessage("hook-streamed", "pending")

	for index := 0; index < 128; index++ {
		if err := sess.AppendEvent(events.Normalize(events.Event{
			Type:          "tool.output",
			ReplayPolicy:  events.ReplayIgnorable,
			SchemaVersion: 1,
			Payload: map[string]any{
				"index": index,
				"body":  strings.Repeat("x", 32*1024),
			},
		})); err != nil {
			t.Fatal(err)
		}
	}
	if err := sess.AppendEvent(events.Normalize(events.Event{
		Type:          provenance.HookContextQueuedType,
		ReplayPolicy:  events.ReplayRequired,
		SchemaVersion: 1,
		Payload:       provenance.HookContextQueuedPayload{Messages: []llm.Message{queued}},
	})); err != nil {
		t.Fatal(err)
	}

	tracker, err := recoverSessionProvenance(sess.Dir)
	if err != nil {
		t.Fatal(err)
	}
	pending := tracker.PendingHookContext()
	if len(pending) != 1 || pending[0].ID != queued.ID {
		t.Fatalf("recovered hook context = %+v, want %q", pending, queued.ID)
	}
}

func provenanceRuntimeContextMessage(id, text string) llm.Message {
	message := llm.TextMessage(llm.RoleUser, text)
	message.ID = id
	message.Kind = llm.MessageKindRuntimeContext
	return message
}

func validRecoveryEpoch(t *testing.T, hookID string) provenance.RequestEpoch {
	t.Helper()
	epoch, err := provenance.BuildRequestEpoch(provenance.RequestInput{
		Provider: provenance.SafeProvider{ID: "test", Model: "model"},
		History: []llm.Message{
			provenanceRuntimeContextMessage("user-1", "hello"),
			provenanceRuntimeContextMessage(hookID, "consumed"),
		},
		HookContextMessageIDs: []string{hookID},
	})
	if err != nil {
		t.Fatal(err)
	}
	epoch.EpochID = "epoch-1"
	if _, err := json.Marshal(epoch); err != nil {
		t.Fatal(err)
	}
	return epoch
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

func newSessionRuntimeTestModuleSet(t *testing.T) *runtimemodule.Set {
	t.Helper()
	set, err := runtimemodule.BuildSessionSet(t.Context(), nil, runtimemodule.SessionContext{ID: "test"}, runtimemodule.ToolContext{})
	if err != nil {
		t.Fatal(err)
	}
	if err := set.StartSession(t.Context(), runtimemodule.SessionContext{ID: "test"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = set.CloseSession(context.Background()) })
	return set
}

func sessionRuntimeTestTools(t *testing.T, name string) *tools.Registry {
	t.Helper()
	registry := tools.NewRegistry()
	if err := registry.Register(tools.Tool{
		Name:    name,
		Handler: func(context.Context, map[string]any) (string, error) { return name, nil },
	}); err != nil {
		t.Fatal(err)
	}
	return registry
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
