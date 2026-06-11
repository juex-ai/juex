package session

import (
	"testing"

	"github.com/juex-ai/juex/internal/events"
)

func TestSessionRuntimeStatsCountsRequestSuccessEvents(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	for _, typ := range []string{
		"llm.requested",
		"llm.requested",
		"llm.responded",
		"tool.requested",
		"tool.requested",
		"tool.completed",
		"tool.errored",
	} {
		if err := s.AppendEvent(events.Event{Type: typ}); err != nil {
			t.Fatal(err)
		}
	}

	stats := s.RuntimeStats()
	if stats.LLMRequests != 2 || stats.LLMSuccesses != 1 || stats.ToolRequests != 2 || stats.ToolSuccesses != 1 {
		t.Fatalf("runtime stats = %+v", stats)
	}
}
