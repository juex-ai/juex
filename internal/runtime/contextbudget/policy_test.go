package contextbudget

import "testing"

func TestEffectiveCompactionPolicyScalesFromThirtyThousandTokenWindow(t *testing.T) {
	p := EffectivePolicy(DefaultCompactionPolicy(), 30_000, 256_000)

	if p.ContextWindow != 30_000 || p.TriggerTokens != 21_000 || p.ReserveTokens != 9_000 {
		t.Fatalf("window/trigger/reserve = %d/%d/%d, want 30000/21000/9000", p.ContextWindow, p.TriggerTokens, p.ReserveTokens)
	}
	if p.SummaryRequestTokens != 24_000 || p.SummaryMaxTokens != 150 {
		t.Fatalf("summary request/output = %d/%d, want 24000/150", p.SummaryRequestTokens, p.SummaryMaxTokens)
	}
	if p.ToolResultMaxChars != 150 {
		t.Fatalf("tool result max chars = %d, want 150", p.ToolResultMaxChars)
	}
	if p.KeepRecentTokens != 2_343 {
		t.Fatalf("keep recent = %d, want floor(30000 * 5 / 64) = 2343", p.KeepRecentTokens)
	}
}

func TestEffectiveCompactionPolicyScalesFromDefaultContextWindow(t *testing.T) {
	p := EffectivePolicy(DefaultCompactionPolicy(), 256_000, 256_000)

	if p.TriggerTokens != 179_200 || p.ReserveTokens != 76_800 {
		t.Fatalf("trigger/reserve = %d/%d, want 179200/76800", p.TriggerTokens, p.ReserveTokens)
	}
	if p.SummaryRequestTokens != 204_800 || p.SummaryMaxTokens != 1_280 || p.ToolResultMaxChars != 1_280 {
		t.Fatalf("summary/tool budgets = request:%d output:%d tool:%d", p.SummaryRequestTokens, p.SummaryMaxTokens, p.ToolResultMaxChars)
	}
	if p.KeepRecentTokens != 20_000 {
		t.Fatalf("keep recent = %d, want 20000", p.KeepRecentTokens)
	}
}

func TestEffectiveCompactionPolicyTreatsAbsoluteValuesAsStricterCeilings(t *testing.T) {
	p := EffectivePolicy(CompactionPolicy{
		Enabled:            true,
		ReserveTokens:      12_000,
		KeepRecentTokens:   1_000,
		SummaryMaxTokens:   100,
		ToolResultMaxChars: 80,
	}, 30_000, 256_000)

	if p.TriggerTokens != 18_000 || p.ReserveTokens != 12_000 {
		t.Fatalf("trigger/reserve = %d/%d, want stricter 18000/12000", p.TriggerTokens, p.ReserveTokens)
	}
	if p.KeepRecentTokens != 1_000 || p.SummaryMaxTokens != 100 || p.ToolResultMaxChars != 80 {
		t.Fatalf("stricter ceilings not preserved: %+v", p)
	}
	if p.SummaryRequestTokens != 24_000 {
		t.Fatalf("summary request = %d, want candidate ratio 24000", p.SummaryRequestTokens)
	}
}

func TestEffectiveCompactionPolicy_ClampsSmallContextWindow(t *testing.T) {
	p := EffectivePolicy(DefaultCompactionPolicy(), 6400, 200000)
	if p.ReserveTokens <= 0 || p.ReserveTokens >= 6400 {
		t.Fatalf("reserve = %d", p.ReserveTokens)
	}
	if p.KeepRecentTokens >= 6400 {
		t.Fatalf("keep recent = %d", p.KeepRecentTokens)
	}
	if p.TriggerTokens >= 6400 {
		t.Fatalf("trigger = %d", p.TriggerTokens)
	}
}

func TestEffectiveCompactionPolicy_PreservesExplicitDisabledZeroPolicy(t *testing.T) {
	p := EffectivePolicy(CompactionPolicy{Enabled: false}, 6400, 200000)
	if p.Enabled {
		t.Fatal("policy enabled = true, want explicit disabled policy preserved")
	}
	if p.ReserveTokens <= 0 || p.KeepRecentTokens <= 0 || p.TriggerTokens <= 0 {
		t.Fatalf("policy defaults were not filled: %+v", p)
	}
}

func TestEffectiveCompactionPolicy_PreservesInstructionsWithZeroValues(t *testing.T) {
	policy := EffectivePolicy(CompactionPolicy{
		Enabled:      true,
		Instructions: "Preserve exact release evidence.",
	}, 6400, 200000)

	if policy.Instructions != "Preserve exact release evidence." {
		t.Fatalf("instructions = %q", policy.Instructions)
	}
	if policy.ReserveTokens <= 0 || policy.KeepRecentTokens <= 0 {
		t.Fatalf("defaults were not applied: %+v", policy)
	}
}
