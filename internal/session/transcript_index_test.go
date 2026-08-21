package session

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/llm"
)

func TestTranscriptMessagePageKeepsToolExchangeCoherent(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name       string
		messages   []llm.Message
		before     string
		limit      int
		wantIDs    string
		wantOldest string
		wantMore   bool
	}{
		{
			name: "initial page",
			messages: []llm.Message{
				messageWithID(compactTestMessage("summary"), "m1"),
				toolUseMessage("m2", "call-1", "read"),
				toolResultMessage("m3", "call-1", "done"),
				messageWithID(llm.TextMessage(llm.RoleAssistant, "latest"), "m4"),
			},
			limit:      2,
			wantIDs:    "m2,m3,m4",
			wantOldest: "m2",
			wantMore:   true,
		},
		{
			name: "multiple result messages",
			messages: []llm.Message{
				messageWithID(compactTestMessage("summary"), "m1"),
				multiToolUseMessage("m2", "call-1", "call-2"),
				toolResultMessage("m3", "call-1", "first"),
				toolResultMessage("m4", "call-2", "second"),
				messageWithID(llm.TextMessage(llm.RoleAssistant, "latest"), "m5"),
			},
			limit:      3,
			wantIDs:    "m2,m3,m4,m5",
			wantOldest: "m2",
			wantMore:   true,
		},
		{
			name: "multiple results in one message",
			messages: []llm.Message{
				messageWithID(compactTestMessage("summary"), "m1"),
				multiToolUseMessage("m2", "call-1", "call-2"),
				multiToolResultMessage("m3", "call-1", "call-2"),
				messageWithID(llm.TextMessage(llm.RoleAssistant, "latest"), "m4"),
			},
			limit:      2,
			wantIDs:    "m2,m3,m4",
			wantOldest: "m2",
			wantMore:   true,
		},
		{
			name: "policy traces between call and result",
			messages: []llm.Message{
				messageWithID(compactTestMessage("summary"), "m1"),
				toolUseMessage("m2", "call-1", "read"),
				policyTraceMessage("m3", "pre hook completed"),
				policyTraceMessage("m4", "post hook completed"),
				toolResultMessage("m5", "call-1", "done"),
				messageWithID(llm.TextMessage(llm.RoleAssistant, "latest"), "m6"),
			},
			limit:      2,
			wantIDs:    "m2,m3,m4,m5,m6",
			wantOldest: "m2",
			wantMore:   true,
		},
		{
			name: "page boundary on policy trace",
			messages: []llm.Message{
				messageWithID(compactTestMessage("summary"), "m1"),
				toolUseMessage("m2", "call-1", "read"),
				policyTraceMessage("m3", "pre hook completed"),
				toolResultMessage("m4", "call-1", "done"),
				messageWithID(llm.TextMessage(llm.RoleAssistant, "latest"), "m5"),
			},
			limit:      3,
			wantIDs:    "m2,m3,m4,m5",
			wantOldest: "m2",
			wantMore:   true,
		},
		{
			name: "boundary sequence starts with orphan result",
			messages: []llm.Message{
				messageWithID(compactTestMessage("summary"), "m1"),
				toolUseMessage("m2", "call-1", "read"),
				toolResultMessage("m3", "missing-call", "orphan"),
				toolResultMessage("m4", "call-1", "done"),
				messageWithID(llm.TextMessage(llm.RoleAssistant, "latest"), "m5"),
			},
			limit:      3,
			wantIDs:    "m2,m3,m4,m5",
			wantOldest: "m2",
			wantMore:   true,
		},
		{
			name: "before page ends before hook result",
			messages: []llm.Message{
				toolUseMessage("m1", "call-1", "read"),
				policyTraceMessage("m2", "pre hook completed"),
				toolResultMessage("m3", "call-1", "done"),
				messageWithID(llm.TextMessage(llm.RoleAssistant, "latest"), "m4"),
			},
			before:     "m3",
			limit:      1,
			wantIDs:    "m2",
			wantOldest: "m2",
			wantMore:   true,
		},
		{
			name: "matched and orphan results in one message",
			messages: []llm.Message{
				messageWithID(compactTestMessage("summary"), "m1"),
				toolUseMessage("m2", "call-1", "read"),
				multiToolResultMessage("m3", "call-1", "missing-call"),
				messageWithID(llm.TextMessage(llm.RoleAssistant, "latest"), "m4"),
			},
			limit:      2,
			wantIDs:    "m2,m3,m4",
			wantOldest: "m2",
			wantMore:   true,
		},
		{
			name: "unmatched result",
			messages: []llm.Message{
				messageWithID(compactTestMessage("summary"), "m1"),
				toolUseMessage("m2", "other-call", "read"),
				toolResultMessage("m3", "missing-call", "orphan"),
				messageWithID(llm.TextMessage(llm.RoleAssistant, "latest"), "m4"),
			},
			limit:      2,
			wantIDs:    "m3,m4",
			wantOldest: "m3",
			wantMore:   true,
		},
		{
			name: "direct message with result block is a hard boundary",
			messages: []llm.Message{
				messageWithID(compactTestMessage("summary"), "m1"),
				toolUseMessage("m2", "call-1", "read"),
				directToolResultMessage("m3", "call-1", "not a protocol result"),
				messageWithID(llm.TextMessage(llm.RoleAssistant, "latest"), "m4"),
			},
			limit:      2,
			wantIDs:    "m3,m4",
			wantOldest: "m3",
			wantMore:   true,
		},
		{
			name: "older page",
			messages: []llm.Message{
				messageWithID(llm.TextMessage(llm.RoleUser, "oldest"), "m1"),
				toolUseMessage("m2", "call-1", "read"),
				toolResultMessage("m3", "call-1", "done"),
				messageWithID(llm.TextMessage(llm.RoleAssistant, "later"), "m4"),
				messageWithID(llm.TextMessage(llm.RoleUser, "newest"), "m5"),
			},
			before:     "m5",
			limit:      2,
			wantIDs:    "m2,m3,m4",
			wantOldest: "m2",
			wantMore:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := makeSession(t, root, "20260812T010101-"+strings.ReplaceAll(tt.name, " ", "-"), tt.messages, time.Now())
			_, page, err := LoadInfoPage(dir, tt.before, tt.limit)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(messageIDsForTest(page.Messages), ","); got != tt.wantIDs {
				t.Fatalf("messages = %s, want %s", got, tt.wantIDs)
			}
			if page.OldestMessageID != tt.wantOldest || page.HasMoreBefore != tt.wantMore {
				t.Fatalf("page = %+v, want oldest %q more %v", page, tt.wantOldest, tt.wantMore)
			}
		})
	}
}

