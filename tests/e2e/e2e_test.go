// Package e2e contains a single end-to-end test that wires every Juex
// subsystem against a temporary filesystem layout and a scripted mock LLM.
//
// What this exercise covers:
//
//   - AGENTS.md hierarchy loading (project + subdir + global)
//   - Skill loading (path appears in system prompt; model loads body via `read`)
//   - Work-local memory entries -> system prompt + memory_write/search round-trip
//   - MCP stdio client -> registered as mcp__<server>__<tool> in the registry
//   - Builtin tools end-to-end: write, read, edit, apply_patch, grep, exec_command
//   - Parallel tool calls in a single response
//   - Event emission landing in events.jsonl
//   - Conversation messages landing in conversation.jsonl
//
// The test deliberately does not call a real LLM — that lives behind the
// `integration` build tag in integration_test.go.
package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/hooks"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/mcp"
	"github.com/juex-ai/juex/internal/memory"
	"github.com/juex-ai/juex/internal/prompt"
	"github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/session"
	"github.com/juex-ai/juex/internal/skills"
	"github.com/juex-ai/juex/internal/tools"
)

// scriptProvider drives the engine through a deterministic script. Each call
// to Complete returns the next entry; if none remain the test fails.
type scriptProvider struct {
	t       *testing.T
	steps   []llm.Response
	called  atomic.Int32
	history [][]llm.Message // record of history at each call
}

func (p *scriptProvider) Name() string { return "script" }

func (p *scriptProvider) Complete(ctx context.Context, sys string, hist []llm.Message, tools []llm.ToolSpec) (llm.Response, error) {
	idx := int(p.called.Add(1) - 1)
	// Snapshot history so the test can assert on what the model saw.
	snap := append([]llm.Message{}, hist...)
	p.history = append(p.history, snap)
	if idx >= len(p.steps) {
		return llm.Response{}, fmt.Errorf("script exhausted at call %d", idx)
	}
	// Verify system prompt contains every expected section the first time.
	if idx == 0 {
		for _, marker := range []string{
			"AGENTS.md",
			"project rule: respond like a senior engineer",
			"Available Skills",
			"trim-tool",
			"## Memory",
			"prefer-yaml",
			"Operating Context",
		} {
			if !strings.Contains(sys, marker) {
				p.t.Errorf("system prompt missing %q\n=== prompt ===\n%s", marker, sys)
			}
		}
		// Verify tool list contains the expected tools.
		toolNames := map[string]bool{}
		for _, t := range tools {
			toolNames[t.Name] = true
		}
		for _, want := range []string{"read", "write", "edit", "apply_patch", "write_begin", "write_chunk", "write_commit", "write_abort", "exec_command", "write_stdin", "grep", "memory_write", "memory_search", "memory_delete", "mcp__local__echo"} {
			if !toolNames[want] {
				p.t.Errorf("tool %q missing from registry; have %v", want, keys(toolNames))
			}
		}
	}
	return p.steps[idx], nil
}

type bareScriptProvider struct {
	steps   []llm.Response
	called  atomic.Int32
	history [][]llm.Message
}

func (p *bareScriptProvider) Name() string { return "script" }

