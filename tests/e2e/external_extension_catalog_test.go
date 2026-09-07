package e2e

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/app/modulecatalog"
	filetoolsmodule "github.com/juex-ai/juex/internal/features/filetools"

	goalmodule "github.com/juex-ai/juex/internal/features/goal"
	"github.com/juex-ai/juex/internal/features/mcp"

	notesmodule "github.com/juex-ai/juex/internal/features/notes"

	observable "github.com/juex-ai/juex/internal/features/observables"
	"github.com/juex-ai/juex/internal/features/skills"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/framework/agentstate"

	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
)

func TestExternalCatalogExtensionEnabledAndDisabled(t *testing.T) {
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
	extensionDir := filepath.Join(home, "extensions", "catalog")
	installCatalogExtensionFixture(t, extensionDir)
	probePath := filepath.Join(work, "module-catalog-probe.txt")
	if err := os.WriteFile(probePath, []byte("module catalog\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	provider := &bareScriptProvider{steps: []llm.Response{
		{
			Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
				{Type: llm.BlockToolUse, ToolUseID: "builtin-read", ToolName: "read", Input: map[string]any{"path": probePath}},
				{Type: llm.BlockToolUse, ToolUseID: "skill-search", ToolName: "skill_search", Input: map[string]any{"query": "catalog"}},
				{Type: llm.BlockToolUse, ToolUseID: "goal-get", ToolName: goalmodule.ToolGet, Input: map[string]any{}},
				{Type: llm.BlockToolUse, ToolUseID: "notes-update", ToolName: notesmodule.ToolUpdate, Input: map[string]any{"content": "- [x] exercise the Module catalog"}},
				{Type: llm.BlockToolUse, ToolUseID: "observable-list", ToolName: "observable_list", Input: map[string]any{}},
				{Type: llm.BlockToolUse, ToolUseID: "catalog-write", ToolName: "mcp__catalog__catalog_write", Input: map[string]any{
					"name": "isolated-home", "description": "test isolation", "type": "feedback", "body": "use a temporary JUEX_HOME",
				}},
			}},
			StopReason: llm.StopToolUse,
		},
		{
			Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{
				Type: llm.BlockToolUse, ToolUseID: "catalog-search", ToolName: "mcp__catalog__catalog_search", Input: map[string]any{"query": "isolated"},
			}}},
			StopReason: llm.StopToolUse,
		},
		{Message: llm.TextMessage(llm.RoleAssistant, "Module catalog flow complete"), StopReason: llm.StopEndTurn},
	}}

	cfg := config.Config{ModuleInventory: modulecatalog.Inventory(),
		ProviderID: "openai", APIKey: "test", Model: "test", WorkDir: work,
		HomeJuexDir: home, AgentAddress: address,
		Extensions: config.ExtensionPolicy{Allow: []string{"catalog"}, Configured: true},
	}
	enabled, err := app.New(app.Options{
		Config: cfg, Provider: provider, WorkDir: work,
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
		"mcp__catalog__catalog_write",
		"mcp__catalog__catalog_search",
		"mcp__catalog__catalog_delete",
	} {
		if _, ok := enabled.Engine.Tools.Get(name); !ok {
			t.Fatalf("enabled extension tool %q is missing", name)
		}
	}
	owners := make(map[string]runtimemodule.ID)
	for _, set := range []*runtimemodule.Set{enabled.Engine.RuntimeModules, enabled.Engine.ThreadRuntimeSnapshot().Modules} {
		for _, entry := range set.ToolCatalog().Entries() {
			owners[entry.Tool.Name] = entry.ModuleID
		}
	}
	for name, wantOwner := range map[string]runtimemodule.ID{
		"read":                         filetoolsmodule.ModuleID,
		"skill_search":                 skills.ModuleID,
		goalmodule.ToolGet:             goalmodule.ModuleID,
		notesmodule.ToolUpdate:         notesmodule.ModuleID,
		"observable_list":              observable.ModuleID,
		"mcp__catalog__catalog_write":  mcp.ModuleID,
		"mcp__catalog__catalog_search": mcp.ModuleID,
		"mcp__catalog__catalog_delete": mcp.ModuleID,
	} {
		if got := owners[name]; got != wantOwner {
			t.Fatalf("tool %q Module owner = %q, want %q", name, got, wantOwner)
		}
	}

	wantOffered := []string{
		"read",
		"skill_search",
		goalmodule.ToolGet,
		notesmodule.ToolUpdate,
		"observable_list",
		"mcp__catalog__catalog_write",
		"mcp__catalog__catalog_search",
	}
	result, err := enabled.Engine.Turn(t.Context(), "exercise every Module catalog family")
	if err != nil {
		t.Fatal(err)
	}
	if result != "Module catalog flow complete" {
		t.Fatalf("Turn result = %q", result)
	}
	if len(provider.history) != 3 {
		t.Fatalf("Provider calls = %d, want 3", len(provider.history))
	}
	assertProviderOfferedTools(t, provider, wantOffered)
	assertSuccessfulProviderToolResults(t, provider.history[len(provider.history)-1], map[string]string{
		"builtin-read":    "module catalog",
		"skill-search":    "catalog",
		"goal-get":        "",
		"notes-update":    "",
		"observable-list": "",
		"catalog-write":   "saved catalog",
		"catalog-search":  "temporary JUEX_HOME",
	})
	dataDir := filepath.Join(address.StateDir(), "extensions", "catalog")
	if marker, err := os.ReadFile(filepath.Join(dataDir, "hook-ran")); err != nil || string(marker) != "ThreadStart" {
		t.Fatalf("extension hook marker = %q, err=%v", marker, err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "catalog-entry")); err != nil {
		t.Fatalf("Catalog MCP did not write Agent-private extension data: %v", err)
	}
	promptText, err := enabled.Engine.SystemPromptWithError()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(promptText, "external-catalog-test") || !strings.Contains(promptText, "ext:catalog") {
		t.Fatalf("enabled extension skill is missing from prompt:\n%s", promptText)
	}
	var status app.RuntimeStatus
	err = enabled.ReadRuntimeModuleSnapshot(func(active app.RuntimeModuleSnapshot) error {
		var snapshotErr error
		status, snapshotErr = app.NewRuntimeCatalogService(cfg).Snapshot(app.RuntimeStatusOptions{ActiveModules: &active})
		return snapshotErr
	})
	if err != nil {
		t.Fatal(err)
	}
	catalogSkillSource := ""
	for _, skill := range status.Skills.Items {
		if skill.Name == "catalog" {
			catalogSkillSource = skill.Source
		}
	}
	if len(status.Extensions.Items) != 1 || status.Extensions.Items[0].Name != "catalog" ||
		catalogSkillSource != "ext:catalog" ||
		len(status.Hooks.Commands) != 1 || status.Hooks.Commands[0].Source != "ext:catalog" ||
		len(status.MCP.Servers) != 1 || status.MCP.Servers[0].Source != "ext:catalog" {
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
		Config: disabledCfg, Provider: &bareScriptProvider{}, WorkDir: work,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = disabled.CloseAndWait() })
	for _, name := range []string{
		"mcp__catalog__catalog_write",
		"mcp__catalog__catalog_search",
		"mcp__catalog__catalog_delete",
	} {
		if _, ok := disabled.Engine.Tools.Get(name); ok {
			t.Fatalf("disabled extension tool %q is still registered", name)
		}
	}
	disabledPrompt, err := disabled.Engine.SystemPromptWithError()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(disabledPrompt, "external-catalog-test") || strings.Contains(disabledPrompt, "ext:catalog") {
		t.Fatalf("disabled extension skill is still present:\n%s", disabledPrompt)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "hook-ran")); !os.IsNotExist(err) {
		t.Fatalf("disabled extension hook ran, stat err=%v", err)
	}
	var disabledStatus app.RuntimeStatus
	err = disabled.ReadRuntimeModuleSnapshot(func(active app.RuntimeModuleSnapshot) error {
		var snapshotErr error
		disabledStatus, snapshotErr = app.NewRuntimeCatalogService(disabledCfg).Snapshot(app.RuntimeStatusOptions{ActiveModules: &active})
		return snapshotErr
	})
	if err != nil {
		t.Fatal(err)
	}
	if disabledStatus.Extensions.Count != 0 || disabledStatus.MCP.Configured != 0 || disabledStatus.Hooks.Configured != 0 {
		t.Fatalf("disabled extension resources remain: %+v", disabledStatus)
	}
}

