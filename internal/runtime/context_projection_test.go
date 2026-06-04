package runtime

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/llm"
)

func TestProjectMessageLockedDoesNotMutateOriginalBlocks(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	cfg := config.DefaultCompactionConfig()
	cfg.UserInputInlineMaxBytes = 64
	cfg.UserInputPreviewHeadBytes = 8
	cfg.UserInputPreviewTailBytes = 8
	policy := effectiveCompactionPolicy(cfg, DefaultContextWindowTokens)
	original := "head-" + strings.Repeat("secret ", 40) + "-tail"
	if err := eng.Session.Append(llm.Message{
		ID:   "legacy",
		Role: llm.RoleUser,
		Blocks: []llm.Block{{
			Type: llm.BlockText,
			Text: original,
		}},
	}); err != nil {
		t.Fatal(err)
	}

	projected, stats, err := eng.projectMessageLocked(eng.Session.History[0], policy)
	if err != nil {
		t.Fatal(err)
	}
	if stats.UserInputsExternalized != 1 {
		t.Fatalf("stats = %+v, want one externalized input", stats)
	}
	if got := eng.Session.History[0].Blocks[0].Text; got != original {
		t.Fatalf("session history was mutated: got %q", got)
	}
	if eng.Session.History[0].Blocks[0].Artifact != nil {
		t.Fatalf("session history artifact = %+v, want nil", eng.Session.History[0].Blocks[0].Artifact)
	}
	if projected.Blocks[0].Artifact == nil || !strings.Contains(projected.Blocks[0].Text, "User input stored outside context.") {
		t.Fatalf("projected block missing artifact projection: %+v", projected.Blocks[0])
	}
}

func TestPreviewPartsKeepsUTF8Boundaries(t *testing.T) {
	content := strings.Repeat("界", 4) + "middle" + strings.Repeat("尾", 4)
	head, tail := previewParts(content, 4, 4)
	if !utf8.ValidString(head) || !utf8.ValidString(tail) {
		t.Fatalf("invalid utf8 preview head=%q tail=%q", head, tail)
	}
	if !strings.HasPrefix(content, head) {
		t.Fatalf("head %q is not a content prefix", head)
	}
	if !strings.HasSuffix(content, tail) {
		t.Fatalf("tail %q is not a content suffix", tail)
	}
	if head != "界" || tail != "尾" {
		t.Fatalf("head/tail = %q/%q, want complete runes", head, tail)
	}
}