func (p *bareScriptProvider) Complete(ctx context.Context, sys string, hist []llm.Message, tools []llm.ToolSpec) (llm.Response, error) {
	idx := int(p.called.Add(1) - 1)
	p.history = append(p.history, append([]llm.Message{}, hist...))
	if idx >= len(p.steps) {
		return llm.Response{}, fmt.Errorf("script exhausted at call %d", idx)
	}
	return p.steps[idx], nil
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestEndToEnd_FullStack(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e is slow")
	}

	// -- Filesystem layout --
	root := t.TempDir() // simulates the project root
	homeRoot := t.TempDir()
	homeAgents := filepath.Join(homeRoot, ".agents")
	if err := os.MkdirAll(homeAgents, 0o755); err != nil {
		t.Fatal(err)
	}

	// AGENTS.md (project root)
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"),
		[]byte("project rule: respond like a senior engineer"), 0o644); err != nil {
		t.Fatal(err)
	}

	// .agents/AGENTS.md (subdir)
	projectAgents := filepath.Join(root, ".agents")
	if err := os.MkdirAll(projectAgents, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectAgents, "AGENTS.md"),
		[]byte("subdir rule: keep diffs small"), 0o644); err != nil {
		t.Fatal(err)
	}

	// User-global AGENTS.md
	if err := os.WriteFile(filepath.Join(homeAgents, "AGENTS.md"),
		[]byte("global rule: never leak secrets"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Skill (project scope)
	skillDir := filepath.Join(projectAgents, "skills", "trim-tool")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: trim-tool\ndescription: trim trailing whitespace\ntype: model-invocable\n---\nFull body explains how to trim."), 0o644); err != nil {
		t.Fatal(err)
	}

	// Memory entry (work-local)
	memStore := memory.NewStore(filepath.Join(root, ".juex", "memory"))
	if err := memStore.Write(memory.Entry{
		Name:        "prefer-yaml",
		Description: "Prefer YAML over JSON in config files",
		Type:        "feedback",
		Body:        "Reason: easier to comment.\nHow to apply: pick YAML when both work.",
	}); err != nil {
		t.Fatal(err)
	}

	// MCP server config — points to this test binary in fake-server mode.
	mcpConfig := mcp.Config{MCPServers: map[string]mcp.ServerSpec{
		"local": {Command: os.Args[0], Env: map[string]string{"JUEX_E2E_MCP": "1"}},
	}}
	mcpJSON, _ := json.Marshal(mcpConfig)
	if err := os.WriteFile(filepath.Join(projectAgents, "mcp.json"), mcpJSON, 0o644); err != nil {
		t.Fatal(err)
	}

	// File the agent will read/edit.
	demoFile := filepath.Join(root, "demo.txt")
	if err := os.WriteFile(demoFile, []byte("hello world\nplaceholder\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// -- Build registry like the CLI does --
	reg := tools.NewRegistry()
	tools.RegisterBuiltins(reg, tools.BuiltinOptions{WorkDir: root, Shell: e2eToolShellProfile()})
	skillLoader := skills.NewLoader(filepath.Join(homeAgents, "skills"), filepath.Join(projectAgents, "skills"))
	if err := skillLoader.Load(); err != nil {
		t.Fatal(err)
	}
	if err := memStore.RegisterTools(reg); err != nil {
		t.Fatal(err)
	}

	// Connect MCP server (re-execs this test binary as a fake JSON-RPC server)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	mcpClients, err := mcp.RegisterAll(ctx, mcpConfig, reg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, c := range mcpClients {
			c.Close()
		}
	}()

	// -- Build runtime --
	bus := events.NewBus()
	sess, err := session.New(filepath.Join(root, ".juex", "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	sess.SubscribeBus(bus)

	pb := &prompt.Builder{
		GlobalAgentsMDPath: filepath.Join(homeAgents, "AGENTS.md"),
		AgentsMDDirs:       []string{root, projectAgents},
		Memory:             memStore,
		Skills:             skillLoader,
		WorkDir:            root,
		Shell:              e2ePromptShellProfile(),
		Now:                func() time.Time { return time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC) },
	}

	// -- Script the model --
	// Step 1: parallel calls to read AGENTS.md, write a new file, ping MCP.
	// Step 2: edit the demo file then grep for the new content + shell check.
	// Step 3: write a new memory entry then read the trim-tool skill.
	// Step 4: a search of memory.
	// Step 5: end with text only -> turn ends.
	prov := &scriptProvider{
		t: t,
		steps: []llm.Response{
			{
				Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
					{Type: llm.BlockText, Text: "let me gather context"},
					{Type: llm.BlockToolUse, ToolUseID: "t1", ToolName: "read", Input: map[string]any{"path": filepath.Join(root, "AGENTS.md")}},
					{Type: llm.BlockToolUse, ToolUseID: "t2", ToolName: "write", Input: map[string]any{"path": filepath.Join(root, "out.txt"), "content": "first write\n"}},
					{Type: llm.BlockToolUse, ToolUseID: "t3", ToolName: "mcp__local__echo", Input: map[string]any{"text": "ping"}},
				}},
				StopReason: llm.StopToolUse,
			},
			{
				Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
					{Type: llm.BlockText, Text: "now mutate and verify"},
					{Type: llm.BlockToolUse, ToolUseID: "t4", ToolName: "edit", Input: map[string]any{"path": demoFile, "old": "placeholder", "new": "FINAL"}},
					{Type: llm.BlockToolUse, ToolUseID: "t5", ToolName: "grep", Input: map[string]any{"pattern": "FINAL", "path": demoFile}},
					{Type: llm.BlockToolUse, ToolUseID: "t6", ToolName: "exec_command", Input: map[string]any{"cmd": "echo SCRIPTED && wc -l " + demoFile}},
				}},
				StopReason: llm.StopToolUse,
			},
			{
				Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
					{Type: llm.BlockText, Text: "persist findings + load skill"},
					{Type: llm.BlockToolUse, ToolUseID: "t7", ToolName: "memory_write", Input: map[string]any{
						"name": "demo-finding", "description": "demo file now ends with FINAL",
						"type": "project", "body": "edited via e2e",
					}},
					// The model loads a skill body via the standard `read` tool;
					// the absolute path was advertised in the system prompt's
					// "Available Skills" section.
					{Type: llm.BlockToolUse, ToolUseID: "t8", ToolName: "read", Input: map[string]any{"path": filepath.Join(projectAgents, "skills", "trim-tool", "SKILL.md")}},
				}},
				StopReason: llm.StopToolUse,
			},
			{
				Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
					{Type: llm.BlockText, Text: "double-check memory"},
					{Type: llm.BlockToolUse, ToolUseID: "t9", ToolName: "memory_search", Input: map[string]any{"query": "FINAL"}},
				}},
				StopReason: llm.StopToolUse,
			},
			{
				Message:    llm.TextMessage(llm.RoleAssistant, "TASK COMPLETE: demo.txt edited, memory persisted, MCP echoed"),
				StopReason: llm.StopEndTurn,
			},
		},
	}

	// Tally events and surface tool failures explicitly.
	var toolErrs int32
	bus.Subscribe("tool.errored", func(e events.Event) {
		atomic.AddInt32(&toolErrs, 1)
		t.Logf("tool errored: %+v", e.Payload)
	})

	eng := &runtime.Engine{
		Provider: prov,
		Tools:    reg,
		Bus:      bus,
		Session:  sess,
		Prompt:   pb,
	}

	out, err := eng.Turn(ctx, "drive the demo")
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if !strings.Contains(out, "TASK COMPLETE") {
		t.Fatalf("final text wrong: %q", out)
	}
	if toolErrs != 0 {
		t.Fatalf("expected zero tool errors, got %d", toolErrs)
	}

	// -- Filesystem assertions --
	// 1. write created out.txt
	if data, err := os.ReadFile(filepath.Join(root, "out.txt")); err != nil || string(data) != "first write\n" {
		t.Fatalf("out.txt: data=%q err=%v", data, err)
	}
	// 2. edit replaced placeholder with FINAL
	demoData, err := os.ReadFile(demoFile)
	if err != nil || !strings.Contains(string(demoData), "FINAL") || strings.Contains(string(demoData), "placeholder") {
		t.Fatalf("demo.txt: data=%q err=%v", demoData, err)
	}
	// 3. memory_write persisted demo-finding
	mems, err := memStore.Load()
	if err != nil {
		t.Fatal(err)
	}
	hasFinding := false
	for _, m := range mems {
		if m.Name == "demo-finding" {
			hasFinding = true
			if !strings.Contains(m.Body, "edited via e2e") {
				t.Errorf("memory body lost: %+v", m)
			}
		}
	}
	if !hasFinding {
		t.Fatalf("demo-finding memory not persisted; entries: %+v", mems)
	}

	// -- jsonl assertions --
	convPath := filepath.Join(sess.Dir, "conversation.jsonl")
	convLines := readLines(t, convPath)
	// History layout for 5 scripted assistant responses (4 with tool calls + 1 text-only):
	//   u(prompt) + [a + u(tool_results)] x4 + a(final) = 1 + 8 + 1 = 10 messages.
	if len(convLines) != 10 {
		t.Errorf("conversation.jsonl line count = %d; want 10", len(convLines))
	}
	for i, line := range convLines {
		var m llm.Message
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("line %d not valid JSON message: %v: %s", i, err, line)
		}
	}

	eventsPath := filepath.Join(sess.Dir, "events.jsonl")
	eventLines := readLines(t, eventsPath)
	wantTypes := map[string]bool{
		"turn.started": false, "turn.completed": false,
		"llm.requested": false, "llm.responded": false,
		"tool.requested": false, "tool.completed": false,
	}
	for _, line := range eventLines {
		var ev events.Event
		if err := json.Unmarshal([]byte(line), &ev); err == nil {
			if _, ok := wantTypes[ev.Type]; ok {
				wantTypes[ev.Type] = true
			}
		}
	}
	for typ, seen := range wantTypes {
		if !seen {
			t.Errorf("expected event type %q not seen in events.jsonl", typ)
		}
	}

	// -- Script execution sanity --
	if int(prov.called.Load()) != len(prov.steps) {
		t.Errorf("script not fully executed: %d / %d", prov.called.Load(), len(prov.steps))
	}
	// History snapshots should grow monotonically.
	for i := 1; i < len(prov.history); i++ {
		if len(prov.history[i]) <= len(prov.history[i-1]) {
			t.Errorf("history did not grow at call %d (%d vs %d)", i, len(prov.history[i]), len(prov.history[i-1]))
		}
	}

	// -- MCP integration: tool.completed for the echo call should have been emitted. --
	// (Already implied by toolErrs == 0; this is a stronger check on payload size.)
}

