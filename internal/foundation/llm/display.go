package llm

import (
	"fmt"
	"path"
	"strings"
)

// FormatImagePlaceholder returns the terminal-friendly representation of an
// image media reference.
func FormatImagePlaceholder(media *MediaRef) string {
	if media == nil {
		return "[图片: unavailable]"
	}
	name := path.Base(strings.ReplaceAll(media.ArtifactPath, "\\", "/"))
	if name == "." || name == "/" || strings.TrimSpace(name) == "" {
		name = "image"
	}
	var meta []string
	if media.Width > 0 && media.Height > 0 {
		meta = append(meta, fmt.Sprintf("%dx%d", media.Width, media.Height))
	}
	if size := formatMediaBytes(media.OriginalBytes); size != "" {
		meta = append(meta, size)
	}
	if len(meta) == 0 {
		return "[图片: " + name + "]"
	}
	return "[图片: " + name + " (" + strings.Join(meta, ", ") + ")]"
}

// FormatBlocksForTerminal flattens displayable blocks into CLI text while
// preserving image references that would otherwise be invisible.
func FormatBlocksForTerminal(blocks []Block) string {
	var parts []string
	for _, block := range blocks {
		switch block.Type {
		case BlockText:
			if block.Text != "" {
				parts = append(parts, block.Text)
			}
		case BlockImage:
			parts = append(parts, FormatImagePlaceholder(block.Media))
		case BlockToolResult:
			if block.Content != "" {
				parts = append(parts, block.Content)
			}
			if block.Media != nil {
				parts = append(parts, FormatImagePlaceholder(block.Media))
			}
		}
	}
	return strings.Join(parts, "\n")
}

func formatMediaBytes(bytes int) string {
	if bytes <= 0 {
		return ""
	}
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	return fmt.Sprintf("%.1f KB", float64(bytes)/1024)
}
