package managedruntime

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/foundation/llm"
)

func TestThreadStateTaskCompletionAndContextRenewal(t *testing.T) {
	now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	text := "Keep the current investigation"
	title, description := "Inspect", "Verify acceptance"
	state, _, err := (ThreadState{}).Apply(ThreadStateAction{Kind: "update_notes", Content: &text}, "", now, true)
	if err != nil {
		t.Fatal(err)
	}
	state, _, err = state.Apply(ThreadStateAction{Kind: "create_task", Title: &title, Description: &description}, "task-a", now, true)
	if err != nil || state.Tasks[0].Status != "todo" || state.Tasks[0].Priority != "p1" || state.Notes.Content != text {
		t.Fatal(state, err)
	}
	original := state
	done := "done"
	completed, _, err := state.Apply(ThreadStateAction{Kind: "update_task", ID: "task-a", Status: &done}, "", now, true)
	if err != nil || completed.Notes.Content != "" || completed.Tasks[0].Status != "done" || original.Tasks[0].Status != "todo" {
		t.Fatal("completion cleanup or immutable input", completed, original, err)
	}
	disabled, _, err := state.Apply(ThreadStateAction{Kind: "update_task", ID: "task-a", Status: &done}, "", now, false)
	if err != nil || disabled.Notes.Content != text {
		t.Fatal("disabled Notes was mutated", disabled, err)
	}
	empty, _, err := state.Apply(ThreadStateAction{Kind: "delete_task", ID: "task-a"}, "", now, true)
	if err != nil || len(empty.Tasks) != 0 || empty.Notes.Content != text {
		t.Fatal("deleting the last task cleared Notes", empty, err)
	}
	pending := "pending"
	state, _, err = disabled.Apply(ThreadStateAction{Kind: "create_task", Title: &title, Description: &description, Status: &pending}, "task-b", now, true)
	if err != nil || state.Notes.Content != text {
		t.Fatal(state, err)
	}
	compact := state.RenewContext(false, true)
	if len(compact.Tasks) != 1 || compact.Tasks[0].ID != "task-b" || compact.Notes.Content != text || len(state.Tasks) != 2 {
		t.Fatal("compaction must retain unfinished work and Notes", compact)
	}
	fresh := state.RenewContext(true, true)
	if len(fresh.Tasks) != 1 || fresh.Notes.Content != "" || fresh.Revision != state.Revision+1 {
		t.Fatal("fresh context must clear Notes, not unfinished work", fresh)
	}
}

func TestThreadStateSelectionCapacityAndValidation(t *testing.T) {
	now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	state := ThreadState{Tasks: []ThreadTask{
		{ID: "todo", Title: "A", Description: "A", Status: "todo", Priority: "p0", UpdatedAt: now},
		{ID: "doing-first", Title: "B", Description: "B", Status: "doing", Priority: "p1", UpdatedAt: now},
		{ID: "doing-second", Title: "C", Description: "C", Status: "doing", Priority: "p1", UpdatedAt: now},
		{ID: "pending", Title: "D", Description: "D", Status: "pending", Priority: "p0", UpdatedAt: now},
	}}
	selected, ok := state.SelectedTask()
	if !ok || selected.ID != "doing-first" {
		t.Fatal("doing must win before priority, preserving order", selected)
	}
	state.Tasks[2].Priority = "p0"
	selected, _ = state.SelectedTask()
	if selected.ID != "doing-second" {
		t.Fatal(selected)
	}
	for i := range state.Tasks {
		state.Tasks[i].Status = "failed"
	}
	if _, ok := state.SelectedTask(); ok {
		t.Fatal("failed work must not spin")
	}
	before, _ := json.Marshal(state)
	bad := "invalid"
	if _, _, err := state.Apply(ThreadStateAction{Kind: "update_task", ID: "todo", Status: &bad}, "", now, true); err == nil {
		t.Fatal("accepted invalid status")
	}
	after, _ := json.Marshal(state)
	if string(before) != string(after) {
		t.Fatal("failed mutation changed caller state")
	}
	long := strings.Repeat("界", 2049)
	if _, _, err := state.Apply(ThreadStateAction{Kind: "update_notes", Content: &long}, "", now, true); err == nil {
		t.Fatal("accepted oversized Notes")
	}
	large := strings.Repeat("x", 32<<10)
	title := "Capacity"
	if _, _, err := state.Apply(ThreadStateAction{Kind: "create_task", Title: &title, Description: &title, Acceptance: &large}, "large", now, true); err == nil {
		t.Fatal("accepted oversized task context")
	}
}

func TestThreadStateSanitizationAndStrictToolInput(t *testing.T) {
	now := time.Now()
	text := "password=private-value\nBearer another-private-value\nsk-examplevalue\nkeep"
	state, _, err := (ThreadState{}).Apply(ThreadStateAction{Kind: "update_notes", Content: &text}, "", now, true)
	if err != nil || strings.Contains(state.Notes.Content, "private-value") || strings.Contains(state.Notes.Content, "sk-examplevalue") || !strings.Contains(state.Notes.Content, "keep") {
		t.Fatal(state, err)
	}
	shared := "Same value"
	state, _, err = state.Apply(ThreadStateAction{Kind: "create_task", Title: &shared, Description: &shared}, "same", now, true)
	if err != nil || state.Tasks[0].Title != shared || state.Tasks[0].Description != shared {
		t.Fatal("aliased input fields were lost", state, err)
	}
	for _, input := range []map[string]any{{"kind": "delete_task"}, {"continuation_count": 1}, {"content": 1}} {
		if _, err := ParseThreadStateAction(llm.Block{ToolName: "update_notes", Input: input}); err == nil {
			t.Fatal("accepted invalid model fields", input)
		}
	}
	copy, value, err := state.Apply(ThreadStateAction{Kind: "list_tasks"}, "", now, true)
	if err != nil || !reflect.DeepEqual(copy, state) || !strings.Contains(string(value), "same") {
		t.Fatal("reading mutated state", copy, string(value), err)
	}
}
