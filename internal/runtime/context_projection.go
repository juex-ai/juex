package runtime

import (
	"fmt"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/juex-ai/juex/internal/artifact"
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
)

type projectionStats struct {
	UserInputsExternalized        int
	ToolResultsExternalized       int
	ReasoningContentsStripped     int
	BytesExternalized             int
	ReasoningContentBytesStripped int
}

func (s projectionStats) empty() bool {
	return s.UserInputsExternalized == 0 &&
		s.ToolResultsExternalized == 0 &&
		s.ReasoningContentsStripped == 0
}

func (e *Engine) projectMessageLocked(msg llm.Message, policy compactionPolicy) (llm.Message, projectionStats, error) {
	if e == nil || e.currentSession() == nil || !policy.Enabled {
		return msg, projectionStats{}, nil
	}
	if msg.ID == "" {
		msg.ID = "msg-" + newID()
	}
	var stats projectionStats
	var clonedBlocks []llm.Block
	for i := range msg.Blocks {
		block := msg.Blocks[i]
		if block.Artifact != nil {
			if clonedBlocks != nil {
				clonedBlocks = append(clonedBlocks, block)
			}
			continue
		}
		switch {
		case msg.Kind != llm.MessageKindCompact && msg.Role == llm.RoleUser && block.Type == llm.BlockText && len(block.Text) > policy.UserInputInlineMaxBytes:
			if clonedBlocks == nil {
				clonedBlocks = make([]llm.Block, i, len(msg.Blocks))
				copy(clonedBlocks, msg.Blocks[:i])
			}
			artifact, text, err := e.writeProjectedArtifact("user_input", msg.ID, block, block.Text, policy.UserInputPreviewHeadBytes, policy.UserInputPreviewTailBytes)
			if err != nil {
				return msg, stats, err
			}
			block.Text = text
			block.Artifact = &artifact
			stats.UserInputsExternalized++
			stats.BytesExternalized += artifact.OriginalBytes
		case block.Type == llm.BlockToolResult && len(block.Content) > policy.ToolResultInlineMaxBytes:
			if clonedBlocks == nil {
				clonedBlocks = make([]llm.Block, i, len(msg.Blocks))
				copy(clonedBlocks, msg.Blocks[:i])
			}
			artifact, text, err := e.writeProjectedArtifact("tool_result", msg.ID, block, block.Content, policy.ToolResultPreviewHeadBytes, policy.ToolResultPreviewTailBytes)
			if err != nil {
				return msg, stats, err
			}
			block.Content = text
			block.Artifact = &artifact
			stats.ToolResultsExternalized++
			stats.BytesExternalized += artifact.OriginalBytes
		}
		if clonedBlocks != nil {
			clonedBlocks = append(clonedBlocks, block)
		}
	}
	if clonedBlocks != nil {
		msg.Blocks = clonedBlocks
	}
	return msg, stats, nil
}

func (e *Engine) projectMessagesForProviderLocked(msgs []llm.Message, policy compactionPolicy) ([]llm.Message, projectionStats, error) {
	out := make([]llm.Message, len(msgs))
	var total projectionStats
	for i, msg := range msgs {
		projected, stats, err := e.projectMessageLocked(msg, policy)
		if err != nil {
			return nil, total, err
		}
		projected = projectToolUseInputsForProvider(projected)
		out[i] = projected
		total.UserInputsExternalized += stats.UserInputsExternalized
		total.ToolResultsExternalized += stats.ToolResultsExternalized
		total.BytesExternalized += stats.BytesExternalized
	}
	return llm.FoldChunkedWriteHistoryForProvider(out), total, nil
}

func projectToolUseInputsForProvider(msg llm.Message) llm.Message {
	var cloned []llm.Block
	for i, block := range msg.Blocks {
		if block.Type != llm.BlockToolUse {
			if cloned != nil {
				cloned = append(cloned, block)
			}
			continue
		}
		projectedInput := llm.ProviderToolInput(block.ToolName, block.Input)
		if cloned == nil {
			cloned = make([]llm.Block, i, len(msg.Blocks))
			copy(cloned, msg.Blocks[:i])
		}
		block.Input = projectedInput
		cloned = append(cloned, block)
	}
	if cloned != nil {
		msg.Blocks = cloned
	}
	return msg
}

func stripRedactedReasoningForProviderBudget(systemPrompt string, tools []llm.ToolSpec, msgs []llm.Message, policy compactionPolicy) ([]llm.Message, projectionStats) {
	if !policy.Enabled || policy.TriggerTokens <= 0 {
		return msgs, projectionStats{}
	}
	if estimateContextTokens(systemPrompt, tools, msgs) < policy.TriggerTokens {
		return msgs, projectionStats{}
	}
	out := make([]llm.Message, len(msgs))
	var total projectionStats
	for i, msg := range msgs {
		out[i] = msg
		var cloned []llm.Block
		for j, block := range msg.Blocks {
			if block.Type != llm.BlockReasoning || !block.Redacted || block.Content == "" {
				if cloned != nil {
					cloned = append(cloned, block)
				}
				continue
			}
			if cloned == nil {
				cloned = make([]llm.Block, j, len(msg.Blocks))
				copy(cloned, msg.Blocks[:j])
			}
			total.ReasoningContentsStripped++
			total.ReasoningContentBytesStripped += len(block.Content)
			block.Content = ""
			cloned = append(cloned, block)
		}
		if cloned != nil {
			out[i].Blocks = cloned
		}
	}
	if total.empty() {
		return msgs, projectionStats{}
	}
	return out, total
}

