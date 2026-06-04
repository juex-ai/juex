package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
)

type projectionStats struct {
	UserInputsExternalized  int
	ToolResultsExternalized int
	BytesExternalized       int
}

func (s projectionStats) empty() bool {
	return s.UserInputsExternalized == 0 && s.ToolResultsExternalized == 0
}

func (e *Engine) projectMessageLocked(msg llm.Message, policy compactionPolicy) (llm.Message, projectionStats, error) {
	if e == nil || e.Session == nil || !policy.Enabled {
		return msg, projectionStats{}, nil
	}
	if msg.ID == "" {
		msg.ID = "msg-" + newID()
	}
	var stats projectionStats
	for i := range msg.Blocks {
		block := &msg.Blocks[i]
		if block.Artifact != nil {
			continue
		}
		switch {
		case msg.Kind != llm.MessageKindCompact && msg.Role == llm.RoleUser && block.Type == llm.BlockText && len(block.Text) > policy.UserInputInlineMaxBytes:
			artifact, text, err := e.writeProjectedArtifact("user_input", msg.ID, *block, block.Text, policy.UserInputPreviewHeadBytes, policy.UserInputPreviewTailBytes)
			if err != nil {
				return msg, stats, err
			}
			block.Text = text
			block.Artifact = &artifact
			stats.UserInputsExternalized++
			stats.BytesExternalized += artifact.OriginalBytes
		case block.Type == llm.BlockToolResult && len(block.Content) > policy.ToolResultInlineMaxBytes:
			artifact, text, err := e.writeProjectedArtifact("tool_result", msg.ID, *block, block.Content, policy.ToolResultPreviewHeadBytes, policy.ToolResultPreviewTailBytes)
			if err != nil {
				return msg, stats, err
			}
			block.Content = text
			block.Artifact = &artifact
			stats.ToolResultsExternalized++
			stats.BytesExternalized += artifact.OriginalBytes
		}
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
		out[i] = projected
		total.UserInputsExternalized += stats.UserInputsExternalized
		total.ToolResultsExternalized += stats.ToolResultsExternalized
		total.BytesExternalized += stats.BytesExternalized
	}
	return out, total, nil
}

func (e *Engine) writeProjectedArtifact(sourceKind, messageID string, block llm.Block, content string, headBytes, tailBytes int) (llm.ContextArtifactProjection, string, error) {
	sum := sha256.Sum256([]byte(content))
	sha := hex.EncodeToString(sum[:])
	path, err := e.artifactPath(sourceKind, messageID, block)
	if err != nil {
		return llm.ContextArtifactProjection{}, "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return llm.ContextArtifactProjection{}, "", fmt.Errorf("context artifact mkdir: %w", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return llm.ContextArtifactProjection{}, "", fmt.Errorf("context artifact write: %w", err)
	}
	head, tail := previewParts(content, headBytes, tailBytes)
	artifact := llm.ContextArtifactProjection{
		SourceKind:    sourceKind,
		MessageID:     messageID,
		ToolUseID:     block.ToolUseID,
		ToolName:      block.ToolName,
		OriginalBytes: len(content),
		StoredPath:    path,
		SHA256:        sha,
		HeadBytes:     len(head),
		TailBytes:     len(tail),
		Truncated:     true,
	}
	return artifact, providerVisibleArtifactText(artifact, head, tail), nil
}

func (e *Engine) artifactPath(sourceKind, messageID string, block llm.Block) (string, error) {
	if e == nil || e.Session == nil || e.Session.Dir == "" {
		return "", fmt.Errorf("context artifact: missing session directory")
	}
	root := filepath.Dir(filepath.Dir(e.Session.Dir))
	var dir, name string
	switch sourceKind {
	case "user_input":
		dir = filepath.Join(root, "artifacts", "user-inputs", e.Session.ID)
		name = messageID
	case "tool_result":
		dir = filepath.Join(root, "artifacts", "tool-results", e.Session.ID)
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
	path := filepath.Join(dir, safeArtifactName(name)+".txt")
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return abs, nil
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
	remaining := len(content) - headBytes
	if tailBytes > remaining {
		tailBytes = remaining
	}
	head := content[:headBytes]
	tail := ""
	if tailBytes > 0 {
		tail = content[len(content)-tailBytes:]
	}
	return head, tail
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
	e.emit(events.Event{Type: "context.projection.applied", TurnID: turnID, Payload: map[string]any{
		"user_inputs_externalized":  stats.UserInputsExternalized,
		"tool_results_externalized": stats.ToolResultsExternalized,
		"bytes_externalized":        stats.BytesExternalized,
	}})
}
