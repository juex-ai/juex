package session

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/toolevents"
)

const (
	toolNotStartedContent     = "TOOL_NOT_STARTED: JueX recovered this tool call before execution started. No tool or pre-tool policy was invoked; issue a new tool call if it is still needed."
	toolOutcomeUnknownContent = "TOOL_OUTCOME_UNKNOWN: JueX recorded that this tool call started, but no durable outcome was recorded. It may already have produced external side effects. Do not retry it until the external state has been checked."
)

type TranscriptRepair struct {
	ToolUseID               string         `json:"tool_use_id"`
	ToolName                string         `json:"tool_name,omitempty"`
	RepairMessageID         string         `json:"repair_message_id"`
	InsertedBeforeMessageID string         `json:"inserted_before_message_id,omitempty"`
	Reason                  string         `json:"reason,omitempty"`
	TurnID                  string         `json:"turn_id,omitempty"`
	ProviderIteration       int            `json:"provider_iteration"`
	CallIndex               int            `json:"call_index"`
	AssistantMessageID      string         `json:"assistant_message_id,omitempty"`
	EffectiveInput          map[string]any `json:"effective_input,omitempty"`
	ExecutionPhase          string         `json:"execution_phase"`
	RecoveryCode            string         `json:"recovery_code"`
	OutcomeUnknownRecorded  bool           `json:"-"`
}

type TranscriptRepairedPayload struct {
	Reason  string             `json:"reason,omitempty"`
	Repairs []TranscriptRepair `json:"repairs"`
}

type pendingTranscriptToolUse struct {
	id        string
	name      string
	messageID string
	input     map[string]any
	execution toolCallRecoveryState
}

// RepairTranscript inserts explicit error tool_result messages for assistant
// tool_use blocks that were persisted without a matching result.
func (s *Session) RepairTranscript(reason string) ([]TranscriptRepair, error) {
	if s == nil {
		return nil, nil
	}
	journal, err := ReadEventsWithCatalog(s.Dir, s.eventCatalog)
	if err != nil {
		return nil, err
	}
	return s.repairTranscriptWithEvents(reason, journal)
}

func (s *Session) repairTranscriptWithEvents(reason string, journal []events.Event) ([]TranscriptRepair, error) {
	executions, err := projectToolCallRecovery(journal)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.retryDerivedStateLocked(); err != nil {
		return nil, err
	}

	if s.transcript.repairSafe {
		return recoverPersistedTranscriptRepairs(s.History, reason, executions, journal), nil
	}
	convPath := filepath.Join(s.Dir, conversationFile)
	fullIndex, err := scanTranscriptIndex(convPath)
	if err != nil {
		return nil, err
	}
	fullHistory, err := readTranscriptMessagesForFingerprint(convPath, fullIndex.entries, fullIndex.fingerprint)
	if err != nil {
		return nil, err
	}
	repaired, repairs := repairTranscriptMessagesWithExecutions(fullHistory, reason, executions)
	if len(repairs) == 0 {
		repairs = recoverPersistedTranscriptRepairs(fullHistory, reason, executions, journal)
		fullIndex.repairSafe = true
		fullIndex.repairPrefixSafe = true
		s.transcript = fullIndex
		return repairs, nil
	}
	if err := s.rewriteConversationLocked(repaired); err != nil {
		return nil, err
	}
	return repairs, nil
}

func recoverPersistedTranscriptRepairs(
	history []llm.Message,
	reason string,
	executions toolCallRecoveryProjection,
	journal []events.Event,
) []TranscriptRepair {
	finalized := make(map[string]struct{})
	unknownRecorded := make(map[string]struct{})
	for _, event := range journal {
		switch event.Type {
		case "transcript.repaired":
			var payload TranscriptRepairedPayload
			if decodeRecoveryPayload(event.Payload, &payload) != nil {
				continue
			}
			for _, repair := range payload.Repairs {
				finalized[transcriptRepairKey(repair.RepairMessageID, repair.ToolUseID)] = struct{}{}
			}
		case toolevents.OutcomeUnknownType:
			var payload toolevents.OutcomeUnknownPayload
			if decodeRecoveryPayload(event.Payload, &payload) != nil {
				continue
			}
			unknownRecorded[toolCallRecoveryKey(payload.MessageID, payload.ToolUseID)] = struct{}{}
		}
	}

	toolUses := make(map[string]pendingTranscriptToolUse)
	var repairs []TranscriptRepair
	for messageIndex, msg := range history {
		for _, block := range msg.Blocks {
			if block.Type != llm.BlockToolUse || block.ToolUseID == "" {
				continue
			}
			toolUses[block.ToolUseID] = pendingTranscriptToolUse{
				id:        block.ToolUseID,
				name:      block.ToolName,
				messageID: msg.ID,
				input:     block.Input,
				execution: executions.lookup(msg.ID, block.ToolUseID),
			}
		}
		for _, block := range msg.Blocks {
			if block.Type != llm.BlockToolResult || block.ToolUseID == "" {
				continue
			}
			toolUse, ok := toolUses[block.ToolUseID]
			if !ok || !persistedRepairBlockMatches(block, toolUse) {
				continue
			}
			key := transcriptRepairKey(msg.ID, block.ToolUseID)
			if _, ok := finalized[key]; ok {
				continue
			}
			beforeID := ""
			if messageIndex+1 < len(history) {
				beforeID = history[messageIndex+1].ID
			}
			_, recoveryCode := recoveryToolResult(toolUse)
			_, recorded := unknownRecorded[toolCallRecoveryKey(toolUse.messageID, toolUse.id)]
			repairs = append(repairs, TranscriptRepair{
				ToolUseID:               toolUse.id,
				ToolName:                toolUse.name,
				RepairMessageID:         msg.ID,
				InsertedBeforeMessageID: beforeID,
				Reason:                  reason,
				TurnID:                  toolUse.execution.turnID,
				ProviderIteration:       toolUse.execution.iter,
				CallIndex:               toolUse.execution.callIndex,
				AssistantMessageID:      toolUse.messageID,
				EffectiveInput:          toolUse.execution.input,
				ExecutionPhase:          string(toolUse.execution.phase),
				RecoveryCode:            recoveryCode,
				OutcomeUnknownRecorded:  recorded,
			})
		}
	}
	return repairs
}

