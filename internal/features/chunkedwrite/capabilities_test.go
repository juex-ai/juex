package chunkedwrite

import (
	"context"
	"strings"
	"testing"

	toolcore "github.com/juex-ai/juex/internal/foundation/tools"
)

func TestRegisterBuiltinsResolvesAgainstExistingTools(t *testing.T) {
	r := toolcore.NewRegistry()
	r.MustRegister(toolcore.Tool{Name: "skill_load", Handler: func(context.Context, map[string]any) (string, error) { return "guide", nil }})
	registerTestTools(r, testToolOptions{WorkDir: t.TempDir()})
	tool, _ := r.Get("write_begin")
	if !strings.Contains(tool.Description, `skill_load("juex-chunked-write")`) {
		t.Fatalf("existing guide loader was ignored: %s", tool.Description)
	}
}
