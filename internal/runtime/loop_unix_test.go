//go:build !windows

package runtime

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
)

func TestTurn_BuiltinShellTimeoutContinuesWhenChildKeepsPipeOpen(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "shell_timeout", ToolName: "shell", Input: map[string]any{
				"cmd":     "printf 'child still owns pipe\\n'; sleep 5 & wait",
				"timeout": 1,
			}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "recovered"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, true)
	eng.MaxDur = 3 * time.Second

	var erroredPayload map[string]any
	bus.Subscribe("tool.errored", func(e events.Event) {
		erroredPayload, _ = e.Payload.(map[string]any)
	})

	start := time.Now()
	out, err := eng.Turn(context.Background(), "run shell")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if out != "recovered" {
		t.Fatalf("out = %q, want recovered", out)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("turn waited for child process to exit: %s", elapsed)
	}
	result := eng.Session.History[2]
	if result.Role != llm.RoleUser || len(result.Blocks) != 1 {
		t.Fatalf("tool result message wrong: %+v", result)
	}
	block := result.Blocks[0]
	if block.Type != llm.BlockToolResult || !block.IsError {
		t.Fatalf("tool result block = %+v, want error result", block)
	}
	if !strings.Contains(block.Content, "timed out after 1s") {
		t.Fatalf("tool result content = %q, want timeout detail", block.Content)
	}
	if got := erroredPayload["timed_out"]; got != true {
		t.Fatalf("errored timed_out = %v, want true", got)
	}
}
