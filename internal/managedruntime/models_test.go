package managedruntime

import (
	"reflect"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/llm"
)

func TestModelHistoryPreservesToolPairsWithoutReplayingForeignReasoning(t *testing.T) {
	model := ModelConfig{ModelID: "one", Provider: "first", Model: "small", Protocol: llm.ProtocolAnthropicMessages, Endpoint: "https://first.example.test", ModelAuthorizationEpoch: 1}
	history := []llm.Message{
		{ID: "request", Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockText, Text: "Read it"}}},
		{ID: "response", Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockReasoning, Text: "private reasoning", Signature: "provider-private-signature"}, {Type: llm.BlockToolUse, ToolUseID: "call", ToolName: "read", Input: map[string]any{"path": "file"}}}},
		{ID: "result", Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "call", Content: "the file"}}},
	}
	work := Work{History: history, ModelOrigins: map[string]ModelConfig{"response": model}}
	same := modelHistory(work, model)
	if !reflect.DeepEqual(same, history) {
		t.Fatal("same-model reasoning lost")
	}
	other := model
	other.Protocol = llm.ProtocolOpenAIResponses
	other.Endpoint = "https://other.example.test"
	projected := modelHistory(work, other)
	if len(projected[1].Blocks) != 1 || projected[1].Blocks[0].Type != llm.BlockToolUse || len(work.History[1].Blocks) != 2 || work.History[1].Blocks[0].Signature != "provider-private-signature" {
		t.Fatal("projection mutated canonical history or tool pair")
	}
	if err := llm.ValidateToolTranscript(projected); err != nil {
		t.Fatal(err)
	}
	changedAccount := model
	changedAccount.ModelAuthorizationEpoch++
	if len(modelHistory(work, changedAccount)[1].Blocks) != 1 {
		t.Fatal("revoked account/configuration epoch retained private reasoning")
	}
	work.ModelOrigins = nil
	if len(modelHistory(work, model)[1].Blocks) != 1 {
		t.Fatal("unknown-origin reasoning replayed")
	}
}
