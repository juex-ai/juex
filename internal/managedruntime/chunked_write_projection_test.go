package managedruntime

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/llm"
)

func writeProjectionPair(ordinal int, kind, action string, index int, content string) []llm.Message {
	id := fmt.Sprintf("op-%d", ordinal)
	input := map[string]any{"write_id": "op-0"}
	if kind == "write_begin" {
		input = map[string]any{"path": "file"}
	}
	if kind == "write_chunk" {
		input["index"], input["content"] = index, content
	}
	receipt := execprotocol.WriteReceipt{WriteID: "op-0", Action: action, Path: "file", Mode: "overwrite", Index: index, Bytes: int64(len(content)), SHA256: strings.Repeat("a", 64), At: time.Unix(int64(100+ordinal), 0)}
	data, _ := json.Marshal(WriteFact{OperationID: id, EnvironmentID: "device", Receipt: receipt})
	return []llm.Message{
		{ID: id, Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ToolUseID: "reused-id", ToolName: kind, Input: input}}},
		{ID: id + "-result", Role: llm.RoleUser, Kind: llm.MessageKindToolResult, Blocks: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "reused-id", ToolName: kind, Content: "display does not prove state", ResultFact: &llm.ResultFact{Owner: "chunked-write", Data: data}}}},
	}
}

func TestWriteProjectionCompactionRetainsOnlyOriginalCompletePairs(t *testing.T) {
	input := llm.TextMessage(llm.RoleUser, "Continue current work")
	input.ID = "current"
	input.Kind = llm.MessageKindDirect
	policy := agentpolicy.Policy{Enabled: []agentpolicy.Capability{agentpolicy.ChunkedWrite}}
	work := Work{InputID: input.ID, Generation: 1, Config: TurnConfig{Capabilities: policy}, Scope: Scope{Capabilities: policy}, History: []llm.Message{input}}
	for i := 0; i < 15; i++ {
		m := llm.TextMessage(llm.RoleUser, strings.Repeat("old context ", 200))
		m.ID = fmt.Sprintf("old-%d", i)
		work.History = append(work.History, m)
	}
	work.History = append(work.History, writeProjectionPair(0, "write_begin", "began", 0, "")...)
	for i := 0; i < 6; i++ {
		work.History = append(work.History, writeProjectionPair(i+1, "write_chunk", "chunk", i, strings.Repeat("x", 1500))...)
	}
	work.History = append(work.History, writeProjectionPair(8, "write_commit", "committed", 0, "")...)
	request, err := planCompaction(work, ModelRequest{System: "Keep the task"}, ModelConfig{ContextWindow: 32768, OutputReserve: 2048, MaxOutput: 2048})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, id := range request.Compaction.RetainedIDs {
		ids[id] = true
	}
	var restored []llm.Message
	for _, message := range work.History {
		if ids[message.ID] {
			restored = append(restored, message)
		}
	}
	if err := llm.ValidateToolTranscript(restored); err != nil {
		t.Fatal("checkpoint resurrected orphaned original tool results", err)
	}
	if ids["op-8-result"] {
		t.Fatal("projected receipt summary was retained by an original result ID")
	}
}

func TestWriteProjectionUsesOwnedFactsAndPreservesOriginalHistory(t *testing.T) {
	history := writeProjectionPair(0, "write_begin", "began", 0, "")
	for i := 0; i < 7; i++ {
		history = append(history, writeProjectionPair(i+1, "write_chunk", "chunk", i, fmt.Sprintf("chunk-%d %s", i, strings.Repeat("x", 1500)))...)
	}
	original, _ := json.Marshal(history)
	projected := projectWrites(history, ModelConfig{ContextWindow: 131072})
	if err := llm.ValidateToolTranscript(projected); err != nil {
		t.Fatal(err)
	}
	var calls []llm.Block
	for _, message := range projected {
		calls = append(calls, message.ToolCalls()...)
	}
	if len(calls) != 5 {
		t.Fatal("active session must keep begin and four recent chunks", len(calls))
	}
	if calls[1].Input["index"] != 3 {
		t.Fatal("wrong recent chunks retained", calls[1])
	}
	if !reflect.DeepEqual(projectWrites(history, ModelConfig{ContextWindow: 1}), history) {
		t.Fatal("insufficient summary budget folded history")
	}
	history = append(history, writeProjectionPair(9, "write_commit", "committed", 0, "")...)
	// A post-tool hook can reject the model result without undoing publication.
	history[len(history)-1].Blocks[0].IsError = true
	projected = projectWrites(history, ModelConfig{ContextWindow: 131072})
	if len(projected) != 1 || projected[0].Blocks[0].Type != llm.BlockText || !strings.Contains(projected[0].FirstText(), "committed") {
		t.Fatal("committed session not summarized", projected)
	}
	after, _ := json.Marshal(history[:len(history)-2])
	if string(original) != string(after) {
		t.Fatal("provider projection mutated durable history")
	}
	for i := range history {
		for j := range history[i].Blocks {
			history[i].Blocks[j].ResultFact = nil
		}
	}
	if !reflect.DeepEqual(projectWrites(history, ModelConfig{ContextWindow: 131072}), history) {
		t.Fatal("display text used as an execution fact")
	}
}

func TestWriteProjectionKeepsUnrelatedToolWithReusedID(t *testing.T) {
	history := writeProjectionPair(0, "write_begin", "began", 0, "")
	history = append(history, writeProjectionPair(1, "write_abort", "aborted", 0, "")...)
	history = append(history, llm.Message{ID: "other", Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ToolUseID: "reused-id", ToolName: "read", Input: map[string]any{"path": "file"}}}}, llm.Message{ID: "other-result", Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "reused-id", ToolName: "read", Content: "keep me"}}})
	projected := projectWrites(history, ModelConfig{ContextWindow: 131072})
	if len(projected) != 3 || projected[1].ToolCalls()[0].ToolName != "read" || projected[2].Blocks[0].Content != "keep me" {
		t.Fatal(projected)
	}
	if err := llm.ValidateToolTranscript(projected); err != nil {
		t.Fatal(err)
	}
}
