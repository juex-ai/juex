package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/app/eventcatalog"
	"github.com/juex-ai/juex/internal/app/modulecatalog"
	workerthreadsmodule "github.com/juex-ai/juex/internal/features/workerthreads"

	notesmodule "github.com/juex-ai/juex/internal/features/notes"
	"github.com/juex-ai/juex/internal/foundation/cancellation"
	"github.com/juex-ai/juex/internal/foundation/command"
	"github.com/juex-ai/juex/internal/foundation/events"
	"github.com/juex-ai/juex/internal/foundation/llm"

	toolcore "github.com/juex-ai/juex/internal/foundation/tools"

	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
	"github.com/juex-ai/juex/internal/framework/runtime"
	"github.com/juex-ai/juex/internal/framework/thread"
	"github.com/juex-ai/juex/tests/testsupport/modulestate"
)

type executionPolicyModule struct{ tools []toolcore.Tool }

func (*executionPolicyModule) ID() runtimemodule.ID { return "execution-policy-fixture" }

func (m *executionPolicyModule) Tools(context.Context, runtimemodule.ToolContext) ([]toolcore.Tool, error) {
	return m.tools, nil
}

func TestEndToEnd_ModuleExecutionPolicyPersistsOrderedOutcomes(t *testing.T) {
	for _, cancelBatch := range []bool{false, true} {
		name := "failure-continues"
		if cancelBatch {
			name = "cancel-queued"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			state, err := thread.New(filepath.Join(root, "threads"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = state.Close() })
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			firstStarted := make(chan struct{})
			parallelStarted := make(chan struct{})
			var firstFinished, secondRan atomic.Bool
			mod := &executionPolicyModule{tools: []toolcore.Tool{
				(toolcore.ToolDefinition{Name: "state_write", Group: "fixture-state", ExecutionPolicy: toolcore.ToolExecutionSerial}).BindResult(func(ctx context.Context, _ map[string]any) (toolcore.Result, error) {
					close(firstStarted)
					select {
					case <-parallelStarted:
					case <-ctx.Done():
						return toolcore.Result{}, ctx.Err()
					}
					firstFinished.Store(true)
					return toolcore.Result{Text: "partial result", Fact: json.RawMessage(`{"attempted":true}`)}, errors.New("fixture failure")
				}),
				(toolcore.ToolDefinition{Name: "state_read", Group: toolcore.ToolGroupFile, ExecutionPolicy: toolcore.ToolExecutionSerial}).Bind(func(context.Context, map[string]any) (string, error) {
					secondRan.Store(true)
					if !firstFinished.Load() {
						return "", errors.New("read overtook write")
					}
					return "observed attempted write", nil
				}),
				(toolcore.ToolDefinition{Name: "parallel_probe", Group: "fixture-state"}).Bind(func(ctx context.Context, _ map[string]any) (string, error) {
					select {
					case <-firstStarted:
					case <-ctx.Done():
						return "", ctx.Err()
					}
					if cancelBatch {
						cancel()
					}
					close(parallelStarted)
					return "overlapped write", nil
				}),
			}}
			modules := runtimemodule.NewRegistry()
			if err := modules.Register(mod); err != nil {
				t.Fatal(err)
			}
			set, err := modules.Seal(ctx, runtimemodule.ToolContext{})
			if err != nil {
				t.Fatal(err)
			}
			registry, err := runtimemodule.BuildToolRegistry(toolcore.RegistryOptions{}, set)
			if err != nil {
				t.Fatal(err)
			}
			var calls []llm.Block
			for _, tool := range mod.tools {
				calls = append(calls, llm.Block{Type: llm.BlockToolUse, ToolUseID: tool.Name, ToolName: tool.Name, Input: map[string]any{}})
			}
			provider := &bareScriptProvider{steps: []llm.Response{
				{Message: llm.Message{Role: llm.RoleAssistant, Blocks: calls}, StopReason: llm.StopToolUse},
				{Message: llm.TextMessage(llm.RoleAssistant, "completed batch"), StopReason: llm.StopEndTurn},
			}}
			bus := events.NewBus()
			sink := events.NewDurableSink(state)
			sink.SetCatalog(eventcatalog.Default())
			bus.SetCommitter(sink)
			defer func() { _ = sink.Close() }()
			engine := &runtime.Engine{Provider: provider, Tools: registry, RuntimeModules: set, Thread: state, Bus: bus, WorkDir: root, MediaDir: filepath.Join(root, "media"), Prompt: e2ePromptBuilder(t, "", []string{root}, root, command.ShellProfile{}, time.Now, state)}
			_, err = engine.Turn(ctx, "Execute all three calls in one batch.")
			if cancelBatch && !cancellation.IsUserCancelled(err) {
				t.Fatalf("canceled Turn=%v", err)
			}
			if !cancelBatch && err != nil {
				t.Fatal(err)
			}
			if secondRan.Load() == cancelBatch {
				t.Fatalf("queued handler ran=%v with cancellation=%v", secondRan.Load(), cancelBatch)
			}
			if err := sink.Close(); err != nil {
				t.Fatal(err)
			}
			if err := state.Close(); err != nil {
				t.Fatal(err)
			}
			reloaded, err := thread.Load(state.Dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = reloaded.Close() }()
			var results []llm.Block
			for _, message := range reloaded.History {
				for _, block := range message.Blocks {
					if block.Type == llm.BlockToolResult {
						results = append(results, block)
					}
				}
			}
			if len(results) != len(calls) {
				t.Fatalf("persisted %d results, want %d", len(results), len(calls))
			}
			for i, result := range results {
				wantError := cancelBatch || i == 0
				if result.ToolUseID != calls[i].ToolUseID || result.IsError != wantError || (result.ResultFact == nil || result.ResultFact.Owner != string(mod.ID())) {
					t.Fatalf("persisted result %d=%+v", i, result)
				}
			}
			if !cancelBatch && (results[1].Content != "observed attempted write" || string(results[0].ResultFact.Data) != `{"attempted":true}`) {
				t.Fatalf("lost execution or failure fact: %+v", results)
			}
		})
	}
}

