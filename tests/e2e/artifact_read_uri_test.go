package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/artifact"
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/modules/promptcontext"
	"github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/session"
	"github.com/juex-ai/juex/internal/tools"
)

type artifactReadURIProvider struct {
	t       *testing.T
	calls   int
	readURI string
}

func (p *artifactReadURIProvider) Name() string { return "artifact-read-uri" }

func (p *artifactReadURIProvider) Complete(_ context.Context, _ string, history []llm.Message, _ []llm.ToolSpec) (llm.Response, error) {
	switch p.calls {
	case 0:
		p.calls++
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{
			Type: llm.BlockToolUse, ToolUseID: "large-result", ToolName: "large_result",
		}}}, StopReason: llm.StopToolUse}, nil
	case 1:
		p.calls++
		p.readURI = providerArtifactPath(messagesText(history))
		if relativePath, recognized, err := artifact.ParseReadURI(p.readURI); err != nil || !recognized || relativePath == "" {
			p.t.Fatalf("provider Artifact path = %q, parsed as (%q, %t, %v)", p.readURI, relativePath, recognized, err)
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

func TestEndToEnd_ProjectedToolResultReadsThroughBuiltinArtifactURI(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "work")
	agentStateDir := filepath.Join(root, "agent")
	artifactDir := filepath.Join(agentStateDir, "artifacts")
	for _, dir := range []string{workDir, agentStateDir} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sess, err := session.New(filepath.Join(agentStateDir, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	bus := events.NewBus()
	sess.SubscribeBus(bus)
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry, tools.BuiltinOptions{
		WorkDir: workDir, AgentStateDir: agentStateDir, ArtifactDir: artifactDir, Shell: tools.DefaultShellProfile(),
	})
	original := "artifact-read-success\n" + strings.Repeat("externalized detail\n", 20)
	registry.MustRegister(tools.Tool{
		Name: "large_result",
		Handler: func(context.Context, map[string]any) (string, error) {
			return original, nil
		},
	})
	provider := &artifactReadURIProvider{t: t}
	engine := &runtime.Engine{
		Provider: provider,
		Tools:    registry,
		Bus:      bus,
		Session:  sess,
		Prompt: e2ePromptBuilder(t, "", []string{workDir}, workDir, promptcontext.ShellProfile{}, func() time.Time {
			return time.Date(2026, 8, 24, 11, 0, 0, 0, time.UTC)
		}, sess),
		WorkDir:     workDir,
		ArtifactDir: artifactDir,
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
	relativePath, _, err := artifact.ParseReadURI(provider.readURI)
	if err != nil {
		t.Fatal(err)
	}
	store, err := artifact.NewStore(artifactDir)
	if err != nil {
		t.Fatal(err)
	}
	data, err := store.Read(artifact.Ref{Path: relativePath})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Fatalf("stored Artifact changed: got %d bytes, want %d", len(data), len(original))
	}
}

func providerArtifactPath(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if value, ok := strings.CutPrefix(line, "path: "); ok && strings.HasPrefix(value, "artifact://") {
			return value
		}
	}
	return ""
}
