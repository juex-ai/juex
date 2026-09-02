package module

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/tools"
)

type testModule struct {
	id         ID
	sections   []ContextSection
	contextErr error
	toolNames  []string
	toolErr    error
}

type schemaToolModule struct {
	schema map[string]any
}

func (schemaToolModule) ID() ID { return "schema-tool" }

func (m schemaToolModule) Tools(context.Context, ToolContext) ([]tools.Tool, error) {
	return []tools.Tool{{
		Name:    "schema_tool",
		Schema:  m.schema,
		Handler: func(context.Context, map[string]any) (string, error) { return "ok", nil },
	}}, nil
}

func (m testModule) ID() ID { return m.id }

func (m testModule) Context(context.Context, ContextRequest) ([]ContextSection, error) {
	sections := append([]ContextSection(nil), m.sections...)
	for i := range sections {
		if sections[i].Source == "" {
			sections[i].Source = "test"
		}
		if sections[i].Projection == "" {
			sections[i].Projection = ContextProjectionSystemPrompt
		}
		if sections[i].Budget.Mode == "" {
			sections[i].Budget = UnboundedContextBudget()
		}
	}
	return sections, m.contextErr
}

func (m testModule) Tools(context.Context, ToolContext) ([]tools.Tool, error) {
	if m.toolErr != nil {
		return nil, m.toolErr
	}
	provided := make([]tools.Tool, 0, len(m.toolNames))
	for _, name := range m.toolNames {
		name := name
		provided = append(provided, tools.Tool{
			Name:    name,
			Handler: func(context.Context, map[string]any) (string, error) { return name, nil },
		})
	}
	return provided, nil
}

func TestRegistrySealsAllCapabilitiesInRegistrationOrder(t *testing.T) {
	registry := NewRegistry()
	for _, mod := range []Module{
		testModule{id: "first", sections: []ContextSection{{Key: "first", Text: "one"}}, toolNames: []string{"first_tool"}},
		testModule{id: "second", sections: []ContextSection{{Key: "empty"}, {Key: "second", Text: "two"}}, toolNames: []string{"second_tool"}},
	} {
		if err := registry.Register(mod); err != nil {
			t.Fatal(err)
		}
	}

	set, err := registry.Seal(context.Background(), ToolContext{})
	if err != nil {
		t.Fatal(err)
	}
	modules := set.Modules()
	if len(modules) != 2 || modules[0].ID() != "first" || modules[1].ID() != "second" {
		t.Fatalf("modules = %#v, want first then second", modules)
	}
	entries := set.ToolCatalog().Entries()
	if len(entries) != 2 || entries[0].ModuleID != "first" || entries[0].Tool.Name != "first_tool" || entries[1].ModuleID != "second" {
		t.Fatalf("tool entries = %#v", entries)
	}
	sections, err := set.Context(context.Background(), ContextRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(sections) != 2 || sections[0].Key != "first" || sections[0].ModuleID != "first" || sections[1].Key != "second" || sections[1].ModuleID != "second" {
		t.Fatalf("sections = %#v, want owned non-empty sections in module order", sections)
	}

	if err := registry.Register(testModule{id: "late"}); !errors.Is(err, ErrSealed) {
		t.Fatalf("Register() after Seal error = %v, want ErrSealed", err)
	}
	if got := set.Modules(); len(got) != 2 {
		t.Fatalf("sealed modules changed after rejected registration: %#v", got)
	}
}

func TestToolCatalogEntriesDeepCopySchemas(t *testing.T) {
	required := []string{"value"}
	schema := map[string]any{
		"type":     "object",
		"required": required,
		"properties": map[string]any{
			"value": map[string]any{"enum": []any{"original"}},
		},
	}
	registry := NewRegistry()
	if err := registry.Register(schemaToolModule{schema: schema}); err != nil {
		t.Fatal(err)
	}
	set, err := registry.Seal(context.Background(), ToolContext{})
	if err != nil {
		t.Fatal(err)
	}

	required[0] = "mutated-input"
	first := set.ToolCatalog().Entries()
	first[0].Tool.Schema["type"] = "array"
	first[0].Tool.Schema["required"].([]string)[0] = "mutated-output"
	first[0].Tool.Schema["properties"].(map[string]any)["value"].(map[string]any)["enum"].([]any)[0] = "mutated-output"

	second := set.ToolCatalog().Entries()
	if second[0].Tool.Schema["type"] != "object" {
		t.Fatalf("catalog type mutated through snapshot: %#v", second[0].Tool.Schema)
	}
	if got := second[0].Tool.Schema["required"].([]string); len(got) != 1 || got[0] != "value" {
		t.Fatalf("catalog required mutated through alias: %#v", got)
	}
	gotEnum := second[0].Tool.Schema["properties"].(map[string]any)["value"].(map[string]any)["enum"].([]any)
	if len(gotEnum) != 1 || gotEnum[0] != "original" {
		t.Fatalf("catalog enum mutated through snapshot: %#v", gotEnum)
	}

	catalog := set.ToolCatalog()
	installed, err := BuildToolRegistry(tools.RegistryOptions{}, set)
	if err != nil {
		t.Fatal(err)
	}
	installedTool, ok := installed.Get("schema_tool")
	if !ok {
		t.Fatal("installed schema_tool is missing")
	}
	installedTool.Schema["required"].([]string)[0] = "mutated-install"
	if got := catalog.Entries()[0].Tool.Schema["required"].([]string); len(got) != 1 || got[0] != "value" {
		t.Fatalf("catalog required mutated through installed registry: %#v", got)
	}
}

func TestRegistryRejectsInvalidAndDuplicateModuleIDs(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(nil); err == nil || !strings.Contains(err.Error(), "nil module") {
		t.Fatalf("nil module error = %v", err)
	}
	if err := registry.Register(testModule{id: "  "}); err == nil || !strings.Contains(err.Error(), "empty id") {
		t.Fatalf("empty id error = %v", err)
	}
	if err := registry.Register(testModule{id: "same"}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(testModule{id: " same "}); err == nil || !strings.Contains(err.Error(), `module "same"`) {
		t.Fatalf("duplicate error = %v", err)
	}
}

func TestSealRejectsDuplicateToolsWithBothOwners(t *testing.T) {
	registry := NewRegistry()
	for _, mod := range []Module{
		testModule{id: "builtin", toolNames: []string{"read"}},
		testModule{id: "extension", toolNames: []string{"read"}},
	} {
		if err := registry.Register(mod); err != nil {
			t.Fatal(err)
		}
	}
	_, err := registry.Seal(context.Background(), ToolContext{})
	for _, want := range []string{`tool "read"`, `module "builtin"`, `module "extension"`} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("Seal() error = %v, want %q", err, want)
		}
	}
}

