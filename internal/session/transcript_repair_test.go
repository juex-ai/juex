package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/events"
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

func TestRepairTranscriptAdoptsCanonicalStateWhenCheckpointSaveFails(t *testing.T) {
	s := newTranscriptRepairSession(t, []llm.Message{
		llm.TextMessage(llm.RoleUser, "search"),
		{Role: llm.RoleAssistant, Blocks: []llm.Block{{
			Type:      llm.BlockToolUse,
			ToolUseID: "call_checkpoint",
			ToolName:  "grep",
		}}},
	})
	defer s.Close()

	metadataPath := filepath.Join(s.Dir, metadataFile)
	originalMetadata, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	s.beforeRepairCheckpointSave = func() {
		if err := os.Remove(metadataPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(metadataPath, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(metadataPath, "block"), []byte("block"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.RepairTranscript("turn_start"); err == nil {
		t.Fatal("repair succeeded despite checkpoint replacement failure")
	}
	s.beforeRepairCheckpointSave = nil
	if err := os.RemoveAll(metadataPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metadataPath, originalMetadata, 0o644); err != nil {
		t.Fatal(err)
	}

	if len(s.History) != 3 {
		t.Fatalf("history len = %d, want adopted repair", len(s.History))
	}
	repairBlock := s.History[2].Blocks[0]
	if repairBlock.Type != llm.BlockToolResult || repairBlock.ToolUseID != "call_checkpoint" || !repairBlock.IsError {
		t.Fatalf("repair block = %+v", repairBlock)
	}
	if repairs, err := s.RepairTranscript("retry"); err != nil || len(repairs) != 0 {
		t.Fatalf("retry repairs = %+v, error = %v; want already-adopted canonical state", repairs, err)
	}
	if err := s.Append(llm.TextMessage(llm.RoleAssistant, "continued")); err != nil {
		t.Fatal(err)
	}

	_, history := s.Snapshot()
	if len(history) != 4 || history[2].Blocks[0].ToolUseID != "call_checkpoint" || history[3].FirstText() != "continued" {
		t.Fatalf("history after append = %+v", history)
	}
	reloaded, err := Load(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	if len(reloaded.History) != 4 || reloaded.History[2].Blocks[0].ToolUseID != "call_checkpoint" || reloaded.History[3].FirstText() != "continued" {
		t.Fatalf("persisted history after append = %+v", reloaded.History)
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

func TestIncrementalRepairStateMatchesFullRepairScan(t *testing.T) {
	tests := []struct {
		name     string
		messages []llm.Message
	}{
		{
			name: "matched tool call",
			messages: []llm.Message{
				toolUseMessage("m1", "call-1", "read"),
				toolResultMessage("m2", "call-1", "done"),
			},
		},
		{
			name: "pending tool call",
			messages: []llm.Message{
				toolUseMessage("m1", "call-1", "read"),
			},
		},
		{
			name: "provider boundary before result",
			messages: []llm.Message{
				toolUseMessage("m1", "call-1", "read"),
				messageWithID(llm.TextMessage(llm.RoleUser, "continue"), "m2"),
				toolResultMessage("m3", "call-1", "late"),
			},
		},
		{
			name: "hook between call and result",
			messages: []llm.Message{
				toolUseMessage("m1", "call-1", "read"),
				hookTraceMessage("m2", "hook"),
				toolResultMessage("m3", "call-1", "done"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idx := transcriptIndex{repairSafe: true, repairPrefixSafe: true, complete: true}
			for i, message := range tt.messages {
				idx.add(message, i, int64(i), 1, uint64(i+1))
			}
			_, repairs := repairTranscriptMessages(tt.messages, "test")
			wantSafe := len(repairs) == 0
			if idx.repairSafe != wantSafe {
				t.Fatalf("incremental repair_safe = %v, full scan = %v", idx.repairSafe, wantSafe)
			}
		})
	}
}

func TestLoadWithRepairTranscriptWritesCompleteEvent(t *testing.T) {
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(llm.TextMessage(llm.RoleUser, "search")); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{
		Type:      llm.BlockToolUse,
		ToolUseID: "call_load",
		ToolName:  "grep",
	}}}); err != nil {
		t.Fatal(err)
	}
	dir := s.Dir
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	repaired, err := LoadWithOptions(dir, Options{RepairTranscript: true})
	if err != nil {
		t.Fatal(err)
	}
	defer repaired.Close()

	data, err := os.ReadFile(filepath.Join(dir, eventsFile))
	if err != nil {
		t.Fatal(err)
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		t.Fatal("expected transcript.repaired event")
	}
	lines := strings.Split(trimmed, "\n")
	var evt events.Event
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &evt); err != nil {
		t.Fatal(err)
	}
	if evt.Type != "transcript.repaired" {
		t.Fatalf("event type = %q, want transcript.repaired", evt.Type)
	}
	if evt.ID == "" {
		t.Fatal("event id is empty")
	}
	if evt.Timestamp.IsZero() {
		t.Fatal("event timestamp is zero")
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
