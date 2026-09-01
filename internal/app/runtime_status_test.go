package app

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/agentstate"
	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/hooks"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/mcp"
	"github.com/juex-ai/juex/internal/modules/builtintools"
	skillsmodule "github.com/juex-ai/juex/internal/modules/skills"
	"github.com/juex-ai/juex/internal/observable"
	juexruntime "github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/runtime/contextbudget"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/tools"
)

type runtimeStatusTestModule struct {
	id    runtimemodule.ID
	tools []tools.Tool
}

func (m runtimeStatusTestModule) ID() runtimemodule.ID { return m.id }

func (m runtimeStatusTestModule) Tools(context.Context, runtimemodule.ToolContext) ([]tools.Tool, error) {
	return append([]tools.Tool(nil), m.tools...), nil
}

func snapshotRuntimeStatus(t *testing.T, cfg config.Config, opts RuntimeStatusOptions) (RuntimeStatus, error) {
	t.Helper()
	manager, err := mcp.NewManagerLayeredSoft(context.Background(), nil, mcp.ConnectOptions{})
	if err != nil {
		return RuntimeStatus{}, err
	}
	a, err := New(Options{
		Config:     cfg,
		Provider:   &stubProvider{},
		WorkDir:    cfg.WorkDir,
		MCPManager: manager,
		DisableMCP: true,
	})
	if err != nil {
		_ = manager.Close()
		return RuntimeStatus{}, err
	}
	t.Cleanup(func() {
		_ = a.CloseAndWait()
		_ = manager.Close()
	})
	var status RuntimeStatus
	err = a.ReadRuntimeModuleSnapshot(func(snapshot RuntimeModuleSnapshot) error {
		opts.ActiveModules = &snapshot
		var snapshotErr error
		status, snapshotErr = NewRuntimeCatalogService(cfg).Snapshot(opts)
		return snapshotErr
	})
	return status, err
}

func mcpRuntimeStatusSnapshot(t *testing.T, serverTools map[string][]mcp.ToolDescriptor) RuntimeModuleSnapshot {
	t.Helper()
	var provided []tools.Tool
	for serverName, descriptors := range serverTools {
		for _, descriptor := range descriptors {
			provided = append(provided, tools.Tool{
				Name:        mcp.ToolName(serverName, descriptor.Name),
				Group:       tools.ToolGroupMCP,
				Description: descriptor.Description,
				Schema:      descriptor.InputSchema,
				Handler:     func(context.Context, map[string]any) (string, error) { return "", nil },
			})
		}
	}
	runtimeSet, err := runtimemodule.BuildRuntimeSet(context.Background(), []runtimemodule.RuntimeFactorySpec{{
		ID: mcp.ModuleID, Enabled: true,
		New: func(context.Context, runtimemodule.RuntimeContext) (runtimemodule.Module, error) {
			return runtimeStatusTestModule{id: mcp.ModuleID, tools: provided}, nil
		},
	}}, runtimemodule.RuntimeContext{}, runtimemodule.ToolContext{})
	if err != nil {
		t.Fatal(err)
	}
	threadSet, err := runtimemodule.BuildThreadSet(context.Background(), nil, runtimemodule.ThreadContext{}, runtimemodule.ToolContext{})
	if err != nil {
		t.Fatal(err)
	}
	return RuntimeModuleSnapshot{Runtime: runtimeSet, Thread: threadSet}
}

func TestRuntimeCatalogServiceProjectsBuiltinToolCatalog(t *testing.T) {
	work := t.TempDir()
	cfg := config.Config{WorkDir: work, ToolTimeout: 1500 * time.Millisecond}
	status, err := snapshotRuntimeStatus(t, cfg, RuntimeStatusOptions{})
	if err != nil {
		t.Fatal(err)
	}

	wantGroups := []tools.ToolGroup{
		tools.ToolGroupFile,
		tools.ToolGroupChunkedWrite,
		tools.ToolGroupShell,
		tools.ToolGroupSearch,
		tools.ToolGroupSkill,
		tools.ToolGroupThreadState,
		tools.ToolGroupWorkerThread,
		tools.ToolGroupObservable,
	}
	if len(status.Tools.Groups) != len(wantGroups) {
		t.Fatalf("tool groups = %#v, want %v", status.Tools.Groups, wantGroups)
	}
	count := 0
	for i, wantGroup := range wantGroups {
		group := status.Tools.Groups[i]
		if group.Group != string(wantGroup) {
			t.Fatalf("group[%d] = %q, want %q", i, group.Group, wantGroup)
		}
		names := make([]string, 0, len(group.Tools))
		for _, tool := range group.Tools {
			names = append(names, tool.Name)
		}
		if !sort.StringsAreSorted(names) {
			t.Fatalf("group %q tools are not sorted: %v", group.Group, names)
		}
		if wantGroup == tools.ToolGroupObservable {
			if len(names) != 7 || !containsString(names, "schedule_create") {
				t.Fatalf("observable tools = %v, want seven including schedule_create", names)
			}
		}
		count += len(group.Tools)
	}
	if status.Tools.Count != count || count != 34 {
		t.Fatalf("tool count = %d, grouped=%d, want 34", status.Tools.Count, count)
	}
}

