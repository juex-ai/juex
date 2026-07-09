package eventmedia

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const (
	DefaultMaxAttachmentBytes int64 = 10 * 1024 * 1024
	DefaultMaxEventBytes      int64 = 32 * 1024 * 1024
)

type AttachmentRef struct {
	Path      string `json:"path"`
	MediaType string `json:"media_type,omitempty"`
}

type ValidationOptions struct {
	WorkDir            string
	MaxAttachmentBytes int64
	MaxEventBytes      int64
}

type ValidatedAttachment struct {
	Ref           AttachmentRef
	ArtifactPath  string
	AbsolutePath  string
	MediaType     string
	SHA256        string
	OriginalBytes int
	Width         int
	Height        int
}

type AttachmentError struct {
	Index int    `json:"index"`
	Path  string `json:"path,omitempty"`
	Error string `json:"error"`
}

type ValidationReport struct {
	Valid  []ValidatedAttachment
	Errors []AttachmentError
}

func ExtractAttachmentRefs(value any) ([]AttachmentRef, error) {
	if value == nil {
		return nil, nil
	}
	switch typed := value.(type) {
	case []AttachmentRef:
		return normalizeAttachmentRefs(typed), nil
	case []any:
		out := make([]AttachmentRef, 0, len(typed))
		for i, item := range typed {
			ref, err := attachmentRefFromAny(item)
			if err != nil {
				return nil, fmt.Errorf("attachments[%d]: %w", i, err)
			}
			out = append(out, ref)
		}
		return normalizeAttachmentRefs(out), nil
	default:
		return nil, fmt.Errorf("attachments must be an array")
	}
}

func ValidateAttachments(refs []AttachmentRef, opts ValidationOptions) ValidationReport {
	opts = normalizeOptions(opts)
	var report ValidationReport
	var total int64
	for i, ref := range normalizeAttachmentRefs(refs) {
		validated, err := validateAttachment(ref, opts)
		if err != nil {
			report.Errors = append(report.Errors, AttachmentError{Index: i, Path: strings.TrimSpace(ref.Path), Error: err.Error()})
			continue
		}
		total += int64(validated.OriginalBytes)
		report.Valid = append(report.Valid, validated)
	}
	if total > opts.MaxEventBytes {
		return ValidationReport{Errors: []AttachmentError{{
			Index: -1,
			Error: fmt.Sprintf("event attachments exceed %d bytes", opts.MaxEventBytes),
		}}}
	}
	return report
}

func IsImageMediaType(mediaType string) bool {
	switch normalizeMediaType(mediaType) {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}

func normalizeOptions(opts ValidationOptions) ValidationOptions {
	if opts.MaxAttachmentBytes <= 0 {
		opts.MaxAttachmentBytes = DefaultMaxAttachmentBytes
	}
	if opts.MaxEventBytes <= 0 {
		opts.MaxEventBytes = DefaultMaxEventBytes
	}
	return opts
}

func normalizeAttachmentRefs(refs []AttachmentRef) []AttachmentRef {
	out := make([]AttachmentRef, 0, len(refs))
	for _, ref := range refs {
		ref.Path = strings.TrimSpace(ref.Path)
		ref.MediaType = normalizeMediaType(ref.MediaType)
		if ref.Path == "" && ref.MediaType == "" {
			continue
		}
		out = append(out, ref)
	}
	return out
}

func attachmentRefFromAny(value any) (AttachmentRef, error) {
	obj, ok := value.(map[string]any)
	if !ok {
		return AttachmentRef{}, fmt.Errorf("attachment must be an object")
	}
	path, _ := obj["path"].(string)
	mediaType, _ := obj["media_type"].(string)
	if strings.TrimSpace(path) == "" {
		return AttachmentRef{}, fmt.Errorf("path is required")
	}
	return AttachmentRef{Path: path, MediaType: mediaType}, nil
}

func validateAttachment(ref AttachmentRef, opts ValidationOptions) (ValidatedAttachment, error) {
	if strings.TrimSpace(ref.Path) == "" {
		return ValidatedAttachment{}, fmt.Errorf("path is required")
	}
	root, err := resolvedWorkDir(opts.WorkDir)
	if err != nil {
		return ValidatedAttachment{}, err
	}
	absPath, err := resolveAttachmentPath(root, ref.Path)
	if err != nil {
		return ValidatedAttachment{}, err
	}
	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return ValidatedAttachment{}, fmt.Errorf("attachment path does not exist")
		}
		return ValidatedAttachment{}, err
	}
	if info.IsDir() {
		return ValidatedAttachment{}, fmt.Errorf("attachment path is a directory")
	}
	if info.Size() > opts.MaxAttachmentBytes {
		return ValidatedAttachment{}, fmt.Errorf("attachment exceeds %d bytes", opts.MaxAttachmentBytes)
	}
	rel, err := filepath.Rel(root, absPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return ValidatedAttachment{}, fmt.Errorf("attachment path is outside allowed roots")
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return ValidatedAttachment{}, err
	}
	mediaType, err := validatedMediaType(data, absPath, ref.MediaType)
	if err != nil {
		return ValidatedAttachment{}, err
	}
	sum := sha256.Sum256(data)
	width, height := imageDimensions(data, mediaType)
	return ValidatedAttachment{
		Ref: AttachmentRef{
			Path:      strings.TrimSpace(ref.Path),
			MediaType: mediaType,
		},
		ArtifactPath:  filepath.ToSlash(rel),
		AbsolutePath:  absPath,
		MediaType:     mediaType,
		SHA256:        hex.EncodeToString(sum[:]),
		OriginalBytes: len(data),
		Width:         width,
		Height:        height,
	}, nil
}

func resolvedWorkDir(workDir string) (string, error) {
	if strings.TrimSpace(workDir) == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("workdir: %w", err)
		}
		workDir = cwd
	}
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return resolved, nil
}

func resolveAttachmentPath(root, rawPath string) (string, error) {
	path := strings.TrimSpace(rawPath)
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	var candidate string
	if filepath.IsAbs(path) {
		candidate = filepath.Clean(path)
	} else {
		candidate = filepath.Join(root, filepath.FromSlash(path))
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("attachment path does not exist")
		}
		return "", err
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("attachment path is outside allowed roots")
	}
	return resolved, nil
}

func validatedMediaType(data []byte, path string, declared string) (string, error) {
	detected := detectMediaType(data, path)
	declared = normalizeMediaType(declared)
	if declared == "" {
		return detected, nil
	}
	if declared != detected {
		return "", fmt.Errorf("media_type %q does not match detected %q", declared, detected)
	}
	return declared, nil
}

func detectMediaType(data []byte, path string) string {
	sample := data
	if len(sample) > 512 {
		sample = sample[:512]
	}
	detected := normalizeMediaType(http.DetectContentType(sample))
	extType := normalizeMediaType(mime.TypeByExtension(strings.ToLower(filepath.Ext(path))))
	if detected == "application/octet-stream" && extType != "" {
		return extType
	}
	return detected
}

func normalizeMediaType(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if i := strings.Index(value, ";"); i >= 0 {
		value = strings.TrimSpace(value[:i])
	}
	if value == "image/jpg" {
		return "image/jpeg"
	}
	return value
}

func imageDimensions(data []byte, mediaType string) (int, int) {
	if !IsImageMediaType(mediaType) {
		return 0, 0
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}
