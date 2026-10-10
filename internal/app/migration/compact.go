package migration

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
)

const compactPrefix = "Context compacted automatically because the provider context window is nearing its limit.\n\nSummary of earlier conversation:\n"

// The fixed source renderer records SummaryChars as UTF-8 bytes. A heading
// search could instead cut model-authored text containing that same heading.
func compactBody(message llm.Message) (string, error) {
	meta := message.Compaction
	text := message.FirstText()
	if message.Kind != llm.MessageKindCompact || meta == nil || !strings.HasPrefix(text, compactPrefix) || meta.SummaryChars <= 0 {
		return "", errors.New("unproven source compaction reference section")
	}
	text = strings.TrimPrefix(text, compactPrefix)
	if meta.SummaryChars > len(text) {
		return "", errors.New("invalid source summary byte length")
	}
	body := text[:meta.SummaryChars]
	if !utf8.ValidString(body) || sourceCompactReferences(body, meta.RetainedInputReferences) != text {
		return "", errors.New("source summary reference section differs from its metadata")
	}
	return body, nil
}

// sourceCompactReferences is the deterministic 281889e5 serialization. It is
// used only to prove which source bytes the offline converter may replace.
func sourceCompactReferences(summary string, messages []llm.Message) string {
	if len(messages) == 0 {
		return summary
	}
	var b strings.Builder
	b.WriteString(strings.TrimSpace(summary))
	b.WriteString("\n\nRetained Input References\n")
	for _, message := range messages {
		fmt.Fprintf(&b, "\nMessage %s", message.ID)
		if message.Kind != "" {
			fmt.Fprintf(&b, " (%s)", message.Kind)
		}
		b.WriteString(":\n")
		for _, block := range message.Blocks {
			if block.Type == llm.BlockText && block.Text != "" && (block.Artifact == nil || block.Artifact.SourceKind == "user_input") {
				b.WriteString(block.Text)
				if !strings.HasSuffix(block.Text, "\n") {
					b.WriteByte('\n')
				}
			}
			if block.Type == llm.BlockImage && block.Media != nil {
				m := block.Media
				fmt.Fprintf(&b, "Image: path=%s type=%s sha256=%s bytes=%d", m.ArtifactPath, m.MediaType, m.SHA256, m.OriginalBytes)
				if m.Width > 0 && m.Height > 0 {
					fmt.Fprintf(&b, " size=%dx%d", m.Width, m.Height)
				}
				b.WriteByte('\n')
			}
		}
	}
	return strings.TrimSpace(b.String())
}

func convertedCompactReferences(body string, messages []llm.Message) string {
	var b strings.Builder
	b.WriteString(compactPrefix)
	b.WriteString(body)
	b.WriteString("\n\nRetained Input References\n")
	for _, message := range messages {
		fmt.Fprintf(&b, "\nMessage %s:\n", message.ID)
		for index, block := range message.Blocks {
			field := ""
			if block.Type == llm.BlockText && block.Text != "" {
				field = "text"
			}
			if block.Type == llm.BlockToolResult && block.Content != "" {
				field = "content"
			}
			if field != "" {
				fmt.Fprintf(&b, "Read full %s: read_context(reference=%q, offset=0).\n", field, managedruntime.ContextReference(message.ID, index, field))
			}
			if block.Media != nil {
				fmt.Fprintf(&b, "Image: artifact:%s type=%s sha256=%s\n", block.Media.ArtifactID, block.Media.MediaType, block.Media.SHA256)
			}
		}
	}
	return strings.TrimSpace(b.String())
}