func TestRuntimeStatusTierTwoToolsUseBuiltinGuidesWithinBudget(t *testing.T) {
	status, err := snapshotRuntimeStatus(t, config.Config{WorkDir: t.TempDir()}, RuntimeStatusOptions{})
	if err != nil {
		t.Fatal(err)
	}

	guides := map[string]string{
		string(tools.ToolGroupChunkedWrite): "juex-chunked-write",
		string(tools.ToolGroupThreadState):  "juex-thread-state",
		string(tools.ToolGroupObservable):   "juex-observables",
	}
	var specs []llm.ToolSpec
	for _, group := range status.Tools.Groups {
		guide, ok := guides[group.Group]
		if !ok {
			continue
		}
		for _, tool := range group.Tools {
			wantPointer := `Guide available via skill_load("` + guide + `").`
			if !strings.Contains(tool.Description, wantPointer) {
				t.Errorf("%s description missing guide availability pointer %q: %q", tool.Name, wantPointer, tool.Description)
			}
			if schemaContainsStringMetadata(tool.Schema, "description") {
				t.Errorf("%s schema retains descriptive prose: %#v", tool.Name, tool.Schema)
			}
			for _, key := range []string{"enum", "minimum", "maximum", "pattern", "maxLength"} {
				if schemaContainsMetadataKey(tool.Schema, key) {
					t.Errorf("%s schema retains %s constraint metadata: %#v", tool.Name, key, tool.Schema)
				}
			}
			specs = append(specs, llm.ToolSpec{Name: tool.Name, Description: tool.Description, Schema: tool.Schema})
		}
	}
	if len(specs) != 17 {
		t.Fatalf("Tier 2 tool count = %d, want 17", len(specs))
	}
	if got := contextbudget.EstimateToolTokens(specs); got > 1900 {
		t.Fatalf("Tier 2 tool estimate = %d tokens, want <= 1900", got)
	}
}

func schemaContainsMetadataKey(value any, key string) bool {
	switch typed := value.(type) {
	case map[string]any:
		for childKey, child := range typed {
			if childKey == key || schemaContainsMetadataKey(child, key) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if schemaContainsMetadataKey(child, key) {
				return true
			}
		}
	}
	return false
}