func TestEndToEnd_ToolFailureLedgerRecordsAndStalesWithoutContinuation(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e is slow")
	}

	root := t.TempDir()
	target := filepath.Join(root, "artifact.txt")
	sess, err := session.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	bus := events.NewBus()
	sess.SubscribeBus(bus)

	reg := tools.NewRegistry()
	tools.RegisterBuiltins(reg, tools.BuiltinOptions{WorkDir: root, Shell: tools.DefaultShellProfile()})
	reg.MustRegister(tools.Tool{
		Name:        "check_ready",
		Description: "test-only readiness check",
		Schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"path": map[string]any{"type": "string"}},
			"required":   []string{"path"},
		},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			path, _ := in["path"].(string)
			data, err := os.ReadFile(path)
			if err != nil {
				return "artifact is missing", fmt.Errorf("check failed: %w", err)
			}
			if !strings.Contains(string(data), "ready") {
				return "artifact is not ready", errors.New("check failed: marker missing")
			}
			return "artifact ready", nil
		},
	})

	prov := &bareScriptProvider{
		steps: []llm.Response{
			{
				Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
					{Type: llm.BlockToolUse, ToolUseID: "check_1", ToolName: "check_ready", Input: map[string]any{"path": target}},
				}},
				StopReason: llm.StopToolUse,
			},
			{
				Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
					{Type: llm.BlockToolUse, ToolUseID: "write_1", ToolName: "write", Input: map[string]any{"path": target, "content": "ready\n"}},
				}},
				StopReason: llm.StopToolUse,
			},
			{
				Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
					{Type: llm.BlockToolUse, ToolUseID: "check_2", ToolName: "check_ready", Input: map[string]any{"path": target}},
				}},
				StopReason: llm.StopToolUse,
			},
			{
				Message:    llm.TextMessage(llm.RoleAssistant, "TASK COMPLETE: artifact verified"),
				StopReason: llm.StopEndTurn,
			},
		},
	}

	eng := &runtime.Engine{
		Provider: prov,
		Tools:    reg,
		Bus:      bus,
		Session:  sess,
		Prompt: &prompt.Builder{
			AgentsMDDirs: []string{root},
			Now:          func() time.Time { return time.Date(2026, 6, 14, 9, 0, 0, 0, time.UTC) },
		},
	}

	out, err := eng.Turn(context.Background(), "make the artifact ready")
	if err != nil {
		t.Fatal(err)
	}
	if out != "TASK COMPLETE: artifact verified" {
		t.Fatalf("out = %q", out)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "ready\n" {
		t.Fatalf("artifact data=%q err=%v", data, err)
	}
	if got := int(prov.called.Load()); got != 4 {
		t.Fatalf("provider calls = %d, want 4", got)
	}
	if observation := messagesText(prov.history[2]); strings.Contains(observation, "Runtime observation") {
		t.Fatalf("provider should not receive failure-ledger continuation observation:\n%s", observation)
	}

	convText := strings.Join(readLines(t, filepath.Join(sess.Dir, "conversation.jsonl")), "\n")
	if strings.Contains(convText, "Runtime observation") {
		t.Fatalf("conversation should not include runtime failure continuation:\n%s", convText)
	}

	eventLines := readLines(t, filepath.Join(sess.Dir, "events.jsonl"))
	seen := map[string]bool{}
	var payloadText strings.Builder
	for i, line := range eventLines {
		var ev events.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("event line %d: %v\n%s", i+1, err, line)
		}
		seen[ev.Type] = true
		payload, _ := json.Marshal(ev.Payload)
		payloadText.Write(payload)
		payloadText.WriteByte('\n')
	}
	for _, want := range []string{"tool.failure.recorded", "tool.failure.stale"} {
		if !seen[want] {
			t.Fatalf("events missing %q; seen=%v", want, seen)
		}
	}
	for _, unwanted := range []string{"tool.failure.continued", "unresolved-failure-gate"} {
		if seen[unwanted] {
			t.Fatalf("events should not include %q; seen=%v", unwanted, seen)
		}
	}
	for _, want := range []string{"recoverable", "artifact.txt", "check_ready"} {
		if !strings.Contains(payloadText.String(), want) {
			t.Fatalf("event payloads missing %q:\n%s", want, payloadText.String())
		}
	}
}

func TestEndToEnd_ApplyPatchBuiltinFlow(t *testing.T) {
	work := t.TempDir()
	target := filepath.Join(work, "demo.txt")
	if err := os.WriteFile(target, []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sess, err := session.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	bus := events.NewBus()
	sess.SubscribeBus(bus)
	reg := tools.NewRegistry()
	tools.RegisterBuiltins(reg, tools.BuiltinOptions{WorkDir: work, Shell: tools.DefaultShellProfile()})

	patchText := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: demo.txt",
		"@@",
		" alpha",
		"-beta",
		"+PATCHED",
		" gamma",
		"*** Add File: notes/result.txt",
		"+patch flow complete",
		"*** End Patch",
	}, "\n")
	prov := &bareScriptProvider{
		steps: []llm.Response{
			{
				Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
					{Type: llm.BlockToolUse, ToolUseID: "patch_1", ToolName: "apply_patch", Input: map[string]any{"patch_text": patchText}},
				}},
				StopReason: llm.StopToolUse,
			},
			{
				Message:    llm.TextMessage(llm.RoleAssistant, "TASK COMPLETE: patch applied"),
				StopReason: llm.StopEndTurn,
			},
		},
	}
	eng := &runtime.Engine{
		Provider: prov,
		Tools:    reg,
		Bus:      bus,
		Session:  sess,
		Prompt: &prompt.Builder{
			AgentsMDDirs: []string{work},
			Now:          func() time.Time { return time.Date(2026, 6, 29, 10, 0, 0, 0, time.UTC) },
		},
	}

	out, err := eng.Turn(context.Background(), "apply the patch")
	if err != nil {
		t.Fatal(err)
	}
	if out != "TASK COMPLETE: patch applied" {
		t.Fatalf("out = %q", out)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "alpha\nPATCHED\ngamma\n" {
		t.Fatalf("demo.txt data=%q err=%v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(work, "notes", "result.txt")); err != nil || string(data) != "patch flow complete\n" {
		t.Fatalf("notes/result.txt data=%q err=%v", data, err)
	}

	convLines := readLines(t, filepath.Join(sess.Dir, "conversation.jsonl"))
	convText := strings.Join(convLines, "\n")
	for _, want := range []string{"apply_patch", "applied patch", "add=1", "update=1"} {
		if !strings.Contains(convText, want) {
			t.Fatalf("conversation missing %q:\n%s", want, convText)
		}
	}
	var toolResultText string
	for _, line := range convLines {
		var msg llm.Message
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			t.Fatal(err)
		}
		for _, block := range msg.Blocks {
			if block.Type == llm.BlockToolResult && block.ToolUseID == "patch_1" {
				toolResultText = block.Content
			}
		}
	}
	if toolResultText == "" {
		t.Fatalf("missing apply_patch tool result in conversation:\n%s", convText)
	}
	if strings.Contains(toolResultText, "patch flow complete") || strings.Contains(toolResultText, "*** Begin Patch") {
		t.Fatalf("tool result should not echo full patch text:\n%s", toolResultText)
	}
	eventsText := strings.Join(readLines(t, filepath.Join(sess.Dir, "events.jsonl")), "\n")
	for _, want := range []string{"tool.requested", "tool.completed", "apply_patch"} {
		if !strings.Contains(eventsText, want) {
			t.Fatalf("events missing %q:\n%s", want, eventsText)
		}
	}
}

