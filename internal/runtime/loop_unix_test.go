//go:build !windows

package runtime

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/toolevents"
)

func TestTurn_BuiltinExecCommandTimeoutFinishesWhenChildKeepsPipeOpen(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "exec_timeout", ToolName: "exec_command", Input: map[string]any{
				"cmd": "printf 'child still owns pipe\\n'; sleep 5 & wait",
			}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "done too early"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngineWithToolTimeout(t, prov, true, 1)

	var erroredPayload toolevents.ErroredPayload
	bus.Subscribe(toolevents.ErroredType, func(e events.Event) {
		erroredPayload, _ = e.Payload.(toolevents.ErroredPayload)
	})

	start := time.Now()
	out, err := eng.Turn(context.Background(), "run shell")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if out != "done too early" {
		t.Fatalf("out = %q, want final answer without failure-ledger continuation", out)
	}
	if len(prov.histories) != 2 {
		t.Fatalf("provider calls = %d, want no failure-ledger continuation", len(prov.histories))
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
	if got := erroredPayload.TimedOut; got != true {
		t.Fatalf("errored timed_out = %v, want true", got)
	}
}
