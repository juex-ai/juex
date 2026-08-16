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
	return append([]ContextSection(nil), m.sections...), m.contextErr
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
	installed := tools.NewRegistry()
	if err := catalog.Install(installed); err != nil {
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

func TestToolCatalogInstallsValidatedTools(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(testModule{id: "builtin", toolNames: []string{"read", "write"}}); err != nil {
		t.Fatal(err)
	}
	set, err := registry.Seal(context.Background(), ToolContext{})
	if err != nil {
		t.Fatal(err)
	}
	toolRegistry := tools.NewRegistry()
	if err := set.ToolCatalog().Install(toolRegistry); err != nil {
		t.Fatal(err)
	}
	if _, ok := toolRegistry.Get("read"); !ok {
		t.Fatal("read tool was not installed")
	}
	if _, ok := toolRegistry.Get("write"); !ok {
		t.Fatal("write tool was not installed")
	}
}

func TestInstallToolCatalogsRejectsCrossScopeDuplicatesWithBothOwners(t *testing.T) {
	runtimeRegistry := NewRegistry()
	if err := runtimeRegistry.Register(testModule{id: "runtime", toolNames: []string{"shared"}}); err != nil {
		t.Fatal(err)
	}
	runtimeSet, err := runtimeRegistry.Seal(context.Background(), ToolContext{})
	if err != nil {
		t.Fatal(err)
	}
	sessionRegistry := NewRegistry()
	if err := sessionRegistry.Register(testModule{id: "session", toolNames: []string{"shared"}}); err != nil {
		t.Fatal(err)
	}
	sessionSet, err := sessionRegistry.Seal(context.Background(), ToolContext{})
	if err != nil {
		t.Fatal(err)
	}
	err = InstallToolCatalogs(tools.NewRegistry(), runtimeSet, sessionSet)
	for _, want := range []string{`tool "shared"`, `module "runtime"`, `module "session"`} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("InstallToolCatalogs() error = %v, want %q", err, want)
		}
	}
}
