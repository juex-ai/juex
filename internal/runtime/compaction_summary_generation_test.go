package runtime

import (
	"testing"

	"github.com/juex-ai/juex/internal/llm"
)

func TestCompactionSummaryRetryBudgetPreservesGenerationRoomAndExplicitCeilings(t *testing.T) {
	for _, tc := range []struct {
		name       string
		window     int
		configured int
		initial    int
		retry      int
	}{
		{name: "small window default", window: 32000, initial: 160, retry: 2048},
		{name: "configured ceiling above derived budget", window: 32000, configured: 2048, initial: 160, retry: 2048},
		{name: "explicit retry ceiling below floor", window: 32000, configured: 1000, initial: 160, retry: 2000},
		{name: "stricter initial ceiling", window: 32000, configured: 100, initial: 100, retry: 200},
		{name: "large window keeps doubled budget", window: 1000000, initial: 5000, retry: 10000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eng := &Engine{Compaction: DefaultCompactionPolicy()}
			eng.Compaction.SummaryMaxTokens = tc.configured
			policy := effectiveCompactionPolicy(eng.Compaction, tc.window)
			policy.SummaryMaxTokens = eng.compactionSummaryInitialMaxOutputTokens("system", llm.Message{}, nil, compactionSummaryState{}, policy, "")
			if policy.SummaryMaxTokens != tc.initial {
				t.Fatalf("initial budget = %d, want %d", policy.SummaryMaxTokens, tc.initial)
			}
			got := eng.compactionSummaryRetryMaxOutputTokens("system", llm.Message{}, nil, compactionSummaryState{}, policy, "")
			if got != tc.retry {
				t.Fatalf("retry budget = %d, want %d", got, tc.retry)
			}
		})
	}
}
