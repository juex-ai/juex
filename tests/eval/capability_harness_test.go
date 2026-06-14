package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/hooks"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/tools"
)

func TestCapabilityHarnessRunsDeterministicCases(t *testing.T) {
	cases := []CapabilityCase{
		{
			Name:   "file-search-shell",
			Prompt: "exercise file, search, and shell tools",
			Files: map[string]string{
				"input.txt": "alpha\nneedle\n",
			},
			Script: []CapabilityStep{
				func(state CapabilityState) llm.Response {
					input := filepath.Join(state.WorkDir, "input.txt")
					return llm.Response{
						Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
							{Type: llm.BlockToolUse, ToolUseID: "read_1", ToolName: "read", Input: map[string]any{"path": input, "offset": 0, "limit": 20}},
							{Type: llm.BlockToolUse, ToolUseID: "write_1", ToolName: "write", Input: map[string]any{"path": filepath.Join(state.WorkDir, "notes.md"), "content": "draft\n"}},
							{Type: llm.BlockToolUse, ToolUseID: "edit_1", ToolName: "edit", Input: map[string]any{"path": input, "old": "needle", "new": "needle-updated"}},
							{Type: llm.BlockToolUse, ToolUseID: "grep_1", ToolName: "grep", Input: map[string]any{"pattern": "needle-updated", "path": state.WorkDir}},
							{Type: llm.BlockToolUse, ToolUseID: "shell_1", ToolName: "exec_command", Input: map[string]any{"cmd": "go env GOOS", "yield_time_ms": 1000}},
						}},
						StopReason: llm.StopToolUse,
					}
				},
				func(state CapabilityState) llm.Response {
					return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "TASK COMPLETE: file-search-shell"), StopReason: llm.StopEndTurn}
				},
			},
			Assert: func(t *testing.T, result CapabilityResult) {
				t.Helper()
				assertCapabilityFile(t, result, "input.txt", "needle-updated")
				assertCapabilityFile(t, result, "notes.md", "draft\n")
				for _, toolName := range []string{"read", "write", "edit", "grep", "exec_command"} {
					if result.ToolNames[toolName] == 0 {
						t.Fatalf("tool %q was not used; metrics=%+v", toolName, result.ToolNames)
					}
				}
				if result.ToolCalls != 5 || result.ErrorToolCalls != 0 {
					t.Fatalf("tool metrics = calls:%d errors:%d", result.ToolCalls, result.ErrorToolCalls)
				}
			},
		},
		{
			Name:   "permission-denial-recovery",
			Prompt: "recover from a protected path denial",
			ExtraTools: []tools.Tool{{
				Name:        "guarded_read",
				Description: "eval-only permission denial tool",
				Schema: map[string]any{
					"type":       "object",
					"properties": map[string]any{"path": map[string]any{"type": "string"}},
					"required":   []string{"path"},
				},
				Handler: func(ctx context.Context, in map[string]any) (string, error) {
					path, _ := in["path"].(string)
					if strings.Contains(path, "protected") {
						root := filepath.Dir(filepath.Dir(path))
						if _, err := os.Stat(filepath.Join(root, "policy_override.txt")); errors.Is(err, os.ErrNotExist) {
							return "permission denied by eval policy", errors.New("permission denied: protected path")
						}
					}
					return "allowed by eval policy", nil
				},
			}},
			Script: []CapabilityStep{
				func(state CapabilityState) llm.Response {
					return llm.Response{
						Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
							{Type: llm.BlockToolUse, ToolUseID: "deny_1", ToolName: "guarded_read", Input: map[string]any{"path": filepath.Join(state.WorkDir, "protected", "secret.txt")}},
						}},
						StopReason: llm.StopToolUse,
					}
				},
				func(state CapabilityState) llm.Response {
					return llm.Response{
						Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
							{Type: llm.BlockToolUse, ToolUseID: "write_allowed", ToolName: "write", Input: map[string]any{"path": filepath.Join(state.WorkDir, "allowed.txt"), "content": "recovered\n"}},
							{Type: llm.BlockToolUse, ToolUseID: "write_policy", ToolName: "write", Input: map[string]any{"path": filepath.Join(state.WorkDir, "policy_override.txt"), "content": "approved\n"}},
						}},
						StopReason: llm.StopToolUse,
					}
				},
				func(state CapabilityState) llm.Response {
					return llm.Response{
						Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
							{Type: llm.BlockToolUse, ToolUseID: "deny_retry", ToolName: "guarded_read", Input: map[string]any{"path": filepath.Join(state.WorkDir, "protected", "secret.txt")}},
						}},
						StopReason: llm.StopToolUse,
					}
				},
				func(state CapabilityState) llm.Response {
					return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "TASK COMPLETE: recovered from denial"), StopReason: llm.StopEndTurn}
				},
			},
			Assert: func(t *testing.T, result CapabilityResult) {
				t.Helper()
				assertCapabilityFile(t, result, "allowed.txt", "recovered\n")
				if result.ErrorToolCalls != 1 || result.Events["tool.errored"] == 0 {
					t.Fatalf("denial metrics missing: errors=%d events=%v", result.ErrorToolCalls, result.Events)
				}
				if !strings.Contains(result.TranscriptText, "permission denied") {
					t.Fatalf("transcript missing denial text:\n%s", result.TranscriptText)
				}
			},
		},
		{
			Name:   "hook-injection-and-stop-gate",
			Prompt: "exercise hook context and stop continuation",
			Hooks: func(workDir string) hooks.Config {
				statePath := filepath.Join(workDir, "hook-state.json")
				return hooks.Config{Commands: []hooks.CommandHook{
					{
						Name:    "inject-context",
						Events:  []hooks.EventName{hooks.EventUserPromptSubmit},
						Command: []string{os.Args[0], "capability-hook", "inject"},
					},
					{
						Name:    "stop-once",
						Events:  []hooks.EventName{hooks.EventStop},
						Command: []string{os.Args[0], "capability-hook", "stop-once", statePath},
					},
				}}
			},
			Script: []CapabilityStep{
				func(state CapabilityState) llm.Response {
					if !strings.Contains(messagesText(state.History), "hook context token") {
						return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "missing hook context"), StopReason: llm.StopEndTurn}
					}
					return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "done too early"), StopReason: llm.StopEndTurn}
				},
				func(state CapabilityState) llm.Response {
					if !strings.Contains(messagesText(state.History), "continue after hook gate") {
						return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "missing continuation"), StopReason: llm.StopEndTurn}
					}
					return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "TASK COMPLETE: hook gate passed"), StopReason: llm.StopEndTurn}
				},
			},
			Assert: func(t *testing.T, result CapabilityResult) {
				t.Helper()
				if result.ProviderCalls != 2 {
					t.Fatalf("provider calls = %d, want hook-gated continuation", result.ProviderCalls)
				}
				if result.Events["hook.started"] == 0 || result.Events["hook.completed"] == 0 {
					t.Fatalf("hook events missing: %+v", result.Events)
				}
				if !strings.Contains(result.TranscriptText, "TASK COMPLETE: hook gate passed") {
					t.Fatalf("final transcript missing hook success:\n%s", result.TranscriptText)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			result := RunCapabilityCase(t, tc)
			if !result.Success {
				t.Fatalf("case failed: %+v", result)
			}
			if result.ContextBytes == 0 || result.Elapsed <= 0 {
				t.Fatalf("metrics incomplete: context_bytes=%d elapsed=%s", result.ContextBytes, result.Elapsed)
			}
			tc.Assert(t, result)
		})
	}
}

