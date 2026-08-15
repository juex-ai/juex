package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/agentstate"
	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/llm"
)

func TestExternalMemoryExtensionEnabledAndDisabled(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e is slow")
	}
	work := t.TempDir()
	home := t.TempDir()
	address, err := agentstate.NewAgentAddress(home, "abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(address.StateDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	extensionDir := filepath.Join(home, "extensions", "memory")
	installMemoryExtensionFixture(t, extensionDir)

	cfg := config.Config{
		ProviderID: "openai", APIKey: "test", Model: "test", WorkDir: work,
		HomeJuexDir: home, AgentAddress: address,
		Extensions: config.ExtensionPolicy{Allow: []string{"memory"}, Configured: true},
	}
	enabled, err := app.New(app.Options{
		Config: cfg, Provider: &bareScriptProvider{}, WorkDir: work,
	})
	if err != nil {
		t.Fatal(err)
	}
	enabledClosed := false
	t.Cleanup(func() {
		if !enabledClosed {
			_ = enabled.CloseAndWait()
		}
	})

	for _, name := range []string{
		"mcp__memory__memory_write",
		"mcp__memory__memory_search",
		"mcp__memory__memory_delete",
	} {
		if _, ok := enabled.Engine.Tools.Get(name); !ok {
			t.Fatalf("enabled extension tool %q is missing", name)
		}
	}
	dataDir := filepath.Join(address.StateDir(), "extensions", "memory")
	if marker, err := os.ReadFile(filepath.Join(dataDir, "hook-ran")); err != nil || string(marker) != "SessionStart" {
		t.Fatalf("extension hook marker = %q, err=%v", marker, err)
	}
	writeResult, err := enabled.Engine.Tools.Call(context.Background(), "mcp__memory__memory_write", map[string]any{
		"name": "isolated-home", "description": "test isolation", "type": "feedback", "body": "use a temporary JUEX_HOME",
	})
	if err != nil || !strings.Contains(writeResult, "saved memory") {
		t.Fatalf("memory_write result = %q, err=%v", writeResult, err)
	}
	searchResult, err := enabled.Engine.Tools.Call(context.Background(), "mcp__memory__memory_search", map[string]any{"query": "isolated"})
	if err != nil || !strings.Contains(searchResult, "temporary JUEX_HOME") {
		t.Fatalf("memory_search result = %q, err=%v", searchResult, err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "memory-entry")); err != nil {
		t.Fatalf("Memory MCP did not write Agent-private extension data: %v", err)
	}
	promptText, err := enabled.Engine.SystemPromptWithError()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(promptText, "external-memory-test") || !strings.Contains(promptText, "ext:memory") {
		t.Fatalf("enabled extension skill is missing from prompt:\n%s", promptText)
	}
	status, err := app.NewRuntimeCatalogService(cfg).Snapshot(app.RuntimeStatusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memorySkillSource := ""
	for _, skill := range status.Skills.Items {
		if skill.Name == "memory" {
			memorySkillSource = skill.Source
		}
	}
	if len(status.Extensions.Items) != 1 || status.Extensions.Items[0].Name != "memory" ||
		memorySkillSource != "ext:memory" ||
		len(status.Hooks.Commands) != 1 || status.Hooks.Commands[0].Source != "ext:memory" ||
		len(status.MCP.Servers) != 1 || status.MCP.Servers[0].Source != "ext:memory" {
		t.Fatalf("enabled extension sources = extensions:%+v skills:%+v hooks:%+v mcp:%+v", status.Extensions, status.Skills, status.Hooks, status.MCP)
	}
	if status.Hooks.Commands[0].Required {
		t.Fatalf("runtime hook policy = %+v, want optional", status.Hooks.Commands[0])
	}
	if err := enabled.CloseAndWait(); err != nil {
		t.Fatal(err)
	}
	enabledClosed = true
	if err := os.Remove(filepath.Join(dataDir, "hook-ran")); err != nil {
		t.Fatal(err)
	}

	disabledCfg := cfg
	disabledCfg.Extensions = config.ExtensionPolicy{Allow: []string{}, Configured: true}
	disabled, err := app.New(app.Options{
		Config: disabledCfg, Provider: &bareScriptProvider{}, WorkDir: work, SessionMode: app.SessionModeNewPrimary,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = disabled.CloseAndWait() })
	for _, name := range []string{
		"mcp__memory__memory_write",
		"mcp__memory__memory_search",
		"mcp__memory__memory_delete",
	} {
		if _, ok := disabled.Engine.Tools.Get(name); ok {
			t.Fatalf("disabled extension tool %q is still registered", name)
		}
	}
	disabledPrompt, err := disabled.Engine.SystemPromptWithError()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(disabledPrompt, "external-memory-test") || strings.Contains(disabledPrompt, "ext:memory") {
		t.Fatalf("disabled extension skill is still present:\n%s", disabledPrompt)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "hook-ran")); !os.IsNotExist(err) {
		t.Fatalf("disabled extension hook ran, stat err=%v", err)
	}
	disabledStatus, err := app.NewRuntimeCatalogService(disabledCfg).Snapshot(app.RuntimeStatusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if disabledStatus.Extensions.Count != 0 || disabledStatus.MCP.Configured != 0 || disabledStatus.Hooks.Configured != 0 {
		t.Fatalf("disabled extension resources remain: %+v", disabledStatus)
	}
}

func installMemoryExtensionFixture(t *testing.T, extensionDir string) {
	t.Helper()
	write := func(relative, body string, mode os.FileMode) {
		t.Helper()
		path := filepath.Join(extensionDir, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("juex.extension.json", `{"manifest_version":1,"name":"memory","version":"1.0.0"}`, 0o600)
	write("mcp.json", `{"mcpServers":{"memory":{"command":"${JUEX_EXT_DIR}/memory-helper","args":["-test.run=TestExternalMemoryMCPHelperProcess"],"env":{"JUEX_E2E_MEMORY_MCP":"1"}}}}`, 0o600)
	write("hooks.yaml", `trusted: true
commands:
  - name: memory-session-start
    events: [SessionStart]
    command: ["${JUEX_EXT_DIR}/memory-helper", "-test.run=TestExternalMemoryHookHelperProcess"]
`, 0o600)
	write("skills/memory/SKILL.md", `---
name: memory
description: external-memory-test
---
Use the external Memory MCP tools.
`, 0o600)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	helperName := "memory-helper"
	if runtime.GOOS == "windows" {
		helperName += ".exe"
	}
	write(helperName, string(body), 0o700)
}

func TestExternalMemoryHookHelperProcess(t *testing.T) {
	dataDir := os.Getenv("JUEX_EXT_DATA_DIR")
	if dataDir == "" {
		return
	}
	var request struct {
		EventName string `json:"event_name"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "hook-ran"), []byte(request.EventName), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}

func TestExternalMemoryMCPHelperProcess(t *testing.T) {
	if os.Getenv("JUEX_E2E_MEMORY_MCP") != "1" {
		return
	}
	dataDir := os.Getenv("JUEX_EXT_DATA_DIR")
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			continue
		}
		id, hasID := request["id"]
		if !hasID {
			continue
		}
		method, _ := request["method"].(string)
		var result any
		switch method {
		case "initialize":
			params, _ := request["params"].(map[string]any)
			version, _ := params["protocolVersion"].(string)
			result = map[string]any{
				"protocolVersion": version,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "external-memory-test", "version": "1.0.0"},
			}
		case "tools/list":
			result = map[string]any{"tools": memoryExtensionFixtureTools()}
		case "tools/call":
			params, _ := request["params"].(map[string]any)
			name, _ := params["name"].(string)
			arguments, _ := params["arguments"].(map[string]any)
			text := "ok"
			switch name {
			case "memory_write":
				text = "saved memory"
				_ = os.WriteFile(filepath.Join(dataDir, "memory-entry"), []byte(fmt.Sprint(arguments["body"])), 0o600)
			case "memory_search":
				body, _ := os.ReadFile(filepath.Join(dataDir, "memory-entry"))
				text = string(body)
			case "memory_delete":
				_ = os.Remove(filepath.Join(dataDir, "memory-entry"))
				text = "deleted memory"
			}
			result = map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": false}
		default:
			result = map[string]any{}
		}
		response := map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
		body, _ := json.Marshal(response)
		_, _ = os.Stdout.Write(append(body, '\n'))
	}
	os.Exit(0)
}

func memoryExtensionFixtureTools() []map[string]any {
	object := map[string]any{"type": "object", "properties": map[string]any{}}
	return []map[string]any{
		{"name": "memory_write", "description": "write", "inputSchema": object},
		{"name": "memory_search", "description": "search", "inputSchema": object},
		{"name": "memory_delete", "description": "delete", "inputSchema": object},
	}
}

var _ llm.Provider = (*bareScriptProvider)(nil)
