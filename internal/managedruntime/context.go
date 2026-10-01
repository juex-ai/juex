package managedruntime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/llm"
)

type ContextPage struct {
	Reference  string `json:"reference"`
	Text       string `json:"text"`
	Offset     int    `json:"offset"`
	NextOffset int    `json:"next_offset"`
	TotalBytes int    `json:"total_bytes"`
	HasMore    bool   `json:"has_more"`
}

type ContextStore interface {
	ReadContext(context.Context, Scope, string, string, int, int) (ContextPage, error)
}

type contextReference struct {
	Message string `json:"m"`
	Block   int    `json:"b"`
	Field   string `json:"f"`
}

// A reference locates immutable text, not authority. Reads are always scoped to
// the calling Thread; no path or caller-supplied owner is accepted.
func ContextReference(message string, block int, field string) string {
	encoded, _ := json.Marshal(contextReference{message, block, field})
	return "ctx_" + base64.RawURLEncoding.EncodeToString(encoded)
}

func ParseContextReference(ref string) (string, int, string, error) {
	var v contextReference
	if len(ref) > 512 || !strings.HasPrefix(ref, "ctx_") {
		return "", 0, "", ErrInvalid
	}
	encoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(ref, "ctx_"))
	if err != nil || json.Unmarshal(encoded, &v) != nil || v.Block < 0 || v.Block > 1024 || (v.Field != "text" && v.Field != "content" && v.Field != "input") {
		return "", 0, "", ErrInvalid
	}
	base := strings.TrimSuffix(strings.TrimSuffix(v.Message, "-observations"), "-result")
	if _, err := uuid.Parse(base); err != nil {
		return "", 0, "", ErrInvalid
	}
	return v.Message, v.Block, v.Field, nil
}

func projectContext(history []llm.Message, model ModelConfig) []llm.Message {
	projected := slices.Clone(history)
	readCalls := map[string]bool{}
	budget := min(4096, max(256, model.ContextWindow/16))
	for i, message := range history {
		for _, call := range message.ToolCalls() {
			readCalls[call.ToolUseID] = call.ToolName == "read_context"
		}
		projected[i].Blocks = slices.Clone(message.Blocks)
		for j, block := range message.Blocks {
			readPage := block.Type == llm.BlockToolResult && readCalls[block.ToolUseID]
			if block.Type == llm.BlockToolResult {
				delete(readCalls, block.ToolUseID)
			}
			if message.ID == "" || block.Type != llm.BlockText && block.Type != llm.BlockToolResult || readPage {
				continue
			}
			if block.Text != "" {
				projected[i].Blocks[j].Text = contextPreview(block.Text, ContextReference(message.ID, j, "text"), budget)
			}
			if block.Content != "" {
				projected[i].Blocks[j].Content = contextPreview(block.Content, ContextReference(message.ID, j, "content"), budget)
			}
		}
	}
	return projected
}

func contextPreview(text, ref string, budget int) string {
	preview := llm.PreviewText(text, budget, budget*4)
	if preview.OmittedBytes == 0 {
		return text
	}
	return fmt.Sprintf("%s\n[Preview: %d original bytes omitted. Read the original with read_context(reference=%q, offset=0). Offsets count UTF-8 bytes.]\n%s", preview.Head, preview.OmittedBytes, ref, preview.Tail)
}

func runtimeTools() []llm.ToolSpec {
	return []llm.ToolSpec{{Name: "read_context", Description: "Read original text from this Thread's durable context reference, even when execution devices are offline. This is not a file path. offset and limit count UTF-8 bytes; default 2048, maximum 4096. Continue from next_offset, never assume a preview is complete.", Schema: map[string]any{"type": "object", "properties": map[string]any{"reference": map[string]any{"type": "string"}, "offset": map[string]any{"type": "integer", "minimum": 0}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 4096}}, "required": []string{"reference"}, "additionalProperties": false}}}
}

func (r toolRunner) contextTool(ctx context.Context, work ToolWork) (ToolOutcome, bool) {
	if work.Call.ToolName != "read_context" {
		return ToolOutcome{}, false
	}
	var args struct {
		Reference string `json:"reference"`
		Offset    int    `json:"offset"`
		Limit     int    `json:"limit"`
	}
	encoded, err := json.Marshal(work.Call.Input)
	if err != nil || json.Unmarshal(encoded, &args) != nil {
		return toolResult(work.Call, map[string]string{"error": "invalid context reference request"}, true), true
	}
	page, err := r.context.ReadContext(ctx, work.Scope, work.ThreadID, args.Reference, args.Offset, args.Limit)
	if err != nil {
		if errors.Is(err, ErrInvalid) || errors.Is(err, ErrDenied) {
			return toolResult(work.Call, map[string]string{"error": err.Error()}, true), true
		}
		return executionFailure(err), true
	}
	return toolResult(work.Call, page, false), true
}
