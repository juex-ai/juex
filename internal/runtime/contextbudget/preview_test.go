package contextbudget

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPreviewTextFitsTokenBudgetAcrossScripts(t *testing.T) {
	tests := []struct {
		name string
		text string
	}{
		{name: "ascii", text: strings.Repeat("abcd", 400)},
		{name: "cjk", text: strings.Repeat("上下文", 400)},
		{name: "mixed", text: strings.Repeat("alpha上下文🚀", 300)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			preview := PreviewText(tt.text, 100, 0)
			if !utf8.ValidString(preview.Head) || !utf8.ValidString(preview.Tail) {
				t.Fatalf("preview is not valid UTF-8: %+v", preview)
			}
			if preview.Head == "" || preview.Tail == "" || preview.OmittedBytes <= 0 {
				t.Fatalf("preview = %+v, want head, tail, and omission", preview)
			}
			if got := EstimateTextTokens(preview.Head) + EstimateTextTokens(preview.Tail); got > 100 {
				t.Fatalf("retained preview tokens = %d, want <= 100", got)
			}
			if preview.OmittedBytes != len(tt.text)-len(preview.Head)-len(preview.Tail) {
				t.Fatalf("omitted bytes = %d, want exact content delta", preview.OmittedBytes)
			}
			if preview.OmittedCharacters != utf8.RuneCountInString(tt.text)-utf8.RuneCountInString(preview.Head)-utf8.RuneCountInString(preview.Tail) {
				t.Fatalf("omitted characters = %d, want exact rune delta", preview.OmittedCharacters)
			}
		})
	}
}

func TestPreviewTextAppliesStricterByteCeiling(t *testing.T) {
	text := strings.Repeat("abcd", 100)
	preview := PreviewText(text, 100, 40)
	if got := len(preview.Head) + len(preview.Tail); got > 40 {
		t.Fatalf("retained preview bytes = %d, want <= 40", got)
	}
	if preview.OmittedBytes != len(text)-len(preview.Head)-len(preview.Tail) {
		t.Fatalf("omitted bytes = %d, want exact content delta", preview.OmittedBytes)
	}
	if preview.OmittedCharacters != utf8.RuneCountInString(text)-utf8.RuneCountInString(preview.Head)-utf8.RuneCountInString(preview.Tail) {
		t.Fatalf("omitted characters = %d, want exact rune delta", preview.OmittedCharacters)
	}
}

func TestPreviewTextWithByteAllocationPreservesAsymmetricSides(t *testing.T) {
	text := strings.Repeat("h", 200) + strings.Repeat("t", 200)
	tests := []struct {
		name      string
		headBytes int
		tailBytes int
	}{
		{name: "asymmetric", headBytes: 10, tailBytes: 90},
		{name: "tail only", headBytes: 0, tailBytes: 100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			preview := PreviewTextWithByteAllocation(text, 500, tt.headBytes, tt.tailBytes)
			if len(preview.Head) != tt.headBytes || len(preview.Tail) != tt.tailBytes {
				t.Fatalf("preview head/tail bytes = %d/%d, want %d/%d", len(preview.Head), len(preview.Tail), tt.headBytes, tt.tailBytes)
			}
			if got := EstimateTextTokens(preview.Head) + EstimateTextTokens(preview.Tail); got > 500 {
				t.Fatalf("preview tokens = %d, want <= 500", got)
			}
		})
	}
}
