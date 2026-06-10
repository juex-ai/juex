package runtime

import "testing"

func TestEffectiveCompactionPolicy_ClampsSmallContextWindow(t *testing.T) {
	p := effectiveCompactionPolicy(DefaultCompactionPolicy(), 6400)
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
	p := effectiveCompactionPolicy(CompactionPolicy{Enabled: false}, 6400)
	if p.Enabled {
		t.Fatal("policy enabled = true, want explicit disabled policy preserved")
	}
	if p.ReserveTokens <= 0 || p.KeepRecentTokens <= 0 || p.TriggerTokens <= 0 {
		t.Fatalf("policy defaults were not filled: %+v", p)
	}
}
