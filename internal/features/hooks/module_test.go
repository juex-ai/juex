package hooks

import (
	"testing"

	hookconfig "github.com/juex-ai/juex/internal/features/hooks/config"
)

func TestModuleResolvesGenerationJournalPathPerRequest(t *testing.T) {
	path := "/threads/0/generations/g000001.jsonl"
	module := NewModule(nil, ModuleOptions{
		GenerationJournalPath: func() string { return path },
	})
	if got := module.request(hookconfig.EventThreadStart).GenerationJournalPath; got != path {
		t.Fatalf("initial Generation Journal path = %q, want %q", got, path)
	}
	path = "/threads/0/generations/g000002.jsonl"
	if got := module.request(hookconfig.EventPreCompact).GenerationJournalPath; got != path {
		t.Fatalf("rolled Generation Journal path = %q, want %q", got, path)
	}
}
