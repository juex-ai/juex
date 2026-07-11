package eventmedia

import (
	"bytes"
	"encoding/json"
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

	"github.com/juex-ai/juex/internal/artifact"
)

const (
	DefaultMaxAttachmentBytes int64 = 10 * 1024 * 1024
	DefaultMaxEventBytes      int64 = 32 * 1024 * 1024
)

var errEventAttachmentsTooLarge = errors.New("event attachments exceed byte limit")

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

type inspectedAttachment struct {
	attachment ValidatedAttachment
	data       []byte
	extension  string
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
	var inspected []inspectedAttachment
	for i, ref := range normalizeAttachmentRefs(refs) {
		item, err := inspectAttachment(ref, opts, opts.MaxEventBytes-total)
		if err != nil {
			if errors.Is(err, errEventAttachmentsTooLarge) {
				return eventSizeLimitReport(opts.MaxEventBytes)
			}
			report.Errors = append(report.Errors, AttachmentError{Index: i, Path: strings.TrimSpace(ref.Path), Error: err.Error()})
			continue
		}
		total += int64(item.attachment.OriginalBytes)
		inspected = append(inspected, item)
	}
	if len(inspected) == 0 {
		return report
	}
	store, err := artifact.NewStore(opts.WorkDir)
	if err != nil {
		report.Errors = append(report.Errors, AttachmentError{Index: -1, Error: fmt.Sprintf("store event attachment: %v", err)})
		return report
	}
	for i, item := range inspected {
		stored, err := store.PutContentAddressed("event-media", item.extension, item.data)
		if err != nil {
			report.Errors = append(report.Errors, AttachmentError{Index: i, Path: item.attachment.Ref.Path, Error: fmt.Sprintf("store event attachment: %v", err)})
			continue
		}
		item.attachment.ArtifactPath = stored.Path
		item.attachment.SHA256 = stored.SHA256
		item.attachment.OriginalBytes = stored.Bytes
		report.Valid = append(report.Valid, item.attachment)
	}
	return report
}

func eventSizeLimitReport(limit int64) ValidationReport {
	return ValidationReport{Errors: []AttachmentError{{
		Index: -1,
		Error: fmt.Sprintf("event attachments exceed %d bytes", limit),
	}}}
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
		out = append(out, ref)
	}
	return out
}

func attachmentRefFromAny(value any) (AttachmentRef, error) {
	obj, ok := value.(map[string]any)
	if !ok {
		return AttachmentRef{}, fmt.Errorf("attachment must be an object")
	}
	pathValue, ok := obj["path"]
	if !ok {
		return AttachmentRef{}, fmt.Errorf("path is required")
	}
	path, ok := pathValue.(string)
	if !ok {
		return AttachmentRef{}, fmt.Errorf("path must be a string")
	}
	mediaType := ""
	if value, exists := obj["media_type"]; exists {
		var ok bool
		mediaType, ok = value.(string)
		if !ok {
			return AttachmentRef{}, fmt.Errorf("media_type must be a string")
		}
	}
	if strings.TrimSpace(path) == "" {
		return AttachmentRef{}, fmt.Errorf("path is required")
	}
	return AttachmentRef{Path: path, MediaType: mediaType}, nil
}

func inspectAttachment(ref AttachmentRef, opts ValidationOptions, remainingEventBytes int64) (inspectedAttachment, error) {
	if strings.TrimSpace(ref.Path) == "" {
		return inspectedAttachment{}, fmt.Errorf("path is required")
	}
	root, err := resolvedWorkDir(opts.WorkDir)
	if err != nil {
		return inspectedAttachment{}, err
	}
	absPath, err := resolveAttachmentPath(root, ref.Path)
	if err != nil {
		return inspectedAttachment{}, err
	}
	rel, err := filepath.Rel(root, absPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return inspectedAttachment{}, fmt.Errorf("attachment path is outside allowed roots")
	}
	rootFS, err := os.OpenRoot(root)
	if err != nil {
		return inspectedAttachment{}, err
	}
	defer func() { _ = rootFS.Close() }()
	file, err := rootFS.Open(rel)
	if err != nil {
		return inspectedAttachment{}, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return inspectedAttachment{}, err
	}
	if !info.Mode().IsRegular() {
		return inspectedAttachment{}, fmt.Errorf("attachment path is not a regular file")
	}
	if info.Size() > opts.MaxAttachmentBytes {
		return inspectedAttachment{}, fmt.Errorf("attachment exceeds %d bytes", opts.MaxAttachmentBytes)
	}
	if info.Size() > remainingEventBytes {
		return inspectedAttachment{}, errEventAttachmentsTooLarge
	}
	readLimit := min(opts.MaxAttachmentBytes, remainingEventBytes)
	data, err := io.ReadAll(io.LimitReader(file, readLimit+1))
	if err != nil {
		return inspectedAttachment{}, err
	}
	if int64(len(data)) > opts.MaxAttachmentBytes {
		return inspectedAttachment{}, fmt.Errorf("attachment exceeds %d bytes", opts.MaxAttachmentBytes)
	}
	if int64(len(data)) > remainingEventBytes {
		return inspectedAttachment{}, errEventAttachmentsTooLarge
	}
	mediaType, err := validatedMediaType(data, absPath, ref.MediaType)
	if err != nil {
		return inspectedAttachment{}, err
	}
	width, height, err := imageDimensions(data, mediaType)
	if err != nil {
		return inspectedAttachment{}, err
	}
	validated := ValidatedAttachment{
		Ref: AttachmentRef{
			Path:      strings.TrimSpace(ref.Path),
			MediaType: mediaType,
		},
		ArtifactPath:  filepath.ToSlash(rel),
		AbsolutePath:  absPath,
		MediaType:     mediaType,
		OriginalBytes: len(data),
		Width:         width,
		Height:        height,
	}
	return inspectedAttachment{
		attachment: validated,
		data:       data,
		extension:  artifactExtension(mediaType),
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
	if declared != detected && !declaredTypeMatchesContent(declared, detected, data, path) {
		return "", fmt.Errorf("media_type %q does not match detected %q", declared, detected)
	}
	return declared, nil
}

func declaredTypeMatchesContent(declared, detected string, data []byte, path string) bool {
	if declared != "application/json" || detected != "text/plain" {
		return false
	}
	extType := normalizeMediaType(mime.TypeByExtension(strings.ToLower(filepath.Ext(path))))
	return extType == declared && json.Valid(data)
}

func detectMediaType(data []byte, path string) string {
	if isWebP(data) {
		return "image/webp"
	}
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

func imageDimensions(data []byte, mediaType string) (int, int, error) {
	if !IsImageMediaType(mediaType) {
		return 0, 0, nil
	}
	if mediaType == "image/webp" {
		if !isWebP(data) {
			return 0, 0, fmt.Errorf("invalid image data for %s", mediaType)
		}
		return 0, 0, nil
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0, fmt.Errorf("invalid image data for %s", mediaType)
	}
	return cfg.Width, cfg.Height, nil
}

func artifactExtension(mediaType string) string {
	switch mediaType {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "text/plain":
		return ".txt"
	case "application/json":
		return ".json"
	case "application/pdf":
		return ".pdf"
	}
	return ".bin"
}

func isWebP(data []byte) bool {
	return len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP"
}
