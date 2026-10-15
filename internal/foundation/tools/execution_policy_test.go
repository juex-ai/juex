package tools

import (
	"context"
	"testing"
)

func TestExecutionPolicySurvivesDefinitionPipeline(t *testing.T) {
	for _, policy := range []ToolExecutionPolicy{ToolExecutionParallel, ToolExecutionSerial} {
		definition := ToolDefinition{Name: "probe", ExecutionPolicy: policy}
		for _, tool := range []Tool{
			definition.Bind(func(context.Context, map[string]any) (string, error) { return "ok", nil }),
			definition.BindResult(func(context.Context, map[string]any) (Result, error) { return Result{Text: "ok"}, nil }),
		} {
			tool.ResolveDefinition = func(ToolAvailability) ToolDefinition { return definition }
			resolved, err := ResolveTools([]Tool{tool.Clone()})
			if err != nil {
				t.Fatal(err)
			}
			registry := NewRegistry()
			if err := registry.Register(resolved[0]); err != nil {
				t.Fatal(err)
			}
			got, _ := registry.Get("probe")
			if got.Definition().Normalized().ExecutionPolicy != policy {
				t.Fatalf("execution policy lost: %+v", got.Definition())
			}
		}
	}
}

func TestExecutionPolicyRejectsInvalidOrAdaptedPolicy(t *testing.T) {
	definition := ToolDefinition{Name: "probe", ExecutionPolicy: ToolExecutionSerial}
	tool := definition.Bind(func(context.Context, map[string]any) (string, error) { return "ok", nil })
	tool.ResolveDefinition = func(ToolAvailability) ToolDefinition {
		changed := definition
		changed.ExecutionPolicy = ToolExecutionParallel
		return changed
	}
	if _, err := ResolveTools([]Tool{tool}); err == nil {
		t.Fatal("adapter changed execution policy")
	}
	tool.ExecutionPolicy = ToolExecutionPolicy(99)
	if err := NewRegistry().Register(tool); err == nil {
		t.Fatal("unknown execution policy accepted")
	}
}
