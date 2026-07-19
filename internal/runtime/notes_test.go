package runtime

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/tools"
)

func TestNotesToolDefinitionsBindSessionStateGroup(t *testing.T) {
	reg := tools.NewRegistry()
	eng := &Engine{Tools: reg}
	if err := RegisterNotesTools(reg, eng); err != nil {
		t.Fatal(err)
	}
	definitions := NotesToolDefinitions()
	if len(definitions) != 1 {
		t.Fatalf("definition count = %d, want 1", len(definitions))
	}
	definition := definitions[0]
	if definition.Group != tools.ToolGroupSessionState {
		t.Fatalf("definition group = %q, want %q", definition.Group, tools.ToolGroupSessionState)
	}
	registered, ok := reg.Get(definition.Name)
	if !ok {
		t.Fatalf("%s is not registered", definition.Name)
	}
	if got := registered.Definition(); !reflect.DeepEqual(got, definition) {
		t.Fatalf("registered definition = %#v, want %#v", got, definition)
	}
}

func TestNotesToolRewritesSessionNotesAndEmitsEvent(t *testing.T) {
	eng, bus := newEngine(t, &mockProvider{}, false)
	eng.Notes = NewNotesStore(eng.Session.Dir)
	if err := RegisterNotesTools(eng.Tools, eng); err != nil {
		t.Fatal(err)
	}
	tool, ok := eng.Tools.Get(NotesToolUpdate)
	if !ok {
		t.Fatal("update_notes is not registered")
	}
	properties := tool.Schema["properties"].(map[string]any)
	if len(properties) != 1 || properties["content"] == nil {
		t.Fatalf("update_notes properties = %#v", properties)
	}
	if _, ok := eng.Tools.Get("get_notes"); ok {
		t.Fatal("get_notes must not be registered")
	}
	for _, want := range []string{"scratchpad", "replace", `guide available via skill_load("juex-session-state")`} {
		if !strings.Contains(strings.ToLower(tool.Description), want) {
			t.Fatalf("tool description missing %q: %q", want, tool.Description)
		}
	}

	var updated NotesUpdatedPayload
	bus.Subscribe("notes.updated", func(event events.Event) {
		updated, _ = event.Payload.(NotesUpdatedPayload)
	})
	out, err := eng.Tools.Call(context.Background(), NotesToolUpdate, map[string]any{
		"content": "- [x] inspect\n- [ ] verify",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"content":"- [x] inspect\n- [ ] verify"`) {
		t.Fatalf("tool output = %s", out)
	}
	if updated.Content != "- [x] inspect\n- [ ] verify" || updated.UpdatedAt.IsZero() {
		t.Fatalf("notes.updated payload = %+v", updated)
	}
	snapshot, err := eng.Notes.Snapshot()
	if err != nil || snapshot.Content != updated.Content {
		t.Fatalf("notes snapshot = %+v, err = %v", snapshot, err)
	}

	_, err = eng.Tools.Call(context.Background(), NotesToolUpdate, map[string]any{
		"content": strings.Repeat("x", MaxNotesCharacters+1),
	})
	if err == nil || !strings.Contains(err.Error(), "maximum is 2048") {
		t.Fatalf("oversize tool error = %v", err)
	}
}

func TestNotesSnapshotEntrypointsUseEngineStore(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	if _, err := NewNotesStore(eng.Session.Dir).Update("session directory store"); err != nil {
		t.Fatal(err)
	}
	injected := NewNotesStore(t.TempDir())
	if _, err := injected.Update("engine-owned store"); err != nil {
		t.Fatal(err)
	}
	eng.SetNotesStore(injected)

	status, err := eng.NotesStatusSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if status == nil || status.Content != "engine-owned store" {
		t.Fatalf("NotesStatusSnapshot() = %+v, want engine-owned store", status)
	}
	contextText, ok := eng.notesContextSnapshot()
	if !ok || !strings.Contains(contextText, "engine-owned store") || strings.Contains(contextText, "session directory store") {
		t.Fatalf("notesContextSnapshot() = %q, %v", contextText, ok)
	}
}

func TestNotesStoreLazyInitializationReturnsOneEngineInstance(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	const callers = 32
	stores := make([]*NotesStore, callers)
	start := make(chan struct{})
	var wg sync.WaitGroup

	for i := range stores {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			stores[index] = eng.notesStoreLocked()
		}(i)
	}
	close(start)
	wg.Wait()

	first := stores[0]
	if first == nil || first.SessionDir != eng.Session.Dir {
		t.Fatalf("lazy store = %+v, want session dir %q", first, eng.Session.Dir)
	}
	for i, store := range stores[1:] {
		if store != first {
			t.Fatalf("store %d = %p, want singleton %p", i+1, store, first)
		}
	}
	if eng.Notes != first {
		t.Fatalf("Engine.Notes = %p, want lazy singleton %p", eng.Notes, first)
	}
}

func TestNotesToolRecitesRewriteOnNextProviderRequest(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{
			Type:      llm.BlockToolUse,
			ToolUseID: "notes-1",
			ToolName:  NotesToolUpdate,
			Input:     map[string]any{"content": "- [x] inspected\n- [ ] finish tests"},
		}}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
	}}
	eng, _ := newEngine(t, prov, false)
	eng.Notes = NewNotesStore(eng.Session.Dir)
	if err := RegisterNotesTools(eng.Tools, eng); err != nil {
		t.Fatal(err)
	}

	if out, err := eng.Turn(context.Background(), "work"); err != nil || out != "done" {
		t.Fatalf("Turn() = %q, %v", out, err)
	}
	if len(prov.histories) != 2 {
		t.Fatalf("provider calls = %d", len(prov.histories))
	}
	second := messagesText(prov.histories[1])
	if !strings.Contains(second, "Current working notes") || !strings.Contains(second, "finish tests") {
		t.Fatalf("second provider context missing notes:\n%s", second)
	}
}

func TestActiveContextAppendsGoalThenNotes(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	eng.GoalState = NewGoalStateStore(eng.Session.Dir, GoalStateOptions{})
	eng.Notes = NewNotesStore(eng.Session.Dir)
	if _, err := eng.GoalState.Create("ship notes", "tests pass"); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Notes.Update("- [ ] run tests"); err != nil {
		t.Fatal(err)
	}

	snapshot := eng.ActiveContext(llm.TextMessage(llm.RoleUser, "continue"))
	if len(snapshot.Messages) < 3 {
		t.Fatalf("active context = %+v", snapshot.Messages)
	}
	goal := snapshot.Messages[len(snapshot.Messages)-2]
	notes := snapshot.Messages[len(snapshot.Messages)-1]
	if goal.ID != "runtime-goal-contract" || goal.Kind != llm.MessageKindRuntimeContext {
		t.Fatalf("goal context = %+v", goal)
	}
	if notes.ID != "runtime-notes" || notes.Kind != llm.MessageKindRuntimeContext || !strings.Contains(notes.FirstText(), "run tests") {
		t.Fatalf("notes context = %+v", notes)
	}

	if _, err := eng.Notes.Update(""); err != nil {
		t.Fatal(err)
	}
	snapshot = eng.ActiveContext(llm.TextMessage(llm.RoleUser, "continue"))
	for _, message := range snapshot.Messages {
		if message.ID == "runtime-notes" {
			t.Fatalf("empty notes leaked into context: %+v", snapshot.Messages)
		}
	}
}

func TestNotesContextFailsLoudOnceAndRecoversThroughUpdateTool(t *testing.T) {
	tests := []struct {
		name      string
		corrupt   []byte
		wantError string
	}{
		{
			name:      "oversized",
			corrupt:   []byte(strings.Repeat("x", MaxNotesCharacters+1)),
			wantError: "maximum is 2048",
		},
		{
			name:      "invalid UTF-8",
			corrupt:   []byte{0xff, 0xfe, 0xfd},
			wantError: "valid UTF-8",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eng, bus := newEngine(t, &mockProvider{}, false)
			eng.Notes = NewNotesStore(eng.Session.Dir)
			if err := RegisterNotesTools(eng.Tools, eng); err != nil {
				t.Fatal(err)
			}
			notesPath := filepath.Join(eng.Session.Dir, NotesFileName)
			if err := os.WriteFile(notesPath, tt.corrupt, 0o600); err != nil {
				t.Fatal(err)
			}

			var errored []NotesErroredPayload
			bus.Subscribe("notes.errored", func(event events.Event) {
				payload, _ := event.Payload.(NotesErroredPayload)
				errored = append(errored, payload)
			})

			for range 2 {
				snapshot := eng.ActiveContext(llm.TextMessage(llm.RoleUser, "continue"))
				message := runtimeContextMessage(snapshot.Messages, "runtime-notes")
				if message == nil {
					t.Fatalf("active context missing Notes error placeholder: %+v", snapshot.Messages)
				}
				text := message.FirstText()
				relativePath := filepath.ToSlash(filepath.Join(".juex", "sessions", filepath.Base(eng.Session.Dir), NotesFileName))
				for _, want := range []string{"Working notes unavailable", tt.wantError, relativePath, "update_notes"} {
					if !strings.Contains(text, want) {
						t.Fatalf("Notes placeholder missing %q: %q", want, text)
					}
				}
			}
			if len(errored) != 1 {
				t.Fatalf("notes.errored events = %+v, want exactly one", errored)
			}
			if errored[0].Path != notesPath || !strings.Contains(errored[0].Error, tt.wantError) {
				t.Fatalf("notes.errored payload = %+v", errored[0])
			}

			if _, err := eng.Tools.Call(context.Background(), NotesToolUpdate, map[string]any{
				"content": "- [ ] recovered through update_notes",
			}); err != nil {
				t.Fatal(err)
			}
			recovered := runtimeContextMessage(eng.ActiveContext().Messages, "runtime-notes")
			if recovered == nil || !strings.Contains(recovered.FirstText(), "Current working notes") || !strings.Contains(recovered.FirstText(), "recovered through update_notes") || strings.Contains(recovered.FirstText(), "unavailable") {
				t.Fatalf("recovered Notes context = %+v", recovered)
			}

			if err := os.WriteFile(notesPath, tt.corrupt, 0o600); err != nil {
				t.Fatal(err)
			}
			_ = eng.ActiveContext()
			if len(errored) != 2 {
				t.Fatalf("notes.errored events after recovery and recurrence = %+v, want two", errored)
			}
		})
	}
}

func TestNotesContextFromStoreHandlesNil(t *testing.T) {
	var nilEngine *Engine
	if text, ok := nilEngine.notesContextFromStore(NewNotesStore(t.TempDir())); ok || text != "" {
		t.Fatalf("nil engine notes context = %q, %v", text, ok)
	}

	eng := &Engine{}
	if text, ok := eng.notesContextFromStore(nil); ok || text != "" {
		t.Fatalf("nil store notes context = %q, %v", text, ok)
	}
}

func TestTurnRecitesNotesReadFailurePlaceholderAfterAutoCompaction(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "summary"), StopReason: llm.StopEndTurn},
		{Message: llm.TextMessage(llm.RoleAssistant, "acknowledged"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, false)
	eng.ContextWindow = 100
	eng.Compaction = DefaultCompactionPolicy()
	eng.Notes = NewNotesStore(eng.Session.Dir)
	if err := eng.Session.Append(llm.TextMessage(llm.RoleUser, strings.Repeat("old ", 80))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(eng.Session.Dir, NotesFileName), []byte{0xff}, 0o600); err != nil {
		t.Fatal(err)
	}

	var notesErrors, compactErrors int
	bus.Subscribe("notes.errored", func(events.Event) { notesErrors++ })
	bus.Subscribe("context.compact.errored", func(events.Event) { compactErrors++ })

	out, err := eng.Turn(context.Background(), "continue")
	if err != nil {
		t.Fatal(err)
	}
	if out != "acknowledged" {
		t.Fatalf("Turn() = %q, want acknowledged", out)
	}
	if len(prov.histories) != 2 {
		t.Fatalf("provider calls = %d, want compaction plus turn", len(prov.histories))
	}
	providerText := messagesText(prov.histories[1])
	for _, want := range []string{"Working notes unavailable", "valid UTF-8", "update_notes"} {
		if !strings.Contains(providerText, want) {
			t.Fatalf("post-compaction provider context missing %q:\n%s", want, providerText)
		}
	}
	if notesErrors != 1 || compactErrors != 0 {
		t.Fatalf("notes errors = %d, compact errors = %d", notesErrors, compactErrors)
	}
}

func TestTurnRecitesNotesReadFailurePlaceholder(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{{
		Message:    llm.TextMessage(llm.RoleAssistant, "acknowledged"),
		StopReason: llm.StopEndTurn,
	}}}
	eng, bus := newEngine(t, prov, false)
	eng.Notes = NewNotesStore(eng.Session.Dir)
	notesPath := filepath.Join(eng.Session.Dir, NotesFileName)
	if err := os.WriteFile(notesPath, []byte{0xff}, 0o600); err != nil {
		t.Fatal(err)
	}
	var errored int
	bus.Subscribe("notes.errored", func(events.Event) {
		errored++
	})

	out, err := eng.Turn(context.Background(), "continue")
	if err != nil || out != "acknowledged" {
		t.Fatalf("Turn() = %q, %v", out, err)
	}
	if len(prov.histories) != 1 {
		t.Fatalf("provider calls = %d, want one", len(prov.histories))
	}
	providerText := messagesText(prov.histories[0])
	for _, want := range []string{"Working notes unavailable", "valid UTF-8", "update_notes"} {
		if !strings.Contains(providerText, want) {
			t.Fatalf("provider context missing %q:\n%s", want, providerText)
		}
	}
	if errored != 1 {
		t.Fatalf("notes.errored events = %d, want one", errored)
	}
}

func runtimeContextMessage(messages []llm.Message, id string) *llm.Message {
	for i := range messages {
		if messages[i].ID == id {
			return &messages[i]
		}
	}
	return nil
}

func TestRegisterNotesToolsRejectsMissingStore(t *testing.T) {
	reg := tools.NewRegistry()
	eng := &Engine{Tools: reg}
	if err := RegisterNotesTools(reg, eng); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Call(context.Background(), NotesToolUpdate, map[string]any{"content": "hi"}); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("missing store error = %v", err)
	}
}
