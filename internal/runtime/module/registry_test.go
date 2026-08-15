package module

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/tools"
)

type testModule struct {
	name      string
	sections  []PromptSection
	promptErr error
	toolName  string
	toolErr   error
}

func (m testModule) Name() string { return m.name }

func (m testModule) PromptContext() ([]PromptSection, error) {
	return append([]PromptSection(nil), m.sections...), m.promptErr
}

func (m testModule) RegisterTools(reg *tools.Registry) error {
	if m.toolErr != nil {
		return m.toolErr
	}
	if m.toolName == "" {
		return nil
	}
	return reg.Register(tools.Tool{
		Name:    m.toolName,
		Handler: func(_ context.Context, _ map[string]any) (string, error) { return m.toolName, nil },
	})
}

func TestRegistryPreservesEnabledModuleAndContributionOrder(t *testing.T) {
	registry := NewRegistry()
	for _, mod := range []Module{
		testModule{name: "first", sections: []PromptSection{{Key: "first", Text: "one"}}, toolName: "first_tool"},
		testModule{name: "second", sections: []PromptSection{{Key: "empty"}, {Key: "second", Text: "two"}}, toolName: "second_tool"},
	} {
		if err := registry.Register(mod, true); err != nil {
			t.Fatal(err)
		}
	}

	modules := registry.Modules()
	if len(modules) != 2 || modules[0].Name() != "first" || modules[1].Name() != "second" {
		t.Fatalf("modules = %#v, want first then second", modules)
	}
	sections, err := registry.PromptContext()
	if err != nil {
		t.Fatal(err)
	}
	if len(sections) != 2 || sections[0].Key != "first" || sections[1].Key != "second" {
		t.Fatalf("sections = %#v, want non-empty sections in module order", sections)
	}

	toolRegistry := tools.NewRegistry()
	if err := registry.RegisterTools(toolRegistry); err != nil {
		t.Fatal(err)
	}
	if _, ok := toolRegistry.Get("first_tool"); !ok {
		t.Fatal("first_tool was not registered")
	}
	if _, ok := toolRegistry.Get("second_tool"); !ok {
		t.Fatal("second_tool was not registered")
	}
}

func TestRegistrySkipsDisabledModule(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(testModule{
		name:      "disabled",
		sections:  []PromptSection{{Key: "disabled", Text: "hidden"}},
		promptErr: errors.New("should not run"),
		toolName:  "disabled_tool",
	}, false); err != nil {
		t.Fatal(err)
	}
	if modules := registry.Modules(); len(modules) != 0 {
		t.Fatalf("modules = %#v, want none", modules)
	}
	sections, err := registry.PromptContext()
	if err != nil || len(sections) != 0 {
		t.Fatalf("PromptContext() = %#v, %v; want empty success", sections, err)
	}
	toolRegistry := tools.NewRegistry()
	if err := registry.RegisterTools(toolRegistry); err != nil {
		t.Fatal(err)
	}
	if _, ok := toolRegistry.Get("disabled_tool"); ok {
		t.Fatal("disabled module registered a tool")
	}
}

func TestRegistryRejectsInvalidAndDuplicateModules(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(nil, true); err == nil || !strings.Contains(err.Error(), "nil module") {
		t.Fatalf("nil module error = %v", err)
	}
	if err := registry.Register(testModule{name: "  "}, true); err == nil || !strings.Contains(err.Error(), "empty name") {
		t.Fatalf("empty name error = %v", err)
	}
	if err := registry.Register(testModule{name: "same"}, true); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(testModule{name: " same "}, true); err == nil || !strings.Contains(err.Error(), "same") {
		t.Fatalf("duplicate error = %v", err)
	}
}

func TestRegistryAttributesCapabilityErrorsToModule(t *testing.T) {
	promptRegistry := NewRegistry()
	if err := promptRegistry.Register(testModule{name: "memory", promptErr: errors.New("read failed")}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := promptRegistry.PromptContext(); err == nil || !strings.Contains(err.Error(), `module "memory" prompt context`) || !strings.Contains(err.Error(), "read failed") {
		t.Fatalf("prompt error = %v", err)
	}

	toolRegistry := NewRegistry()
	if err := toolRegistry.Register(testModule{name: "memory", toolErr: errors.New("duplicate tool")}, true); err != nil {
		t.Fatal(err)
	}
	if err := toolRegistry.RegisterTools(tools.NewRegistry()); err == nil || !strings.Contains(err.Error(), `module "memory" register tools`) || !strings.Contains(err.Error(), "duplicate tool") {
		t.Fatalf("tool error = %v", err)
	}
}
