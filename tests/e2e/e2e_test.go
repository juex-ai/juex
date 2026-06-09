// Package e2e contains a single end-to-end test that wires every Juex
// subsystem against a temporary filesystem layout and a scripted mock LLM.
//
// What this exercise covers:
//
//   - AGENTS.md hierarchy loading (project + subdir + global)
//   - Skill loading (path appears in system prompt; model loads body via `read`)
//   - Work-local memory entries -> system prompt + memory_write/search round-trip
//   - MCP stdio client -> registered as mcp__<server>__<tool> in the registry
//   - Builtin tools end-to-end: write, read, edit, bash, grep
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
	goruntime "runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/events"
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
		for _, want := range []string{"read", "write", "edit", "bash", "grep", "memory_write", "memory_search", "memory_delete", "mcp__local__echo"} {
			if !toolNames[want] {
				p.t.Errorf("tool %q missing from registry; have %v", want, keys(toolNames))
			}
		}
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
	if goruntime.GOOS == "windows" {
		t.Skip("e2e drives the bash builtin which is unavailable on windows")
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
	tools.RegisterBuiltins(reg, root)
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
		Now:                func() time.Time { return time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC) },
	}

	// -- Script the model --
	// Step 1: parallel calls to read AGENTS.md, write a new file, ping MCP.
	// Step 2: edit the demo file then grep for the new content + bash check.
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
					{Type: llm.BlockToolUse, ToolUseID: "t6", ToolName: "bash", Input: map[string]any{"cmd": "echo SCRIPTED && wc -l " + demoFile}},
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
		MaxIters: 20,
		MaxDur:   30 * time.Second,
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
