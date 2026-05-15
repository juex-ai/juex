package runtime

import (
	"testing"

	"github.com/juex-ai/juex/internal/llm"
)

func TestActiveContext_AssemblesSummaryBeforeRetainedTail(t *testing.T) {
	h := []llm.Message{
		testMsg("old-1", llm.RoleUser, "old"),
		testMsg("tail-1", llm.RoleUser, "tail"),
	}
	c := testMsg("compact-1", llm.RoleUser, "Summary of earlier conversation:\nold summary")
	c.Kind = llm.MessageKindCompact
	c.Compaction = &llm.CompactionMetadata{TailStartMessageID: "tail-1"}
	h = append(h, c, testMsg("new-1", llm.RoleUser, "new"))
	got := assembleActiveContext(h, nil)
	if len(got.Messages) != 3 {
		t.Fatalf("active len = %d", len(got.Messages))
	}
	if got.Messages[0].ID != "compact-1" || got.Messages[1].ID != "tail-1" || got.Messages[2].ID != "new-1" {
		t.Fatalf("active order = %+v", got.Messages)
	}
}
