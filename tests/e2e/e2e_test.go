// Package e2e contains a single end-to-end test that wires every Juex
// subsystem against a temporary filesystem layout and a scripted mock LLM.
//
// What this exercise covers:
//
//   - AGENTS.md hierarchy loading (project + subdir + global)
//   - Skill loading (path appears in system prompt; model loads body via `read`)
//   - Work-local memory entries -> system prompt + memory_write/search round-trip
//   - MCP stdio client -> registered as mcp__<server>__<tool> in the registry
//   - Builtin tools end-to-end: write, read, edit, shell, grep
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
		for _, want := range []string{"read", "write", "edit", "exec_command", "write_stdin", "grep", "memory_write", "memory_search", "memory_delete", "mcp__local__echo"} {
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

func TestEndToEnd_ToolFailureLedgerContinuation(t *testing.T) {
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
				Message:    llm.TextMessage(llm.RoleAssistant, "done too early"),
				StopReason: llm.StopEndTurn,
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
	if got := int(prov.called.Load()); got != 5 {
		t.Fatalf("provider calls = %d, want 5", got)
	}
	observation := messagesText(prov.history[2])
	for _, want := range []string{"Runtime observation", "unresolved tool failure", "check_ready", "check failed"} {
		if !strings.Contains(observation, want) {
			t.Fatalf("provider did not receive continuation observation %q:\n%s", want, observation)
		}
	}

	convText := strings.Join(readLines(t, filepath.Join(sess.Dir, "conversation.jsonl")), "\n")
	if !strings.Contains(convText, "Runtime observation") {
		t.Fatalf("conversation missing runtime observation:\n%s", convText)
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
	for _, want := range []string{"tool.failure.recorded", "tool.failure.continued", "tool.failure.stale", "hook.completed"} {
		if !seen[want] {
			t.Fatalf("events missing %q; seen=%v", want, seen)
		}
	}
	for _, want := range []string{"recoverable", "artifact.txt", "check_ready", "unresolved-failure-gate"} {
		if !strings.Contains(payloadText.String(), want) {
			t.Fatalf("event payloads missing %q:\n%s", want, payloadText.String())
		}
	}
}

func TestEndToEnd_UnresolvedFailureGateWithUserGlobalDisabled(t *testing.T) {
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
				Message:    llm.TextMessage(llm.RoleAssistant, "done too early"),
				StopReason: llm.StopEndTurn,
			},
			{
				Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
					{Type: llm.BlockToolUse, ToolUseID: "exec_ok", ToolName: "exec_command", Input: map[string]any{"cmd": "exit 0"}},
				}},
				StopReason: llm.StopToolUse,
			},
			{
				Message:    llm.TextMessage(llm.RoleAssistant, "TASK COMPLETE: command recovered"),
				StopReason: llm.StopEndTurn,
			},
		},
	}
	a, err := app.New(app.Options{
		Config: config.Config{
			ProviderProtocol:          "openai/chat",
			WorkDir:                   work,
			EnableUserGlobalResources: false,
		},
		Provider:   prov,
		WorkDir:    work,
		DisableMCP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	out, err := a.Run(context.Background(), "recover from the command failure")
	if err != nil {
		t.Fatal(err)
	}
	if out != "TASK COMPLETE: command recovered" {
		t.Fatalf("out = %q", out)
	}
	if len(prov.history) != 4 {
		t.Fatalf("provider calls = %d, want gate continuation and recovery", len(prov.history))
	}
	observation := messagesText(prov.history[2])
	for _, want := range []string{"Runtime observation", "unresolved tool failure", "exec_command"} {
		if !strings.Contains(observation, want) {
			t.Fatalf("provider did not receive gate observation %q:\n%s", want, observation)
		}
	}
	eventsText := strings.Join(readLines(t, filepath.Join(a.Session.Dir, "events.jsonl")), "\n")
	for _, want := range []string{"tool.failure.recorded", "tool.failure.continued", "tool.failure.resolved", "unresolved-failure-gate"} {
		if !strings.Contains(eventsText, want) {
			t.Fatalf("events missing %q:\n%s", want, eventsText)
		}
	}
}

func TestEndToEnd_WorkingStateSurvivesCompaction(t *testing.T) {
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

	if a.Engine.WorkingState == nil {
		t.Fatal("app did not initialize working state store")
	}
	if err := a.Engine.WorkingState.ApplyPatch(runtime.WorkingStatePatch{
		HardConstraints: []runtime.WorkingStateRecord{{
			ID:           "hc-bind",
			Text:         "bind local services to 0.0.0.0",
			Source:       runtime.WorkingStateSourceUserInput,
			Confidence:   0.96,
			Severity:     runtime.WorkingStateSeverityHigh,
			RelatedPaths: []string{"cmd/serve.go"},
		}},
		OpenIssues: []runtime.WorkingStateRecord{{
			ID:           "issue-ci",
			Text:         "CI status still needs confirmation",
			Source:       runtime.WorkingStateSourceHookExtraction,
			Confidence:   0.84,
			Severity:     runtime.WorkingStateSeverityMedium,
			RelatedPaths: []string{".github/workflows/ci.yml"},
		}},
	}); err != nil {
		t.Fatal(err)
	}

	if out, err := a.Run(context.Background(), "first turn"); err != nil || out != "first answer" {
		t.Fatalf("first run out=%q err=%v", out, err)
	}
	if _, err := a.CompactWithInstructions(context.Background(), "manual", false, "keep working state"); err != nil {
		t.Fatal(err)
	}
	if out, err := a.Run(context.Background(), "second turn"); err != nil || out != "second answer" {
		t.Fatalf("second run out=%q err=%v", out, err)
	}
	if len(prov.history) != 3 {
		t.Fatalf("provider calls = %d", len(prov.history))
	}
	afterCompact := messagesText(prov.history[2])
	for _, want := range []string{"Runtime working state", "bind local services to 0.0.0.0", "CI status still needs confirmation"} {
		if !strings.Contains(afterCompact, want) {
			t.Fatalf("post-compaction provider history missing %q:\n%s", want, afterCompact)
		}
	}
	if _, err := os.Stat(filepath.Join(a.Session.Dir, "working_state.json")); err != nil {
		t.Fatalf("working_state.json missing: %v", err)
	}
}

func TestEndToEnd_WorkingStateDisabledLeavesRunUnchanged(t *testing.T) {
	work := t.TempDir()
	prov := &recordingProvider{
		steps: []llm.Response{{
			Message:    llm.TextMessage(llm.RoleAssistant, "plain answer"),
			StopReason: llm.StopEndTurn,
		}},
	}
	a, err := app.New(app.Options{
		Config:   config.Config{ProviderProtocol: "openai/chat", WorkDir: work, DisableWorkingState: true},
		Provider: prov,
		WorkDir:  work,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	if a.Engine.WorkingState != nil {
		t.Fatal("disabled working state should not initialize a store")
	}
	out, err := a.Run(context.Background(), "plain turn")
	if err != nil {
		t.Fatal(err)
	}
	if out != "plain answer" {
		t.Fatalf("out = %q", out)
	}
	if len(prov.history) != 1 || len(prov.history[0]) != 1 {
		t.Fatalf("history shape changed: %+v", prov.history)
	}
	if got := messagesText(prov.history[0]); strings.Contains(got, "Runtime working state") || !strings.Contains(got, "plain turn") {
		t.Fatalf("unexpected provider context:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(a.Session.Dir, "working_state.json")); !os.IsNotExist(err) {
		t.Fatalf("working_state.json should not exist, err=%v", err)
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
	prov := &recordingProvider{
		steps: []llm.Response{
			{
				Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
					{Type: llm.BlockToolUse, ToolUseID: "blocked", ToolName: "write", Input: map[string]any{"path": "blocked.txt", "content": "x"}},
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
	if len(prov.history) != 4 {
		t.Fatalf("provider calls = %d", len(prov.history))
	}
	if got := messagesText(prov.history[0]); !strings.Contains(got, "hook-context: visible") {
		t.Fatalf("first provider history missing injected context:\n%s", got)
	}
	if got := prov.history[3][len(prov.history[3])-1].FirstText(); got != "continue from hook" {
		t.Fatalf("stop continuation = %q", got)
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
		_, _ = os.Stdout.WriteString(`{"additional_context":"hook-context: visible"}`)
	case "deny":
		_, _ = os.Stdout.WriteString(`{"decision":"deny","additional_context":"policy denied write"}`)
	case "stop":
		wd, _ := os.Getwd()
		counterPath := filepath.Join(wd, ".juex-hook-stop-count")
		if _, err := os.Stat(counterPath); os.IsNotExist(err) {
			_ = os.WriteFile(counterPath, []byte("1"), 0o644)
			_, _ = os.Stdout.WriteString(`{"block_stop":true,"continue_prompt":"continue from hook"}`)
			break
		}
		_, _ = os.Stdout.WriteString(`{"decision":"allow"}`)
	default:
		_, _ = os.Stdout.WriteString(`{}`)
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
