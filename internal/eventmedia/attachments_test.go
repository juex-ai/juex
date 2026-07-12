package eventmedia

import (
	"encoding/base64"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/artifact"
)

func TestValidateAttachmentsAcceptsWorkdirImage(t *testing.T) {
	workDir := t.TempDir()
	relPath := ".juex/inbox/pixel.png"
	sourcePath := filepath.Join(workDir, filepath.FromSlash(relPath))
	writeAttachmentPNG(t, sourcePath)

	report := ValidateAttachments([]AttachmentRef{{
		Path:      relPath,
		MediaType: "image/png",
	}}, ValidationOptions{WorkDir: workDir})
	if len(report.Errors) != 0 {
		t.Fatalf("errors = %+v, want none", report.Errors)
	}
	if len(report.Valid) != 1 {
		t.Fatalf("valid = %+v, want one attachment", report.Valid)
	}
	got := report.Valid[0]
	if !strings.HasPrefix(got.ArtifactPath, ".juex/artifacts/event-media/") || got.MediaType != "image/png" || got.SHA256 == "" {
		t.Fatalf("validated attachment = %+v", got)
	}
	if got.OriginalBytes <= 0 || got.Width != 1 || got.Height != 1 {
		t.Fatalf("validated metadata = %+v, want byte count and 1x1 dimensions", got)
	}
	resolvedWorkDir, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		t.Fatal(err)
	}
	if got.AbsolutePath == "" || !strings.HasPrefix(got.AbsolutePath, resolvedWorkDir) {
		t.Fatalf("absolute path = %q, want inside workdir %q", got.AbsolutePath, resolvedWorkDir)
	}
	store, err := artifact.NewStore(workDir)
	if err != nil {
		t.Fatal(err)
	}
	ref := artifact.Ref{Path: got.ArtifactPath, SHA256: got.SHA256, Bytes: got.OriginalBytes}
	if err := os.Remove(sourcePath); err != nil {
		t.Fatal(err)
	}
	if data, err := store.Read(ref); err != nil || len(data) == 0 {
		t.Fatalf("stored event artifact after source removal = %d bytes, err=%v", len(data), err)
	}
}

func TestValidateAttachmentsRejectsInvalidInputs(t *testing.T) {
	workDir := t.TempDir()
	relPath := "pixel.png"
	writeAttachmentPNG(t, filepath.Join(workDir, relPath))
	outside := filepath.Join(t.TempDir(), "outside.png")
	writeAttachmentPNG(t, outside)
	if err := os.WriteFile(filepath.Join(workDir, "fake.png"), make([]byte, 32), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		refs []AttachmentRef
		opts ValidationOptions
		want string
	}{
		{
			name: "missing path",
			refs: []AttachmentRef{{}},
			opts: ValidationOptions{WorkDir: workDir},
			want: "path is required",
		},
		{
			name: "missing",
			refs: []AttachmentRef{{Path: "missing.png"}},
			opts: ValidationOptions{WorkDir: workDir},
			want: "does not exist",
		},
		{
			name: "outside",
			refs: []AttachmentRef{{Path: outside}},
			opts: ValidationOptions{WorkDir: workDir},
			want: "outside allowed roots",
		},
		{
			name: "oversize",
			refs: []AttachmentRef{{Path: relPath}},
			opts: ValidationOptions{WorkDir: workDir, MaxAttachmentBytes: 1},
			want: "exceeds",
		},
		{
			name: "mime mismatch",
			refs: []AttachmentRef{{Path: relPath, MediaType: "text/plain"}},
			opts: ValidationOptions{WorkDir: workDir},
			want: "media_type",
		},
		{
			name: "invalid image bytes",
			refs: []AttachmentRef{{Path: "fake.png"}},
			opts: ValidationOptions{WorkDir: workDir},
			want: "invalid image data",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report := ValidateAttachments(tt.refs, tt.opts)
			if len(report.Valid) != 0 {
				t.Fatalf("valid = %+v, want none", report.Valid)
			}
			if len(report.Errors) != 1 || !strings.Contains(report.Errors[0].Error, tt.want) {
				t.Fatalf("errors = %+v, want %q", report.Errors, tt.want)
			}
		})
	}
}

func TestExtractAttachmentRefsRejectsInvalidFieldTypes(t *testing.T) {
	for _, value := range []any{
		[]any{map[string]any{"path": 42}},
		[]any{map[string]any{"path": "image.png", "media_type": 42}},
	} {
		if _, err := ExtractAttachmentRefs(value); err == nil {
			t.Fatalf("ExtractAttachmentRefs(%#v) succeeded, want error", value)
		}
	}
}

