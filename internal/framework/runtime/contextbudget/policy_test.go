package contextbudget

import (
	"fmt"
	"testing"
)

func TestEffectiveCompactionPolicyScalesFromThirtyThousandTokenWindow(t *testing.T) {
	p := EffectivePolicy(DefaultCompactionPolicy(), 30_000, 256_000)

	if p.ContextWindow != 30_000 || p.TriggerTokens != 24_000 || p.ReserveTokens != 6_000 {
		t.Fatalf("window/trigger/reserve = %d/%d/%d, want 30000/24000/6000", p.ContextWindow, p.TriggerTokens, p.ReserveTokens)
	}
	if p.SummaryRequestTokens != 27_000 || p.SummaryMaxTokens != 1_000 {
		t.Fatalf("summary request/output = %d/%d, want 27000/1000", p.SummaryRequestTokens, p.SummaryMaxTokens)
	}
	if p.ToolResultMaxTokens != 500 || p.ToolResultMaxChars != 0 {
		t.Fatalf("tool result token/char limits = %d/%d, want 500/0", p.ToolResultMaxTokens, p.ToolResultMaxChars)
	}
	if p.KeepRecentTokens != 2_343 {
		t.Fatalf("keep recent = %d, want floor(30000 * 5 / 64) = 2343", p.KeepRecentTokens)
	}
}

func TestEffectiveCompactionPolicyScalesFromDefaultContextWindow(t *testing.T) {
	p := EffectivePolicy(DefaultCompactionPolicy(), 256_000, 256_000)

	if p.TriggerTokens != 204_800 || p.ReserveTokens != 51_200 {
		t.Fatalf("trigger/reserve = %d/%d, want 204800/51200", p.TriggerTokens, p.ReserveTokens)
	}
	if p.SummaryRequestTokens != 230_400 || p.SummaryMaxTokens != 2_000 || p.ToolResultMaxTokens != 2_000 || p.ToolResultMaxChars != 0 {
		t.Fatalf("summary/tool budgets = request:%d output:%d tool_tokens:%d tool_chars:%d", p.SummaryRequestTokens, p.SummaryMaxTokens, p.ToolResultMaxTokens, p.ToolResultMaxChars)
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
	if p.KeepRecentTokens != 1_000 || p.SummaryMaxTokens != 100 || p.ToolResultMaxTokens != 500 || p.ToolResultMaxChars != 80 {
		t.Fatalf("stricter ceilings not preserved: %+v", p)
	}
	if p.SummaryRequestTokens != 27_000 {
		t.Fatalf("summary request = %d, want candidate ratio 27000", p.SummaryRequestTokens)
	}
}

func TestEffectiveCompactionPolicyUsesSteppedBudgetsBelowOneMillion(t *testing.T) {
	tests := []struct {
		window        int
		summaryTokens int
		toolTokens    int
		toolChars     int
	}{
		{window: 99_999, summaryTokens: 1_000, toolTokens: 500},
		{window: 100_000, summaryTokens: 2_000, toolTokens: 2_000},
		{window: 999_999, summaryTokens: 2_000, toolTokens: 2_000},
		{window: 1_000_000, summaryTokens: 5_000, toolChars: 5_000},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.window), func(t *testing.T) {
			p := EffectivePolicy(DefaultCompactionPolicy(), tt.window, 256_000)
			if p.SummaryMaxTokens != tt.summaryTokens || p.ToolResultMaxTokens != tt.toolTokens || p.ToolResultMaxChars != tt.toolChars {
				t.Fatalf("window %d budgets = summary:%d tool_tokens:%d tool_chars:%d, want %d/%d/%d", tt.window, p.SummaryMaxTokens, p.ToolResultMaxTokens, p.ToolResultMaxChars, tt.summaryTokens, tt.toolTokens, tt.toolChars)
			}
		})
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