func TestCapabilityHarnessReportShapeIsStable(t *testing.T) {
	result := RunCapabilityCase(t, CapabilityCase{
		Name:   "report-shape",
		Prompt: "return final text",
		Script: []CapabilityStep{
			func(state CapabilityState) llm.Response {
				return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "TASK COMPLETE: report shape"), StopReason: llm.StopEndTurn}
			},
		},
	})
	report := result.Report()
	for _, want := range []string{
		`"name"`,
		`"success"`,
		`"provider_calls"`,
		`"tool_calls"`,
		`"error_tool_calls"`,
		`"context_bytes"`,
		`"tool_bytes"`,
		`"elapsed_ms"`,
		`"events"`,
		`"tool_names"`,
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %s:\n%s", want, report)
		}
	}
	if strings.Contains(report, "Snapshots") || strings.Contains(report, "History") {
		t.Fatalf("report leaked provider snapshots:\n%s", report)
	}
}

func TestMain(m *testing.M) {
	if idx := capabilityHookArgIndex(os.Args); idx >= 0 {
		os.Exit(runCapabilityHookHelper(os.Args[idx+1:]))
	}
	os.Exit(m.Run())
}

func capabilityHookArgIndex(args []string) int {
	for i, arg := range args {
		if arg == "capability-hook" {
			return i
		}
	}
	return -1
}

func runCapabilityHookHelper(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "missing capability hook mode")
		return 2
	}
	switch args[0] {
	case "inject":
		writeHookOutput(map[string]any{"additional_context": "hook context token"})
	case "stop-once":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "missing stop-once state path")
			return 2
		}
		statePath := args[1]
		if _, err := os.Stat(statePath); os.IsNotExist(err) {
			if err := os.WriteFile(statePath, []byte(`{"blocked":true}`), 0o644); err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			writeHookOutput(map[string]any{"block_stop": true, "continue_prompt": "continue after hook gate"})
			return 0
		}
		writeHookOutput(map[string]any{"decision": "allow"})
	default:
		fmt.Fprintf(os.Stderr, "unknown capability hook mode %q\n", args[0])
		return 2
	}
	return 0
}

func writeHookOutput(v map[string]any) {
	if err := json.NewEncoder(os.Stdout).Encode(v); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
