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
	"github.com/juex-ai/juex/internal/tools"
)

func TestTurn_BuiltinExecCommandYieldDoesNotWaitForChildPipe(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "exec_yield", ToolName: "exec_command", Input: map[string]any{
				"cmd":           "printf 'child still owns pipe\\n'; sleep 5 & wait",
				"yield_time_ms": 250,
			}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "done after yield"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngineWithToolTimeout(t, prov, true, 1)

	var completedPayload toolevents.CompletedPayload
	bus.Subscribe(toolevents.CompletedType, func(e events.Event) {
		completedPayload, _ = e.Payload.(toolevents.CompletedPayload)
	})

	start := time.Now()
	out, err := eng.Turn(context.Background(), "run shell")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if out != "done after yield" {
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
	if block.Type != llm.BlockToolResult || block.IsError {
		t.Fatalf("tool result block = %+v, want successful running result", block)
	}
	if !strings.Contains(block.Content, "child still owns pipe") ||
		!strings.Contains(block.Content, "Process running with session ID") {
		t.Fatalf("tool result content = %q, want running shell result", block.Content)
	}
	shellResult, ok := completedPayload.Result.(tools.ShellResult)
	if !ok {
		t.Fatalf("completed result = %#v, want tools.ShellResult", completedPayload.Result)
	}
	if !shellResult.Running || shellResult.SessionID <= 0 || shellResult.TimedOut || completedPayload.TimeoutSeconds != 0 {
		t.Fatalf("completed shell result = %+v timeout=%d, want running non-timeout session", shellResult, completedPayload.TimeoutSeconds)
	}
	if _, err := eng.Tools.Call(context.Background(), "write_stdin", map[string]any{
		"session_id":    shellResult.SessionID,
		"chars":         "\x03",
		"yield_time_ms": 250,
	}); err != nil {
		t.Logf("cleanup interrupt result: %v", err)
	}
}

func TestTurn_BuiltinShellErroredEventCarriesAuthoritativeContent(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "exec_fail", ToolName: "exec_command", Input: map[string]any{
				"cmd": "printf 'before failure\\n'; exit 7",
			}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "failure handled"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, true)

	var errored toolevents.ErroredPayload
	bus.Subscribe(toolevents.ErroredType, func(event events.Event) {
		payload, _ := event.Payload.(toolevents.ErroredPayload)
		if payload.ToolUseID == "exec_fail" {
			errored = payload
		}
	})

	if _, err := eng.Turn(context.Background(), "run failing shell"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errored.Content, "before failure") || !strings.Contains(errored.Content, "Process exited with code 7") {
		t.Fatalf("errored content = %q, want authoritative bounded shell result", errored.Content)
	}
	if errored.Preview != "" {
		t.Fatalf("errored preview = %q, want no duplicate shell output", errored.Preview)
	}
	result, ok := errored.Result.(tools.ShellResult)
	if !ok {
		t.Fatalf("errored result = %#v, want tools.ShellResult", errored.Result)
	}
	if result.Output != "" || result.ExitCode == nil || *result.ExitCode != 7 {
		t.Fatalf("errored result = %+v, want metadata-only exit 7", result)
	}
}
