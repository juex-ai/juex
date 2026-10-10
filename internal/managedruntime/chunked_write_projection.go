package managedruntime

import (
	"encoding/json"
	"slices"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/llm"
)

type writePosition struct{ message, block int }
type writePair struct {
	use, result writePosition
	call        llm.Block
	fact        WriteFact
}

// Pair by occurrence, never by a global call-ID map: providers can reuse an ID
// in a later attempt. Only owned receipts, not displayed result text or IsError,
// authorize a projection. PostToolUse cannot undo a successful file commit.
func projectWrites(history []llm.Message, model ModelConfig) []llm.Message {
	projected, _ := projectWritesWithChanges(history, model)
	return projected
}

func projectWritesWithChanges(history []llm.Message, model ModelConfig) ([]llm.Message, map[string]bool) {
	pending := map[string]writePosition{}
	sessions := map[string][]writePair{}
	for i, message := range history {
		for j, block := range message.Blocks {
			position := writePosition{i, j}
			if block.Type == llm.BlockToolUse {
				pending[block.ToolUseID] = position
				continue
			}
			if block.Type != llm.BlockToolResult {
				continue
			}
			use, ok := pending[block.ToolUseID]
			delete(pending, block.ToolUseID)
			if !ok || block.ResultFact == nil || block.ResultFact.Owner != "chunked-write" {
				continue
			}
			call := history[use.message].Blocks[use.block]
			if !execprotocol.IsChunkedWrite(call.ToolName) || block.ToolName != call.ToolName {
				continue
			}
			var fact WriteFact
			if json.Unmarshal(block.ResultFact.Data, &fact) != nil || fact.OperationID == "" || fact.EnvironmentID == "" {
				continue
			}
			args, _ := json.Marshal(call.Input)
			if fact.Receipt.Validate(execprotocol.Request{ID: fact.OperationID, Kind: call.ToolName, Arguments: args}) != nil {
				continue
			}
			key := fact.EnvironmentID + ":" + fact.Receipt.WriteID
			sessions[key] = append(sessions[key], writePair{use: use, result: position, call: call, fact: fact})
		}
	}
	omit := map[writePosition]bool{}
	summaries := map[writePosition]string{}
	total := 0
	for _, pairs := range sessions {
		var terminal *writePair
		var chunks []writePair
		for i, pair := range pairs {
			if pair.fact.Receipt.Action == "committed" || pair.fact.Receipt.Action == "aborted" {
				terminal = &pairs[i]
			}
			if pair.fact.Receipt.Action == "chunk" {
				chunks = append(chunks, pair)
			}
		}
		var selected []writePair
		var summary any
		if terminal != nil {
			selected = pairs
			summary = map[string]any{"write": terminal.fact.Receipt, "environment_id": terminal.fact.EnvironmentID, "note": "Buffered content was folded from provider replay; the original tool history remains available."}
		} else if len(chunks) > 4 {
			selected = chunks[:len(chunks)-4]
			indices := make([]int, 0, len(selected))
			for _, pair := range selected {
				indices = append(indices, pair.fact.Receipt.Index)
			}
			summary = map[string]any{"write_id": selected[0].fact.Receipt.WriteID, "environment_id": selected[0].fact.EnvironmentID, "confirmed_folded_indices": indices, "note": "Active buffer: the four most recent chunk calls remain visible. Execution retains the confirmed bytes."}
		} else {
			continue
		}
		encoded, _ := json.Marshal(summary)
		text := "Chunked write receipt summary (data): " + string(encoded)
		total += llm.EstimateTextTokens(text)
		if total > min(2048, model.ContextWindow/32) {
			return history, nil
		}
		for _, pair := range selected {
			omit[pair.use], omit[pair.result] = true, true
		}
		anchor := selected[len(selected)-1].result
		summaries[anchor] = text
	}
	if len(omit) == 0 {
		return history, nil
	}
	projected := make([]llm.Message, 0, len(history))
	changed := map[string]bool{}
	for i, message := range history {
		message.Blocks = slices.Clone(message.Blocks[:0:0])
		var notices []llm.Block
		for j, block := range history[i].Blocks {
			position := writePosition{i, j}
			if text := summaries[position]; text != "" {
				notices = append(notices, llm.Block{Type: llm.BlockText, Text: text})
			}
			if !omit[position] {
				message.Blocks = append(message.Blocks, block)
			} else {
				changed[message.ID] = true
			}
		}
		message.Blocks = append(message.Blocks, notices...)
		if len(message.Blocks) > 0 {
			projected = append(projected, message)
		}
	}
	// Fall back without modifying history if a malformed imported transcript
	// cannot support complete pair removal.
	if llm.ValidateToolTranscript(projected) != nil {
		return history, nil
	}
	return projected, changed
}
