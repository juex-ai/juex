package contextbudget

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
	got := AssembleActiveContext(h, nil)
	if len(got.Messages) != 3 {
		t.Fatalf("active len = %d", len(got.Messages))
	}
	if got.Messages[0].ID != "compact-1" || got.Messages[1].ID != "tail-1" || got.Messages[2].ID != "new-1" {
		t.Fatalf("active order = %+v", got.Messages)
	}
}

func TestActiveContext_AssemblesExplicitRetainedMessages(t *testing.T) {
	h := []llm.Message{
		testMsg("direct-1", llm.RoleUser, "first"),
		testMsg("assistant-1", llm.RoleAssistant, "answer"),
		testMsg("event-1", llm.RoleUser, "follow-up"),
	}
	compact := testMsg("compact-1", llm.RoleUser, "summary")
	compact.Kind = llm.MessageKindCompact
	compact.Compaction = &llm.CompactionMetadata{RetainedMessageIDs: []string{"direct-1", "event-1"}}
	h = append(h, compact)

	got := AssembleActiveContext(h, nil)
	if len(got.Messages) != 3 || got.Messages[0].ID != "compact-1" || got.Messages[1].ID != "direct-1" || got.Messages[2].ID != "event-1" {
		t.Fatalf("active messages = %+v", got.Messages)
	}
}

func TestActiveContext_SkipsHookEventMessages(t *testing.T) {
	hookTrace := testMsg("hook-1", llm.RoleSystem, "hook check allow UserPromptSubmit in 1ms")
	hookTrace.Kind = llm.MessageKindHookEvent
	h := []llm.Message{
		testMsg("user-1", llm.RoleUser, "start"),
		hookTrace,
		testMsg("assistant-1", llm.RoleAssistant, "ok"),
	}

	got := AssembleActiveContext(h, nil)
	if len(got.Messages) != 2 {
		t.Fatalf("active len = %d, want 2: %+v", len(got.Messages), got.Messages)
	}
	if got.Messages[0].ID != "user-1" || got.Messages[1].ID != "assistant-1" {
		t.Fatalf("active order = %+v", got.Messages)
	}
}

func TestActiveContext_SkipsPolicyBlockedMessages(t *testing.T) {
	blocked := testMsg("blocked-1", llm.RoleUser, "sensitive rejected input")
	blocked.Kind = llm.MessageKindDirect
	blocked.PolicyBlocked = true
	h := []llm.Message{
		testMsg("user-1", llm.RoleUser, "allowed input"),
		blocked,
		testMsg("assistant-1", llm.RoleAssistant, "ok"),
	}

	got := AssembleActiveContext(h, nil)
	if len(got.Messages) != 2 || got.Messages[0].ID != "user-1" || got.Messages[1].ID != "assistant-1" {
		t.Fatalf("active messages = %+v, want policy-blocked input omitted", got.Messages)
	}
}
