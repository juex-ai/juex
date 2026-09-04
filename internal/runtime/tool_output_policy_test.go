package runtime

import "testing"

func TestEffectiveToolOutputPolicyScalesDefaultsFromContextWindow(t *testing.T) {
	p := effectiveToolOutputPolicy(ToolOutputPolicy{}, 30_000)
	if p.ContentMaxTokens != 500 || p.InlineMaxBytes != 0 || p.PreviewHeadBytes != 0 || p.PreviewTailBytes != 0 {
		t.Fatalf("policy = %+v, want 500-token content budget without default byte ceilings", p)
	}
}

func TestEffectiveToolOutputPolicyPreservesStricterOverrides(t *testing.T) {
	p := effectiveToolOutputPolicy(ToolOutputPolicy{
		InlineMaxBytes:   123,
		PreviewHeadBytes: 45,
		PreviewTailBytes: 67,
	}, 30_000)
	if p.ContentMaxTokens != 500 || p.InlineMaxBytes != 123 || p.PreviewHeadBytes != 45 || p.PreviewTailBytes != 67 {
		t.Fatalf("policy = %+v", p)
	}
}

func TestEffectiveToolOutputPolicyClampsSinglePreviewOverrideBeforeDerivingRemainder(t *testing.T) {
	tests := []struct {
		name   string
		policy ToolOutputPolicy
		want   effectiveToolOutput
	}{
		{
			name:   "head only",
			policy: ToolOutputPolicy{PreviewHeadBytes: 300},
			want:   effectiveToolOutput{ContentMaxTokens: 500, PreviewHeadBytes: 300},
		},
		{
			name:   "tail only",
			policy: ToolOutputPolicy{PreviewTailBytes: 300},
			want:   effectiveToolOutput{ContentMaxTokens: 500, PreviewTailBytes: 300},
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
	want := effectiveToolOutput{
		ContentMaxTokens: 500,
		InlineMaxBytes:   100,
		PreviewHeadBytes: 1,
		PreviewTailBytes: 99,
	}
	if p != want {
		t.Fatalf("policy = %+v, want %+v", p, want)
	}
}

func TestEffectiveToolOutputPolicyPreservesLegacyByteDerivationAtOneMillion(t *testing.T) {
	p := effectiveToolOutputPolicy(ToolOutputPolicy{}, 1_000_000)
	want := effectiveToolOutput{InlineMaxBytes: 5_000, PreviewHeadBytes: 2_500, PreviewTailBytes: 2_500}
	if p != want {
		t.Fatalf("policy = %+v, want %+v", p, want)
	}
}