func assertProviderOfferedTools(t *testing.T, provider *bareScriptProvider, names []string) {
	t.Helper()
	if len(provider.tools) == 0 {
		t.Fatal("Provider received no Tool catalog")
	}
	offered := make(map[string]bool, len(provider.tools[0]))
	for _, name := range provider.tools[0] {
		offered[name] = true
	}
	for _, name := range names {
		if !offered[name] {
			t.Errorf("Provider Tool catalog is missing %q", name)
		}
	}
}

func assertSuccessfulProviderToolResults(t *testing.T, history []llm.Message, wants map[string]string) {
	t.Helper()
	found := make(map[string]bool, len(wants))
	for _, message := range history {
		for _, block := range message.Blocks {
			want, ok := wants[block.ToolUseID]
			if !ok || block.Type != llm.BlockToolResult {
				continue
			}
			found[block.ToolUseID] = true
			if block.IsError {
				t.Errorf("Tool result %q failed: %s", block.ToolUseID, block.Content)
			}
			if want != "" && !strings.Contains(block.Content, want) {
				t.Errorf("Tool result %q = %q, want content %q", block.ToolUseID, block.Content, want)
			}
		}
	}
	for id := range wants {
		if !found[id] {
			t.Errorf("Provider history is missing Tool result %q", id)
		}
	}
}