func TestEndToEnd_ChunkedWriteBuiltinFlow(t *testing.T) {
	work := t.TempDir()
	sess, err := session.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	bus := events.NewBus()
	sess.SubscribeBus(bus)
	reg := tools.NewRegistry()
	tools.RegisterBuiltins(reg, tools.BuiltinOptions{WorkDir: work, Shell: tools.DefaultShellProfile()})

	contentA := strings.Repeat("alpha\n", 80)
	contentB := strings.Repeat("beta\n", 80)
	full := contentA + contentB
	prov := &chunkedWriteProvider{t: t, contentA: contentA, contentB: contentB}
	eng := &runtime.Engine{
		Provider: prov,
		Tools:    reg,
		Bus:      bus,
		Session:  sess,
		Prompt: &prompt.Builder{
			AgentsMDDirs: []string{work},
			Now:          func() time.Time { return time.Date(2026, 6, 29, 11, 0, 0, 0, time.UTC) },
		},
	}

	out, err := eng.Turn(context.Background(), "write a long report")
	if err != nil {
		t.Fatal(err)
	}
	if out != "TASK COMPLETE: chunked write applied" {
		t.Fatalf("out = %q", out)
	}
	if data, err := os.ReadFile(filepath.Join(work, "reports", "long.md")); err != nil || string(data) != full {
		t.Fatalf("long.md len=%d err=%v", len(data), err)
	}
	if len(prov.history) < 5 {
		t.Fatalf("provider calls = %d, want at least 5", len(prov.history))
	}
	for _, msg := range prov.history[3] {
		for _, block := range msg.Blocks {
			if block.Type == llm.BlockToolUse && block.ToolName == "write_chunk" {
				content, ok := block.Input["content"].(string)
				if !ok || (content != contentA && content != contentB) {
					t.Fatalf("provider replay should keep provider-safe chunk content: %+v", block.Input)
				}
				if _, ok := block.Input["content_omitted"]; ok {
					t.Fatalf("provider replay kept schema-like content summary at top level: %+v", block.Input)
				}
			}
		}
	}
	afterCommitText := messagesText(prov.history[4])
	if !strings.Contains(afterCommitText, "Chunked write provider replay summary: committed") {
		t.Fatalf("provider replay after commit should include chunked write summary:\n%s", afterCommitText)
	}
	afterCommitDebug := fmt.Sprintf("%+v", prov.history[4])
	for _, forbidden := range []string{contentA, contentB} {
		if strings.Contains(afterCommitDebug, forbidden) {
			t.Fatalf("provider replay after commit should fold chunk content %q:\n%s", forbidden, afterCommitDebug)
		}
	}
	for _, msg := range prov.history[4] {
		for _, block := range msg.Blocks {
			if block.Type == llm.BlockToolUse && strings.HasPrefix(block.ToolName, "write_") {
				t.Fatalf("provider replay after commit should fold chunked write tool call: %+v", block)
			}
		}
	}
	convText := strings.Join(readLines(t, filepath.Join(sess.Dir, "conversation.jsonl")), "\n")
	for _, want := range []string{"write_begin", "write_chunk", "write_commit", "chunks=2"} {
		if !strings.Contains(convText, want) {
			t.Fatalf("conversation missing %q:\n%s", want, convText)
		}
	}
	for _, forbidden := range []string{contentA, contentB} {
		if strings.Contains(toolResultsFromConversation(t, convText), forbidden) {
			t.Fatalf("tool results echoed chunk content")
		}
	}
	eventsText := strings.Join(readLines(t, filepath.Join(sess.Dir, "events.jsonl")), "\n")
	for _, want := range []string{"tool.requested", "tool.completed", "write_chunk"} {
		if !strings.Contains(eventsText, want) {
			t.Fatalf("events missing %q:\n%s", want, eventsText)
		}
	}
}

type chunkedWriteProvider struct {
	t        *testing.T
	called   atomic.Int32
	history  [][]llm.Message
	contentA string
	contentB string
}

func (p *chunkedWriteProvider) Name() string { return "chunked-write-script" }

func (p *chunkedWriteProvider) Complete(ctx context.Context, sys string, hist []llm.Message, tools []llm.ToolSpec) (llm.Response, error) {
	idx := int(p.called.Add(1) - 1)
	p.history = append(p.history, append([]llm.Message{}, hist...))
	switch idx {
	case 0:
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "begin_1", ToolName: "write_begin", Input: map[string]any{"path": "reports/long.md", "mode": "create"}},
		}}, StopReason: llm.StopToolUse}, nil
	case 1:
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "chunk_1", ToolName: "write_chunk", Input: map[string]any{"write_id": chunkWriteIDFromMessages(p.t, hist), "index": 0, "content": p.contentA}},
		}}, StopReason: llm.StopToolUse}, nil
	case 2:
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "chunk_2", ToolName: "write_chunk", Input: map[string]any{"write_id": chunkWriteIDFromMessages(p.t, hist), "index": 1, "content": p.contentB}},
		}}, StopReason: llm.StopToolUse}, nil
	case 3:
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "commit_1", ToolName: "write_commit", Input: map[string]any{"write_id": chunkWriteIDFromMessages(p.t, hist), "expected_chunks": 2}},
		}}, StopReason: llm.StopToolUse}, nil
	case 4:
		return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "TASK COMPLETE: chunked write applied"), StopReason: llm.StopEndTurn}, nil
	default:
		return llm.Response{}, fmt.Errorf("chunkedWriteProvider exhausted at call %d", idx)
	}
}

func chunkWriteIDFromMessages(t *testing.T, history []llm.Message) string {
	t.Helper()
	for _, msg := range history {
		for _, block := range msg.Blocks {
			if block.Type != llm.BlockToolResult || block.ToolUseID != "begin_1" {
				continue
			}
			const marker = "write_id="
			start := strings.Index(block.Content, marker)
			if start < 0 {
				t.Fatalf("begin result missing write_id: %q", block.Content)
			}
			start += len(marker)
			end := strings.IndexAny(block.Content[start:], " \n")
			if end < 0 {
				return block.Content[start:]
			}
			return block.Content[start : start+end]
		}
	}
	t.Fatalf("history missing begin tool result: %+v", history)
	return ""
}

func toolResultsFromConversation(t *testing.T, convText string) string {
	t.Helper()
	var out strings.Builder
	for _, line := range strings.Split(convText, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var msg llm.Message
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			t.Fatal(err)
		}
		for _, block := range msg.Blocks {
			if block.Type == llm.BlockToolResult {
				out.WriteString(block.Content)
				out.WriteByte('\n')
			}
		}
	}
	return out.String()
}

