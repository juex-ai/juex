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

func TestEffectiveToolOutputPolicyClampsSinglePreviewOverrideBeforeDerivingRemainder(t *testing.T) {
	tests := []struct {
		name   string
		policy ToolOutputPolicy
		want   ToolOutputPolicy
	}{
		{
			name:   "head only",
			policy: ToolOutputPolicy{PreviewHeadBytes: 300},
			want:   ToolOutputPolicy{InlineMaxBytes: 150, PreviewHeadBytes: 150, PreviewTailBytes: 0},
		},
		{
			name:   "tail only",
			policy: ToolOutputPolicy{PreviewTailBytes: 300},
			want:   ToolOutputPolicy{InlineMaxBytes: 150, PreviewHeadBytes: 0, PreviewTailBytes: 150},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := effectiveToolOutputPolicy(tt.policy, 30_000); got != tt.want {
				t.Fatalf("policy = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestEffectiveToolOutputPolicyDoesNotIncreaseConfiguredPreviewCeilings(t *testing.T) {
	p := effectiveToolOutputPolicy(ToolOutputPolicy{
		InlineMaxBytes:   100,
		PreviewHeadBytes: 1,
		PreviewTailBytes: 100,
	}, 30_000)
	want := ToolOutputPolicy{
		InlineMaxBytes:   100,
		PreviewHeadBytes: 1,
		PreviewTailBytes: 99,
	}
	if p != want {
		t.Fatalf("policy = %+v, want %+v", p, want)
	}
}
