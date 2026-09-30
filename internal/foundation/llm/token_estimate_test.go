package llm

import (
	"strings"
	"testing"
)

func TestEstimateTextTokensClassifiesCJKRunes(t *testing.T) {
	ascii := strings.Repeat("a", 24)
	cjk := strings.Repeat("界", 24)
	mixed := "hello世界"

	if got := EstimateTextTokens(ascii); got != 6 {
		t.Fatalf("ASCII estimate = %d, want 6", got)
	}
	if got := EstimateTextTokens(cjk); got != 24 {
		t.Fatalf("CJK estimate = %d, want one token per rune", got)
	}
	if got := EstimateTextTokens(mixed); got != 4 {
		t.Fatalf("mixed estimate = %d, want ASCII bucket plus CJK runes", got)
	}
	if EstimateTextTokens(cjk) <= EstimateTextTokens(ascii)*2 {
		t.Fatalf("CJK estimate should be materially higher than same-length ASCII")
	}
}

func TestEstimateMessageTokensIncludesImageFootprint(t *testing.T) {
	history := []Message{{
		Role: RoleUser,
		Blocks: []Block{{
			Type: BlockImage,
			Media: &MediaRef{
				ArtifactPath:  "threads/s/media/image.png",
				MediaType:     "image/png",
				SHA256:        strings.Repeat("a", 64),
				OriginalBytes: 1000,
				Width:         1000,
				Height:        1000,
			},
		}},
	}}

	got := EstimateMessageTokens(history)
	if got < 1333 {
		t.Fatalf("image token estimate = %d, want at least pixel-derived footprint", got)
	}
}
