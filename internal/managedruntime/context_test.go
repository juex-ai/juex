package managedruntime

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/llm"
)

func TestContextProjectionPreservesOriginalAndReadableReference(t *testing.T) {
	id := uuid.NewString()
	original := strings.Repeat("正文中间内容", 2000)
	history := []llm.Message{{ID: id, Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockText, Text: original}}}}
	projected := projectContext(history, ModelConfig{ContextWindow: 32768, MaxOutput: 4096})
	if history[0].Blocks[0].Text != original {
		t.Fatal("changed durable input")
	}
	if len(projected[0].Blocks[0].Text) >= len(original) || !utf8.ValidString(projected[0].Blocks[0].Text) || !strings.Contains(projected[0].Blocks[0].Text, "read_context") {
		t.Fatal("missing readable preview")
	}
	ref := ContextReference(id, 0, "text")
	if !strings.Contains(projected[0].Blocks[0].Text, ref) {
		t.Fatal("missing original content reference")
	}
	message, index, field, err := ParseContextReference(ref)
	if err != nil || message != id || index != 0 || field != "text" {
		t.Fatal(message, index, field, err)
	}
	for _, bad := range []string{"", "file:///private/data", ContextReference(id, -1, "text"), ContextReference("not-id", 0, "text"), ContextReference(id, 0, "secret")} {
		if _, _, _, err := ParseContextReference(bad); err == nil {
			t.Fatal("accepted invalid reference", bad)
		}
	}
}

func TestContextReadPageIsNotTruncatedOrRewrapped(t *testing.T) {
	large := strings.Repeat("data", 20000)
	history := []llm.Message{
		{ID: uuid.NewString(), Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ToolUseID: "reused", ToolName: "read_context"}}},
		{ID: uuid.NewString() + "-result", Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "reused", Content: large}}},
		{ID: uuid.NewString(), Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ToolUseID: "reused", ToolName: "read"}}},
		{ID: uuid.NewString() + "-result", Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "reused", Content: large}}},
	}
	projected := projectContext(history, ModelConfig{ContextWindow: 8192})
	if projected[1].Blocks[0].Content != large {
		t.Fatal("read_context page no longer matches next_offset")
	}
	if projected[3].Blocks[0].Content == large {
		t.Fatal("reused call ID bypassed ordinary result projection")
	}
	ref := ContextReference(history[3].ID, 0, "content")
	if _, _, _, err := ParseContextReference(ref); err != nil {
		t.Fatal("tool result identity cannot be referenced", err)
	}
}
