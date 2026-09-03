//go:build !windows

package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/hooks"
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
	result := eng.Thread.History[2]
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
		t.Fatalf("completed shell result = %+v timeout=%d, want running non-timeout thread", shellResult, completedPayload.TimeoutSeconds)
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
				"cmd": "awk 'BEGIN { printf \"HEAD-FAILURE-SENTINEL\\n\"; for (i = 0; i < 1100000; i++) printf \"x\"; printf \"\\nTAIL-FAILURE-SENTINEL\\n\"; exit 7 }'",
			}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "failure handled"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, true)
	eng.ContextWindow = 1 << 30
	eng.ToolOutput = ToolOutputPolicy{InlineMaxBytes: 4 << 20}

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
	if errored.Outcome == nil {
		t.Fatal("errored event is missing recorded outcome")
	}
	for _, want := range []string{"HEAD-FAILURE-SENTINEL", "TAIL-FAILURE-SENTINEL", "[output truncated:", "Process exited with code 7"} {
		if !strings.Contains(errored.Outcome.Block.Content, want) {
			t.Fatalf("errored content missing %q", want)
		}
	}
	if strings.Contains(errored.Outcome.Block.Content, "remaining output truncated") {
		t.Fatalf("errored content was truncated a second time")
	}
	if len(eng.Thread.History) < 3 || len(eng.Thread.History[2].Blocks) != 1 {
		t.Fatalf("thread history missing shell result: %+v", eng.Thread.History)
	}
	conversation := eng.Thread.History[2].Blocks[0].Content
	rawBytes := len("HEAD-FAILURE-SENTINEL\n") + 1100000 + len("\nTAIL-FAILURE-SENTINEL\n")
	wantMarker := fmt.Sprintf("[output truncated: %d bytes omitted]\n", rawBytes-(1<<20))
	if strings.Count(conversation, wantMarker) != 1 || strings.Count(errored.Outcome.Block.Content, wantMarker) != 1 {
		t.Fatalf("exact raw-output marker missing: want %q", wantMarker)
	}
	for _, want := range []string{"HEAD-FAILURE-SENTINEL", "TAIL-FAILURE-SENTINEL", "[output truncated:"} {
		if !strings.Contains(conversation, want) {
			t.Fatalf("conversation shell result missing %q", want)
		}
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

func TestTurn_BuiltinShellCompletedEventUsesFinalizedHookContent(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "exec_hook_context", ToolName: "exec_command", Input: map[string]any{
				"cmd": "printf 'shell output'",
			}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "hook handled"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, true)
	installHookRunner(t, eng, &fakeHookRunner{responses: map[hooks.EventName][]fakeHookResponse{
		hooks.EventPostToolUse: {{ExitCode: 2, Stdout: "redaction required"}},
	}})

	var completed toolevents.CompletedPayload
	bus.Subscribe(toolevents.CompletedType, func(event events.Event) {
		payload, _ := event.Payload.(toolevents.CompletedPayload)
		if payload.ToolUseID == "exec_hook_context" {
			completed = payload
		}
	})

	if _, err := eng.Turn(context.Background(), "run shell with policy context"); err != nil {
		t.Fatal(err)
	}
	conversation := eng.Thread.History[2].Blocks[0].Content
	for _, want := range []string{"shell output", "redaction required"} {
		if !strings.Contains(conversation, want) {
			t.Fatalf("conversation content missing %q: %q", want, conversation)
		}
	}
	if completed.Outcome == nil || completed.Outcome.Block.Content != conversation || completed.Len != len(conversation) {
		t.Fatalf("completed event = %+v, want finalized conversation %q", completed, conversation)
	}
	if completed.Preview != "" {
		t.Fatalf("completed preview = %q, want no duplicate shell output", completed.Preview)
	}
}

func TestTurn_BuiltinShellErroredEventUsesFinalizedHookErrorContent(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "exec_hook_error", ToolName: "exec_command", Input: map[string]any{
				"cmd": "printf 'shell output'",
			}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "hook failure handled"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, true)
	installHookRunner(t, eng, &fakeHookRunner{errors: map[hooks.EventName]error{
		hooks.EventPostToolUse: errors.New("post hook failed"),
	}})

	var errored toolevents.ErroredPayload
	bus.Subscribe(toolevents.ErroredType, func(event events.Event) {
		payload, _ := event.Payload.(toolevents.ErroredPayload)
		if payload.ToolUseID == "exec_hook_error" {
			errored = payload
		}
	})

	if _, err := eng.Turn(context.Background(), "run shell with failing hook"); err != nil {
		t.Fatal(err)
	}
	conversation := eng.Thread.History[2].Blocks[0].Content
	for _, want := range []string{"shell output", "post hook failed"} {
		if !strings.Contains(conversation, want) {
			t.Fatalf("conversation content missing %q: %q", want, conversation)
		}
	}
	if errored.Outcome == nil || errored.Outcome.Block.Content != conversation || errored.Len != len(conversation) {
		t.Fatalf("errored event = %+v, want finalized conversation %q", errored, conversation)
	}
	if errored.Preview != "" {
		t.Fatalf("errored preview = %q, want no duplicate shell output", errored.Preview)
	}
}