type executionWorkerProvider struct {
	started chan string
	release chan struct{}
}

func (*executionWorkerProvider) Name() string { return "execution-workers" }

func (p *executionWorkerProvider) Complete(ctx context.Context, _ string, history []llm.Message, _ []llm.ToolSpec) (llm.Response, error) {
	query := lastDirectUserText(history)
	if query == "launch two workers" {
		if !historyHasToolResult(history, "worker-list") {
			return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
				{Type: llm.BlockToolUse, ToolUseID: "create-one", ToolName: app.WorkerThreadToolCreate, Input: map[string]any{"query": "worker-one", "alias": "one"}},
				{Type: llm.BlockToolUse, ToolUseID: "create-two", ToolName: app.WorkerThreadToolCreate, Input: map[string]any{"query": "worker-two", "alias": "two"}},
				{Type: llm.BlockToolUse, ToolUseID: "worker-list", ToolName: app.WorkerThreadToolList, Input: map[string]any{}},
			}}, StopReason: llm.StopToolUse}, nil
		}
		return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "workers started"), StopReason: llm.StopEndTurn}, nil
	}
	if !historyHasToolResult(history, "notes-second") {
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "notes-first", ToolName: notesmodule.ToolUpdate, Input: map[string]any{"content": query + " first"}},
			{Type: llm.BlockToolUse, ToolUseID: "notes-second", ToolName: notesmodule.ToolUpdate, Input: map[string]any{"content": query + " second"}},
		}}, StopReason: llm.StopToolUse}, nil
	}
	p.started <- query
	select {
	case <-p.release:
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	}
	return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "worker finished"), StopReason: llm.StopEndTurn}, nil
}

func TestEndToEnd_WorkerBatchesKeepIndependentStateAndProgress(t *testing.T) {
	isolateModuleConfig(t)
	provider := &executionWorkerProvider{started: make(chan string, 2), release: make(chan struct{})}
	a, err := app.New(app.Options{Config: config.Config{ModuleInventory: modulecatalog.Inventory(), ProviderID: "openai", Model: "test", Preset: config.PresetMinimal, WorkDir: t.TempDir(), AgentStateDir: t.TempDir(), Modules: config.ModulePolicy{workerthreadsmodule.ModuleID: {Enabled: true}, notesmodule.ModuleID: {Enabled: true}}}, Provider: provider, DisableMCP: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := a.CloseAndWait(); err != nil {
			t.Error(err)
		}
	})
	defer close(provider.release)
	ctx, cancel := context.WithTimeout(t.Context(), workerThreadE2ETimeout)
	defer cancel()
	if out, err := a.Run(ctx, "launch two workers"); err != nil || out != "workers started" {
		t.Fatalf("Main Turn=%q, %v", out, err)
	}
	seen := map[string]bool{}
	for range 2 {
		select {
		case query := <-provider.started:
			seen[query] = true
		case <-ctx.Done():
			t.Fatal("two Workers did not independently reach their second provider call")
		}
	}
	if !seen["worker-one"] || !seen["worker-two"] {
		t.Fatalf("started Workers=%v", seen)
	}
	var list string
	for _, message := range a.Thread.History {
		for _, block := range message.Blocks {
			if block.Type == llm.BlockToolResult && block.ToolUseID == "worker-list" {
				list = block.Content
			}
		}
	}
	var listed struct {
		Threads []app.WorkerThreadStatus `json:"threads"`
	}
	if err := json.Unmarshal([]byte(list), &listed); err != nil || len(listed.Threads) != 2 {
		t.Fatalf("ordered create/create/list=%s, %v", list, err)
	}
	for _, status := range listed.Threads {
		worker, ok := a.ManagedWorkerApp(status.ThreadID)
		if !ok {
			t.Fatalf("missing Worker %s", status.ThreadID)
		}
		_, notes := modulestate.Stores(worker.Engine.ThreadRuntimeSnapshot().Modules)
		snapshot, err := notes.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Content != "worker-"+status.Alias+" second" {
			t.Fatalf("Worker %s Notes=%q", status.Alias, snapshot.Content)
		}
	}
}
