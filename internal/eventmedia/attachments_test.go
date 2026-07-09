package eventmedia

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateAttachmentsAcceptsWorkdirImage(t *testing.T) {
	workDir := t.TempDir()
	relPath := ".juex/inbox/pixel.png"
	writeAttachmentPNG(t, filepath.Join(workDir, filepath.FromSlash(relPath)))

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
	if got.ArtifactPath != relPath || got.MediaType != "image/png" || got.SHA256 == "" {
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
}

func TestValidateAttachmentsRejectsInvalidInputs(t *testing.T) {
	workDir := t.TempDir()
	relPath := "pixel.png"
	writeAttachmentPNG(t, filepath.Join(workDir, relPath))
	outside := filepath.Join(t.TempDir(), "outside.png")
	writeAttachmentPNG(t, outside)

	tests := []struct {
		name string
		refs []AttachmentRef
		opts ValidationOptions
		want string
	}{
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
