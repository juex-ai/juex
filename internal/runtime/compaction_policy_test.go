package runtime

import (
	"testing"

	"github.com/juex-ai/juex/internal/config"
)

func TestEffectiveCompactionPolicy_ClampsSmallContextWindow(t *testing.T) {
	p := effectiveCompactionPolicy(config.DefaultCompactionConfig(), 6400)
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
