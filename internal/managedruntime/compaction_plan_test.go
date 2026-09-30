package managedruntime

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/llm"
)

func TestCompactionCanReduceAnActiveLongTurnWithoutLosingInputOrToolPairs(t *testing.T) {
	model := ModelConfig{ModelID: uuid.NewString(), Provider: "fixture", Model: "small", ContextWindow: 32768, MaxOutput: 4096}
	input := llm.TextMessage(llm.RoleUser, "Current task must continue")
	input.ID = uuid.NewString()
	input.Kind = llm.MessageKindDirect
	work := Work{InputID: input.ID, Generation: 1, History: []llm.Message{input}}
	for i := 0; i < 40; i++ {
		id := uuid.NewString()
		work.History = append(work.History, llm.Message{ID: uuid.NewString(), Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ToolUseID: id, ToolName: "read", Input: map[string]any{"path": "proof.txt"}}}}, llm.Message{ID: uuid.NewString() + "-result", Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: id, Content: strings.Repeat("tool facts ", 120)}}})
	}
	notice := llm.TextMessage(llm.RoleUser, "Device offline: do not repeat unknown operations")
	notice.ID = uuid.NewString() + "-observations"
	notice.Kind = llm.MessageKindSystemNotice
	work.History = append(work.History, notice)
	base := ModelRequest{System: "Agent constraints", Tools: runtimeTools()}
	request, err := planCompaction(work, base, model)
	if err != nil {
		t.Fatal(err)
	}
	if request.Purpose != "compaction" || len(request.Tools) != 0 || request.Model != model || request.MaxOutputTokens >= model.MaxOutput {
		t.Fatal("summary request changed model or tool authority", request)
	}
	retained := request.Compaction.Retained
	if len(retained) >= len(work.History)/2 {
		t.Fatal("retained the entire active Turn")
	}
	if err := llm.ValidateToolTranscript(retained); err != nil {
		t.Fatal(err)
	}
	foundInput, foundNotice := false, false
	for _, message := range retained {
		foundInput = foundInput || message.ID == input.ID
		foundNotice = foundNotice || message.ID == notice.ID
	}
	if !foundInput || !foundNotice {
		t.Fatal("lost current user or observation input")
	}
	if llm.EstimateContextTokens(request.System, nil, request.Messages)+request.MaxOutputTokens+1024 > model.ContextWindow {
		t.Fatal("summary request overflow")
	}
	if len(work.History) != 82 {
		t.Fatal("modified original history")
	}
	good := llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "Task: continue. Tool results: file reads succeeded. Device offline; no operation may be replayed."), StopReason: llm.StopEndTurn}
	if err := ValidateCompactionSummary(request, good); err != nil {
		t.Fatal(err)
	}
	good.Message.Blocks = append(good.Message.Blocks, llm.Block{Type: llm.BlockToolUse, ToolUseID: "forbidden", ToolName: "write", Input: map[string]any{}})
	if err := ValidateCompactionSummary(request, good); err == nil {
		t.Fatal("summary tool call accepted")
	}
}

func TestCompactionSerializesLargeArgumentsAsReadableData(t *testing.T) {
	input := llm.TextMessage(llm.RoleUser, "Keep my task")
	input.ID = uuid.NewString()
	id := uuid.NewString()
	call := llm.Message{ID: uuid.NewString(), Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ToolUseID: id, ToolName: "write", Input: map[string]any{"content": strings.Repeat("large", 20000)}}}}
	result := llm.Message{ID: uuid.NewString() + "-result", Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: id, Content: "written"}}}
	work := Work{InputID: input.ID, History: []llm.Message{input, call, result}}
	request, err := planCompaction(work, ModelRequest{System: "instructions"}, ModelConfig{ContextWindow: 32768, MaxOutput: 4096})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(request.Messages)
	if !strings.Contains(string(encoded), ContextReference(call.ID, 0, "input")) {
		t.Fatal("large tool arguments lost their source")
	}
	if len(call.Blocks[0].Input["content"].(string)) != 100000 {
		t.Fatal("modified arguments")
	}
}

func TestContextReferenceKeepsOriginalBlockAfterReasoningRemoval(t *testing.T) {
	id := uuid.NewString()
	work := Work{History: []llm.Message{{ID: id, Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockReasoning, Text: "private reasoning"}, {Type: llm.BlockText, Text: strings.Repeat("durable original ", 3000)}}}}}
	projected := projectModelHistory(work, ModelConfig{ModelID: "different-model", ContextWindow: 32768})
	if len(projected[0].Blocks) != 1 || !strings.Contains(projected[0].Blocks[0].Text, ContextReference(id, 1, "text")) {
		t.Fatal("reference shifted when incompatible reasoning was removed")
	}
	if strings.Contains(projected[0].Blocks[0].Text, ContextReference(id, 0, "text")) {
		t.Fatal("text reference points to reasoning")
	}
}
