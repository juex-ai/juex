package llm

import (
	"fmt"
	"strings"
)

const MaxProviderImageArtifactBytes = 10 * 1024 * 1024

func imagePlaceholderBlock(b Block) Block {
	return Block{Type: BlockText, Text: UnavailableMediaReferenceText("image", b.Media)}
}

func ToolResultContentWithMediaReference(b Block) string {
	ref := mediaReferenceText("tool_result_image", b.Media)
	return appendMediaReference(b.Content, ref)
}

func ToolResultContentWithUnavailableMediaReference(b Block) string {
	ref := UnavailableMediaReferenceText("tool_result_image", b.Media)
	return appendMediaReference(b.Content, ref)
}

func appendMediaReference(content, ref string) string {
	if strings.TrimSpace(content) == "" {
		return ref
	}
	return content + "\n" + ref
}

func mediaReferenceText(label string, media *MediaRef) string {
	if media == nil {
		return "[" + label + ": missing media reference]"
	}
	parts := make([]string, 0, 6)
	if media.ArtifactPath != "" {
		parts = append(parts, "path="+media.ArtifactPath)
	}
	if media.MediaType != "" {
		parts = append(parts, "type="+media.MediaType)
	}
	if media.SHA256 != "" {
		parts = append(parts, "sha256="+media.SHA256)
	}
	if media.OriginalBytes > 0 {
		parts = append(parts, fmt.Sprintf("bytes=%d", media.OriginalBytes))
	}
	if media.Width > 0 && media.Height > 0 {
		parts = append(parts, fmt.Sprintf("size=%dx%d", media.Width, media.Height))
	}
	if len(parts) == 0 {
		return "[" + label + ": empty media reference]"
	}
	return "[" + label + ": " + strings.Join(parts, " ") + "]"
}

func UnavailableMediaReferenceText(label string, media *MediaRef) string {
	const guidance = "the current model cannot view image content; state that you cannot see the image instead of guessing"
	return strings.TrimSuffix(mediaReferenceText(label, media), "]") + "; " + guidance + "]"
}