func TestTranscriptContainsMessageIDFallsBackPastOversizedRow(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(messageWithID(llm.TextMessage(llm.RoleUser, "target"), "m1")); err != nil {
		t.Fatal(err)
	}
	oversized := llm.TextMessage(llm.RoleAssistant, strings.Repeat("x", maxEventLineBytes+1))
	if err := s.Append(messageWithID(oversized, "m2")); err != nil {
		t.Fatal(err)
	}
	dir := s.Dir
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	found, err := transcriptContainsMessageID(filepath.Join(dir, conversationFile), "m1")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("target message before oversized row was not found")
	}
}

func messageWithID(msg llm.Message, id string) llm.Message {
	msg.ID = id
	return msg
}

func toolUseMessage(id, toolUseID, name string) llm.Message {
	return llm.Message{
		ID:   id,
		Role: llm.RoleAssistant,
		Blocks: []llm.Block{{
			Type:      llm.BlockToolUse,
			ToolUseID: toolUseID,
			ToolName:  name,
		}},
	}
}

func multiToolUseMessage(id string, toolUseIDs ...string) llm.Message {
	msg := llm.Message{ID: id, Role: llm.RoleAssistant}
	for _, toolUseID := range toolUseIDs {
		msg.Blocks = append(msg.Blocks, llm.Block{
			Type:      llm.BlockToolUse,
			ToolUseID: toolUseID,
			ToolName:  "read",
		})
	}
	return msg
}

func toolResultMessage(id, toolUseID, content string) llm.Message {
	return llm.Message{
		ID:   id,
		Role: llm.RoleUser,
		Kind: llm.MessageKindToolResult,
		Blocks: []llm.Block{{
			Type:      llm.BlockToolResult,
			ToolUseID: toolUseID,
			Content:   content,
		}},
	}
}

func directToolResultMessage(id, toolUseID, content string) llm.Message {
	msg := toolResultMessage(id, toolUseID, content)
	msg.Kind = llm.MessageKindDirect
	return msg
}

func multiToolResultMessage(id string, toolUseIDs ...string) llm.Message {
	msg := llm.Message{ID: id, Role: llm.RoleUser, Kind: llm.MessageKindToolResult}
	for _, toolUseID := range toolUseIDs {
		msg.Blocks = append(msg.Blocks, llm.Block{
			Type:      llm.BlockToolResult,
			ToolUseID: toolUseID,
			Content:   toolUseID + " result",
		})
	}
	return msg
}

func policyTraceMessage(id, text string) llm.Message {
	msg := messageWithID(llm.TextMessage(llm.RoleSystem, text), id)
	msg.Kind = llm.MessageKindPolicyEvent
	return msg
}