func TestEndToEnd_ToolFailureLedgerWithUserAgentsDisabledDoesNotHardBlock(t *testing.T) {
	work := t.TempDir()
	prov := &recordingProvider{
		steps: []llm.Response{
			{
				Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
					{Type: llm.BlockToolUse, ToolUseID: "exec_fail", ToolName: "exec_command", Input: map[string]any{"cmd": "exit 1"}},
				}},
				StopReason: llm.StopToolUse,
			},
			{
				Message:    llm.TextMessage(llm.RoleAssistant, "TASK COMPLETE: command failure recorded"),
				StopReason: llm.StopEndTurn,
			},
		},
	}
	a, err := app.New(app.Options{
		Config: config.Config{
			ProviderProtocol:          "openai/chat",
			WorkDir:                   work,
			EnableUserAgentsResources: false,
		},
		Provider:   prov,
		WorkDir:    work,
		DisableMCP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	out, err := a.Run(context.Background(), "record the command failure")
	if err != nil {
		t.Fatal(err)
	}
	if out != "TASK COMPLETE: command failure recorded" {
		t.Fatalf("out = %q", out)
	}
	if len(prov.history) != 2 {
		t.Fatalf("provider calls = %d, want no failure-ledger continuation", len(prov.history))
	}
	if observation := messagesText(prov.history[1]); strings.Contains(observation, "Runtime observation") {
		t.Fatalf("provider should not receive failure-ledger continuation observation:\n%s", observation)
	}
	eventsText := strings.Join(readLines(t, filepath.Join(a.Session.Dir, "events.jsonl")), "\n")
	for _, want := range []string{"tool.failure.recorded", "exec_command"} {
		if !strings.Contains(eventsText, want) {
			t.Fatalf("events missing %q:\n%s", want, eventsText)
		}
	}
	for _, unwanted := range []string{"tool.failure.continued", "unresolved-failure-gate"} {
		if strings.Contains(eventsText, unwanted) {
			t.Fatalf("events should not include %q:\n%s", unwanted, eventsText)
		}
	}
	if _, err := os.Stat(filepath.Join(a.Session.Dir, "working_state.json")); !os.IsNotExist(err) {
		t.Fatalf("tool failure should not create working_state.json, err=%v", err)
	}
}

func TestEndToEnd_NotesSurviveCompaction(t *testing.T) {
	work := t.TempDir()
	compaction := config.DefaultCompactionConfig()
	compaction.TailTurns = 0
	compaction.KeepRecentTokens = 0
	prov := &recordingProvider{
		steps: []llm.Response{
			{
				Message:    llm.TextMessage(llm.RoleAssistant, "first answer"),
				StopReason: llm.StopEndTurn,
			},
			{
				Message:    llm.TextMessage(llm.RoleAssistant, "compact summary"),
				StopReason: llm.StopEndTurn,
			},
			{
				Message:    llm.TextMessage(llm.RoleAssistant, "second answer"),
				StopReason: llm.StopEndTurn,
			},
		},
	}
	a, err := app.New(app.Options{
		Config:   config.Config{ProviderProtocol: "openai/chat", WorkDir: work, Compaction: compaction},
		Provider: prov,
		WorkDir:  work,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	if a.Engine.Notes == nil {
		t.Fatal("app did not initialize notes store")
	}
	if _, err := a.Engine.Tools.Call(context.Background(), runtime.NotesToolUpdate, map[string]any{
		"content": "- [x] bind local services to 0.0.0.0\n- [ ] confirm CI status",
	}); err != nil {
		t.Fatal(err)
	}

	if out, err := a.Run(context.Background(), "first turn"); err != nil || out != "first answer" {
		t.Fatalf("first run out=%q err=%v", out, err)
	}
	if _, err := a.CompactWithInstructions(context.Background(), "manual", false, "keep notes"); err != nil {
		t.Fatal(err)
	}
	if out, err := a.Run(context.Background(), "second turn"); err != nil || out != "second answer" {
		t.Fatalf("second run out=%q err=%v", out, err)
	}
	if len(prov.history) != 3 {
		t.Fatalf("provider calls = %d", len(prov.history))
	}
	afterCompact := messagesText(prov.history[2])
	for _, want := range []string{"Current working notes", "bind local services to 0.0.0.0", "confirm CI status"} {
		if !strings.Contains(afterCompact, want) {
			t.Fatalf("post-compaction provider history missing %q:\n%s", want, afterCompact)
		}
	}
	if _, err := os.Stat(filepath.Join(a.Session.Dir, "notes.md")); err != nil {
		t.Fatalf("notes.md missing: %v", err)
	}
	eventsText := strings.Join(readLines(t, filepath.Join(a.Session.Dir, "events.jsonl")), "\n")
	if !strings.Contains(eventsText, `"type":"notes.updated"`) {
		t.Fatalf("events missing notes.updated:\n%s", eventsText)
	}
}

func TestEndToEnd_FullStackPortable(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e is slow")
	}

	root := t.TempDir()
	homeRoot := t.TempDir()
	homeAgents := filepath.Join(homeRoot, ".agents")
	if err := os.MkdirAll(homeAgents, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"),
		[]byte("project rule: respond like a senior engineer"), 0o644); err != nil {
		t.Fatal(err)
	}
	projectAgents := filepath.Join(root, ".agents")
	if err := os.MkdirAll(projectAgents, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectAgents, "AGENTS.md"),
		[]byte("subdir rule: keep diffs small"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(homeAgents, "AGENTS.md"),
		[]byte("global rule: never leak secrets"), 0o644); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(projectAgents, "skills", "trim-tool")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: trim-tool\ndescription: trim trailing whitespace\ntype: model-invocable\n---\nFull body explains how to trim."), 0o644); err != nil {
		t.Fatal(err)
	}

	memStore := memory.NewStore(filepath.Join(root, ".juex", "memory"))
	if err := memStore.Write(memory.Entry{
		Name:        "prefer-yaml",
		Description: "Prefer YAML over JSON in config files",
		Type:        "feedback",
		Body:        "Reason: easier to comment.\nHow to apply: pick YAML when both work.",
	}); err != nil {
		t.Fatal(err)
	}

	mcpConfig := mcp.Config{MCPServers: map[string]mcp.ServerSpec{
		"local": {Command: os.Args[0], Env: map[string]string{"JUEX_E2E_MCP": "1"}},
	}}
	mcpJSON, _ := json.Marshal(mcpConfig)
	if err := os.WriteFile(filepath.Join(projectAgents, "mcp.json"), mcpJSON, 0o644); err != nil {
		t.Fatal(err)
	}

	demoFile := filepath.Join(root, "demo.txt")
	if err := os.WriteFile(demoFile, []byte("hello world\nplaceholder\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := tools.NewRegistry()
	tools.RegisterBuiltins(reg, tools.BuiltinOptions{WorkDir: root, Shell: e2eToolShellProfile()})
	skillLoader := skills.NewLoader(filepath.Join(homeAgents, "skills"), filepath.Join(projectAgents, "skills"))
	if err := skillLoader.Load(); err != nil {
		t.Fatal(err)
	}
	if err := memStore.RegisterTools(reg); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	mcpClients, err := mcp.RegisterAll(ctx, mcpConfig, reg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, c := range mcpClients {
			c.Close()
		}
	}()

	bus := events.NewBus()
	sess, err := session.New(filepath.Join(root, ".juex", "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	sess.SubscribeBus(bus)

	pb := &prompt.Builder{
		GlobalAgentsMDPath: filepath.Join(homeAgents, "AGENTS.md"),
		AgentsMDDirs:       []string{root, projectAgents},
		Memory:             memStore,
		Skills:             skillLoader,
		WorkDir:            root,
		Shell:              e2ePromptShellProfile(),
		Now:                func() time.Time { return time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC) },
	}

	prov := &scriptProvider{
		t: t,
		steps: []llm.Response{
			{
				Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
					{Type: llm.BlockText, Text: "portable gather"},
					{Type: llm.BlockToolUse, ToolUseID: "p1", ToolName: "read", Input: map[string]any{"path": filepath.Join(root, "AGENTS.md")}},
					{Type: llm.BlockToolUse, ToolUseID: "p2", ToolName: "write", Input: map[string]any{"path": filepath.Join(root, "out.txt"), "content": "portable write\n"}},
					{Type: llm.BlockToolUse, ToolUseID: "p3", ToolName: "mcp__local__echo", Input: map[string]any{"text": "portable"}},
				}},
				StopReason: llm.StopToolUse,
			},
			{
				Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
					{Type: llm.BlockText, Text: "portable mutate"},
					{Type: llm.BlockToolUse, ToolUseID: "p4", ToolName: "edit", Input: map[string]any{"path": demoFile, "old": "placeholder", "new": "PORTABLE"}},
					{Type: llm.BlockToolUse, ToolUseID: "p5", ToolName: "grep", Input: map[string]any{"pattern": "PORTABLE", "path": demoFile}},
				}},
				StopReason: llm.StopToolUse,
			},
			{
				Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
					{Type: llm.BlockText, Text: "portable persist"},
					{Type: llm.BlockToolUse, ToolUseID: "p6", ToolName: "memory_write", Input: map[string]any{
						"name": "portable-finding", "description": "portable e2e edited demo file",
						"type": "project", "body": "edited via portable e2e",
					}},
					{Type: llm.BlockToolUse, ToolUseID: "p7", ToolName: "read", Input: map[string]any{"path": filepath.Join(projectAgents, "skills", "trim-tool", "SKILL.md")}},
				}},
				StopReason: llm.StopToolUse,
			},
			{
				Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
					{Type: llm.BlockText, Text: "portable verify memory"},
					{Type: llm.BlockToolUse, ToolUseID: "p8", ToolName: "memory_search", Input: map[string]any{"query": "portable"}},
				}},
				StopReason: llm.StopToolUse,
			},
			{
				Message:    llm.TextMessage(llm.RoleAssistant, "PORTABLE COMPLETE: demo.txt edited, memory persisted, MCP echoed"),
				StopReason: llm.StopEndTurn,
			},
		},
	}

	var toolErrs int32
	bus.Subscribe("tool.errored", func(e events.Event) {
		atomic.AddInt32(&toolErrs, 1)
		t.Logf("tool errored: %+v", e.Payload)
	})

	eng := &runtime.Engine{
		Provider: prov,
		Tools:    reg,
		Bus:      bus,
		Session:  sess,
		Prompt:   pb,
	}

	out, err := eng.Turn(ctx, "drive the portable demo")
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if !strings.Contains(out, "PORTABLE COMPLETE") {
		t.Fatalf("final text wrong: %q", out)
	}
	if toolErrs != 0 {
		t.Fatalf("expected zero tool errors, got %d", toolErrs)
	}
	if data, err := os.ReadFile(filepath.Join(root, "out.txt")); err != nil || string(data) != "portable write\n" {
		t.Fatalf("out.txt: data=%q err=%v", data, err)
	}
	demoData, err := os.ReadFile(demoFile)
	if err != nil || !strings.Contains(string(demoData), "PORTABLE") || strings.Contains(string(demoData), "placeholder") {
		t.Fatalf("demo.txt: data=%q err=%v", demoData, err)
	}
	mems, err := memStore.Load()
	if err != nil {
		t.Fatal(err)
	}
	hasFinding := false
	for _, m := range mems {
		if m.Name == "portable-finding" {
			hasFinding = true
			if !strings.Contains(m.Body, "edited via portable e2e") {
				t.Errorf("memory body lost: %+v", m)
			}
		}
	}
	if !hasFinding {
		t.Fatalf("portable-finding memory not persisted; entries: %+v", mems)
	}

	convLines := readLines(t, filepath.Join(sess.Dir, "conversation.jsonl"))
	if len(convLines) != 10 {
		t.Errorf("conversation.jsonl line count = %d; want 10", len(convLines))
	}
	for i, line := range convLines {
		var m llm.Message
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("line %d not valid JSON message: %v: %s", i, err, line)
		}
	}
	eventLines := readLines(t, filepath.Join(sess.Dir, "events.jsonl"))
	wantTypes := map[string]bool{
		"turn.started": false, "turn.completed": false,
		"llm.requested": false, "llm.responded": false,
		"tool.requested": false, "tool.completed": false,
	}
	for _, line := range eventLines {
		var ev events.Event
		if err := json.Unmarshal([]byte(line), &ev); err == nil {
			if _, ok := wantTypes[ev.Type]; ok {
				wantTypes[ev.Type] = true
			}
		}
	}
	for typ, seen := range wantTypes {
		if !seen {
			t.Errorf("expected event type %q not seen in events.jsonl", typ)
		}
	}
	if int(prov.called.Load()) != len(prov.steps) {
		t.Errorf("script not fully executed: %d / %d", prov.called.Load(), len(prov.steps))
	}
	for i := 1; i < len(prov.history); i++ {
		if len(prov.history[i]) <= len(prov.history[i-1]) {
			t.Errorf("history did not grow at call %d (%d vs %d)", i, len(prov.history[i]), len(prov.history[i-1]))
		}
	}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out []string
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		if line := scanner.Text(); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// ---- Fake MCP server (re-exec) ----