func TestExtractAttachmentRefsPreservesValidEntries(t *testing.T) {
	refs, err := ExtractAttachmentRefs([]any{
		map[string]any{"path": "first.png", "media_type": "image/png"},
		map[string]any{"path": 42},
		map[string]any{"path": "second.json", "media_type": "application/json"},
	})
	if err == nil || !strings.Contains(err.Error(), "attachments[1]") {
		t.Fatalf("error = %v, want malformed index", err)
	}
	if len(refs) != 2 || refs[0].Path != "first.png" || refs[1].Path != "second.json" {
		t.Fatalf("refs = %+v, want both valid entries", refs)
	}
}

func TestValidateAttachmentsRejectsTotalEventSizeLimit(t *testing.T) {
	workDir := t.TempDir()
	writeAttachmentPNG(t, filepath.Join(workDir, "a.png"))
	writeAttachmentPNG(t, filepath.Join(workDir, "b.png"))

	report := ValidateAttachments([]AttachmentRef{{Path: "a.png"}, {Path: "b.png"}}, ValidationOptions{
		WorkDir:       workDir,
		MaxEventBytes: 100,
	})
	if len(report.Valid) != 0 {
		t.Fatalf("valid = %+v, want none when event total exceeds limit", report.Valid)
	}
	if len(report.Errors) != 1 || !strings.Contains(report.Errors[0].Error, "event attachments exceed") {
		t.Fatalf("errors = %+v, want total size error", report.Errors)
	}
	if !report.EventBytesExceeded {
		t.Fatal("EventBytesExceeded = false, want structured total size signal")
	}
}

func TestValidateAttachmentsAcceptsDeclaredJSON(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "event.json"), []byte(`{"kind":"deploy"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	report := ValidateAttachments([]AttachmentRef{{
		Path:      "event.json",
		MediaType: "application/json",
	}}, ValidationOptions{WorkDir: workDir})
	if len(report.Errors) != 0 || len(report.Valid) != 1 {
		t.Fatalf("report = %+v, want one valid JSON attachment", report)
	}
	if got := report.Valid[0]; got.MediaType != "application/json" || !strings.HasSuffix(got.ArtifactPath, ".json") {
		t.Fatalf("validated attachment = %+v, want application/json artifact", got)
	}
}

func TestValidateAttachmentsRejectsInvalidDeclaredJSON(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "event.json"), []byte(`not json`), 0o644); err != nil {
		t.Fatal(err)
	}

	report := ValidateAttachments([]AttachmentRef{{
		Path:      "event.json",
		MediaType: "application/json",
	}}, ValidationOptions{WorkDir: workDir})
	if len(report.Valid) != 0 || len(report.Errors) != 1 || !strings.Contains(report.Errors[0].Error, "media_type") {
		t.Fatalf("report = %+v, want invalid JSON media type error", report)
	}
}

func TestValidateAttachmentsParsesWebPDimensions(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "image.webp"), testWebPVP8X(640, 480), 0o644); err != nil {
		t.Fatal(err)
	}

	report := ValidateAttachments([]AttachmentRef{{Path: "image.webp", MediaType: "image/webp"}}, ValidationOptions{WorkDir: workDir})
	if len(report.Errors) != 0 || len(report.Valid) != 1 {
		t.Fatalf("report = %+v, want one valid WebP attachment", report)
	}
	if got := report.Valid[0]; got.Width != 640 || got.Height != 480 {
		t.Fatalf("dimensions = %dx%d, want 640x480", got.Width, got.Height)
	}
}

func TestValidateAttachmentsRejectsTruncatedWebP(t *testing.T) {
	workDir := t.TempDir()
	data := []byte{'R', 'I', 'F', 'F', 4, 0, 0, 0, 'W', 'E', 'B', 'P'}
	if err := os.WriteFile(filepath.Join(workDir, "truncated.webp"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	report := ValidateAttachments([]AttachmentRef{{Path: "truncated.webp", MediaType: "image/webp"}}, ValidationOptions{WorkDir: workDir})
	if len(report.Valid) != 0 || len(report.Errors) != 1 || !strings.Contains(report.Errors[0].Error, "invalid image data") {
		t.Fatalf("report = %+v, want invalid WebP error", report)
	}
}

func testWebPVP8X(width, height int) []byte {
	data := make([]byte, 30)
	copy(data[0:4], "RIFF")
	binary.LittleEndian.PutUint32(data[4:8], uint32(len(data)-8))
	copy(data[8:12], "WEBP")
	copy(data[12:16], "VP8X")
	binary.LittleEndian.PutUint32(data[16:20], 10)
	writeLittleEndian24(data[24:27], width-1)
	writeLittleEndian24(data[27:30], height-1)
	return data
}

func writeLittleEndian24(dst []byte, value int) {
	dst[0] = byte(value)
	dst[1] = byte(value >> 8)
	dst[2] = byte(value >> 16)
}

func writeAttachmentPNG(t *testing.T, path string) {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
