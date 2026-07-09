package llm

import (
	"encoding/base64"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func imagePlaceholderBlock(b Block) Block {
	return Block{Type: BlockText, Text: mediaReferenceText("image", b.Media)}
}

func toolResultContentWithMediaReference(b Block) string {
	ref := mediaReferenceText("tool_result_image", b.Media)
	if strings.TrimSpace(b.Content) == "" {
		return ref
	}
	return b.Content + "\n" + ref
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

func imageDataURL(media *MediaRef) (string, bool) {
	encoded, mediaType, ok := readImageBase64(media)
	if !ok {
		return "", false
	}
	return "data:" + mediaType + ";base64," + encoded, true
}

func readImageBase64(media *MediaRef) (string, string, bool) {
	if media == nil || media.ArtifactPath == "" {
		return "", "", false
	}
	path, ok := safeMediaArtifactPath(media.ArtifactPath)
	if !ok {
		return "", "", false
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return "", "", false
	}
	mediaType := normalizeImageMediaType(media.MediaType, path, data)
	if !supportedImageMediaType(mediaType) {
		return "", "", false
	}
	return base64.StdEncoding.EncodeToString(data), mediaType, true
}

func safeMediaArtifactPath(path string) (string, bool) {
	cleaned := filepath.Clean(strings.TrimSpace(path))
	if cleaned == "." || filepath.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", false
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", false
	}
	resolvedCWD, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return "", false
	}
	resolvedPath, err := filepath.EvalSymlinks(filepath.Join(resolvedCWD, cleaned))
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(resolvedCWD, resolvedPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return resolvedPath, true
}

func normalizeImageMediaType(mediaType, path string, data []byte) string {
	if mediaType = normalizeMediaTypeAlias(strings.TrimSpace(mediaType)); mediaType != "" {
		return mediaType
	}
	if extType := mime.TypeByExtension(filepath.Ext(path)); extType != "" {
		return normalizeMediaTypeAlias(strings.Split(extType, ";")[0])
	}
	return normalizeMediaTypeAlias(http.DetectContentType(data))
}

func normalizeMediaTypeAlias(mediaType string) string {
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if mediaType == "image/jpg" {
		return "image/jpeg"
	}
	return mediaType
}

func supportedImageMediaType(mediaType string) bool {
	switch mediaType {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}
