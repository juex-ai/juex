package runtime

import "testing"

func TestEffectiveToolOutputPolicyScalesDefaultsFromContextWindow(t *testing.T) {
	p := effectiveToolOutputPolicy(ToolOutputPolicy{}, 30_000)
	if p.InlineMaxBytes != 150 || p.PreviewHeadBytes != 75 || p.PreviewTailBytes != 75 {
		t.Fatalf("policy = %+v, want 150-byte inline and 75/75 preview", p)
	}
}

func TestEffectiveToolOutputPolicyPreservesStricterOverrides(t *testing.T) {
	p := effectiveToolOutputPolicy(ToolOutputPolicy{
		InlineMaxBytes:   123,
		PreviewHeadBytes: 45,
		PreviewTailBytes: 67,
	}, 30_000)
	if p.InlineMaxBytes != 123 || p.PreviewHeadBytes != 45 || p.PreviewTailBytes != 67 {
		t.Fatalf("policy = %+v", p)
	}
}
