package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/juex-ai/juex/internal/llm"
)

const interruptedToolResultContent = "JueX recovered an interrupted tool call: no tool result was recorded before the session continued. The tool did not complete; rerun it if still needed."

type TranscriptRepair struct {
	ToolUseID               string `json:"tool_use_id"`
	ToolName                string `json:"tool_name,omitempty"`
	RepairMessageID         string `json:"repair_message_id"`
	InsertedBeforeMessageID string `json:"inserted_before_message_id,omitempty"`
	Reason                  string `json:"reason,omitempty"`
}

type TranscriptRepairedPayload struct {
	Reason  string             `json:"reason,omitempty"`
	Repairs []TranscriptRepair `json:"repairs"`
}

type pendingTranscriptToolUse struct {
	id   string
	name string
}

// RepairTranscript inserts explicit error tool_result messages for assistant
// tool_use blocks that were persisted without a matching result.
func (s *Session) RepairTranscript(reason string) ([]TranscriptRepair, error) {
	if s == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	convPath := filepath.Join(s.Dir, conversationFile)
	history := s.History
	if len(s.transcript.entries) > len(s.History) {
		fullHistory, err := readTranscriptMessages(convPath, s.transcript.entries)
		if err != nil {
			return nil, err
		}
		history = fullHistory
	}
	repaired, repairs := repairTranscriptMessages(history, reason)
	if len(repairs) == 0 {
		return nil, nil
	}
	if err := s.rewriteConversationLocked(repaired); err != nil {
		return nil, err
	}
	return repairs, nil
}

func repairTranscriptMessages(history []llm.Message, reason string) ([]llm.Message, []TranscriptRepair) {
	out := make([]llm.Message, 0, len(history))
	var repairs []TranscriptRepair
	var pending []pendingTranscriptToolUse
	for _, msg := range history {
		if len(pending) > 0 {
			remaining, invalid := consumePendingToolResults(pending, msg)
			if invalid {
				repairMsg, msgRepairs := newTranscriptRepairMessage(pending, reason, msg.ID)
				out = append(out, repairMsg)
				repairs = append(repairs, msgRepairs...)
				pending = nil
			} else {
				out = append(out, msg)
				pending = remaining
				if len(pending) > 0 {
					continue
				}
				pending = append(pending, messageToolUses(msg)...)
				continue
			}
		}
		out = append(out, msg)
		pending = append(pending, messageToolUses(msg)...)
	}
	if len(pending) > 0 {
		repairMsg, msgRepairs := newTranscriptRepairMessage(pending, reason, "")
		out = append(out, repairMsg)
		repairs = append(repairs, msgRepairs...)
	}
	return out, repairs
}

func consumePendingToolResults(pending []pendingTranscriptToolUse, msg llm.Message) ([]pendingTranscriptToolUse, bool) {
	remaining := append([]pendingTranscriptToolUse(nil), pending...)
	for _, block := range msg.Blocks {
		if block.Type == llm.BlockToolResult {
			remaining = removePendingToolUse(remaining, block.ToolUseID)
			continue
		}
		if len(remaining) > 0 && providerVisibleRepairBoundary(block) {
			return pending, true
		}
	}
	return remaining, false
}

func providerVisibleRepairBoundary(block llm.Block) bool {
	switch block.Type {
	case llm.BlockText:
		return block.Text != ""
	case llm.BlockReasoning:
		return block.Text != "" || block.Content != ""
	case llm.BlockToolUse:
		return block.ToolUseID != ""
	default:
		return false
	}
}

func messageToolUses(msg llm.Message) []pendingTranscriptToolUse {
	var out []pendingTranscriptToolUse
	for _, block := range msg.Blocks {
		if block.Type == llm.BlockToolUse && block.ToolUseID != "" {
			out = append(out, pendingTranscriptToolUse{id: block.ToolUseID, name: block.ToolName})
		}
	}
	return out
}

func removePendingToolUse(pending []pendingTranscriptToolUse, id string) []pendingTranscriptToolUse {
	for i, item := range pending {
		if item.id == id {
			return append(pending[:i], pending[i+1:]...)
		}
	}
	return pending
}

func newTranscriptRepairMessage(pending []pendingTranscriptToolUse, reason, beforeID string) (llm.Message, []TranscriptRepair) {
	msg := llm.Message{ID: newMessageID(), Role: llm.RoleUser, Blocks: make([]llm.Block, 0, len(pending))}
	repairs := make([]TranscriptRepair, 0, len(pending))
	for _, item := range pending {
		msg.Blocks = append(msg.Blocks, llm.Block{
			Type:      llm.BlockToolResult,
			ToolUseID: item.id,
			ToolName:  item.name,
			Content:   interruptedToolResultContent,
			IsError:   true,
		})
		repairs = append(repairs, TranscriptRepair{
			ToolUseID:               item.id,
			ToolName:                item.name,
			RepairMessageID:         msg.ID,
			InsertedBeforeMessageID: beforeID,
			Reason:                  reason,
		})
	}
	return msg, repairs
}

func (s *Session) rewriteConversationLocked(history []llm.Message) error {
	if s.convFD != nil {
		if err := s.convFD.Close(); err != nil {
			return err
		}
		s.convFD = nil
	}
	convPath := filepath.Join(s.Dir, conversationFile)
	if err := writeConversationMessages(convPath, history); err != nil {
		return err
	}
	idx, err := scanTranscriptIndex(convPath)
	if err != nil {
		return err
	}
	activeHistory, err := readActiveTranscriptWindow(convPath, idx)
	if err != nil {
		return err
	}
	convFD, err := os.OpenFile(convPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("session: reopen repaired conversation: %w", err)
	}
	s.convFD = convFD
	s.transcript = idx
	s.History = activeHistory
	return nil
}

func writeConversationMessages(path string, history []llm.Message) error {
	var data []byte
	for _, msg := range history {
		buf, err := marshalJSONLine(msg)
		if err != nil {
			return err
		}
		data = append(data, buf...)
	}
	return atomicWriteFile(path, data, 0o644)
}

func marshalJSONLine(v any) ([]byte, error) {
	buf, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return append(buf, '\n'), nil
}
