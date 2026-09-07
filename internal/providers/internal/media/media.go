package media

import (
	"encoding/base64"
	"mime"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/juex-ai/juex/internal/foundation/artifact"
	"github.com/juex-ai/juex/internal/foundation/llm"
)

func ImageDataURL(mediaDir string, media *llm.MediaRef) (string, bool) {
	encoded, mediaType, ok := ReadImageBase64(mediaDir, media)
	if !ok {
		return "", false
	}
	return "data:" + mediaType + ";base64," + encoded, true
}

func ReadImageBase64(mediaDir string, media *llm.MediaRef) (string, string, bool) {
	if media == nil || media.ArtifactPath == "" {
		return "", "", false
	}
	store, err := artifact.NewStore(mediaDir)
	if err != nil {
		return "", "", false
	}
	data, err := store.ReadLimit(artifact.Ref{Path: media.ArtifactPath, SHA256: media.SHA256, Bytes: media.OriginalBytes}, llm.MaxProviderImageArtifactBytes)
	if err != nil || len(data) == 0 {
		return "", "", false
	}
	mediaType := normalizeImageMediaType(media.MediaType, media.ArtifactPath, data)
	if !supportedImageMediaType(mediaType) {
		return "", "", false
	}
	return base64.StdEncoding.EncodeToString(data), mediaType, true
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
