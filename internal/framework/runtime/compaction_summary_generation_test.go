package runtime

import (
	"fmt"
	"github.com/juex-ai/juex/internal/foundation/events"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/llm"
)

func TestCompactionSummaryRetryBudgetNeverExceedsSteppedOrExplicitCeilings(t *testing.T) {
	for _, tc := range []struct {
		name       string
		window     int
		configured int
		initial    int
		retry      int
	}{
		{name: "small window default", window: 32000, initial: 1000, retry: 1000},
		{name: "configured ceiling above derived budget", window: 32000, configured: 2048, initial: 1000, retry: 1000},
		{name: "explicit ceiling at stepped budget", window: 32000, configured: 1000, initial: 1000, retry: 1000},
		{name: "stricter explicit ceiling", window: 32000, configured: 100, initial: 100, retry: 100},
		{name: "million-token window keeps legacy ratio", window: 1000000, initial: 5000, retry: 5000},
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

func TestCompactRetriesOversizedCompleteSummary(t *testing.T) {
	for _, recover := range []bool{true, false} {
		t.Run(fmt.Sprint(recover), func(t *testing.T) {
			oversized := strings.Repeat("x", 4004)
			second := oversized
			if recover {
				second = "Tasks\nKeep CMP-1003\nNext Steps\nRun checks"
			}
			provider := &scriptedCompactionProvider{name: "summary", attempts: []scriptedCompactionAttempt{
				{response: llm.Response{Message: llm.TextMessage(llm.RoleAssistant, oversized), StopReason: llm.StopEndTurn}},
				{response: llm.Response{Message: llm.TextMessage(llm.RoleAssistant, second), StopReason: llm.StopEndTurn}},
			}}
			eng, bus := newEngine(t, provider, false)
			configureCompactionRetryTest(t, eng, 4, 100)
			eng.ContextWindow = 16000
			generation := eng.Thread.CurrentGenerationJournalPath()
			var retry ContextCompactSummaryRetryPayload
			bus.Subscribe("context.compact.summary_retry", func(event events.Event) { retry = event.Payload.(ContextCompactSummaryRetryPayload) })
			_, err := eng.Compact(t.Context(), "compact", "system", "manual", false)
			if recover && err != nil {
				t.Fatal(err)
			}
			if !recover && (err == nil || eng.Thread.CurrentGenerationJournalPath() != generation) {
				t.Fatalf("unbounded result or changed generation: %v", err)
			}
			if provider.calls != 2 || retry.Reason != "summary_budget" {
				t.Fatalf("calls=%d retry=%+v", provider.calls, retry)
			}
			if provider.systems[0] == provider.systems[1] || !strings.Contains(provider.systems[1], "1001") {
				t.Fatalf("retry lacks budget feedback: %s", provider.systems[1])
			}
		})
	}
}

func TestCompactReservesFullContextBeforeSummary(t *testing.T) {
	for _, room := range []int{600, 0} {
		t.Run(fmt.Sprint(room), func(t *testing.T) {
			provider := &scriptedCompactionProvider{name: "summary", attempts: []scriptedCompactionAttempt{{response: llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "Critical Context\nCMP-600\nNext Steps\nVerify"), StopReason: llm.StopEndTurn}}}}
			eng, _ := newEngine(t, provider, false)
			configureCompactionRetryTest(t, eng, 2, 20)
			eng.ContextWindow = 16000
			generation := eng.Thread.CurrentGenerationJournalPath()
			result, err := eng.Compact(t.Context(), "compact", strings.Repeat("s", 4*(12800-room)), "manual", false)
			if room == 0 {
				if err == nil || !strings.Contains(err.Error(), "leave no summary room") || provider.calls != 0 || eng.Thread.CurrentGenerationJournalPath() != generation {
					t.Fatalf("calls=%d err=%v", provider.calls, err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if provider.calls != 1 || provider.options[0].MaxOutputTokens >= 600 || result.TokensAfter > 12800 {
					t.Fatalf("result=%+v opts=%+v", result, provider.options)
				}
			}
		})
	}
}
