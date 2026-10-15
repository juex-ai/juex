package tools

import (
	"context"
	"strings"
	"testing"
)

func TestResolveToolsGuidesAndExecutionBoundary(t *testing.T) {
	handler := func(context.Context, map[string]any) (string, error) { return "ran", nil }
	for _, enabled := range []bool{false, true} {
		input := []Tool{{Name: "guided", Group: ToolGroupThreadState, Guide: ToolGuide{Loader: "docs_load", Name: "test-guide"}, Description: "Self-contained operation.", Handler: handler}}
		if enabled {
			input = append(input, Tool{Name: "docs_load", Handler: handler})
		}
		resolved, err := ResolveTools(input)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(resolved[0].Description, `docs_load("test-guide")`) != enabled {
			t.Fatalf("guide with skills=%v: %s", enabled, resolved[0].Description)
		}
	}
	original := Tool{Name: "run", Handler: handler}
	original.ResolveDefinition = func(ToolAvailability) ToolDefinition {
		return ToolDefinition{Name: "renamed"}
	}
	if _, err := ResolveTools([]Tool{original}); err == nil {
		t.Fatal("definition adapter changed tool identity")
	}
}
