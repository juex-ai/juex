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
