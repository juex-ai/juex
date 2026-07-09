package contextbudget

import (
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/llm"
)

func TestSelectCompactionInput_KeepsRecentTailOutOfSummary(t *testing.T) {
	h := []llm.Message{
		testMsg("m1", llm.RoleUser, "old question"),
		testMsg("m2", llm.RoleAssistant, "old answer"),
		testMsg("m3", llm.RoleUser, "recent question"),
		testMsg("m4", llm.RoleAssistant, "recent answer"),
	}
	sel := SelectInput(h, Policy{KeepRecentTokens: 1000, TailTurns: 1})
	if len(sel.SummaryInput) != 2 {
		t.Fatalf("summary len = %d, want 2", len(sel.SummaryInput))
	}
	if len(sel.RetainedTail) != 2 || sel.RetainedTail[0].ID != "m3" {
		t.Fatalf("tail = %+v", sel.RetainedTail)
	}
}

func TestSelectCompactionInput_DoesNotOrphanToolResult(t *testing.T) {
	h := []llm.Message{
		testMsg("m1", llm.RoleUser, "old"),
		{ID: "m2", Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ToolUseID: "tu1", ToolName: "read", Input: map[string]any{"path": "x"}}}},
		{ID: "m3", Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "tu1", Content: strings.Repeat("result ", 200)}}},
		testMsg("m4", llm.RoleAssistant, "done"),
	}
	sel := SelectInput(h, Policy{KeepRecentTokens: 40, TailTurns: 1})
	if sel.RetainedTail[0].ID == "m3" {
		t.Fatalf("tail starts with orphan tool result: %+v", sel.RetainedTail)
	}
}

func TestSelectCompactionInput_IgnoresRuntimeContextAsTailTurnStart(t *testing.T) {
	runtimeContext := testMsg("runtime-working-state", llm.RoleUser, "Current working observations")
	runtimeContext.Kind = llm.MessageKindRuntimeContext
	h := []llm.Message{
		testMsg("m1", llm.RoleUser, "old question"),
		testMsg("m2", llm.RoleAssistant, "old answer"),
		testMsg("m3", llm.RoleUser, "recent question"),
		runtimeContext,
	}

	sel := SelectInput(h, Policy{KeepRecentTokens: 1000, TailTurns: 1})
	if len(sel.RetainedTail) != 2 || sel.RetainedTail[0].ID != "m3" {
		t.Fatalf("tail = %+v, want recent user turn plus runtime context", sel.RetainedTail)
	}
}

func TestSelectCompactionInputUsesEstimatorForTailBudget(t *testing.T) {
	h := []llm.Message{
		testMsg("m1", llm.RoleUser, "old question"),
		testMsg("m2", llm.RoleAssistant, "old answer"),
		testMsg("m3", llm.RoleUser, "recent question"),
	}
	baseRecentTokens := EstimateMessageTokens(h[2:3])
	estimator := func(msgs []llm.Message) int {
		if len(msgs) == 1 && msgs[0].ID == "m3" {
			return baseRecentTokens + 2
		}
		return EstimateMessageTokens(msgs)
	}

	sel := SelectInputWithEstimator(h, Policy{KeepRecentTokens: baseRecentTokens + 1, TailTurns: 99}, estimator)

	if len(sel.RetainedTail) != 1 || sel.RetainedTail[0].ID != "m3" {
		t.Fatalf("tail = %+v, want estimator-limited recent message only", sel.RetainedTail)
	}
}

func testMsg(id string, role llm.Role, text string) llm.Message {
	m := llm.TextMessage(role, text)
	m.ID = id
	return m
}
