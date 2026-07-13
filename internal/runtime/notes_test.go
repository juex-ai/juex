package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/tools"
)

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
	for _, want := range []string{"2048", "checkbox", "scratchpad", "rewrite"} {
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