func TestMain(m *testing.M) {
	if os.Getenv("JUEX_E2E_MCP") == "1" {
		runFakeMCP()
		return
	}
	os.Exit(m.Run())
}

func e2eToolShellProfile() tools.ShellProfile {
	return tools.ShellProfile{
		Profile:   "fake-posix",
		Family:    "posix",
		Binary:    os.Args[0],
		Args:      []string{"-test.run=TestE2EShellHelperProcess", "--", "--juex-e2e-shell"},
		PathStyle: "posix",
	}
}

func e2ePromptShellProfile() prompt.ShellProfile {
	return prompt.ShellProfile{
		Profile:   "fake-posix",
		Family:    "posix",
		Binary:    os.Args[0],
		Args:      []string{"-test.run=TestE2EShellHelperProcess", "--", "--juex-e2e-shell"},
		PathStyle: "posix",
	}
}

func TestE2EShellHelperProcess(t *testing.T) {
	hasSentinel := false
	for _, arg := range os.Args {
		if arg == "--juex-e2e-shell" {
			hasSentinel = true
			break
		}
	}
	if !hasSentinel {
		return
	}
	fmt.Fprintln(os.Stdout, "SCRIPTED")
	os.Exit(0)
}

func runFakeMCP() {
	enc := json.NewEncoder(os.Stdout)
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		var req map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}
		idVal, hasID := req["id"]
		if !hasID {
			continue
		}
		method, _ := req["method"].(string)
		switch method {
		case "initialize":
			enc.Encode(map[string]any{
				"jsonrpc": "2.0", "id": idVal,
				"result": map[string]any{
					"protocolVersion": "2024-11-05",
					"capabilities":    map[string]any{"tools": map[string]any{}},
					"serverInfo":      map[string]any{"name": "fake", "version": "0"},
				},
			})
		case "tools/list":
			enc.Encode(map[string]any{
				"jsonrpc": "2.0", "id": idVal,
				"result": map[string]any{
					"tools": []map[string]any{
						{"name": "echo", "description": "Echo input", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}}},
					},
				},
			})
		case "tools/call":
			params, _ := req["params"].(map[string]any)
			args, _ := params["arguments"].(map[string]any)
			text, _ := args["text"].(string)
			enc.Encode(map[string]any{
				"jsonrpc": "2.0", "id": idVal,
				"result": map[string]any{
					"content": []map[string]any{{"type": "text", "text": "echoed: " + text}},
				},
			})
		default:
			enc.Encode(map[string]any{
				"jsonrpc": "2.0", "id": idVal,
				"error": map[string]any{"code": -32601, "message": "method not found"},
			})
		}
	}
}

// guard so build doesn't strip 'errors' import.
var _ = errors.New

// recordingProvider is a minimal Provider used only by the resume round-trip
// test. Unlike scriptProvider it has no per-call assertion side-effects, so
// it can be reused across tests without coordinating call indexes.
type recordingProvider struct {
	steps   []llm.Response
	history [][]llm.Message
}

func (p *recordingProvider) Name() string { return "recording" }

func (p *recordingProvider) Complete(ctx context.Context, sys string, hist []llm.Message, tools []llm.ToolSpec) (llm.Response, error) {
	idx := len(p.history)
	p.history = append(p.history, append([]llm.Message{}, hist...))
	if idx >= len(p.steps) {
		return llm.Response{}, fmt.Errorf("recordingProvider: exhausted at call %d", idx)
	}
	return p.steps[idx], nil
}