func persistedRepairBlockMatches(block llm.Block, toolUse pendingTranscriptToolUse) bool {
	if toolUse.execution.phase != toolCallRecoveryDeclared && toolUse.execution.phase != toolCallRecoveryStarted {
		return false
	}
	expected, _ := recoveryToolResult(toolUse)
	return reflect.DeepEqual(block, expected)
}

func transcriptRepairKey(messageID, toolUseID string) string {
	return messageID + "\x00" + toolUseID
}

func repairTranscriptMessages(history []llm.Message, reason string) ([]llm.Message, []TranscriptRepair) {
	return repairTranscriptMessagesWithExecutions(history, reason, nil)
}

func repairTranscriptMessagesWithExecutions(history []llm.Message, reason string, executions toolCallRecoveryProjection) ([]llm.Message, []TranscriptRepair) {
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
				pending = append(pending, messageToolUses(msg, executions)...)
				continue
			}
		}
		out = append(out, msg)
		pending = append(pending, messageToolUses(msg, executions)...)
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

func messageToolUses(msg llm.Message, executions toolCallRecoveryProjection) []pendingTranscriptToolUse {
	var out []pendingTranscriptToolUse
	for _, block := range msg.Blocks {
		if block.Type == llm.BlockToolUse && block.ToolUseID != "" {
			out = append(out, pendingTranscriptToolUse{
				id:        block.ToolUseID,
				name:      block.ToolName,
				messageID: msg.ID,
				input:     block.Input,
				execution: executions.lookup(msg.ID, block.ToolUseID),
			})
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
	messageID := ""
	for _, item := range pending {
		if item.execution.outcome == nil || item.execution.outcome.MessageID == "" {
			continue
		}
		if messageID == "" {
			messageID = item.execution.outcome.MessageID
		}
	}
	if messageID == "" {
		messageID = newMessageID()
	}
	msg := llm.Message{ID: messageID, Role: llm.RoleUser, Kind: llm.MessageKindToolResult, Blocks: make([]llm.Block, 0, len(pending))}
	repairs := make([]TranscriptRepair, 0, len(pending))
	for _, item := range pending {
		block, recoveryCode := recoveryToolResult(item)
		msg.Blocks = append(msg.Blocks, block)
		repairs = append(repairs, TranscriptRepair{
			ToolUseID:               item.id,
			ToolName:                item.name,
			RepairMessageID:         msg.ID,
			InsertedBeforeMessageID: beforeID,
			Reason:                  reason,
			TurnID:                  item.execution.turnID,
			ProviderIteration:       item.execution.iter,
			CallIndex:               item.execution.callIndex,
			AssistantMessageID:      item.messageID,
			EffectiveInput:          item.execution.input,
			ExecutionPhase:          string(item.execution.phase),
			RecoveryCode:            recoveryCode,
		})
	}
	return msg, repairs
}

func recoveryToolResult(item pendingTranscriptToolUse) (llm.Block, string) {
	if item.execution.outcome != nil {
		block := item.execution.outcome.Block
		block.Type = llm.BlockToolResult
		block.ToolUseID = item.id
		block.ToolName = item.name
		return block, string(toolCallRecoveryOutcomeRecorded)
	}
	content := toolNotStartedContent
	recoveryCode := "TOOL_NOT_STARTED"
	if item.execution.phase == toolCallRecoveryStarted {
		content = toolOutcomeUnknownContent
		if !reflect.DeepEqual(item.input, item.execution.input) {
			if encoded, err := json.Marshal(item.execution.input); err == nil {
				content += "\nEffective input at execution: " + string(encoded)
			}
		}
		recoveryCode = "TOOL_OUTCOME_UNKNOWN"
	}
	return llm.Block{
		Type:      llm.BlockToolResult,
		ToolUseID: item.id,
		ToolName:  item.name,
		Content:   content,
		IsError:   true,
	}, recoveryCode
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
	idx.repairSafe = true
	idx.repairPrefixSafe = true
	idx = activeTranscriptIndex(idx)
	activeHistory, err := readTranscriptMessagesForFingerprint(convPath, idx.entries, idx.fingerprint)
	if err != nil {
		return err
	}
	s.transcript = idx
	s.History = activeHistory
	s.metadataDirty = true
	s.historyDirty = s.historyPath != ""
	convFD, err := openJournalForAppend(convPath, true)
	if err != nil {
		return fmt.Errorf("session: reopen repaired conversation: %w", err)
	}
	s.convFD = convFD
	if s.beforeRepairCheckpointSave != nil {
		s.beforeRepairCheckpointSave()
	}
	metadataErr := s.persistMetadataLocked()
	return metadataErr
}

func writeConversationMessages(path string, history []llm.Message) error {
	var data []byte
	for i, msg := range history {
		buf, err := marshalTranscriptJournalLine(journalSessionID(path), uint64(i+1), msg)
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