func TestTurn_BuiltinShellFinalContentBoundsMultipleEscapedHooksAndReplays(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "exec_large_hooks", ToolName: "exec_command", Input: map[string]any{
				"cmd": "awk 'BEGIN { for (i = 0; i < 1100000; i++) printf \"<\" }'",
			}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "large hooks handled"), StopReason: llm.StopEndTurn},
	}}
	eng, _ := newEngine(t, prov, true)
	eng.ContextWindow = 1 << 30
	eng.ToolOutput = ToolOutputPolicy{InlineMaxBytes: 4 << 20}
	installHookRunner(t, eng, hookRunnerFunc(func(_ context.Context, request hooks.Request) ([]hooks.Result, error) {
		if request.EventName != hooks.EventPostToolUse {
			return nil, nil
		}
		results := make([]hooks.Result, 4)
		for index := range results {
			results[index] = hooks.Result{
				Hook:      hooks.CommandHook{Name: "large", Events: []hooks.EventName{hooks.EventPostToolUse}},
				EventName: hooks.EventPostToolUse,
				ToolName:  "exec_command",
				ExitCode:  2,
				Stdout:    strings.Repeat("<", 384<<10),
			}
		}
		return results, nil
	}))

	if _, err := eng.Turn(context.Background(), "run large shell and hooks"); err != nil {
		t.Fatal(err)
	}
	conversation := eng.Thread.History[2].Blocks[0].Content
	if len(conversation) > (1<<20)+maxShellPolicyContent+1024 {
		t.Fatalf("finalized shell content bytes = %d, want hard bound", len(conversation))
	}
	if !strings.Contains(conversation, "[output truncated:") || !strings.HasSuffix(conversation, "<") {
		t.Fatalf("finalized shell content lost marker/tail: len=%d tail=%q", len(conversation), conversation[len(conversation)-64:])
	}

	journal, err := eng.Thread.ReadEvents()
	if err != nil {
		t.Fatal(err)
	}
	terminalIndex := -1
	completionIndex := -1
	for index, event := range journal {
		switch event.Type {
		case toolevents.CompletedType:
			payload, _ := event.Payload.(map[string]any)
			if payload["tool_use_id"] == "exec_large_hooks" {
				terminalIndex = index
				outcome, _ := payload["outcome"].(map[string]any)
				block, _ := outcome["block"].(map[string]any)
				if block["content"] != conversation {
					t.Fatalf("replayed terminal content does not match finalized conversation")
				}
			}
		case "turn.completed":
			completionIndex = index
		}
	}
	if terminalIndex < 0 || completionIndex <= terminalIndex {
		t.Fatalf("journal lost terminal or later completion: terminal=%d completion=%d", terminalIndex, completionIndex)
	}
}

func TestTurn_BuiltinShellBoundsEscapedHookErrorDiagnosticsAndReplays(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "exec_large_hook_error", ToolName: "exec_command", Input: map[string]any{
				"cmd": "awk 'BEGIN { printf \"HEAD-HOOK-ERROR-SENTINEL\\n\"; for (i = 0; i < 1100000; i++) printf \"x\"; printf \"\\nTAIL-HOOK-ERROR-SENTINEL\\n\" }'",
			}},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "large hook failure handled"), StopReason: llm.StopEndTurn},
	}}
	eng, bus := newEngine(t, prov, true)
	eng.ContextWindow = 1 << 30
	eng.ToolOutput = ToolOutputPolicy{InlineMaxBytes: 4 << 20}
	installHookRunner(t, eng, &fakeHookRunner{errors: map[hooks.EventName]error{
		hooks.EventPostToolUse: errors.New("post hook failed: " + strings.Repeat("<", 2<<20)),
	}})

	var errored toolevents.ErroredPayload
	bus.Subscribe(toolevents.ErroredType, func(event events.Event) {
		payload, _ := event.Payload.(toolevents.ErroredPayload)
		if payload.ToolUseID == "exec_large_hook_error" {
			errored = payload
		}
	})

	if _, err := eng.Turn(context.Background(), "run shell with large failing hook"); err != nil {
		t.Fatal(err)
	}
	if len(errored.Error) > maxShellEventDiagnostic+64 || !strings.Contains(errored.Error, "truncated") {
		t.Fatalf("errored diagnostic was not bounded: bytes=%d", len(errored.Error))
	}
	conversation := eng.Thread.History[2].Blocks[0].Content
	rawBytes := len("HEAD-HOOK-ERROR-SENTINEL\n") + 1100000 + len("\nTAIL-HOOK-ERROR-SENTINEL\n")
	wantMarker := fmt.Sprintf("[output truncated: %d bytes omitted]\n", rawBytes-(1<<20))
	if errored.Outcome == nil || errored.Outcome.Block.Content != conversation || !strings.Contains(conversation, "HEAD-HOOK-ERROR-SENTINEL") ||
		!strings.Contains(conversation, "TAIL-HOOK-ERROR-SENTINEL") || strings.Count(conversation, wantMarker) != 1 {
		t.Fatalf("bounded terminal content does not match conversation: bytes=%d", len(conversation))
	}
	journal, err := eng.Thread.ReadEvents()
	if err != nil {
		t.Fatal(err)
	}
	terminalIndex := -1
	completionIndex := -1
	for index, event := range journal {
		switch event.Type {
		case toolevents.ErroredType:
			payload, _ := event.Payload.(map[string]any)
			if payload["tool_use_id"] == "exec_large_hook_error" {
				terminalIndex = index
				outcome, _ := payload["outcome"].(map[string]any)
				block, _ := outcome["block"].(map[string]any)
				if block["content"] != conversation {
					t.Fatalf("replayed errored content does not match conversation")
				}
			}
		case "turn.completed":
			completionIndex = index
		}
	}
	if terminalIndex < 0 || completionIndex <= terminalIndex {
		t.Fatalf("journal lost errored terminal or later completion: terminal=%d completion=%d", terminalIndex, completionIndex)
	}
}
