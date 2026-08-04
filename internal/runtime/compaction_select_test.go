package runtime

import (
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/llm"
)

func TestSelectCompactionInput_KeepsRecentRealInputByTokenBudget(t *testing.T) {
	h := []llm.Message{
		testMsg("m1", llm.RoleUser, "old question"),
		testMsg("m2", llm.RoleAssistant, "old answer"),
		testMsg("m3", llm.RoleUser, "recent question"),
		testMsg("m4", llm.RoleAssistant, "recent answer"),
	}
	sel := selectCompactionInput(h, compactionPolicy{KeepRecentTokens: estimateMessageTokens(h[2:3])})
	if len(sel.SummaryInput) != 3 {
		t.Fatalf("summary len = %d, want 3", len(sel.SummaryInput))
	}
	if len(sel.RetainedTail) != 1 || sel.RetainedTail[0].ID != "m3" {
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
	sel := selectCompactionInput(h, compactionPolicy{KeepRecentTokens: 40})
	if sel.RetainedTail[0].ID == "m3" {
		t.Fatalf("tail starts with orphan tool result: %+v", sel.RetainedTail)
	}
}

func TestSelectCompactionInput_IgnoresRuntimeContextAsTailTurnStart(t *testing.T) {
	runtimeContext := testMsg("runtime-notes", llm.RoleUser, "Current working notes")
	runtimeContext.Kind = llm.MessageKindRuntimeContext
	h := []llm.Message{
		testMsg("m1", llm.RoleUser, "old question"),
		testMsg("m2", llm.RoleAssistant, "old answer"),
		testMsg("m3", llm.RoleUser, "recent question"),
		runtimeContext,
	}

	sel := selectCompactionInput(h, compactionPolicy{KeepRecentTokens: estimateMessageTokens(h[2:3])})
	if len(sel.RetainedTail) != 1 || sel.RetainedTail[0].ID != "m3" {
		t.Fatalf("tail = %+v, want only recent real input", sel.RetainedTail)
	}
}

func TestSelectCompactionInputUsesEstimatorForTailBudget(t *testing.T) {
	h := []llm.Message{
		testMsg("m1", llm.RoleUser, "old question"),
		testMsg("m2", llm.RoleAssistant, "old answer"),
		testMsg("m3", llm.RoleUser, "recent question"),
	}
	baseRecentTokens := estimateMessageTokens(h[2:3])
	estimator := func(msgs []llm.Message) int {
		if len(msgs) == 1 && msgs[0].ID == "m3" {
			return baseRecentTokens + 2
		}
		return estimateMessageTokens(msgs)
	}

	sel := selectCompactionInputWithEstimator(h, compactionPolicy{KeepRecentTokens: baseRecentTokens + 2}, estimator)

	if len(sel.RetainedTail) != 1 || sel.RetainedTail[0].ID != "m3" {
		t.Fatalf("tail = %+v, want estimator-limited recent message only", sel.RetainedTail)
	}
}

func testMsg(id string, role llm.Role, text string) llm.Message {
	m := llm.TextMessage(role, text)
	m.ID = id
	return m
}