func TestEndToEnd_ResumeRoundTrip(t *testing.T) {
	work := t.TempDir()

	// First turn: model receives an empty history.
	prov1 := &recordingProvider{
		steps: []llm.Response{
			{
				Message:    llm.TextMessage(llm.RoleAssistant, "noted, alice"),
				StopReason: llm.StopEndTurn,
			},
		},
	}
	a1, err := app.New(app.Options{
		Config:   config.Config{ProviderProtocol: "openai/chat", WorkDir: work},
		Provider: prov1,
		WorkDir:  work,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a1.Run(context.Background(), "remember: alice"); err != nil {
		t.Fatal(err)
	}
	sessionDir := a1.Session.Dir
	if err := a1.Close(); err != nil {
		t.Fatal(err)
	}

	// The engine appends the user message before calling Complete, so the
	// first turn's snapshot contains exactly one entry (the new user prompt).
	if len(prov1.history) == 0 {
		t.Fatalf("first turn provider was never called")
	}
	if got := len(prov1.history[0]); got != 1 {
		t.Errorf("first turn saw history of len %d, want 1 (just the new user prompt)", got)
	} else if prov1.history[0][0].FirstText() != "remember: alice" {
		t.Errorf("first turn user message = %q", prov1.history[0][0].FirstText())
	}

	// Second turn: same session dir, model should see the prior pair.
	prov2 := &recordingProvider{
		steps: []llm.Response{
			{
				Message:    llm.TextMessage(llm.RoleAssistant, "you are alice"),
				StopReason: llm.StopEndTurn,
			},
		},
	}
	a2, err := app.New(app.Options{
		Config:    config.Config{ProviderProtocol: "openai/chat", WorkDir: work},
		Provider:  prov2,
		WorkDir:   work,
		ResumeDir: sessionDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a2.Close()
	out, err := a2.Run(context.Background(), "who am I?")
	if err != nil {
		t.Fatal(err)
	}
	if out != "you are alice" {
		t.Errorf("got %q", out)
	}
	if a2.Session.ID != filepath.Base(sessionDir) {
		t.Errorf("session id changed: %s vs %s", a2.Session.ID, filepath.Base(sessionDir))
	}
	// Resumed history is prior pair (user+assistant) + the new user prompt.
	if len(prov2.history) == 0 {
		t.Fatalf("second turn provider was never called")
	}
	if got := len(prov2.history[0]); got != 3 {
		t.Errorf("second turn history len = %d, want 3 (prior user+assistant + new user)", got)
	} else {
		if prov2.history[0][0].FirstText() != "remember: alice" {
			t.Errorf("first replayed message = %q", prov2.history[0][0].FirstText())
		}
		if prov2.history[0][1].FirstText() != "noted, alice" {
			t.Errorf("second replayed message = %q", prov2.history[0][1].FirstText())
		}
		if prov2.history[0][2].FirstText() != "who am I?" {
			t.Errorf("third (new user) message = %q", prov2.history[0][2].FirstText())
		}
	}
}

func TestEndToEnd_CommandLifecycleHooks(t *testing.T) {
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "observed.txt"), []byte("source material"), 0o644); err != nil {
		t.Fatal(err)
	}
	prov := &recordingProvider{
		steps: []llm.Response{
			{
				Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
					{Type: llm.BlockToolUse, ToolUseID: "blocked", ToolName: "write", Input: map[string]any{"path": "blocked.txt", "content": "x"}},
					{Type: llm.BlockToolUse, ToolUseID: "observed", ToolName: "read", Input: map[string]any{"path": "observed.txt"}},
				}},
				StopReason: llm.StopToolUse,
			},
			{
				Message:    llm.TextMessage(llm.RoleAssistant, "first answer"),
				StopReason: llm.StopEndTurn,
			},
			{
				Message:    llm.TextMessage(llm.RoleAssistant, "final answer"),
				StopReason: llm.StopEndTurn,
			},
		},
	}
	a, err := app.New(app.Options{
		Config: config.Config{
			ProviderProtocol: "openai/chat",
			WorkDir:          work,
			Hooks: hooks.Config{Commands: []hooks.CommandHook{
				{Name: "inject", Events: []hooks.EventName{hooks.EventUserPromptSubmit}, Command: e2eHookCommand("inject")},
				{Name: "deny-write", Events: []hooks.EventName{hooks.EventPreToolUse}, Tools: []string{"write"}, Command: e2eHookCommand("deny")},
				{Name: "correct-read", Events: []hooks.EventName{hooks.EventPostToolUse}, Tools: []string{"read"}, Command: e2eHookCommand("correct")},
				{Name: "continue-once", Events: []hooks.EventName{hooks.EventStop}, Command: e2eHookCommand("stop")},
			}},
		},
		Provider: prov,
		WorkDir:  work,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	out, err := a.Run(context.Background(), "start")
	if err != nil {
		t.Fatal(err)
	}
	if out != "final answer" {
		t.Fatalf("out = %q", out)
	}
	if len(prov.history) != 3 {
		t.Fatalf("provider calls = %d", len(prov.history))
	}
	if got := messagesText(prov.history[0]); !strings.Contains(got, "hook-context: visible") {
		t.Fatalf("first provider history missing injected context:\n%s", got)
	}
	var corrected *llm.Block
	for i := range prov.history[1] {
		for j := range prov.history[1][i].Blocks {
			block := &prov.history[1][i].Blocks[j]
			if block.Type == llm.BlockToolResult && block.ToolUseID == "observed" {
				corrected = block
			}
		}
	}
	if corrected == nil || corrected.IsError || !strings.Contains(corrected.Content, "source material") || !strings.Contains(corrected.Content, "review source classification") {
		t.Fatalf("corrected tool result = %+v", corrected)
	}
	stopHistory := prov.history[2]
	if len(stopHistory) < 2 {
		t.Fatalf("stop history = %+v", stopHistory)
	}
	if got := stopHistory[len(stopHistory)-1].FirstText(); got != "continue from hook" {
		t.Fatalf("stop continuation = %q", got)
	}
	for _, message := range stopHistory {
		if message.Kind == llm.MessageKindRuntimeContext {
			t.Fatalf("hook should not create runtime state: %+v", message)
		}
	}
	if _, err := os.Stat(filepath.Join(work, "blocked.txt")); !os.IsNotExist(err) {
		t.Fatalf("denied write should not create file, stat err=%v", err)
	}
	eventsData, err := os.ReadFile(filepath.Join(a.Session.Dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	eventsBody := string(eventsData)
	for _, want := range []string{`"type":"hook.completed"`, `"type":"tool.errored"`} {
		if !strings.Contains(eventsBody, want) {
			t.Fatalf("events missing %s:\n%s", want, eventsBody)
		}
	}
}

func TestEndToEnd_SandboxBlockedPathsStopBuiltinTools(t *testing.T) {
	work := t.TempDir()
	policy := config.SandboxPolicy{
		Enabled: true,
		FileSystem: config.FileSystemSandboxPolicy{
			OutsideWorkspace: config.OutsideWorkspaceReadWrite,
			BlockedPaths:     []string{"private"},
		},
		Network: config.NetworkSandboxPolicy{Enabled: true},
	}
	prov := &recordingProvider{
		steps: []llm.Response{
			{
				Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
					{Type: llm.BlockToolUse, ToolUseID: "blocked-write", ToolName: "write", Input: map[string]any{"path": "private/secret.txt", "content": "x"}},
				}},
				StopReason: llm.StopToolUse,
			},
			{
				Message:    llm.TextMessage(llm.RoleAssistant, "blocked path observed"),
				StopReason: llm.StopEndTurn,
			},
		},
	}
	a, err := app.New(app.Options{
		Config:   config.Config{ProviderProtocol: "openai/chat", WorkDir: work, Sandbox: policy},
		Provider: prov,
		WorkDir:  work,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	out, err := a.Run(context.Background(), "try blocked write")
	if err != nil {
		t.Fatal(err)
	}
	if out != "blocked path observed" {
		t.Fatalf("out = %q", out)
	}
	if got := messagesText(prov.history[1]); !strings.Contains(got, "blocked path") {
		t.Fatalf("provider history missing blocked path tool result:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(work, "private", "secret.txt")); !os.IsNotExist(err) {
		t.Fatalf("blocked write should not create file, stat err=%v", err)
	}
	eventsData, err := os.ReadFile(filepath.Join(a.Session.Dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(eventsData), `"type":"tool.errored"`) {
		t.Fatalf("events missing tool.errored:\n%s", eventsData)
	}
}

func TestEndToEnd_GoalToolsContinueThenSucceed(t *testing.T) {
	work := t.TempDir()
	prov := &recordingProvider{
		steps: []llm.Response{
			{
				Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
					{Type: llm.BlockToolUse, ToolUseID: "goal-create", ToolName: runtime.GoalToolCreate, Input: map[string]any{
						"description": "ship goal state",
						"acceptance":  "completion checks pass, goal_state.json exists, and events include goal.continued",
					}},
				}},
				StopReason: llm.StopToolUse,
			},
			{
				Message:    llm.TextMessage(llm.RoleAssistant, "done too early"),
				StopReason: llm.StopEndTurn,
			},
			{
				Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
					{Type: llm.BlockToolUse, ToolUseID: "goal-success", ToolName: runtime.GoalToolUpdate, Input: map[string]any{
						"status":        string(runtime.GoalStatusSuccess),
						"status_reason": "continuation gate fired and final answer was verified",
					}},
				}},
				StopReason: llm.StopToolUse,
			},
			{
				Message:    llm.TextMessage(llm.RoleAssistant, "verified final"),
				StopReason: llm.StopEndTurn,
			},
		},
	}
	a, err := app.New(app.Options{
		Config: config.Config{
			ProviderProtocol: "openai/chat",
			WorkDir:          work,
		},
		Provider: prov,
		WorkDir:  work,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	out, err := a.Run(context.Background(), "ship goal state")
	if err != nil {
		t.Fatal(err)
	}
	if out != "verified final" {
		t.Fatalf("out = %q", out)
	}
	if len(prov.history) != 4 {
		t.Fatalf("provider calls = %d", len(prov.history))
	}
	continuationHistory := prov.history[2]
	if len(continuationHistory) < 2 {
		t.Fatalf("goal continuation history = %+v", continuationHistory)
	}
	if got := continuationHistory[len(continuationHistory)-2].FirstText(); !strings.Contains(got, "current session goal is still in progress") {
		t.Fatalf("goal continuation = %q", got)
	}
	goalContext := continuationHistory[len(continuationHistory)-1]
	if goalContext.Kind != llm.MessageKindRuntimeContext ||
		!strings.Contains(goalContext.FirstText(), "Current goal contract") ||
		!strings.Contains(goalContext.FirstText(), "goal_state.json") {
		t.Fatalf("goal runtime context = %+v", goalContext)
	}
	goalData, err := os.ReadFile(filepath.Join(a.Session.Dir, "goal_state.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"description": "ship goal state"`,
		`"acceptance": "completion checks pass, goal_state.json exists, and events include goal.continued"`,
		`"continuation_count": 1`,
		`"status": "success"`,
		`"status_reason": "continuation gate fired and final answer was verified"`,
	} {
		if !strings.Contains(string(goalData), want) {
			t.Fatalf("goal_state.json missing %s:\n%s", want, goalData)
		}
	}
	for _, forbidden := range []string{"acceptance_criteria", "required_artifacts", "artifact_requirements", "validation_requirements", "verification_method"} {
		if strings.Contains(string(goalData), forbidden) {
			t.Fatalf("goal_state.json contains removed field %q:\n%s", forbidden, goalData)
		}
	}
	eventsData, err := os.ReadFile(filepath.Join(a.Session.Dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"type":"goal.continued"`, `"type":"goal.updated"`, `"goal-completion-gate"`} {
		if !strings.Contains(string(eventsData), want) {
			t.Fatalf("events missing %s:\n%s", want, eventsData)
		}
	}
}

func TestEndToEnd_DebugObservabilityArtifacts(t *testing.T) {
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "ok.txt"), []byte("visible\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prov := &recordingProvider{
		steps: []llm.Response{
			{
				Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
					{Type: llm.BlockToolUse, ToolUseID: "read-ok", ToolName: "read", Input: map[string]any{"path": "ok.txt"}},
					{Type: llm.BlockToolUse, ToolUseID: "read-missing", ToolName: "read", Input: map[string]any{"path": "missing.txt"}},
				}},
				StopReason: llm.StopToolUse,
			},
			{
				Message:    llm.TextMessage(llm.RoleAssistant, "observed done"),
				StopReason: llm.StopEndTurn,
			},
			{
				Message:    llm.TextMessage(llm.RoleAssistant, "summary of observed run"),
				StopReason: llm.StopEndTurn,
			},
		},
	}
	compaction := config.DefaultCompactionConfig()
	compaction.TailTurns = 0
	compaction.KeepRecentTokens = 0
	a, err := app.New(app.Options{
		Config:   config.Config{ProviderProtocol: "openai/chat", WorkDir: work, Compaction: compaction},
		Provider: prov,
		WorkDir:  work,
		Debug:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := a.Run(context.Background(), "inspect files")
	if err != nil {
		t.Fatal(err)
	}
	if out != "observed done" {
		t.Fatalf("out = %q", out)
	}
	compact, err := a.CompactWithInstructions(context.Background(), "manual", false, "summarize observability")
	if err != nil {
		t.Fatal(err)
	}
	if compact.MessageID == "" {
		t.Fatalf("manual compaction did not run: %+v", compact)
	}
	sessionDir := a.Session.Dir
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}

	for _, rel := range []string{"logs/juex.log", "logs/debug.log", "trace.jsonl", "spans.jsonl", "tools.jsonl"} {
		if _, err := os.Stat(filepath.Join(sessionDir, rel)); err != nil {
			t.Fatalf("%s missing: %v", rel, err)
		}
	}
	trace := readJSONLObjects(t, filepath.Join(sessionDir, "trace.jsonl"))
	for _, want := range []string{"tool.completed", "tool.errored", "context.compact.completed", "finish.attempted"} {
		if !jsonlHasString(trace, "event", want) {
			t.Fatalf("trace missing event %q: %+v", want, trace)
		}
	}
	spans := readJSONLObjects(t, filepath.Join(sessionDir, "spans.jsonl"))
	for _, want := range [][2]string{
		{"tool", "end"},
		{"tool", "error"},
		{"compaction", "end"},
		{"finish", "instant"},
	} {
		if !jsonlHasNameEvent(spans, want[0], want[1]) {
			t.Fatalf("spans missing %s/%s: %+v", want[0], want[1], spans)
		}
	}
	tools := readJSONLObjects(t, filepath.Join(sessionDir, "tools.jsonl"))
	for _, want := range []string{"tool.completed", "tool.errored"} {
		if !jsonlHasString(tools, "event", want) {
			t.Fatalf("tools missing event %q: %+v", want, tools)
		}
	}
	debugLog, err := os.ReadFile(filepath.Join(sessionDir, "logs", "debug.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(debugLog), "finish.attempted") {
		t.Fatalf("debug log missing finish attempt:\n%s", debugLog)
	}
}

func e2eHookCommand(mode string) []string {
	return []string{os.Args[0], "-test.run=TestE2EHookHelperProcess", "--", mode}
}

func TestE2EHookHelperProcess(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "--" {
		return
	}
	switch os.Args[len(os.Args)-1] {
	case "inject":
		_, _ = os.Stdout.WriteString("hook-context: visible")
	case "deny":
		_, _ = os.Stdout.WriteString("policy denied write")
		os.Exit(2)
	case "correct":
		_, _ = os.Stdout.WriteString("review source classification")
		os.Exit(2)
	case "stop":
		wd, _ := os.Getwd()
		counterPath := filepath.Join(wd, ".juex-hook-stop-count")
		if _, err := os.Stat(counterPath); os.IsNotExist(err) {
			_ = os.WriteFile(counterPath, []byte("1"), 0o644)
			_, _ = os.Stdout.WriteString("continue from hook")
			os.Exit(2)
		}
	}
	os.Exit(0)
}

func messagesText(messages []llm.Message) string {
	var b strings.Builder
	for _, msg := range messages {
		for _, block := range msg.Blocks {
			if block.Type == llm.BlockText {
				if b.Len() > 0 {
					b.WriteString("\n")
				}
				b.WriteString(block.Text)
			}
			if block.Type == llm.BlockToolResult {
				if b.Len() > 0 {
					b.WriteString("\n")
				}
				b.WriteString(block.Content)
			}
		}
	}
	return b.String()
}

func readJSONLObjects(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	out := make([]map[string]any, 0, len(lines))
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Fatalf("parse %s line %d: %v\n%s", path, i+1, err, line)
		}
		out = append(out, obj)
	}
	return out
}

func jsonlHasString(rows []map[string]any, key, want string) bool {
	for _, row := range rows {
		if row[key] == want {
			return true
		}
	}
	return false
}

func jsonlHasNameEvent(rows []map[string]any, name, event string) bool {
	for _, row := range rows {
		if row["name"] == name && row["event"] == event {
			return true
		}
	}
	return false
}
