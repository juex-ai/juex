package llm

import (
	"fmt"
	"strings"
)

type pendingToolResult struct {
	id   string
	name string
}

// ValidateToolTranscript verifies that provider-visible tool_use blocks have
// matching tool_result blocks before normal conversation continues.
func ValidateToolTranscript(history []Message) error {
	var pending []pendingToolResult
	for msgIndex, msg := range history {
		pendingAtStart := len(pending) > 0
		for blockIndex, block := range msg.Blocks {
			if pendingAtStart && block.Type != BlockToolResult && len(pending) > 0 && providerVisibleValidationBoundary(block) {
				return missingToolResultError(pending, msgIndex, blockIndex)
			}
			switch block.Type {
			case BlockToolUse:
				if block.ToolUseID == "" {
					continue
				}
				pending = append(pending, pendingToolResult{id: block.ToolUseID, name: block.ToolName})
			case BlockToolResult:
				if block.ToolUseID == "" {
					continue
				}
				var removed bool
				pending, removed = removePendingToolResult(pending, block.ToolUseID)
				if !removed {
					return fmt.Errorf("llm: tool_result for unknown function call %q at message %d block %d", block.ToolUseID, msgIndex+1, blockIndex+1)
				}
			}
		}
	}
	if len(pending) > 0 {
		return missingToolResultError(pending, len(history), -1)
	}
	return nil
}

func validateProviderTranscript(history []Message, profile ProviderProfile, opts providerProjectionOptions) error {
	_, _ = profile, opts
	return ValidateToolTranscript(history)
}

func providerVisibleValidationBoundary(block Block) bool {
	switch block.Type {
	case BlockText:
		return block.Text != ""
	case BlockReasoning:
		return block.Text != "" || block.Content != ""
	case BlockToolUse:
		return block.ToolUseID != ""
	default:
		return false
	}
}

func removePendingToolResult(pending []pendingToolResult, id string) ([]pendingToolResult, bool) {
	for i, item := range pending {
		if item.id == id {
			return append(pending[:i], pending[i+1:]...), true
		}
	}
	return pending, false
}

func missingToolResultError(pending []pendingToolResult, msgIndex, blockIndex int) error {
	ids := make([]string, 0, len(pending))
	for _, item := range pending {
		if item.name != "" {
			ids = append(ids, fmt.Sprintf("%s (%s)", item.id, item.name))
			continue
		}
		ids = append(ids, item.id)
	}
	if blockIndex >= 0 {
		return fmt.Errorf("llm: missing tool_result for function call %s before message %d block %d", strings.Join(ids, ", "), msgIndex+1, blockIndex+1)
	}
	return fmt.Errorf("llm: missing tool_result for function call %s before end of transcript", strings.Join(ids, ", "))
}
