package module

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/llm"
)

type historyProjector struct {
	id      ID
	project func(context.Context, []ToolResultPair) (ProviderHistoryPlan, error)
}

func (m *historyProjector) ID() ID { return m.id }
func (m *historyProjector) ProjectProviderHistory(ctx context.Context, pairs []ToolResultPair, _ ProviderHistoryBudget) (ProviderHistoryPlan, error) {
	return m.project(ctx, pairs)
}

func projectionHistory() []llm.Message {
	return []llm.Message{
		{ID: "assistant", Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockText, Text: "Keep reasoning context."},
			{Type: llm.BlockToolUse, ToolUseID: "owned", ToolName: "large", Input: map[string]any{"content": "large input"}},
			{Type: llm.BlockToolUse, ToolUseID: "other", ToolName: "small"},
		}},
		{ID: "results", Role: llm.RoleUser, Blocks: []llm.Block{
			{Type: llm.BlockToolResult, ToolUseID: "owned", ToolName: "large", Content: "large result", ResultFact: &llm.ResultFact{Owner: "custom", Data: json.RawMessage(`{"kind":"complete"}`)}},
			{Type: llm.BlockToolResult, ToolUseID: "other", ToolName: "small", Content: "untouched", ResultFact: &llm.ResultFact{Owner: "other"}},
		}},
	}
}

func projectorSet(t *testing.T, project func(context.Context, []ToolResultPair) (ProviderHistoryPlan, error)) *Set {
	t.Helper()
	r := NewRegistry()
	if err := r.Register(&historyProjector{id: "custom", project: project}); err != nil {
		t.Fatal(err)
	}
	s, err := r.Seal(t.Context(), ToolContext{})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestProviderHistoryProjectionPreservesSourceAndOtherToolPairs(t *testing.T) {
	history := projectionHistory()
	before, _ := json.Marshal(history)
	set := projectorSet(t, func(_ context.Context, pairs []ToolResultPair) (ProviderHistoryPlan, error) {
		if len(pairs) != 1 || pairs[0].Use.ToolUseID != "owned" {
			t.Fatalf("pairs=%+v", pairs)
		}
		pairs[0].Use.Input["content"] = "attempted mutation"
		pairs[0].Result.ResultFact.Data[0] = 'x'
		return ProviderHistoryPlan{Omit: []string{"owned"}, Summaries: []ToolSummary{{ToolUseID: "owned", Text: "bounded summary"}}}, nil
	})
	got, err := ProjectProviderHistory(t.Context(), history, ProviderHistoryBudget{MaxBytes: 1000, MaxTokens: 1000, EstimateTokens: func(text string) int { return len(text) }}, set)
	if err != nil {
		t.Fatal(err)
	}
	if err := llm.ValidateToolTranscript(got); err != nil {
		t.Fatal(err)
	}
	if len(got[1].Blocks) != 2 || got[1].Blocks[0].ToolUseID != "other" || got[1].Blocks[1].Text != "bounded summary" {
		t.Fatalf("result order=%+v", got[1].Blocks)
	}
	if !reflect.DeepEqual(got[0].Blocks[0], history[0].Blocks[0]) {
		t.Fatal("non-tool content changed")
	}
	after, _ := json.Marshal(history)
	if string(before) != string(after) {
		t.Fatal("projection mutated source facts")
	}
}

func TestProviderHistoryProjectionRejectsInvalidContribution(t *testing.T) {
	for name, plan := range map[string]ProviderHistoryPlan{
		"foreign owner":      {Omit: []string{"other"}},
		"unknown pair":       {Omit: []string{"missing"}},
		"duplicate omission": {Omit: []string{"owned", "owned"}},
		"unselected anchor":  {Summaries: []ToolSummary{{ToolUseID: "owned", Text: "summary"}}},
		"duplicate anchor":   {Omit: []string{"owned"}, Summaries: []ToolSummary{{ToolUseID: "owned", Text: "a"}, {ToolUseID: "owned", Text: "b"}}},
		"oversized summary":  {Omit: []string{"owned"}, Summaries: []ToolSummary{{ToolUseID: "owned", Text: strings.Repeat("large", 100)}}},
	} {
		t.Run(name, func(t *testing.T) {
			history := projectionHistory()
			before, _ := json.Marshal(history)
			set := projectorSet(t, func(context.Context, []ToolResultPair) (ProviderHistoryPlan, error) { return plan, nil })
			if _, err := ProjectProviderHistory(t.Context(), history, ProviderHistoryBudget{MaxBytes: 100, MaxTokens: 100, EstimateTokens: func(text string) int { return len(text) }}, set); err == nil {
				t.Fatal("invalid contribution accepted")
			}
			after, _ := json.Marshal(history)
			if string(before) != string(after) {
				t.Fatal("invalid contribution mutated source")
			}
		})
	}
}

func TestProviderHistoryProjectionHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	set := projectorSet(t, func(context.Context, []ToolResultPair) (ProviderHistoryPlan, error) {
		cancel()
		return ProviderHistoryPlan{Omit: []string{"owned"}}, nil
	})
	if _, err := ProjectProviderHistory(ctx, projectionHistory(), ProviderHistoryBudget{MaxBytes: 100, MaxTokens: 100, EstimateTokens: func(text string) int { return len(text) }}, set); err == nil {
		t.Fatal("projection ignored cancellation")
	}
}

func TestProviderHistoryBudgetAppliesPerSummaryAnchor(t *testing.T) {
	history := projectionHistory()
	history[1].Blocks[1].ResultFact.Owner = "custom"
	set := projectorSet(t, func(context.Context, []ToolResultPair) (ProviderHistoryPlan, error) {
		return ProviderHistoryPlan{Omit: []string{"owned", "other"}, Summaries: []ToolSummary{
			{ToolUseID: "owned", Text: strings.Repeat("a", 60)},
			{ToolUseID: "other", Text: strings.Repeat("b", 60)},
		}}, nil
	})
	projected, err := ProjectProviderHistory(t.Context(), history, ProviderHistoryBudget{MaxBytes: 100}, set)
	if err != nil {
		t.Fatal(err)
	}
	if len(projected[1].Blocks) != 2 {
		t.Fatalf("summary blocks=%+v", projected[1].Blocks)
	}
}
