package usermedia

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

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
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	name := sha + ext
	dir, err := ensureSessionMediaDir(workDir, sessionID)
	if err != nil {
		return MediaRef{}, fmt.Errorf("user media: create media dir: %w", err)
	}
	path := filepath.Join(dir, name)
	if err := writeIfMissing(path, data); err != nil {
		return MediaRef{}, err
	}
	return MediaRef{
		ArtifactPath:  filepath.ToSlash(filepath.Join(".juex", "artifacts", "media", sessionID, name)),
		MediaType:     mediaType,
		SHA256:        sha,
		OriginalBytes: len(data),
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
	for i, ref := range refs {
		if err := validateSessionMediaRef(workDir, sessionID, ref, limits); err != nil {
			return fmt.Errorf("user media ref %d: %w", i, err)
		}
	}
	return nil
}

func validateSessionMediaRef(workDir, sessionID string, ref MediaRef, limits Limits) error {
	clean, err := cleanSessionArtifactPath(sessionID, ref.ArtifactPath)
	if err != nil {
		return err
	}
	absPath := filepath.Join(workDir, clean)
	resolvedRoot, resolvedPath, err := resolveExistingInside(filepath.Join(workDir, ".juex", "artifacts", "media", sessionID), absPath)
	if err != nil {
		return err
	}
	if resolvedRoot == "" || resolvedPath == "" {
		return errors.New("path escapes session media root")
	}
	info, err := os.Stat(resolvedPath)
	if err != nil {
		return fmt.Errorf("stat media: %w", err)
	}
	if info.IsDir() {
		return errors.New("media path is a directory")
	}
	if info.Size() > limits.MaxBytes {
		return fmt.Errorf("media exceeds %d bytes", limits.MaxBytes)
	}
	if ref.OriginalBytes > 0 && int64(ref.OriginalBytes) != info.Size() {
		return fmt.Errorf("media size mismatch: ref=%d actual=%d", ref.OriginalBytes, info.Size())
	}
	data, err := os.ReadFile(resolvedPath)
	if err != nil {
		return fmt.Errorf("read media: %w", err)
	}
	mediaType, _, err := uploadedImageType(data, ref.ArtifactPath)
	if err != nil {
		return err
	}
	if ref.MediaType != "" && normalizeMediaType(ref.MediaType) != mediaType {
		return fmt.Errorf("media type mismatch: ref=%s actual=%s", ref.MediaType, mediaType)
	}
	if strings.TrimSpace(ref.SHA256) == "" {
		return errors.New("missing sha256")
	}
	sum := sha256.Sum256(data)
	actualSHA := hex.EncodeToString(sum[:])
	if !strings.EqualFold(ref.SHA256, actualSHA) {
		return fmt.Errorf("sha256 mismatch: ref=%s actual=%s", ref.SHA256, actualSHA)
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

func ensureSessionMediaDir(workDir, sessionID string) (string, error) {
	dir := workDir
	for _, elem := range []string{".juex", "artifacts", "media", sessionID} {
		dir = filepath.Join(dir, elem)
		if err := os.Mkdir(dir, 0o700); err != nil && !os.IsExist(err) {
			return "", err
		}
		info, err := os.Lstat(dir)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("%s is a symlink", dir)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("%s is not a directory", dir)
		}
	}
	return dir, nil
}

func writeIfMissing(path string, data []byte) error {
	if info, err := os.Stat(path); err == nil {
		if info.IsDir() {
			return errors.New("user media: media path is a directory")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("user media: stat media: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".media-upload-*.tmp")
	if err != nil {
		return fmt.Errorf("user media: create temp media: %w", err)
	}
	tmpPath := tmp.Name()
	closed := false
	defer func() {
		if !closed {
			_ = tmp.Close()
		}
		_ = os.Remove(tmpPath)
	}()

	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("user media: write temp media: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("user media: close temp media: %w", err)
	}
	closed = true

	if err := os.Rename(tmpPath, path); err != nil {
		if info, statErr := os.Stat(path); statErr == nil && !info.IsDir() {
			return nil
		} else if statErr == nil && info.IsDir() {
			return errors.New("user media: media path is a directory")
		} else if !os.IsNotExist(statErr) {
			return fmt.Errorf("user media: stat media: %w", statErr)
		}
		return fmt.Errorf("user media: rename media: %w", err)
	}
	return nil
}

func cleanSessionArtifactPath(sessionID, artifactPath string) (string, error) {
	path := strings.TrimSpace(artifactPath)
	if path == "" {
		return "", errors.New("missing artifact path")
	}
	if strings.HasPrefix(path, "/") || filepath.IsAbs(path) {
		return "", errors.New("absolute artifact path is not allowed")
	}
	clean := filepath.Clean(filepath.FromSlash(path))
	if clean == "." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
		return "", errors.New("artifact path escapes work dir")
	}
	root := filepath.Join(".juex", "artifacts", "media", sessionID)
	rel, err := filepath.Rel(root, clean)
	if err != nil {
		return "", err
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", errors.New("artifact path is outside session media root")
	}
	return clean, nil
}

func resolveExistingInside(root, path string) (string, string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", "", err
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", "", err
	}
	resolvedRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return "", "", fmt.Errorf("resolve media root: %w", err)
	}
	resolvedPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return "", "", fmt.Errorf("resolve media path: %w", err)
	}
	rel, err := filepath.Rel(resolvedRoot, resolvedPath)
	if err != nil {
		return "", "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", "", errors.New("path escapes session media root")
	}
	return resolvedRoot, resolvedPath, nil
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
		id == filepath.Base(id) &&
		!strings.Contains(id, "/") &&
		!strings.Contains(id, "\\") &&
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
