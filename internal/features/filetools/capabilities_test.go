package filetools

import (
	"context"
	"strings"
	"testing"

	toolcore "github.com/juex-ai/juex/internal/foundation/tools"
)

func TestBasicFileToolsAreSelfContained(t *testing.T) {
	r := toolcore.NewRegistry()
	registerTestTools(r, testToolOptions{WorkDir: t.TempDir()})
	if len(r.List()) != 3 {
		t.Errorf("basic file tool count = %d, want 3", len(r.List()))
	}
	write, _ := r.Get("write")
	contentSchema := write.Schema["properties"].(map[string]any)["content"].(map[string]any)
	if _, limited := contentSchema["maxLength"]; limited {
		t.Error("standalone write restricts long content")
	}
	if strings.Contains(write.Description, "write_begin") {
		t.Error("standalone write recommends an unavailable tool")
	}
	content := strings.Repeat("长文本 abc\n", 600)
	if _, err := r.Call(context.Background(), "write", map[string]any{"path": "draft.txt", "content": content}); err != nil {
		t.Fatal(err)
	}
	replacement := strings.Repeat("updated\n", 650)
	if _, err := r.Call(context.Background(), "edit", map[string]any{"path": "draft.txt", "old": content, "new": replacement}); err != nil {
		t.Fatal(err)
	}
	if got, err := r.Call(context.Background(), "read", map[string]any{"path": "draft.txt"}); err != nil || got != replacement {
		t.Fatalf("read edited long file: len=%d err=%v", len(got), err)
	}
}
