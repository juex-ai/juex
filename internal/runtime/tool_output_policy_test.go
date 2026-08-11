package runtime

import "testing"

func TestEffectiveToolOutputPolicy_FillsDefaults(t *testing.T) {
	p := effectiveToolOutputPolicy(ToolOutputPolicy{})
	defaults := DefaultToolOutputPolicy()
	if p != defaults {
		t.Fatalf("policy = %+v, want %+v", p, defaults)
	}
}

func TestEffectiveToolOutputPolicy_PreservesOverrides(t *testing.T) {
	p := effectiveToolOutputPolicy(ToolOutputPolicy{
		InlineMaxBytes:   123,
		PreviewHeadBytes: 45,
		PreviewTailBytes: 67,
	})
	if p.InlineMaxBytes != 123 || p.PreviewHeadBytes != 45 || p.PreviewTailBytes != 67 {
		t.Fatalf("policy = %+v", p)
	}
}