func schemaContainsStringMetadata(value any, key string) bool {
	switch typed := value.(type) {
	case map[string]any:
		for childKey, child := range typed {
			if childKey == key {
				if _, ok := child.(string); ok {
					return true
				}
			}
			if schemaContainsStringMetadata(child, key) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if schemaContainsStringMetadata(child, key) {
				return true
			}
		}
	}
	return false
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestRuntimeCatalogServiceCatalogMatchesRealAppRegistry(t *testing.T) {
	work := t.TempDir()
	cfg := config.Config{WorkDir: work, ToolTimeout: 1500 * time.Millisecond}
	a, err := New(Options{Config: cfg, Provider: &stubProvider{}, WorkDir: work, DisableMCP: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })

	var status RuntimeStatus
	err = a.ReadRuntimeModuleSnapshot(func(snapshot RuntimeModuleSnapshot) error {
		var snapshotErr error
		status, snapshotErr = NewRuntimeCatalogService(cfg).Snapshot(RuntimeStatusOptions{ActiveModules: &snapshot})
		return snapshotErr
	})
	if err != nil {
		t.Fatal(err)
	}
	var wantModules []RuntimeModuleStatus
	for _, set := range []*runtimemodule.Set{a.Engine.RuntimeModules, a.Engine.ThreadRuntimeSnapshot().Modules} {
		for _, descriptor := range set.Descriptors() {
			wantModules = append(wantModules, RuntimeModuleStatus{ID: string(descriptor.ID), Scope: string(descriptor.Scope)})
		}
	}
	if !reflect.DeepEqual(status.Modules, wantModules) {
		t.Fatalf("runtime Module status = %#v, want active descriptors %#v", status.Modules, wantModules)
	}
	type catalogEntry struct {
		group string
		info  RuntimeToolInfo
	}
	catalog := make(map[string]catalogEntry, status.Tools.Count)
	for _, group := range status.Tools.Groups {
		for _, info := range group.Tools {
			catalog[info.Name] = catalogEntry{group: group.Group, info: info}
		}
	}

	actualCount := 0
	for _, actual := range a.Engine.Tools.List() {
		if actual.Group == tools.ToolGroupMCP {
			continue
		}
		actualCount++
		entry, ok := catalog[actual.Name]
		if !ok {
			t.Errorf("registered tool %q missing from runtime catalog", actual.Name)
			continue
		}
		info := entry.info
		definition := actual.Definition()
		if info.Description != definition.Description || entry.group != string(definition.Group) || !reflect.DeepEqual(info.Schema, definition.Schema) {
			t.Errorf("catalog %q = %#v, registered definition = %#v", actual.Name, info, definition)
		}
		if info.Module == "" {
			t.Errorf("catalog %q has no module owner", actual.Name)
		}
		effective := tools.EffectiveToolTimeout(definition, durationSeconds(cfg.RuntimeLimits().ToolTimeout))
		if info.Timeout.Mode != string(effective.Mode) || info.Timeout.Seconds != effective.Seconds {
			t.Errorf("catalog %q timeout = %#v, want %#v", actual.Name, info.Timeout, effective)
		}
	}
	if actualCount != status.Tools.Count {
		t.Fatalf("registered non-MCP tools = %d, catalog count = %d", actualCount, status.Tools.Count)
	}
}

func TestAppServingToolRegistryMatchesSealedModuleCatalogs(t *testing.T) {
	work := t.TempDir()
	a, err := New(Options{
		Config:     config.Config{WorkDir: work},
		Provider:   &stubProvider{},
		WorkDir:    work,
		DisableMCP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })

	owners := make(map[string]runtimemodule.ID)
	for _, set := range []*runtimemodule.Set{a.Engine.RuntimeModules, a.Engine.ThreadRuntimeSnapshot().Modules} {
		for _, entry := range set.ToolCatalog().Entries() {
			if first, exists := owners[entry.Tool.Name]; exists {
				t.Fatalf("tool %q has duplicate catalog owners %q and %q", entry.Tool.Name, first, entry.ModuleID)
			}
			owners[entry.Tool.Name] = entry.ModuleID
		}
	}

	serving := a.Engine.Tools.List()
	if len(serving) != len(owners) {
		t.Fatalf("serving tools = %d, catalog tools = %d", len(serving), len(owners))
	}
	for _, tool := range serving {
		if _, ok := owners[tool.Name]; !ok {
			t.Errorf("serving tool %q is absent from sealed Module catalogs", tool.Name)
		}
	}

	for tool, wantOwner := range map[string]runtimemodule.ID{
		"read":            builtintools.ModuleID,
		"skill_search":    skillsmodule.ModuleID,
		"get_goal":        juexruntime.GoalModuleID,
		"update_notes":    juexruntime.NotesModuleID,
		"thread_create":   workerThreadModuleID,
		"observable_list": observable.ModuleID,
	} {
		if got := owners[tool]; got != wantOwner {
			t.Errorf("tool %q owner = %q, want %q", tool, got, wantOwner)
		}
	}
}

func TestAppDisabledModulesLeaveNoToolsOrCatalogEntries(t *testing.T) {
	work := t.TempDir()
	a, err := New(Options{
		Config: config.Config{WorkDir: work, Modules: config.ModulePolicy{
			string(workerThreadModuleID): {Enabled: false},
			string(observable.ModuleID):  {Enabled: false},
		}},
		Provider:   &stubProvider{},
		WorkDir:    work,
		DisableMCP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })

	owners := make(map[string]runtimemodule.ID)
	for _, set := range []*runtimemodule.Set{a.Engine.RuntimeModules, a.Engine.ThreadRuntimeSnapshot().Modules} {
		for _, entry := range set.ToolCatalog().Entries() {
			owners[entry.Tool.Name] = entry.ModuleID
			if entry.ModuleID == workerThreadModuleID || entry.ModuleID == observable.ModuleID {
				t.Errorf("disabled module %q contributed tool %q", entry.ModuleID, entry.Tool.Name)
			}
		}
	}
	for _, name := range []string{"thread_create", "observable_list"} {
		if _, ok := a.Engine.Tools.Get(name); ok {
			t.Errorf("disabled module tool %q is still serving", name)
		}
		if _, ok := owners[name]; ok {
			t.Errorf("disabled module tool %q remains in catalog", name)
		}
	}
}

func TestAppModuleConfigDisablesEveryCompiledModuleBeforeConstruction(t *testing.T) {
	work := t.TempDir()
	policy := config.ModulePolicy{}
	for _, id := range compiledModuleIDs() {
		policy[id] = config.ModuleSettings{Enabled: false}
	}
	a, err := New(Options{
		Config:   config.Config{WorkDir: work, Modules: policy},
		Provider: &stubProvider{},
		WorkDir:  work,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.CloseAndWait() })

	if descriptors := a.runtimeModules.Descriptors(); len(descriptors) != 0 {
		t.Fatalf("Runtime Modules = %#v, want none", descriptors)
	}
	if descriptors := a.Engine.ThreadRuntimeSnapshot().Modules.Descriptors(); len(descriptors) != 0 {
		t.Fatalf("Thread Modules = %#v, want none", descriptors)
	}
	if tools := a.Engine.Tools.List(); len(tools) != 0 {
		t.Fatalf("serving Tools = %#v, want none", tools)
	}
	if a.shellSessions != nil || a.workers != nil || a.obsv != nil || a.mcpManager != nil {
		t.Fatalf("disabled resources were constructed: shell=%p side=%p observable=%p mcp=%p", a.shellSessions, a.workers, a.obsv, a.mcpManager)
	}
	if err := a.ReadRuntimeModuleSnapshot(func(active RuntimeModuleSnapshot) error {
		status, statusErr := NewRuntimeCatalogService(a.cfg).Snapshot(RuntimeStatusOptions{ActiveModules: &active})
		if statusErr != nil {
			return statusErr
		}
		if len(status.Modules) != 0 || status.Tools.Count != 0 || status.MCP.Configured != 0 || status.Hooks.Configured != 0 || status.Skills.Count != 0 {
			t.Fatalf("disabled runtime status = %+v", status)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAppRejectsUnknownModuleConfigBeforeThreadSideEffects(t *testing.T) {
	work := t.TempDir()
	_, err := New(Options{
		Config: config.Config{
			WorkDir: work,
			Modules: config.ModulePolicy{
				"typo-module": {Enabled: false},
			},
		},
		Provider: &stubProvider{},
		WorkDir:  work,
	})
	if err == nil || !strings.Contains(err.Error(), `unsupported module "typo-module"`) {
		t.Fatalf("New() error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(work, ".juex", "threads")); !os.IsNotExist(statErr) {
		t.Fatalf("unknown Module config created Thread state: %v", statErr)
	}
}

func TestRuntimeToolsStatusRejectsInvalidBuiltinGroups(t *testing.T) {
	for _, group := range []tools.ToolGroup{"", tools.ToolGroupMCP, "unknown"} {
		t.Run(string(group), func(t *testing.T) {
			_, err := runtimeToolsStatusFromDefinitions([]tools.ToolDefinition{{
				Name:   "bad",
				Group:  group,
				Schema: map[string]any{"type": "object"},
			}}, tools.DefaultTimeoutSeconds)
			if err == nil {
				t.Fatalf("group %q unexpectedly accepted", group)
			}
		})
	}
}

func TestRuntimeMCPToolSchemaMatchesNormalizedRegistryDefinition(t *testing.T) {
	descriptor := mcp.ToolDescriptor{
		Name:        "nullable",
		Description: "Contains nullable schema fragments",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": nil,
			"properties": map[string]any{
				"query": nil,
				"items": map[string]any{
					"type":  "array",
					"items": nil,
				},
			},
		},
	}
	definition := tools.ToolDefinition{
		Name:        descriptor.Name,
		Group:       tools.ToolGroupMCP,
		Description: descriptor.Description,
		Schema:      descriptor.InputSchema,
	}
	registry := tools.NewRegistry()
	if err := registry.Register(definition.Bind(func(context.Context, map[string]any) (string, error) {
		return "", nil
	})); err != nil {
		t.Fatal(err)
	}
	registered, ok := registry.Get(descriptor.Name)
	if !ok {
		t.Fatal("normalized MCP tool was not registered")
	}

	active := mcpRuntimeStatusSnapshot(t, map[string][]mcp.ToolDescriptor{"catalog": {descriptor}})
	catalog := map[string]runtimemodule.ToolEntry{}
	for _, entry := range active.Runtime.ToolCatalog().Entries() {
		catalog[entry.Tool.Name] = entry
	}
	projected, err := runtimeMCPToolInfos("catalog", []mcp.ToolDescriptor{descriptor}, 0, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if len(projected) != 1 {
		t.Fatalf("projected tools = %#v", projected)
	}
	if projected[0].Module != string(mcp.ModuleID) {
		t.Fatalf("projected Module owner = %q, want %q", projected[0].Module, mcp.ModuleID)
	}
	if !reflect.DeepEqual(projected[0].Schema, registered.Schema) {
		t.Fatalf("catalog schema = %#v, registered schema = %#v", projected[0].Schema, registered.Schema)
	}
}

func TestRuntimeCatalogServiceIncludesPromptSkillsAndProvider(t *testing.T) {
	work := t.TempDir()
	homeAgents := t.TempDir()
	mustWriteRuntimeStatusFile(t, filepath.Join(work, "AGENTS.md"), "你好世界")
	mustWriteRuntimeStatusFile(t, filepath.Join(work, ".agents", "skills", "review", "SKILL.md"), `---
name: review
description: Review code changes
type: model-invocable
---
body`)
	tools := false
	cfg := config.Config{
		ProviderID:                "openai",
		ProviderProtocol:          "openai/responses",
		APIKey:                    "x",
		Model:                     "gpt-test",
		BaseURL:                   "https://example.test",
		ProviderCapabilities:      llm.CapabilityOverrides{Tools: &tools},
		WorkDir:                   work,
		HomeAgentsDir:             homeAgents,
		EnableUserAgentsResources: true,
	}

	status, err := snapshotRuntimeStatus(t, cfg, RuntimeStatusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if status.WorkDir != work {
		t.Fatalf("workdir = %q, want %q", status.WorkDir, work)
	}
	if status.Provider.ID != "openai" || status.Provider.Protocol != "openai/responses" || status.Provider.Model != "gpt-test" || status.Provider.Capabilities.Tools {
		t.Fatalf("provider = %+v", status.Provider)
	}
	if want := cfg.SandboxPolicy(); !reflect.DeepEqual(status.Sandbox, want) {
		t.Fatalf("sandbox = %+v, want %+v", status.Sandbox, want)
	}
	if status.Skills.Count != 4 || status.Skills.Items[0].Name != "review" || status.Skills.Items[0].Source != "project" {
		t.Fatalf("skills = %+v", status.Skills)
	}
	builtinNames := map[string]bool{}
	for _, skill := range status.Skills.Items {
		if skill.Source == "builtin" {
			builtinNames[skill.Name] = true
			if skill.Path != "builtin://skills/"+skill.Name+"/SKILL.md" {
				t.Fatalf("builtin skill path = %+v", skill)
			}
		}
	}
	for _, name := range []string{"juex-observables", "juex-thread-state", "juex-chunked-write"} {
		if !builtinNames[name] {
			t.Fatalf("runtime skills missing builtin %q: %+v", name, status.Skills.Items)
		}
	}
	for _, item := range status.SystemPrompt.Items {
		if item.Key == "skills" && strings.Contains(item.Text, "juex-observables") {
			t.Fatalf("builtin guide duplicated in prompt skill catalog: %+v", item)
		}
	}
	var agentsEntry *RuntimeSystemPromptEntry
	for i := range status.SystemPrompt.Items {
		if status.SystemPrompt.Items[i].Path == filepath.Join(work, "AGENTS.md") {
			agentsEntry = &status.SystemPrompt.Items[i]
			break
		}
	}
	if agentsEntry == nil {
		t.Fatalf("system prompt missing workspace AGENTS.md: %+v", status.SystemPrompt.Items)
		return
	}
	if !strings.Contains(agentsEntry.Text, "你好世界") {
		t.Fatalf("agents text = %q", agentsEntry.Text)
	}
	if agentsEntry.Tokens != juexruntime.EstimateTextTokens(agentsEntry.Text) {
		t.Fatalf("tokens = %d, want byte-based runtime estimate", agentsEntry.Tokens)
	}
}

func TestRuntimeCatalogServiceIncludesThreadScratchpadPrompt(t *testing.T) {
	work := t.TempDir()
	cfg := config.Config{WorkDir: work}
	status, err := snapshotRuntimeStatus(t, cfg, RuntimeStatusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range status.SystemPrompt.Items {
		if item.Key == "thread_scratchpad" {
			if item.Path == "" || !strings.Contains(item.Text, item.Path) {
				t.Fatalf("scratchpad prompt = %+v, want active Thread scratchpad path", item)
			}
			return
		}
	}
	t.Fatalf("system prompt missing Thread scratchpad: %+v", status.SystemPrompt.Items)
}

func TestRuntimeCatalogServiceMCPStatusSourcesAndOverrides(t *testing.T) {
	work := t.TempDir()
	homeAgents := t.TempDir()
	mustWriteRuntimeStatusFile(t, filepath.Join(homeAgents, "mcp.json"), `{
  "mcpServers": {
    "shared": { "command": "user-shared" },
    "zeta": { "command": "user-zeta" }
  }
}`)
	mustWriteRuntimeStatusFile(t, filepath.Join(work, ".agents", "mcp.json"), `{
  "mcpServers": {
    "alpha": { "command": "$WORKDIR/bin/alpha", "args": ["--workdir", "$WORKDIR"] },
    "shared": { "command": "project-shared" }
  }
}`)
	cfg := config.Config{
		WorkDir:                   work,
		HomeAgentsDir:             homeAgents,
		EnableUserAgentsResources: true,
	}

	descriptors := map[string][]mcp.ToolDescriptor{
		"shared": {
			{Name: "alpha", Description: "first", InputSchema: map[string]any{"type": "object"}},
			{Name: "zeta", Description: "last", InputSchema: map[string]any{"type": "object"}},
		},
	}
	active := mcpRuntimeStatusSnapshot(t, descriptors)
	status, err := NewRuntimeCatalogService(cfg).Snapshot(RuntimeStatusOptions{
		ActiveModules: &active,
		MCPToolDescriptors: map[string][]mcp.ToolDescriptor{
			"shared": descriptors["shared"],
		},
		MCPErrors: map[string]string{"zeta": "boom"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if status.MCP.Configured != 3 || status.MCP.Connected != 1 || status.MCP.Errors != 1 {
		t.Fatalf("mcp = %+v", status.MCP)
	}
	if len(status.MCP.Servers) != 3 {
		t.Fatalf("servers = %+v", status.MCP.Servers)
	}
	alpha, shared, zeta := status.MCP.Servers[0], status.MCP.Servers[1], status.MCP.Servers[2]
	if alpha.Name != "alpha" || alpha.Source != "project" || alpha.Type != "stdio" || alpha.URL != "" || filepath.ToSlash(alpha.Command) != filepath.ToSlash(work)+"/bin/alpha" || alpha.Args[0] != "--workdir" || alpha.Args[1] != work || alpha.Status != "not_started" {
		t.Fatalf("alpha = %+v", alpha)
	}
	if shared.Name != "shared" || shared.Source != "project" || shared.Command != "project-shared" || !shared.Connected || shared.ToolCount != 2 {
		t.Fatalf("shared = %+v", shared)
	}
	if zeta.Name != "zeta" || zeta.Source != "user" || zeta.Status != "error" || zeta.Error != "boom" {
		t.Fatalf("zeta = %+v", zeta)
	}
}

func TestRuntimeCatalogServiceMCPTransportMetadata(t *testing.T) {
	work := t.TempDir()
	mustWriteRuntimeStatusFile(t, filepath.Join(work, ".agents", "mcp.json"), `{
  "mcpServers": {
    "alias": { "type": "streamable-http", "url": "https://alias.example.com/mcp?token=alias-secret" },
    "local": { "command": "local-server", "args": ["--stdio"] },
    "remote": { "type": "http", "url": "https://remote.example.com/mcp?token=remote-secret" }
  }
}`)

	status, err := snapshotRuntimeStatus(t, config.Config{WorkDir: work}, RuntimeStatusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(status.MCP.Servers) != 3 {
		t.Fatalf("servers = %+v", status.MCP.Servers)
	}
	alias, local, remote := status.MCP.Servers[0], status.MCP.Servers[1], status.MCP.Servers[2]
	if alias.Name != "alias" || alias.Type != "http" || alias.URL != "https://alias.example.com/mcp" || alias.Command != "" || len(alias.Args) != 0 {
		t.Fatalf("alias = %+v", alias)
	}
	if local.Name != "local" || local.Type != "stdio" || local.URL != "" || local.Command != "local-server" || !reflect.DeepEqual(local.Args, []string{"--stdio"}) {
		t.Fatalf("local = %+v", local)
	}
	if remote.Name != "remote" || remote.Type != "http" || remote.URL != "https://remote.example.com/mcp" || remote.Command != "" || len(remote.Args) != 0 {
		t.Fatalf("remote = %+v", remote)
	}
}

func TestRuntimeCatalogServiceTreatsZeroToolDescriptorMembershipAsConnected(t *testing.T) {
	work := t.TempDir()
	mustWriteRuntimeStatusFile(t, filepath.Join(work, ".agents", "mcp.json"), `{
  "mcpServers": {
    "empty": { "command": "empty-server" }
  }
}`)
	descriptors := map[string][]mcp.ToolDescriptor{"empty": {}}
	active := mcpRuntimeStatusSnapshot(t, descriptors)
	status, err := NewRuntimeCatalogService(config.Config{WorkDir: work}).Snapshot(RuntimeStatusOptions{
		ActiveModules:      &active,
		MCPToolDescriptors: map[string][]mcp.ToolDescriptor{"empty": {}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if status.MCP.Connected != 1 || len(status.MCP.Servers) != 1 {
		t.Fatalf("mcp = %+v", status.MCP)
	}
	server := status.MCP.Servers[0]
	if !server.Connected || server.Status != "connected" || server.ToolCount != 0 || len(server.Tools) != 0 {
		t.Fatalf("zero-tool server = %+v", server)
	}
}

func TestRuntimeCatalogServiceIncludesHookStatus(t *testing.T) {
	cfg := config.Config{
		WorkDir: t.TempDir(),
		Hooks: hooks.Config{Commands: []hooks.CommandHook{{
			Name:    "protect-write",
			Events:  []hooks.EventName{hooks.EventPreToolUse, hooks.EventStop},
			Tools:   []string{"write"},
			Command: []string{"python3", "hooks/protect.py"},
			Source:  "project",
		}}},
	}

	status, err := snapshotRuntimeStatus(t, cfg, RuntimeStatusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if status.Hooks.Configured != 1 || len(status.Hooks.Commands) != 1 {
		t.Fatalf("hooks = %+v", status.Hooks)
	}
	hook := status.Hooks.Commands[0]
	if hook.Name != "protect-write" || hook.Source != "project" {
		t.Fatalf("hook identity = %+v", hook)
	}
	if strings.Join(hook.Events, ",") != "PreToolUse,Stop" {
		t.Fatalf("events = %+v", hook.Events)
	}
	if strings.Join(hook.Tools, ",") != "write" || strings.Join(hook.Command, " ") != "python3 hooks/protect.py" {
		t.Fatalf("hook command = %+v tools=%+v", hook.Command, hook.Tools)
	}
	if hook.TimeoutSeconds != hooks.DefaultTimeoutSeconds || hook.MaxOutputBytes != hooks.DefaultMaxOutputBytes {
		t.Fatalf("effective limits = timeout %d output %d", hook.TimeoutSeconds, hook.MaxOutputBytes)
	}
}

func TestRuntimeCatalogServiceIncludesSandboxPolicy(t *testing.T) {
	cfg := config.Config{
		WorkDir: t.TempDir(),
		Sandbox: config.SandboxPolicy{
			Enabled: true,
			FileSystem: config.FileSystemSandboxPolicy{
				OutsideWorkspace: config.OutsideWorkspaceReadOnly,
			},
			Network: config.NetworkSandboxPolicy{Enabled: false},
		},
	}

	status, err := snapshotRuntimeStatus(t, cfg, RuntimeStatusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !status.Sandbox.Enabled || status.Sandbox.FileSystem.OutsideWorkspace != config.OutsideWorkspaceReadOnly || status.Sandbox.Network.Enabled {
		t.Fatalf("sandbox = %+v", status.Sandbox)
	}
}

func TestRuntimeCatalogServiceIncludesSelectedExtensionMetadataAndDefinitionCounts(t *testing.T) {
	work := t.TempDir()
	address, err := agentstate.NewAgentAddress(t.TempDir(), "abcdef")
	if err != nil {
		t.Fatal(err)
	}
	extensionDir := filepath.Join(work, ".juex", "extensions", "demo")
	mustWriteRuntimeStatusFile(t, filepath.Join(extensionDir, "juex.extension.json"), `{
  "manifest_version": 1,
  "name": "demo",
  "version": "1.2.3",
  "description": "Demo integration"
}`)
	for _, name := range []string{"alpha", "beta"} {
		mustWriteRuntimeStatusFile(t, filepath.Join(extensionDir, "skills", name, "SKILL.md"), "---\nname: "+name+"\ndescription: "+name+"\n---\nbody")
	}
	mustWriteRuntimeStatusFile(t, filepath.Join(extensionDir, "juex.extension.json"), `{
  "manifest_version":1,
  "name":"demo",
  "version":"1.2.3",
  "description":"Demo integration",
	"future_metadata":{"ignored":true},
	"requirements":[
		{"name":"Demo CLI","description":"Install the Demo CLI.","url":"https://example.com/demo-cli","future_metadata":true},
		{"name":"Demo account","description":"Create a Demo account.","url":"https://example.com/signup"}
	],
  "agent":{"environment":{"variables":{"DEMO_RUNTIME_DEFAULT":"${JUEX_EXT_DATA_DIR}"}}}
}`)
	mustWriteRuntimeStatusFile(t, filepath.Join(extensionDir, "mcp.json"), `{
  "mcpServers": {
    "alpha": {"command":"alpha"},
    "beta": {"command":"beta"}
  }
}`)
	mustWriteRuntimeStatusFile(t, filepath.Join(extensionDir, "hooks.yaml"), `trusted: true
commands:
  - name: alpha
    events: [Stop]
    command: ["alpha"]
  - name: beta
    events: [Stop]
    command: ["beta"]
`)
	mustWriteRuntimeStatusFile(t, filepath.Join(extensionDir, "observables.json"), `{
  "observables": [
    {"id":"alpha","type":"command","command_config":{"command":"alpha"}},
    {"id":"beta","type":"command","command_config":{"command":"beta"}}
  ]
}`)

	cfg := config.Config{
		WorkDir:      work,
		AgentAddress: address,
		Extensions:   allowExtensions("demo"),
		Skills:       config.SkillsConfig{Include: []string{"alpha"}},
	}
	status, err := snapshotRuntimeStatus(t, cfg, RuntimeStatusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if status.Extensions.Count != 1 || len(status.Extensions.Items) != 1 {
		t.Fatalf("extensions = %+v", status.Extensions)
	}
	ext := status.Extensions.Items[0]
	if ext.Name != "demo" || ext.Version != "1.2.3" || ext.Description != "Demo integration" || ext.Scope != "project" || ext.Path != extensionDir || ext.ManifestVersion != 1 {
		t.Fatalf("extension metadata = %+v", ext)
	}
	if len(ext.Requirements) != 2 || ext.Requirements[0].Name != "Demo CLI" || ext.Requirements[0].Description != "Install the Demo CLI." || ext.Requirements[0].URL != "https://example.com/demo-cli" || ext.Requirements[1].Name != "Demo account" {
		t.Fatalf("extension requirements = %+v", ext.Requirements)
	}
	if ext.Resources.Skills != 1 || ext.Resources.MCPServers != 2 || ext.Resources.Hooks != 2 || ext.Resources.Observables != 2 {
		t.Fatalf("extension resource counts = %+v", ext.Resources)
	}
	if len(ext.Environment) != 1 || ext.Environment[0].Name != "DEMO_RUNTIME_DEFAULT" || ext.Environment[0].Source != "ext:demo" || ext.Environment[0].Status != "effective" {
		t.Fatalf("extension environment diagnostics = %+v", ext.Environment)
	}
	if len(status.Skills.Filtered) != 1 || status.Skills.Filtered[0].Name != "beta" {
		t.Fatalf("filtered skills = %+v, want beta excluded from effective Extension count", status.Skills.Filtered)
	}
}

func TestRuntimeCatalogServiceReadsFrozenActiveSkillModule(t *testing.T) {
	work := t.TempDir()
	skillPath := filepath.Join(work, ".agents", "skills", "review", "SKILL.md")
	mustWriteRuntimeStatusFile(t, skillPath, `---
name: review
description: cached
---
body`)
	cfg := config.Config{WorkDir: work}
	manager, err := mcp.NewManagerLayeredSoft(context.Background(), nil, mcp.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(Options{Config: cfg, Provider: &stubProvider{}, WorkDir: work, MCPManager: manager, DisableMCP: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = a.CloseAndWait()
		_ = manager.Close()
	})
	snapshot := func() RuntimeStatus {
		var status RuntimeStatus
		if err := a.ReadRuntimeModuleSnapshot(func(active RuntimeModuleSnapshot) error {
			var snapshotErr error
			status, snapshotErr = NewRuntimeCatalogService(cfg).Snapshot(RuntimeStatusOptions{ActiveModules: &active})
			return snapshotErr
		}); err != nil {
			t.Fatal(err)
		}
		return status
	}
	first := snapshot()
	mustWriteRuntimeStatusFile(t, skillPath, `---
name: review
description: changed
---
body`)
	second := snapshot()
	if first.Skills.Items[0].Description != "cached" || second.Skills.Items[0].Description != "cached" {
		t.Fatalf("skills cache did not preserve first load: first=%+v second=%+v", first.Skills.Items, second.Skills.Items)
	}
}

func TestRuntimeCatalogServiceRejectsExtensionResourceDuplicates(t *testing.T) {
	t.Run("mcp", func(t *testing.T) {
		work := t.TempDir()
		mustWriteRuntimeStatusFile(t, filepath.Join(work, ".agents", "mcp.json"), `{
  "mcpServers": {
    "shared": { "command": "project" }
  }
}`)
		mustWriteRuntimeStatusFile(t, filepath.Join(work, ".juex", "extensions", "demo", "mcp.json"), `{
  "mcpServers": {
    "shared": { "command": "extension" }
  }
}`)
		_, err := snapshotRuntimeStatus(t, config.Config{WorkDir: work, Extensions: allowExtensions("demo")}, RuntimeStatusOptions{})
		if err == nil || !strings.Contains(err.Error(), `duplicate MCP server "shared"`) {
			t.Fatalf("err = %v, want duplicate MCP error", err)
		}
	})

	t.Run("skill", func(t *testing.T) {
		work := t.TempDir()
		mustWriteRuntimeStatusFile(t, filepath.Join(work, ".agents", "skills", "shared", "SKILL.md"), `---
name: shared
description: project
---
body`)
		mustWriteRuntimeStatusFile(t, filepath.Join(work, ".juex", "extensions", "demo", "skills", "shared", "SKILL.md"), `---
name: shared
description: extension
---
body`)
		_, err := snapshotRuntimeStatus(t, config.Config{WorkDir: work, Extensions: allowExtensions("demo")}, RuntimeStatusOptions{})
		if err == nil || !strings.Contains(err.Error(), `duplicate skill "shared"`) {
			t.Fatalf("err = %v, want duplicate skill error", err)
		}
	})

	t.Run("hook", func(t *testing.T) {
		work := t.TempDir()
		mustWriteRuntimeStatusFile(t, filepath.Join(work, ".juex", "extensions", "demo", "hooks.yaml"), `trusted: true
commands:
  - name: shared
    events: [Stop]
    command: ["python3", "x.py"]
`)
		cfg := config.Config{
			WorkDir:    work,
			Extensions: allowExtensions("demo"),
			Hooks: hooks.Config{Commands: []hooks.CommandHook{{
				Name:    "shared",
				Events:  []hooks.EventName{hooks.EventStop},
				Command: []string{"python3", "base.py"},
				Source:  "project",
			}}},
		}
		_, err := snapshotRuntimeStatus(t, cfg, RuntimeStatusOptions{})
		if err == nil || !strings.Contains(err.Error(), `duplicate hook "shared"`) {
			t.Fatalf("err = %v, want duplicate hook error", err)
		}
	})
}

func mustWriteRuntimeStatusFile(t *testing.T, path, body string) {
	t.Helper()
	ensureRuntimeStatusExtensionManifest(t, path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func ensureRuntimeStatusExtensionManifest(t *testing.T, path string) {
	t.Helper()
	if filepath.Base(path) == "juex.extension.json" {
		return
	}
	dir := filepath.Dir(path)
	for {
		if filepath.Base(filepath.Dir(dir)) == "extensions" {
			manifestPath := filepath.Join(dir, "juex.extension.json")
			if _, err := os.Stat(manifestPath); err == nil {
				return
			} else if !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			body := `{"manifest_version":1,"name":"` + filepath.Base(dir) + `","version":"1.0.0"}`
			if err := os.WriteFile(manifestPath, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return
		}
		dir = parent
	}
}
