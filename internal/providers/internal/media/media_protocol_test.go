package media

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/artifact"
	"github.com/juex-ai/juex/internal/foundation/llm"
	eventmedia "github.com/juex-ai/juex/internal/framework/observationmedia"
)

func TestReadImageBase64RejectsUnsafePathsAndMediaTypes(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile("image.txt", []byte("not an image"), 0o600); err != nil {
		t.Fatal(err)
	}
	if encoded, mediaType, ok := ReadImageBase64("", &llm.MediaRef{ArtifactPath: "image.txt", MediaType: "text/plain"}); ok || encoded != "" || mediaType != "" {
		t.Fatalf("text media should be rejected: encoded=%q mediaType=%q ok=%t", encoded, mediaType, ok)
	}
	for _, unsafe := range []string{"/etc/passwd", "../image.png", filepath.Join("..", "image.png")} {
		if encoded, mediaType, ok := ReadImageBase64("", &llm.MediaRef{ArtifactPath: unsafe, MediaType: "image/png"}); ok || encoded != "" || mediaType != "" {
			t.Fatalf("unsafe path %q should be rejected: encoded=%q mediaType=%q ok=%t", unsafe, encoded, mediaType, ok)
		}
	}
}

func TestReadImageBase64ResolvesRelativeArtifactFromWorkDir(t *testing.T) {
	workDir := t.TempDir()
	otherDir := t.TempDir()
	artifactPath := "threads/123456/media/image.png"
	filePath := filepath.Join(workDir, filepath.FromSlash(artifactPath))
	if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte("fake image bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(otherDir)

	encoded, mediaType, ok := ReadImageBase64(workDir, &llm.MediaRef{ArtifactPath: artifactPath, MediaType: "image/png"})
	if !ok || mediaType != "image/png" || encoded == "" {
		t.Fatalf("readImageBase64 = encoded:%q mediaType:%q ok:%t", encoded, mediaType, ok)
	}
}

func TestReadImageBase64ReadsStoredEventAttachmentAfterSourceRemoval(t *testing.T) {
	workDir := t.TempDir()
	mediaDir := filepath.Join(t.TempDir(), "media")
	sourcePath := filepath.Join(workDir, ".juex", "inbox", "event.png")
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourcePath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	report := eventmedia.ValidateAttachments([]eventmedia.AttachmentRef{{
		Path:      ".juex/inbox/event.png",
		MediaType: "image/png",
	}}, eventmedia.ValidationOptions{WorkDir: workDir, MediaDir: mediaDir})
	if len(report.Valid) != 1 || len(report.Errors) != 0 {
		t.Fatalf("event attachment report = %+v", report)
	}
	attachment := report.Valid[0]
	if err := os.Remove(sourcePath); err != nil {
		t.Fatal(err)
	}
	encoded, mediaType, ok := ReadImageBase64(mediaDir, &llm.MediaRef{
		ArtifactPath:  attachment.ArtifactPath,
		MediaType:     attachment.MediaType,
		SHA256:        attachment.SHA256,
		OriginalBytes: attachment.OriginalBytes,
	})
	if !ok || mediaType != "image/png" || encoded == "" {
		t.Fatalf("readImageBase64 = encoded:%q mediaType:%q ok:%t", encoded, mediaType, ok)
	}
}

func TestReadImageBase64RejectsIntegrityMismatch(t *testing.T) {
	workDir := t.TempDir()
	store, err := artifact.NewStore(workDir)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := store.Put("threads/123456/media/image.png", []byte("fake image bytes"))
	if err != nil {
		t.Fatal(err)
	}
	if encoded, mediaType, ok := ReadImageBase64(workDir, &llm.MediaRef{
		ArtifactPath: ref.Path,
		MediaType:    "image/png",
		SHA256:       strings.Repeat("0", 64),
	}); ok || encoded != "" || mediaType != "" {
		t.Fatalf("integrity mismatch accepted: encoded=%q mediaType=%q ok=%t", encoded, mediaType, ok)
	}
}

func TestReadImageBase64RejectsOversizedArtifacts(t *testing.T) {
	mediaDir := t.TempDir()
	if encoded, mediaType, ok := ReadImageBase64(mediaDir, &llm.MediaRef{
		ArtifactPath:  "threads/123456/media/too-large.png",
		MediaType:     "image/png",
		OriginalBytes: llm.MaxProviderImageArtifactBytes + 1,
	}); ok || encoded != "" || mediaType != "" {
		t.Fatalf("oversized metadata accepted: encoded=%q mediaType=%q ok=%t", encoded, mediaType, ok)
	}

	store, err := artifact.NewStore(mediaDir)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := store.Put("threads/123456/media/large.png", bytes.Repeat([]byte("x"), llm.MaxProviderImageArtifactBytes+1))
	if err != nil {
		t.Fatal(err)
	}
	if encoded, mediaType, ok := ReadImageBase64(mediaDir, &llm.MediaRef{
		ArtifactPath: ref.Path,
		MediaType:    "image/png",
		SHA256:       ref.SHA256,
	}); ok || encoded != "" || mediaType != "" {
		t.Fatalf("oversized bytes accepted: encoded=%q mediaType=%q ok=%t", encoded, mediaType, ok)
	}
}