func (e *Engine) writeProjectedArtifact(sourceKind, messageID string, block llm.Block, content string, headBytes, tailBytes int) (llm.ContextArtifactProjection, string, error) {
	store, err := e.projectedArtifactStore()
	if err != nil {
		return llm.ContextArtifactProjection{}, "", err
	}
	relativePath, err := e.projectedArtifactPath(sourceKind, messageID, block)
	if err != nil {
		return llm.ContextArtifactProjection{}, "", err
	}
	ref, err := store.Put(relativePath, []byte(content))
	if err != nil {
		return llm.ContextArtifactProjection{}, "", fmt.Errorf("context artifact: %w", err)
	}
	head, tail := previewParts(content, headBytes, tailBytes)
	projection := llm.ContextArtifactProjection{
		SourceKind:    sourceKind,
		MessageID:     messageID,
		ToolUseID:     block.ToolUseID,
		ToolName:      block.ToolName,
		OriginalBytes: ref.Bytes,
		StoredPath:    ref.Path,
		SHA256:        ref.SHA256,
		HeadBytes:     len(head),
		TailBytes:     len(tail),
		Truncated:     true,
	}
	return projection, providerVisibleArtifactText(projection, head, tail), nil
}

func (e *Engine) projectedArtifactStore() (artifact.Store, error) {
	if e == nil || e.WorkDir == "" {
		return artifact.Store{}, fmt.Errorf("context artifact: missing workspace directory")
	}
	store, err := artifact.NewStore(e.WorkDir)
	if err != nil {
		return artifact.Store{}, fmt.Errorf("context artifact store: %w", err)
	}
	return store, nil
}

func (e *Engine) projectedArtifactPath(sourceKind, messageID string, block llm.Block) (string, error) {
	if e == nil {
		return "", fmt.Errorf("context artifact: missing session identity")
	}
	sess := e.currentSession()
	if sess == nil || sess.ID == "" {
		return "", fmt.Errorf("context artifact: missing session identity")
	}
	var dir, name string
	sessionID := safeArtifactName(sess.ID)
	switch sourceKind {
	case "user_input":
		dir = path.Join("user-inputs", sessionID)
		name = messageID
	case "tool_result":
		dir = path.Join("tool-results", sessionID)
		name = block.ToolUseID
		if name == "" {
			name = messageID
		}
	default:
		return "", fmt.Errorf("context artifact: unknown source kind %q", sourceKind)
	}
	if name == "" {
		name = "item-" + newID()
	}
	return path.Join(dir, safeArtifactName(name)+".txt"), nil
}

func previewParts(content string, headBytes, tailBytes int) (string, string) {
	if headBytes < 0 {
		headBytes = 0
	}
	if tailBytes < 0 {
		tailBytes = 0
	}
	if headBytes > len(content) {
		headBytes = len(content)
	}
	headBytes = utf8BoundaryEnd(content, headBytes)
	remaining := len(content) - headBytes
	if tailBytes > remaining {
		tailBytes = remaining
	}
	head := content[:headBytes]
	tail := ""
	if tailBytes > 0 {
		tailStart := utf8BoundaryStart(content, len(content)-tailBytes)
		if tailStart < len(content) {
			tail = content[tailStart:]
		}
	}
	return head, tail
}

func utf8BoundaryEnd(s string, n int) int {
	if n <= 0 {
		return 0
	}
	if n >= len(s) {
		return len(s)
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return n
}

func utf8BoundaryStart(s string, n int) int {
	if n <= 0 {
		return 0
	}
	if n >= len(s) {
		return len(s)
	}
	for n < len(s) && !utf8.RuneStart(s[n]) {
		n++
	}
	return n
}

func providerVisibleArtifactText(artifact llm.ContextArtifactProjection, head, tail string) string {
	var b strings.Builder
	if artifact.SourceKind == "tool_result" {
		b.WriteString("Tool output stored outside context.\n")
		fmt.Fprintf(&b, "tool_use_id: %s\n", artifact.ToolUseID)
		if artifact.ToolName != "" {
			fmt.Fprintf(&b, "tool_name: %s\n", artifact.ToolName)
		}
	} else {
		b.WriteString("User input stored outside context.\n")
		fmt.Fprintf(&b, "message_id: %s\n", artifact.MessageID)
	}
	fmt.Fprintf(&b, "bytes: %d\nsha256: %s\npath: %s\n\n", artifact.OriginalBytes, artifact.SHA256, artifact.StoredPath)
	b.WriteString("Preview:\n")
	b.WriteString(head)
	if tail != "" {
		b.WriteString("\n...\n")
		b.WriteString(tail)
	}
	return b.String()
}

func safeArtifactName(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '_'
	}, s)
}

func (e *Engine) emitProjectionApplied(turnID string, stats projectionStats) {
	if stats.empty() {
		return
	}
	payload := map[string]any{
		"user_inputs_externalized":  stats.UserInputsExternalized,
		"tool_results_externalized": stats.ToolResultsExternalized,
		"bytes_externalized":        stats.BytesExternalized,
	}
	if stats.ReasoningContentsStripped > 0 {
		payload["reasoning_contents_stripped"] = stats.ReasoningContentsStripped
		payload["reasoning_content_bytes_stripped"] = stats.ReasoningContentBytesStripped
	}
	e.emit(events.Event{Type: "context.projection.applied", TurnID: turnID, Payload: payload})
}
