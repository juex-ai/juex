package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/llm"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/runtime/workmem"
	"github.com/juex-ai/juex/internal/thread"
)

func TestNewContextStopsBeforeGenerationWhenModuleStateCannotClear(t *testing.T) {
	engine, _ := newEngine(t, &mockProvider{}, false)
	goal := workmem.NewGoalStateStore(engine.Thread.Dir, workmem.GoalStateOptions{})
	if _, err := goal.Create("finish the migration", "module files remain authoritative"); err != nil {
		t.Fatal(err)
	}
	goalBefore, err := os.ReadFile(goal.Path)
	if err != nil {
		t.Fatal(err)
	}
	notesPath := filepath.Join(engine.Thread.Dir, workmem.NotesFileName)
	if err := os.Mkdir(notesPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(notesPath, "block"), []byte("keep directory non-empty"), 0o600); err != nil {
		t.Fatal(err)
	}
	installThreadStateModulesWithStores(t, engine, goal, workmem.NewNotesStore(engine.Thread.Dir))

	err = engine.NewContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), `module "notes" clear context state`) {
		t.Fatalf("NewContext() error = %v", err)
	}
	goalAfter, err := os.ReadFile(goal.Path)
	if err != nil {
		t.Fatalf("read restored Goal state: %v", err)
	}
	if string(goalAfter) != string(goalBefore) {
		t.Fatalf("restored Goal state = %q, want %q", goalAfter, goalBefore)
	}
	projection := engine.Thread.Projection()
	if projection.CurrentGeneration.ID != thread.InitialGeneration || projection.Counts.GenerationCount != 1 {
		t.Fatalf("failed clear created Generation: %+v", projection.CurrentGeneration)
	}
}

func TestContextNewToolCreatesGenerationAndEndsTurn(t *testing.T) {
	provider := &mockProvider{script: []llm.Response{{
		Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{
			Type: llm.BlockToolUse, ToolUseID: "renew", ToolName: ContextToolNew, Input: map[string]any{},
		}}},
		StopReason: llm.StopToolUse,
	}}}
	engine, _ := newEngine(t, provider, false)
	module := NewContextControlModule(engine)
	installModuleTools(t, engine.Tools, module)

	output, err := engine.Turn(context.Background(), "finish this task")
	if err != nil {
		t.Fatal(err)
	}
	if output != "Context renewed." || provider.called != 1 {
		t.Fatalf("Turn() = %q, provider calls = %d", output, provider.called)
	}
	state := engine.Thread.ReplaySnapshot()
	if state.Projection.CurrentGeneration.ID != "g000002" || state.Projection.Counts.GenerationCount != 2 {
		t.Fatalf("generation = %+v", state.Projection.CurrentGeneration)
	}
	if len(state.ProviderMessages) != 0 || len(state.Activities) != 1 || state.Activities[0].Type != "context.renewed" {
		t.Fatalf("renewed replay = %+v", state)
	}
	if state.Projection.Counts.TurnCount != 1 || state.Projection.ExecutionState != thread.ExecutionIdle {
		t.Fatalf("projection = %+v", state.Projection)
	}
}

func TestContextCompactToolRunsBetweenProviderIterations(t *testing.T) {
	provider := &mockProvider{script: []llm.Response{
		{
			Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{
				Type: llm.BlockToolUse, ToolUseID: "compact", ToolName: ContextToolCompact,
				Input: map[string]any{"instructions": "retain the implementation state"},
			}}},
			StopReason: llm.StopToolUse,
		},
		{Message: llm.TextMessage(llm.RoleAssistant, "compaction summary"), StopReason: llm.StopEndTurn},
		{Message: llm.TextMessage(llm.RoleAssistant, "continued"), StopReason: llm.StopEndTurn},
	}}
	engine, _ := newEngine(t, provider, false)
	engine.ContextWindow = 8192
	engine.Compaction = DefaultCompactionPolicy()
	module := NewContextControlModule(engine)
	installModuleTools(t, engine.Tools, module)

	output, err := engine.Turn(context.Background(), "continue after reducing context")
	if err != nil {
		t.Fatal(err)
	}
	if output != "continued" || provider.called != 3 {
		t.Fatalf("Turn() = %q, provider calls = %d", output, provider.called)
	}
	state := engine.Thread.ReplaySnapshot()
	if state.Projection.CurrentGeneration.ID != "g000002" || len(state.Activities) != 1 || state.Activities[0].Type != "context.compacted" {
		t.Fatalf("compacted replay = %+v", state)
	}
	if len(provider.histories) != 3 || len(provider.histories[2]) == 0 || provider.histories[2][0].Kind != llm.MessageKindCompact {
		t.Fatalf("post-compact provider history = %+v", provider.histories)
	}
}

func TestContextControlRecitationReportsWindowAndGeneration(t *testing.T) {
	engine, _ := newEngine(t, &mockProvider{}, false)
	engine.ContextWindow = 1000
	if err := engine.Thread.Append(llm.TextMessage(llm.RoleUser, strings.Repeat("context ", 40))); err != nil {
		t.Fatal(err)
	}
	engine.setContextPromptInputs("system guidance", []llm.ToolSpec{{Name: "read"}})
	sections, err := NewContextControlModule(engine).Context(context.Background(), runtimemodule.ContextRequest{
		Purpose: runtimemodule.ContextPurposeProviderIteration,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sections) != 1 || sections[0].Projection != runtimemodule.ContextProjectionRuntimeMessage {
		t.Fatalf("sections = %+v", sections)
	}
	for _, want := range []string{"1000 tokens", "Thread " + engine.Thread.ID, "Generation g000001", "context_compact", "context_new"} {
		if !strings.Contains(sections[0].Text, want) {
			t.Fatalf("recitation %q does not contain %q", sections[0].Text, want)
		}
	}
}
