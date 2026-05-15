package runtime

import (
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/prompt"
)

func TestContextUsageSnapshotFallsBackToEstimatedInputWhenProviderOmitsInput(t *testing.T) {
	sections := []prompt.Section{
		{Key: "instructions", Text: strings.Repeat("system ", 40)},
		{Key: "skills", Text: strings.Repeat("skill ", 80)},
	}
	tools := []llm.ToolSpec{{
		Name:        "read",
		Description: strings.Repeat("tool ", 60),
		Schema:      map[string]any{"type": "object"},
	}}
	history := []llm.Message{
		llm.TextMessage(llm.RoleUser, strings.Repeat("message ", 100)),
	}

	got := contextUsageSnapshot("mock", 64000, llm.Usage{OutputTokens: 13}, sections, tools, history)

	estimatedInput := 0
	for _, part := range got.Breakdown {
		if part.Key != "response" {
			estimatedInput += part.Tokens
		}
	}
	if estimatedInput <= 0 {
		t.Fatalf("estimated input = %d, want positive", estimatedInput)
	}
	if got.InputTokens != estimatedInput {
		t.Fatalf("input tokens = %d, want estimated input %d", got.InputTokens, estimatedInput)
	}
	if got.OutputTokens != 13 {
		t.Fatalf("output tokens = %d, want 13", got.OutputTokens)
	}
	if got.TotalTokens != estimatedInput+13 {
		t.Fatalf("total tokens = %d, want estimated input + output %d", got.TotalTokens, estimatedInput+13)
	}
}
