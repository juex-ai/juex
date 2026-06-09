package llm

import (
	"strings"
	"testing"
)

func TestAnthropicStreamDiagnosticsBoundsOversizedLine(t *testing.T) {
	diagnostics := &anthropicStreamDiagnostics{}
	diagnostics.observe([]byte("event: content_block_start\n"))
	diagnostics.observe([]byte("data: "))
	diagnostics.observe([]byte(strings.Repeat("x", anthropicStreamPreviewBytes*8) + "\n\n"))

	got := diagnostics.last()
	if got.EventType != "content_block_start" {
		t.Fatalf("event type = %q, want content_block_start", got.EventType)
	}
	if len(got.RawPreview) == 0 || len(got.RawPreview) > anthropicStreamPreviewBytes {
		t.Fatalf("raw preview length = %d, want within (0, %d]", len(got.RawPreview), anthropicStreamPreviewBytes)
	}
}
