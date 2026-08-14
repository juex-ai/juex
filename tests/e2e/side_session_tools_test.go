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
	"github.com/juex-ai/juex/internal/session"
)

type sideSessionToolProvider struct {
	childAnswered chan struct{}
	answerOnce    sync.Once
}

func (p *sideSessionToolProvider) Name() string { return "side-session-tool-e2e" }

func (p *sideSessionToolProvider) Complete(ctx context.Context, _ string, history []llm.Message, specs []llm.ToolSpec) (llm.Response, error) {
	if historyHasKind(history, llm.MessageKindSideSession) {
		return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "PRIMARY_SAW_SIDE_OK"), StopReason: llm.StopEndTurn}, nil
	}
	last := lastDirectUserText(history)
	if last == "Reply with exactly SIDE_OK" {
		p.answerOnce.Do(func() { close(p.childAnswered) })
		return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "SIDE_OK"), StopReason: llm.StopEndTurn}, nil
	}
	if last != "delegate through a Side Session" {
		return llm.Response{}, fmt.Errorf("unexpected provider history: %+v", history)
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
	select {
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	case <-p.childAnswered:
		return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "waiting for subscribed result"), StopReason: llm.StopEndTurn}, nil
	}
}

func TestEndToEnd_SideSessionToolDelegation(t *testing.T) {
	workDir := t.TempDir()
	stateDir := filepath.Join(workDir, ".juex")
	provider := &sideSessionToolProvider{childAnswered: make(chan struct{})}
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

	if _, err := a.Run(context.Background(), "delegate through a Side Session"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, history, ok := a.SessionSnapshot()
		if ok && historyHasKind(history, llm.MessageKindSideSession) && historyHasAssistantText(history, "PRIMARY_SAW_SIDE_OK") {
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
