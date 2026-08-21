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
	"github.com/juex-ai/juex/internal/session"
)

type sideSessionToolProvider struct {
	childStarted chan struct{}
	releaseChild chan struct{}
	startOnce    sync.Once
}

const sideSessionE2ETimeout = 15 * time.Second

func (p *sideSessionToolProvider) Name() string { return "side-session-tool-e2e" }

func (p *sideSessionToolProvider) Complete(ctx context.Context, _ string, history []llm.Message, specs []llm.ToolSpec) (llm.Response, error) {
	if historyHasKind(history, llm.MessageKindSideSession) {
		if !historyHasToolResult(history, "finish-goal") {
			return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{
				Type:      llm.BlockToolUse,
				ToolUseID: "finish-goal",
				ToolName:  juexruntime.GoalToolUpdate,
				Input: map[string]any{
					"status":        string(juexruntime.GoalStatusSuccess),
					"status_reason": "subscribed worker result received",
				},
			}}}, StopReason: llm.StopToolUse}, nil
		}
		return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "PRIMARY_SAW_SIDE_OK"), StopReason: llm.StopEndTurn}, nil
	}
	last := lastDirectUserText(history)
	if last == "Reply with exactly SIDE_OK" {
		p.startOnce.Do(func() { close(p.childStarted) })
		select {
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		case <-p.releaseChild:
			return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "SIDE_OK"), StopReason: llm.StopEndTurn}, nil
		}
	}
	if last != "delegate through a Side Session" {
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
	if !historyHasToolResult(history, "create-side") {
		if !toolSpecExists(specs, app.SideSessionToolCreate) {
			return llm.Response{}, fmt.Errorf("primary tool catalog missing %s", app.SideSessionToolCreate)
		}
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{
			Type:      llm.BlockToolUse,
			ToolUseID: "create-side",
			ToolName:  app.SideSessionToolCreate,
			Input: map[string]any{
				"query": "Reply with exactly SIDE_OK",
			},
		}}}, StopReason: llm.StopToolUse}, nil
	}
	return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "waiting for subscribed result"), StopReason: llm.StopEndTurn}, nil
}

func TestEndToEnd_SideSessionToolDelegation(t *testing.T) {
	workDir := t.TempDir()
	stateDir := filepath.Join(workDir, ".juex")
	provider := &sideSessionToolProvider{
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

	ctx, cancel := context.WithTimeout(context.Background(), sideSessionE2ETimeout)
	defer cancel()
	out, err := a.Run(ctx, "delegate through a Side Session")
	if err != nil {
		t.Fatal(err)
	}
	if out != "waiting for subscribed result" {
		t.Fatalf("initial primary output = %q", out)
	}
	select {
	case <-provider.childStarted:
	case <-time.After(sideSessionE2ETimeout):
		t.Fatal("side worker did not start")
	}
	goalState, _ := juexruntime.SessionStateStoresFromModules(a.Engine.SessionRuntimeSnapshot().Modules)
	if goalState == nil {
		t.Fatal("active Goal Module did not provide a store")
	}
	goal, err := goalState.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if goal.Status != juexruntime.GoalStatusInProgress || goal.ContinuationCount != 0 {
		t.Fatalf("waiting Goal = %+v", goal)
	}
	close(provider.releaseChild)
	deadline := time.Now().Add(sideSessionE2ETimeout)
	for time.Now().Before(deadline) {
		_, history, ok := a.SessionSnapshot()
		if ok && historyHasKind(history, llm.MessageKindSideSession) && historyHasAssistantText(history, "PRIMARY_SAW_SIDE_OK") {
			for _, message := range history {
				if message.Kind == llm.MessageKindContinuation {
					t.Fatalf("unexpected Goal continuation in history: %+v", message)
				}
			}
			goal, err := goalState.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			if goal.Status != juexruntime.GoalStatusSuccess || goal.ContinuationCount != 0 {
				t.Fatalf("completed Goal = %+v", goal)
			}
			infos, err := session.List(filepath.Join(stateDir, "sessions"))
			if err != nil {
				t.Fatal(err)
			}
			sideCount := 0
			for _, info := range infos {
				if session.NormalizeKind(info.Kind) == session.KindSide {
					sideCount++
				}
			}
			if sideCount != 1 {
				t.Fatalf("side session count = %d, want 1; infos=%+v", sideCount, infos)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, history, _ := a.SessionSnapshot()
	t.Fatalf("primary did not receive and process Side Session result: %+v", history)
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
