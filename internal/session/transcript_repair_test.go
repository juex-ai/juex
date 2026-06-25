package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/llm"
)

func TestRepairTranscriptInsertsInterruptedToolResultAtTail(t *testing.T) {
	s := newTranscriptRepairSession(t, []llm.Message{
		llm.TextMessage(llm.RoleUser, "search"),
		{Role: llm.RoleAssistant, Blocks: []llm.Block{{
			Type:      llm.BlockToolUse,
			ToolUseID: "call_tail",
			ToolName:  "grep",
			Input:     map[string]any{"pattern": "needle"},
		}}},
	})
	defer s.Close()

	repairs, err := s.RepairTranscript("turn_start")
	if err != nil {
		t.Fatal(err)
	}
	if len(repairs) != 1 {
		t.Fatalf("repairs len = %d, want 1: %+v", len(repairs), repairs)
	}
	if len(s.History) != 3 {
		t.Fatalf("history len = %d, want 3", len(s.History))
	}
	result := s.History[2]
	if result.Role != llm.RoleUser || len(result.Blocks) != 1 {
		t.Fatalf("repair message = %+v", result)
	}
	block := result.Blocks[0]
	if block.Type != llm.BlockToolResult || block.ToolUseID != "call_tail" || block.ToolName != "grep" || !block.IsError {
		t.Fatalf("repair block = %+v", block)
	}
	if !strings.Contains(block.Content, "interrupted tool call") {
		t.Fatalf("repair content = %q", block.Content)
	}

	reloaded, err := Load(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	if len(reloaded.History) != 3 || reloaded.History[2].Blocks[0].ToolUseID != "call_tail" {
		t.Fatalf("persisted history = %+v", reloaded.History)
	}
}

func TestRepairTranscriptInsertsBeforeNormalUserMessage(t *testing.T) {
	s := newTranscriptRepairSession(t, []llm.Message{
		llm.TextMessage(llm.RoleUser, "first"),
		{Role: llm.RoleAssistant, Blocks: []llm.Block{{
			Type:      llm.BlockToolUse,
			ToolUseID: "call_gap",
			ToolName:  "read",
		}}},
		llm.TextMessage(llm.RoleUser, "second"),
	})
	defer s.Close()

	repairs, err := s.RepairTranscript("attach")
	if err != nil {
		t.Fatal(err)
	}
	if len(repairs) != 1 || repairs[0].InsertedBeforeMessageID == "" {
		t.Fatalf("repairs = %+v, want insertion before existing user message", repairs)
	}
	if len(s.History) != 4 {
		t.Fatalf("history len = %d, want 4", len(s.History))
	}
	if got := s.History[2].Blocks[0]; got.Type != llm.BlockToolResult || got.ToolUseID != "call_gap" || !got.IsError {
		t.Fatalf("inserted block = %+v", got)
	}
	if got := s.History[3].FirstText(); got != "second" {
		t.Fatalf("last message = %q, want original user text", got)
	}
}

func TestRepairTranscriptLeavesValidMultiToolHistoryUnchanged(t *testing.T) {
	valid := []llm.Message{
		llm.TextMessage(llm.RoleUser, "batch"),
		{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "a", ToolName: "read"},
			{Type: llm.BlockToolUse, ToolUseID: "b", ToolName: "grep"},
		}},
		{Role: llm.RoleUser, Blocks: []llm.Block{
			{Type: llm.BlockToolResult, ToolUseID: "a", Content: "file"},
			{Type: llm.BlockToolResult, ToolUseID: "b", Content: "match"},
		}},
		llm.TextMessage(llm.RoleAssistant, "done"),
	}
	s := newTranscriptRepairSession(t, valid)
	defer s.Close()

	repairs, err := s.RepairTranscript("turn_start")
	if err != nil {
		t.Fatal(err)
	}
	if len(repairs) != 0 {
		t.Fatalf("repairs = %+v, want none", repairs)
	}
	data, err := os.ReadFile(filepath.Join(s.Dir, conversationFile))
	if err != nil {
		t.Fatal(err)
	}
	if countLines(data) != len(valid) {
		t.Fatalf("conversation lines changed: %s", data)
	}
}

func newTranscriptRepairSession(t *testing.T, messages []llm.Message) *Session {
	t.Helper()
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range messages {
		if err := s.Append(msg); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}
