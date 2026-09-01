package e2e

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/llm"
	juexruntime "github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/runtime/workmem"
	"github.com/juex-ai/juex/internal/thread"
)

type workerThreadToolProvider struct {
	childStarted chan struct{}
	releaseChild chan struct{}
	startOnce    sync.Once
}

const workerThreadE2ETimeout = 15 * time.Second

func (p *workerThreadToolProvider) Name() string { return "worker-thread-tool-e2e" }

func (p *workerThreadToolProvider) Complete(ctx context.Context, _ string, history []llm.Message, specs []llm.ToolSpec) (llm.Response, error) {
	if historyHasKind(history, llm.MessageKindWorkerThread) {
		if !historyHasToolResult(history, "finish-goal") {
			return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{
				Type:      llm.BlockToolUse,
				ToolUseID: "finish-goal",
				ToolName:  juexruntime.GoalToolUpdate,
				Input: map[string]any{
					"status":        string(workmem.GoalStatusSuccess),
					"status_reason": "subscribed worker result received",
				},
			}}}, StopReason: llm.StopToolUse}, nil
		}
		return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "PRIMARY_SAW_SIDE_OK"), StopReason: llm.StopEndTurn}, nil
	}
	last := lastDirectUserText(history)
	if last == "Reply with exactly WORKER_OK" {
		p.startOnce.Do(func() { close(p.childStarted) })
		select {
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		case <-p.releaseChild:
			return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "WORKER_OK"), StopReason: llm.StopEndTurn}, nil
		}
	}
	if last != "delegate through a Worker Thread" {
		return llm.Response{}, fmt.Errorf("unexpected provider history: %+v", history)
	}
	if !historyHasToolResult(history, "create-goal") {
		if !toolSpecExists(specs, juexruntime.GoalToolCreate) {
			return llm.Response{}, fmt.Errorf("primary tool catalog missing %s", juexruntime.GoalToolCreate)
		}
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{
			Type:      llm.BlockToolUse,
			ToolUseID: "create-goal",
			ToolName:  juexruntime.GoalToolCreate,
			Input: map[string]any{
				"description": "finish delegated work",
				"acceptance":  "the subscribed worker result is incorporated",
			},
		}}}, StopReason: llm.StopToolUse}, nil
	}
	if !historyHasToolResult(history, "create-worker") {
		if !toolSpecExists(specs, app.WorkerThreadToolCreate) {
			return llm.Response{}, fmt.Errorf("Main tool catalog missing %s", app.WorkerThreadToolCreate)
		}
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{
			Type:      llm.BlockToolUse,
			ToolUseID: "create-worker",
			ToolName:  app.WorkerThreadToolCreate,
			Input: map[string]any{
				"query":     "Reply with exactly WORKER_OK",
				"subscribe": true,
			},
		}}}, StopReason: llm.StopToolUse}, nil
	}
	return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "waiting for subscribed result"), StopReason: llm.StopEndTurn}, nil
}

func TestEndToEnd_WorkerThreadToolDelegation(t *testing.T) {
	workDir := t.TempDir()
	stateDir := filepath.Join(workDir, ".juex")
	provider := &workerThreadToolProvider{
		childStarted: make(chan struct{}),
		releaseChild: make(chan struct{}),
	}
	a, err := app.New(app.Options{
		Config: config.Config{
			ProviderID:    "openai",
			Model:         "test",
			WorkDir:       workDir,
			AgentStateDir: stateDir,
		},
		Provider:   provider,
		WorkDir:    workDir,
		DisableMCP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := a.CloseAndWait(); err != nil {
			t.Errorf("close app: %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), workerThreadE2ETimeout)
	defer cancel()
	out, err := a.Run(ctx, "delegate through a Worker Thread")
	if err != nil {
		t.Fatal(err)
	}
	if out != "waiting for subscribed result" {
		t.Fatalf("initial primary output = %q", out)
	}
	select {
	case <-provider.childStarted:
	case <-time.After(workerThreadE2ETimeout):
		t.Fatal("Worker Thread did not start")
	}
	goalState, _ := juexruntime.ThreadStateStoresFromModules(a.Engine.ThreadRuntimeSnapshot().Modules)
	if goalState == nil {
		t.Fatal("active Goal Module did not provide a store")
	}
	goal, err := goalState.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if goal.Status != workmem.GoalStatusInProgress || goal.ContinuationCount != 0 {
		t.Fatalf("waiting Goal = %+v", goal)
	}
	close(provider.releaseChild)
	deadline := time.Now().Add(workerThreadE2ETimeout)
	for time.Now().Before(deadline) {
		_, history, ok := a.ThreadSnapshot()
		if ok && historyHasKind(history, llm.MessageKindWorkerThread) && historyHasAssistantText(history, "PRIMARY_SAW_SIDE_OK") {
			for _, message := range history {
				if message.Kind == llm.MessageKindContinuation {
					t.Fatalf("unexpected Goal continuation in history: %+v", message)
				}
			}
			goal, err := goalState.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			if goal.Status != workmem.GoalStatusSuccess || goal.ContinuationCount != 0 {
				t.Fatalf("completed Goal = %+v", goal)
			}
			infos, err := thread.NewStore(stateDir).List()
			if err != nil {
				t.Fatal(err)
			}
			workerCount := 0
			for _, info := range infos {
				if info.ThreadID != thread.MainID {
					workerCount++
				}
			}
			if workerCount != 1 {
				t.Fatalf("Worker count = %d, want 1; infos=%+v", workerCount, infos)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, history, _ := a.ThreadSnapshot()
	t.Fatalf("Main did not receive and process Worker result: %+v", history)
}

func toolSpecExists(specs []llm.ToolSpec, name string) bool {
	for _, spec := range specs {
		if spec.Name == name {
			return true
		}
	}
	return false
}

func historyHasKind(history []llm.Message, kind string) bool {
	for _, message := range history {
		if message.Kind == kind {
			return true
		}
	}
	return false
}

func historyHasToolResult(history []llm.Message, id string) bool {
	for _, message := range history {
		for _, block := range message.Blocks {
			if block.Type == llm.BlockToolResult && block.ToolUseID == id {
				return true
			}
		}
	}
	return false
}

func lastDirectUserText(history []llm.Message) string {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == llm.RoleUser && history[i].Kind == llm.MessageKindDirect {
			return history[i].FirstText()
		}
	}
	return ""
}

func historyHasAssistantText(history []llm.Message, text string) bool {
	for _, message := range history {
		if message.Role == llm.RoleAssistant && strings.Contains(message.FirstText(), text) {
			return true
		}
	}
	return false
}