func installCatalogExtensionFixture(t *testing.T, extensionDir string) {
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
	write("juex.extension.json", `{"manifest_version":1,"name":"catalog","version":"1.0.0"}`, 0o600)
	write("mcp.json", `{"mcpServers":{"catalog":{"command":"${JUEX_EXT_DIR}/catalog-helper","args":["-test.run=TestExternalCatalogMCPHelperProcess"],"env":{"JUEX_E2E_CATALOG_MCP":"1"}}}}`, 0o600)
	write("hooks.yaml", `trusted: true
commands:
  - name: catalog-thread-start
    events: [ThreadStart]
    command: ["${JUEX_EXT_DIR}/catalog-helper", "-test.run=TestExternalCatalogHookHelperProcess"]
`, 0o600)
	write("skills/catalog/SKILL.md", `---
name: catalog
description: external-catalog-test
---
Use the external Catalog MCP tools.
`, 0o600)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	helperName := "catalog-helper"
	if runtime.GOOS == "windows" {
		helperName += ".exe"
	}
	write(helperName, string(body), 0o700)
}

func TestExternalCatalogHookHelperProcess(t *testing.T) {
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

func TestExternalCatalogMCPHelperProcess(t *testing.T) {
	if os.Getenv("JUEX_E2E_CATALOG_MCP") != "1" {
		return
	}
	dataDir := os.Getenv("JUEX_EXT_DATA_DIR")
	if dataDir == "" {
		fmt.Fprintln(os.Stderr, "fixture requires an Agent-private Extension data directory")
		os.Exit(2)
	}
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
				"serverInfo":      map[string]any{"name": "external-catalog-test", "version": "1.0.0"},
			}
		case "tools/list":
			result = map[string]any{"tools": catalogExtensionFixtureTools()}
		case "tools/call":
			params, _ := request["params"].(map[string]any)
			name, _ := params["name"].(string)
			arguments, _ := params["arguments"].(map[string]any)
			text := "ok"
			switch name {
			case "catalog_write":
				text = "saved catalog"
				_ = os.WriteFile(filepath.Join(dataDir, "catalog-entry"), []byte(fmt.Sprint(arguments["body"])), 0o600)
			case "catalog_search":
				body, _ := os.ReadFile(filepath.Join(dataDir, "catalog-entry"))
				text = string(body)
			case "catalog_delete":
				_ = os.Remove(filepath.Join(dataDir, "catalog-entry"))
				text = "deleted catalog"
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

func catalogExtensionFixtureTools() []map[string]any {
	object := map[string]any{"type": "object", "properties": map[string]any{}}
	return []map[string]any{
		{"name": "catalog_write", "description": "write", "inputSchema": object},
		{"name": "catalog_search", "description": "search", "inputSchema": object},
		{"name": "catalog_delete", "description": "delete", "inputSchema": object},
	}
}

var _ llm.Provider = (*bareScriptProvider)(nil)