func TestSealAttributesToolProviderError(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(testModule{id: "skills", toolErr: errors.New("load failed")}); err != nil {
		t.Fatal(err)
	}
	_, err := registry.Seal(context.Background(), ToolContext{})
	if err == nil || !strings.Contains(err.Error(), `module "skills" tools`) || !strings.Contains(err.Error(), "load failed") {
		t.Fatalf("Seal() error = %v", err)
	}
}

func TestContextRejectsDuplicateKeysWithBothOwners(t *testing.T) {
	registry := NewRegistry()
	for _, mod := range []Module{
		testModule{id: "skills", sections: []ContextSection{{Key: "catalog", Text: "one"}}},
		testModule{id: "runtime", sections: []ContextSection{{Key: "catalog", Text: "two"}}},
	} {
		if err := registry.Register(mod); err != nil {
			t.Fatal(err)
		}
	}
	set, err := registry.Seal(context.Background(), ToolContext{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = set.Context(context.Background(), ContextRequest{})
	for _, want := range []string{`context key "catalog"`, `module "skills"`, `module "runtime"`} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("Context() error = %v, want %q", err, want)
		}
	}
}

func TestContextAttributesProviderErrors(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(testModule{id: "skills", contextErr: errors.New("render failed")}); err != nil {
		t.Fatal(err)
	}
	set, err := registry.Seal(context.Background(), ToolContext{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = set.Context(context.Background(), ContextRequest{})
	if err == nil || !strings.Contains(err.Error(), `module "skills" context`) || !strings.Contains(err.Error(), "render failed") {
		t.Fatalf("Context() error = %v", err)
	}
}

type rawContextModule struct {
	id      ID
	section ContextSection
}

func (m rawContextModule) ID() ID { return m.id }

func (m rawContextModule) Context(context.Context, ContextRequest) ([]ContextSection, error) {
	return []ContextSection{m.section}, nil
}

func TestContextAssignsFrameworkMetadata(t *testing.T) {
	set, err := BuildRuntimeSet(context.Background(), []RuntimeFactorySpec{{
		ID:      "guidance",
		Enabled: true,
		New: func(context.Context, RuntimeContext) (Module, error) {
			return rawContextModule{id: "guidance", section: ContextSection{
				Key:        "agents",
				Source:     "project",
				Text:       "instructions",
				Projection: ContextProjectionSystemPrompt,
				Budget:     BoundedContextBudget(32),
			}}, nil
		},
	}}, RuntimeContext{}, ToolContext{})
	if err != nil {
		t.Fatal(err)
	}
	sections, err := set.Context(context.Background(), ContextRequest{Purpose: ContextPurposeProviderIteration})
	if err != nil {
		t.Fatal(err)
	}
	if len(sections) != 1 {
		t.Fatalf("sections = %#v", sections)
	}
	section := sections[0]
	if section.ModuleID != "guidance" || section.Scope != ScopeRuntime || section.Purpose != ContextPurposeProviderIteration {
		t.Fatalf("framework metadata = %#v", section)
	}
	if section.Budget.Mode != ContextBudgetBounded || section.Budget.MaxChars != 32 {
		t.Fatalf("budget = %#v", section.Budget)
	}
}

func TestContextRejectsInvalidContributionMetadata(t *testing.T) {
	tests := []struct {
		name    string
		section ContextSection
		want    string
	}{
		{name: "source", section: ContextSection{Key: "x", Text: "text", Projection: ContextProjectionSystemPrompt, Budget: UnboundedContextBudget()}, want: "empty source"},
		{name: "projection", section: ContextSection{Key: "x", Source: "test", Text: "text", Budget: UnboundedContextBudget()}, want: "invalid projection"},
		{name: "budget", section: ContextSection{Key: "x", Source: "test", Text: "text", Projection: ContextProjectionSystemPrompt}, want: "invalid budget"},
		{name: "bounded limit", section: ContextSection{Key: "x", Source: "test", Text: "text", Projection: ContextProjectionSystemPrompt, Budget: BoundedContextBudget(2)}, want: "exceeds max_chars 2"},
		{name: "scope", section: ContextSection{Key: "x", Source: "test", Text: "text", Projection: ContextProjectionSystemPrompt, Budget: UnboundedContextBudget(), Scope: ScopeThread}, want: "claims scope"},
		{name: "purpose", section: ContextSection{Key: "x", Source: "test", Text: "text", Projection: ContextProjectionSystemPrompt, Budget: UnboundedContextBudget(), Purpose: ContextPurposeThreadStart}, want: "claims purpose"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry := NewRegistry()
			if err := registry.Register(rawContextModule{id: "invalid", section: tt.section}); err != nil {
				t.Fatal(err)
			}
			set, err := registry.Seal(context.Background(), ToolContext{})
			if err != nil {
				t.Fatal(err)
			}
			set.scope = ScopeRuntime
			_, err = set.Context(context.Background(), ContextRequest{Purpose: ContextPurposeProviderIteration})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Context() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestBuildToolRegistryIsAtomicAcrossSets(t *testing.T) {
	runtimeSet := mustToolSet(t, ScopeRuntime, testModule{id: "runtime", toolNames: []string{"first"}})
	threadSet := mustToolSet(t, ScopeThread, testModule{id: "thread", toolNames: []string{"first"}})
	registry, err := BuildToolRegistry(tools.RegistryOptions{DefaultTimeoutSeconds: 17}, runtimeSet, threadSet)
	if err == nil || !strings.Contains(err.Error(), `tool "first"`) {
		t.Fatalf("BuildToolRegistry() error = %v", err)
	}
	if registry != nil {
		t.Fatalf("failed build returned partial registry: %#v", registry.List())
	}

	threadSet = mustToolSet(t, ScopeThread, testModule{id: "thread", toolNames: []string{"second"}})
	registry, err = BuildToolRegistry(tools.RegistryOptions{DefaultTimeoutSeconds: 17}, runtimeSet, threadSet)
	if err != nil {
		t.Fatal(err)
	}
	if got := registry.List(); len(got) != 2 || got[0].Name != "first" || got[1].Name != "second" {
		t.Fatalf("registry tools = %#v", got)
	}
	if got := registry.TimeoutSecondsFor("first"); got != 17 {
		t.Fatalf("default timeout = %d, want 17", got)
	}
}

func mustToolSet(t *testing.T, scope Scope, mod Module) *Set {
	t.Helper()
	registry := NewRegistry()
	if err := registry.Register(mod); err != nil {
		t.Fatal(err)
	}
	set, err := registry.Seal(context.Background(), ToolContext{})
	if err != nil {
		t.Fatal(err)
	}
	set.scope = scope
	return set
}
