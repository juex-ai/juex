package usermedia

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"path/filepath"
	"strings"

	"github.com/juex-ai/juex/internal/artifact"
	"github.com/juex-ai/juex/internal/llm"
)

const (
	DefaultMaxBytes = 10 * 1024 * 1024
	DefaultMaxCount = 8
)

type MediaRef = llm.MediaRef

type Limits struct {
	MaxBytes int64
	MaxCount int
}

func StoreUpload(workDir, sessionID, filename string, r io.Reader, limits Limits) (MediaRef, error) {
	if strings.TrimSpace(workDir) == "" {
		return MediaRef{}, errors.New("user media: missing work dir")
	}
	if !safeSessionID(sessionID) {
		return MediaRef{}, fmt.Errorf("user media: unsafe session id %q", sessionID)
	}
	if r == nil {
		return MediaRef{}, errors.New("user media: missing upload body")
	}
	limits = effectiveLimits(limits)
	data, err := readLimited(r, limits.MaxBytes)
	if err != nil {
		return MediaRef{}, err
	}
	mediaType, ext, err := uploadedImageType(data, filename)
	if err != nil {
		return MediaRef{}, err
	}
	width, height, ok := imageDimensions(data)
	if !ok && mediaType != "image/webp" {
		return MediaRef{}, errors.New("user media: invalid image data")
	}
	store, err := artifact.NewStore(workDir)
	if err != nil {
		return MediaRef{}, fmt.Errorf("user media: artifact store: %w", err)
	}
	stored, err := store.PutContentAddressed(path.Join("media", sessionID), ext, data)
	if err != nil {
		return MediaRef{}, fmt.Errorf("user media: store upload: %w", err)
	}
	return MediaRef{
		ArtifactPath:  stored.Path,
		MediaType:     mediaType,
		SHA256:        stored.SHA256,
		OriginalBytes: stored.Bytes,
		Width:         width,
		Height:        height,
	}, nil
}

func ValidateSessionMediaRefs(workDir, sessionID string, refs []MediaRef, limits Limits) error {
	if len(refs) == 0 {
		return nil
	}
	if strings.TrimSpace(workDir) == "" {
		return errors.New("user media: missing work dir")
	}
	if !safeSessionID(sessionID) {
		return fmt.Errorf("user media: unsafe session id %q", sessionID)
	}
	limits = effectiveLimits(limits)
	if limits.MaxCount > 0 && len(refs) > limits.MaxCount {
		return fmt.Errorf("user media: too many images (%d/%d)", len(refs), limits.MaxCount)
	}
	store, err := artifact.NewStore(workDir)
	if err != nil {
		return fmt.Errorf("user media: artifact store: %w", err)
	}
	for i, ref := range refs {
		if err := validateSessionMediaRef(store, sessionID, ref, limits); err != nil {
			return fmt.Errorf("user media ref %d: %w", i, err)
		}
	}
	return nil
}

func validateSessionMediaRef(store artifact.Store, sessionID string, ref MediaRef, limits Limits) error {
	artifactPath, err := sessionArtifactPath(sessionID, ref.ArtifactPath)
	if err != nil {
		return err
	}
	if strings.TrimSpace(ref.SHA256) == "" {
		return errors.New("missing sha256")
	}
	data, err := store.ReadLimit(artifact.Ref{
		Path:   artifactPath,
		SHA256: ref.SHA256,
		Bytes:  ref.OriginalBytes,
	}, limits.MaxBytes)
	if err != nil {
		if errors.Is(err, artifact.ErrTooLarge) {
			return fmt.Errorf("media exceeds %d bytes: %w", limits.MaxBytes, err)
		}
		return fmt.Errorf("verify media: %w", err)
	}
	mediaType, _, err := uploadedImageType(data, artifactPath)
	if err != nil {
		return err
	}
	if ref.MediaType != "" && normalizeMediaType(ref.MediaType) != mediaType {
		return fmt.Errorf("media type mismatch: ref=%s actual=%s", ref.MediaType, mediaType)
	}
	width, height, ok := imageDimensions(data)
	if !ok && mediaType != "image/webp" {
		return errors.New("invalid image data")
	}
	if ref.Width > 0 && width > 0 && ref.Width != width {
		return fmt.Errorf("image width mismatch: ref=%d actual=%d", ref.Width, width)
	}
	if ref.Height > 0 && height > 0 && ref.Height != height {
		return fmt.Errorf("image height mismatch: ref=%d actual=%d", ref.Height, height)
	}
	return nil
}

func readLimited(r io.Reader, maxBytes int64) ([]byte, error) {
	var limit int64 = DefaultMaxBytes
	if maxBytes > 0 {
		limit = maxBytes
	}
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, fmt.Errorf("user media: read upload: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("user media: upload exceeds %d bytes", limit)
	}
	if len(data) == 0 {
		return nil, errors.New("user media: empty upload")
	}
	return data, nil
}

func uploadedImageType(data []byte, filename string) (mediaType, ext string, err error) {
	detected := normalizeMediaType(http.DetectContentType(data))
	switch detected {
	case "image/png":
		return "image/png", ".png", nil
	case "image/jpeg":
		return "image/jpeg", ".jpg", nil
	case "image/gif":
		return "image/gif", ".gif", nil
	case "image/webp":
		return "image/webp", ".webp", nil
	}
	if isWebP(data) {
		return "image/webp", ".webp", nil
	}
	extType := normalizeMediaType(mime.TypeByExtension(strings.ToLower(filepath.Ext(filename))))
	if extType == "image/webp" && isWebP(data) {
		return "image/webp", ".webp", nil
	}
	return "", "", fmt.Errorf("user media: unsupported image type %q", detected)
}

func imageDimensions(data []byte) (int, int, bool) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0, false
	}
	return cfg.Width, cfg.Height, true
}

func sessionArtifactPath(sessionID, artifactPath string) (string, error) {
	artifactPath = strings.TrimSpace(artifactPath)
	if artifactPath == "" {
		return "", errors.New("missing artifact path")
	}
	root := path.Join(".juex", "artifacts", "media", sessionID)
	if strings.Contains(artifactPath, `\`) || !fs.ValidPath(artifactPath) || path.Clean(artifactPath) != artifactPath || path.Dir(artifactPath) != root {
		return "", errors.New("artifact path is outside session media root")
	}
	return artifactPath, nil
}

func effectiveLimits(limits Limits) Limits {
	if limits.MaxBytes <= 0 {
		limits.MaxBytes = DefaultMaxBytes
	}
	if limits.MaxCount <= 0 {
		limits.MaxCount = DefaultMaxCount
	}
	return limits
}

func safeSessionID(id string) bool {
	id = strings.TrimSpace(id)
	return id != "" &&
		!strings.ContainsAny(id, `/\:`) &&
		id != "." &&
		id != ".."
}

func normalizeMediaType(value string) string {
	if i := strings.Index(value, ";"); i >= 0 {
		value = value[:i]
	}
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "image/jpg" {
		return "image/jpeg"
	}
	return value
}

func isWebP(data []byte) bool {
	return len(data) >= 12 &&
		string(data[:4]) == "RIFF" &&
		string(data[8:12]) == "WEBP"
}
