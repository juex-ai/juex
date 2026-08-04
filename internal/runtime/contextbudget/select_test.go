package contextbudget

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
	sel := SelectInput(h, Policy{KeepRecentTokens: EstimateMessageTokens(h[2:3])})
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
	sel := SelectInput(h, Policy{KeepRecentTokens: 40})
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

	sel := SelectInput(h, Policy{KeepRecentTokens: EstimateMessageTokens(h[2:3])})
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
	baseRecentTokens := EstimateMessageTokens(h[2:3])
	estimator := func(msgs []llm.Message) int {
		if len(msgs) == 1 && msgs[0].ID == "m3" {
			return baseRecentTokens + 2
		}
		return EstimateMessageTokens(msgs)
	}

	sel := SelectInputWithEstimator(h, Policy{KeepRecentTokens: baseRecentTokens + 2}, estimator)

	if len(sel.RetainedTail) != 1 || sel.RetainedTail[0].ID != "m3" {
		t.Fatalf("tail = %+v, want estimator-limited recent message only", sel.RetainedTail)
	}
}

func TestSelectCompactionInputSummarizesNewestRealInputWhenItExceedsBudget(t *testing.T) {
	h := []llm.Message{
		testMsg("direct-1", llm.RoleUser, strings.Repeat("important context ", 200)),
	}
	h[0].Kind = llm.MessageKindDirect
	budget := EstimateMessageTokens(h) - 1

	sel := SelectInput(h, Policy{KeepRecentTokens: budget})

	if len(sel.RetainedTail) != 0 || len(sel.RetainedMessageIDs) != 0 {
		t.Fatalf("oversized input retained outside budget: tail=%+v ids=%v", sel.RetainedTail, sel.RetainedMessageIDs)
	}
	if len(sel.OversizedInputIDs) != 1 || sel.OversizedInputIDs[0] != "direct-1" {
		t.Fatalf("oversized input ids = %v, want direct-1", sel.OversizedInputIDs)
	}
	if len(sel.SummaryInput) != 1 || sel.SummaryInput[0].ID != "direct-1" {
		t.Fatalf("summary input = %+v, want oversized direct input", sel.SummaryInput)
	}
}

func TestSelectCompactionInputReferencesOversizedImageOnlyInput(t *testing.T) {
	image := llm.Message{
		ID:   "image-1",
		Role: llm.RoleUser,
		Kind: llm.MessageKindDirect,
		Blocks: []llm.Block{{Type: llm.BlockImage, Media: &llm.MediaRef{
			ArtifactPath: ".juex/artifacts/media/session/photo.png",
			MediaType:    "image/png",
			SHA256:       "image-sha",
			Width:        4000,
			Height:       4000,
		}}},
	}

	sel := SelectInput([]llm.Message{image}, Policy{KeepRecentTokens: 200})

	if len(sel.RetainedMessageIDs) != 0 || len(sel.OversizedInputIDs) != 1 || sel.OversizedInputIDs[0] != "image-1" {
		t.Fatalf("selection = %+v, want oversized image reference", sel)
	}
	if len(sel.SummaryInput) != 1 || sel.SummaryInput[0].ID != "image-1" {
		t.Fatalf("summary input = %+v, want image-1", sel.SummaryInput)
	}
}

func TestSelectCompactionInputNoticesDoNotDisplaceRealInputs(t *testing.T) {
	first := testMsg("direct-1", llm.RoleUser, "original request")
	first.Kind = llm.MessageKindDirect
	second := testMsg("event-1", llm.RoleUser, "channel follow-up")
	second.Kind = llm.MessageKindMCPEvent
	h := []llm.Message{first, testMsg("assistant-1", llm.RoleAssistant, "working")}
	for i := 0; i < 6; i++ {
		notice := testMsg("notice-"+string(rune('a'+i)), llm.RoleUser, "model changed")
		notice.Kind = llm.MessageKindModelChange
		h = append(h, notice)
	}
	h = append(h, second)
	budget := EstimateMessageTokens([]llm.Message{first, second})

	sel := SelectInput(h, Policy{KeepRecentTokens: budget})
	if got := sel.RetainedMessageIDs; len(got) != 2 || got[0] != "direct-1" || got[1] != "event-1" {
		t.Fatalf("retained ids = %v, want both real inputs", got)
	}
	for _, msg := range append(append([]llm.Message(nil), sel.SummaryInput...), sel.RetainedTail...) {
		if msg.Kind == llm.MessageKindModelChange || msg.Kind == llm.MessageKindSystemNotice {
			t.Fatalf("notice survived compaction selection: %+v", msg)
		}
	}
}

func TestSelectCompactionInputKeepsActiveToolProtocolClosed(t *testing.T) {
	direct := testMsg("direct-1", llm.RoleUser, "read the file")
	direct.Kind = llm.MessageKindDirect
	h := []llm.Message{
		direct,
		{ID: "tool-use-1", Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ToolUseID: "call-1", ToolName: "read"}}},
		{ID: "tool-result-1", Role: llm.RoleUser, Kind: llm.MessageKindToolResult, Blocks: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "call-1", Content: "contents"}}},
	}

	sel := SelectInput(h, Policy{KeepRecentTokens: EstimateMessageTokens(h[:1])})
	if got := sel.RetainedMessageIDs; len(got) != 3 || got[0] != "direct-1" || got[1] != "tool-use-1" || got[2] != "tool-result-1" {
		t.Fatalf("retained protocol ids = %v", got)
	}
}

func TestSelectCompactionInputProjectsOversizedInitiatorButKeepsToolProtocol(t *testing.T) {
	direct := testMsg("direct-1", llm.RoleUser, strings.Repeat("oversized ", 200))
	direct.Kind = llm.MessageKindDirect
	h := []llm.Message{
		direct,
		{ID: "tool-use-1", Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ToolUseID: "call-1", ToolName: "read"}}},
		{ID: "tool-result-1", Role: llm.RoleUser, Kind: llm.MessageKindToolResult, Blocks: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "call-1", Content: "contents"}}},
	}

	sel := SelectInput(h, Policy{KeepRecentTokens: 20})
	if got := sel.OversizedInputIDs; len(got) != 1 || got[0] != "direct-1" {
		t.Fatalf("oversized ids = %v, want direct-1", got)
	}
	if len(sel.SummaryInput) != 1 || sel.SummaryInput[0].ID != "direct-1" {
		t.Fatalf("summary input = %+v, want projected direct initiator", sel.SummaryInput)
	}
	if got := sel.RetainedMessageIDs; len(got) != 2 || got[0] != "tool-use-1" || got[1] != "tool-result-1" {
		t.Fatalf("retained protocol ids = %v, want tool use/result only", got)
	}
}

func testMsg(id string, role llm.Role, text string) llm.Message {
	m := llm.TextMessage(role, text)
	m.ID = id
	return m
}
