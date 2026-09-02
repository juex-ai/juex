package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/modules/promptcontext"
	"github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/thread"
	"github.com/juex-ai/juex/internal/tools"
)

type spoolReadProvider struct {
	t       *testing.T
	calls   int
	readURI string
}

func (p *spoolReadProvider) Name() string { return "spool-read" }

func (p *spoolReadProvider) Complete(_ context.Context, _ string, history []llm.Message, _ []llm.ToolSpec) (llm.Response, error) {
	switch p.calls {
	case 0:
		p.calls++
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{
			Type: llm.BlockToolUse, ToolUseID: "large-result", ToolName: "large_result",
		}}}, StopReason: llm.StopToolUse}, nil
	case 1:
		p.calls++
		p.readURI = providerSpoolPath(messagesText(history))
		if !filepath.IsAbs(p.readURI) {
			p.t.Fatalf("provider spool path = %q, want an absolute read path", p.readURI)
		}
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{
			Type: llm.BlockToolUse, ToolUseID: "read-result", ToolName: "read",
			Input: map[string]any{"path": p.readURI, "limit": 1},
		}}}, StopReason: llm.StopToolUse}, nil
	case 2:
		p.calls++
		if !strings.Contains(messagesText(history), "artifact-read-success") {
			p.t.Fatalf("builtin read result missing from provider history:\n%s", messagesText(history))
		}
		return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "TASK COMPLETE: projected Artifact read"), StopReason: llm.StopEndTurn}, nil
	default:
		return llm.Response{}, fmt.Errorf("unexpected provider call %d", p.calls)
	}
}

func TestEndToEnd_ProjectedToolResultReadsThroughBuiltinSpoolPath(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "work")
	agentStateDir := filepath.Join(root, "agent")
	mediaDir := filepath.Join(agentStateDir, "media")
	for _, dir := range []string{workDir, agentStateDir} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	threadState, err := thread.New(filepath.Join(agentStateDir, "threads"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = threadState.Close() })
	bus := events.NewBus()
	threadState.SubscribeBus(bus)
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry, tools.BuiltinOptions{
		WorkDir: workDir, AgentStateDir: agentStateDir, MediaDir: threadState.SpoolDir(),
		Shell: tools.DefaultShellProfile(),
	})
	original := "artifact-read-success\n" + strings.Repeat("externalized detail\n", 20)
	registry.MustRegister(tools.Tool{
		Name: "large_result",
		Handler: func(context.Context, map[string]any) (string, error) {
			return original, nil
		},
	})
	provider := &spoolReadProvider{t: t}
	engine := &runtime.Engine{
		Provider: provider,
		Tools:    registry,
		Bus:      bus,
		Thread:   threadState,
		Prompt: e2ePromptBuilder(t, "", []string{workDir}, workDir, promptcontext.ShellProfile{}, func() time.Time {
			return time.Date(2026, 8, 24, 11, 0, 0, 0, time.UTC)
		}, threadState),
		WorkDir:  workDir,
		MediaDir: mediaDir,
		ToolOutput: runtime.ToolOutputPolicy{
			InlineMaxBytes: 32, PreviewHeadBytes: 8, PreviewTailBytes: 8,
		},
	}

	out, err := engine.Turn(context.Background(), "read the complete externalized tool result")
	if err != nil {
		t.Fatal(err)
	}
	if out != "TASK COMPLETE: projected Artifact read" || provider.calls != 3 {
		t.Fatalf("out=%q provider calls=%d", out, provider.calls)
	}
	data, err := os.ReadFile(provider.readURI)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Fatalf("stored spool payload changed: got %d bytes, want %d", len(data), len(original))
	}
}

func providerSpoolPath(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if value, ok := strings.CutPrefix(line, "path: "); ok && filepath.IsAbs(value) {
			return value
		}
	}
	return ""
}
