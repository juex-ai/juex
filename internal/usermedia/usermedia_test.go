package usermedia

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestStoreUploadStoresSessionScopedImage(t *testing.T) {
	workDir := t.TempDir()
	data := testPNG(t)
	limits := Limits{MaxBytes: int64(len(data) + 1), MaxCount: 8}

	ref, err := StoreUpload(workDir, "session-1", "screen.png", bytes.NewReader(data), limits)
	if err != nil {
		t.Fatal(err)
	}

	wantSHA := sha256Hex(data)
	if ref.SHA256 != wantSHA {
		t.Fatalf("sha256 = %q, want %q", ref.SHA256, wantSHA)
	}
	if ref.MediaType != "image/png" {
		t.Fatalf("media type = %q, want image/png", ref.MediaType)
	}
	if ref.OriginalBytes != len(data) || ref.Width != 2 || ref.Height != 3 {
		t.Fatalf("media metadata = bytes:%d size:%dx%d", ref.OriginalBytes, ref.Width, ref.Height)
	}
	wantPrefix := filepath.ToSlash(filepath.Join(".juex", "artifacts", "media", "session-1")) + "/"
	if !strings.HasPrefix(ref.ArtifactPath, wantPrefix) || !strings.HasSuffix(ref.ArtifactPath, ".png") {
		t.Fatalf("artifact path = %q, want under %q with .png", ref.ArtifactPath, wantPrefix)
	}

	stored, err := os.ReadFile(filepath.Join(workDir, filepath.FromSlash(ref.ArtifactPath)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, data) {
		t.Fatalf("stored bytes changed")
	}

	duplicate, err := StoreUpload(workDir, "session-1", "other-name.png", bytes.NewReader(data), limits)
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.ArtifactPath != ref.ArtifactPath {
		t.Fatalf("duplicate path = %q, want %q", duplicate.ArtifactPath, ref.ArtifactPath)
	}
}

func TestStoreUploadRejectsUnsupportedAndOversizedFiles(t *testing.T) {
	workDir := t.TempDir()

	if _, err := StoreUpload(workDir, "session-1", "note.txt", strings.NewReader("not an image"), Limits{MaxBytes: 1024, MaxCount: 8}); err == nil {
		t.Fatalf("StoreUpload accepted non-image content")
	}

	data := testPNG(t)
	if _, err := StoreUpload(workDir, "session-1", "screen.png", bytes.NewReader(data), Limits{MaxBytes: int64(len(data) - 1), MaxCount: 8}); err == nil {
		t.Fatalf("StoreUpload accepted oversized content")
	}
}

func TestInspectFilesAndStoreFilesResolveLocalPaths(t *testing.T) {
	workDir := t.TempDir()
	data := testPNG(t)
	relativePath := filepath.Join("inputs", "screen one.png")
	if err := os.MkdirAll(filepath.Join(workDir, "inputs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, relativePath), data, 0o644); err != nil {
		t.Fatal(err)
	}
	absolutePath := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(absolutePath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	limits := Limits{MaxBytes: int64(len(data) + 1), MaxCount: 2}

	infos, err := InspectFiles(workDir, []string{relativePath, absolutePath}, limits)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 2 {
		t.Fatalf("infos len = %d, want 2", len(infos))
	}
	for i, info := range infos {
		if info.MediaType != "image/png" || info.Bytes != len(data) || info.Width != 2 || info.Height != 3 {
			t.Fatalf("info %d = %+v", i, info)
		}
		if !filepath.IsAbs(info.Path) {
			t.Fatalf("info %d path = %q, want absolute", i, info.Path)
		}
	}
	if _, err := os.Stat(filepath.Join(workDir, ".juex", "artifacts", "media")); !os.IsNotExist(err) {
		t.Fatalf("InspectFiles wrote artifacts: %v", err)
	}

	refs, err := StoreFiles(workDir, "session-1", []string{relativePath, absolutePath}, limits)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 || refs[0].ArtifactPath != refs[1].ArtifactPath {
		t.Fatalf("refs = %+v, want two deduplicated content references", refs)
	}
	if err := ValidateSessionMediaRefs(workDir, "session-1", refs, limits); err != nil {
		t.Fatalf("stored refs failed validation: %v", err)
	}
}

func TestInspectFilesRejectsInvalidPathsAndCount(t *testing.T) {
	workDir := t.TempDir()
	data := testPNG(t)
	imagePath := filepath.Join(workDir, "screen.png")
	if err := os.WriteFile(imagePath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	textPath := filepath.Join(workDir, "note.txt")
	if err := os.WriteFile(textPath, []byte("not an image"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name        string
		paths       []string
		limits      Limits
		wantInvalid bool
	}{
		{name: "missing", paths: []string{"missing.png"}},
		{name: "directory", paths: []string{"."}, wantInvalid: true},
		{name: "unsupported", paths: []string{"note.txt"}, wantInvalid: true},
		{name: "oversized", paths: []string{"screen.png"}, limits: Limits{MaxBytes: int64(len(data) - 1), MaxCount: 8}, wantInvalid: true},
		{name: "too many", paths: []string{"screen.png", "screen.png"}, limits: Limits{MaxBytes: int64(len(data) + 1), MaxCount: 1}, wantInvalid: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := InspectFiles(workDir, tc.paths, tc.limits)
			if err == nil {
				t.Fatalf("InspectFiles accepted %s", tc.name)
			}
			if errors.Is(err, ErrInvalidInput) != tc.wantInvalid {
				t.Fatalf("errors.Is(%v, ErrInvalidInput) = %v, want %v", err, errors.Is(err, ErrInvalidInput), tc.wantInvalid)
			}
		})
	}
}

func TestStoreFilesValidatesEveryInputBeforeWriting(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "screen.png"), testPNG(t), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "note.txt"), []byte("not an image"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := StoreFiles(workDir, "session-1", []string{"screen.png", "note.txt"}, Limits{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("StoreFiles error = %v, want ErrInvalidInput", err)
	}
	if _, err := os.Stat(filepath.Join(workDir, ".juex", "artifacts", "media")); !os.IsNotExist(err) {
		t.Fatalf("StoreFiles wrote before validating every input: %v", err)
	}
}

func TestPreparedFilesStoresCapturedBytesAfterSourceRemoval(t *testing.T) {
	workDir := t.TempDir()
	data := testPNG(t)
	imagePath := filepath.Join(workDir, "screen.png")
	if err := os.WriteFile(imagePath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareFiles(workDir, []string{"screen.png"}, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(imagePath); err != nil {
		t.Fatal(err)
	}
	refs, err := prepared.Store(workDir, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0].SHA256 != sha256Hex(data) {
		t.Fatalf("refs = %+v", refs)
	}
	stored, err := os.ReadFile(filepath.Join(workDir, filepath.FromSlash(refs[0].ArtifactPath)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, data) {
		t.Fatal("prepared bytes changed before storage")
	}
}

func TestStoreUploadConcurrentSameImage(t *testing.T) {
	workDir := t.TempDir()
	data := testPNG(t)
	limits := Limits{MaxBytes: int64(len(data) + 1), MaxCount: 8}

	const uploads = 24
	refs := make([]MediaRef, uploads)
	errs := make([]error, uploads)
	var wg sync.WaitGroup
	for i := range uploads {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			refs[i], errs[i] = StoreUpload(workDir, "session-1", "screen.png", bytes.NewReader(data), limits)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("upload %d failed: %v", i, err)
		}
		if refs[i].ArtifactPath != refs[0].ArtifactPath {
			t.Fatalf("upload %d path = %q, want %q", i, refs[i].ArtifactPath, refs[0].ArtifactPath)
		}
	}
	stored, err := os.ReadFile(filepath.Join(workDir, filepath.FromSlash(refs[0].ArtifactPath)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, data) {
		t.Fatalf("stored bytes changed after concurrent uploads")
	}
}

func TestStoreUploadRepairsCorruptContentAddressedImage(t *testing.T) {
	workDir := t.TempDir()
	data := testPNG(t)
	limits := Limits{MaxBytes: int64(len(data) + 1), MaxCount: 8}
	ref, err := StoreUpload(workDir, "session-1", "screen.png", bytes.NewReader(data), limits)
	if err != nil {
		t.Fatal(err)
	}
	artifactPath := filepath.Join(workDir, filepath.FromSlash(ref.ArtifactPath))
	if err := os.WriteFile(artifactPath, []byte("corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := StoreUpload(workDir, "session-1", "screen.png", bytes.NewReader(data), limits); err != nil {
		t.Fatal(err)
	}
	repaired, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(repaired, data) {
		t.Fatalf("content-addressed upload did not repair corrupt bytes")
	}
}

func TestStoreUploadRejectsSymlinkedMediaRoots(t *testing.T) {
	data := testPNG(t)
	limits := Limits{MaxBytes: int64(len(data) + 1), MaxCount: 8}
	cases := []string{
		".juex",
		filepath.Join(".juex", "artifacts"),
		filepath.Join(".juex", "artifacts", "media"),
		filepath.Join(".juex", "artifacts", "media", "session-1"),
	}
	for _, linkRel := range cases {
		t.Run(linkRel, func(t *testing.T) {
			workDir := t.TempDir()
			outside := t.TempDir()
			linkPath := filepath.Join(workDir, linkRel)
			if err := os.MkdirAll(filepath.Dir(linkPath), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, linkPath); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}

			_, err := StoreUpload(workDir, "session-1", "screen.png", bytes.NewReader(data), limits)
			if err == nil {
				t.Fatalf("StoreUpload accepted symlinked media root %s", linkRel)
			}
			entries, readErr := os.ReadDir(outside)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if len(entries) != 0 {
				t.Fatalf("upload wrote through symlinked media root %s into %s", linkRel, outside)
			}
		})
	}
}

func TestValidateSessionMediaRefsAcceptsStoredImage(t *testing.T) {
	workDir := t.TempDir()
	data := testPNG(t)
	limits := Limits{MaxBytes: int64(len(data) + 1), MaxCount: 8}
	ref, err := StoreUpload(workDir, "session-1", "screen.png", bytes.NewReader(data), limits)
	if err != nil {
		t.Fatal(err)
	}

	if err := ValidateSessionMediaRefs(workDir, "session-1", []MediaRef{ref}, limits); err != nil {
		t.Fatalf("ValidateSessionMediaRefs rejected stored image: %v", err)
	}
}

func TestValidateSessionMediaRefsRejectsUnsafeOrTamperedRefs(t *testing.T) {
	workDir := t.TempDir()
	data := testPNG(t)
	limits := Limits{MaxBytes: int64(len(data) + 1), MaxCount: 8}
	ref, err := StoreUpload(workDir, "session-1", "screen.png", bytes.NewReader(data), limits)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		ref  MediaRef
	}{
		{name: "cross session", ref: withPath(ref, strings.Replace(ref.ArtifactPath, "session-1", "session-2", 1))},
		{name: "traversal", ref: withPath(ref, "../secret.png")},
		{name: "absolute", ref: withPath(ref, filepath.Join(workDir, filepath.FromSlash(ref.ArtifactPath)))},
		{name: "media type mismatch", ref: withMediaType(ref, "text/plain")},
		{name: "sha mismatch", ref: withSHA(ref, strings.Repeat("0", 64))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateSessionMediaRefs(workDir, "session-1", []MediaRef{tc.ref}, limits); err == nil {
				t.Fatalf("ValidateSessionMediaRefs accepted unsafe ref: %+v", tc.ref)
			}
		})
	}
}

func TestValidateSessionMediaRefsRejectsTooManyImages(t *testing.T) {
	workDir := t.TempDir()
	data := testPNG(t)
	ref, err := StoreUpload(workDir, "session-1", "screen.png", bytes.NewReader(data), Limits{MaxBytes: int64(len(data) + 1), MaxCount: 1})
	if err != nil {
		t.Fatal(err)
	}

	if err := ValidateSessionMediaRefs(workDir, "session-1", []MediaRef{ref, ref}, Limits{MaxBytes: int64(len(data) + 1), MaxCount: 1}); err == nil {
		t.Fatalf("ValidateSessionMediaRefs accepted too many refs")
	}
}

func testPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 3))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func withPath(ref MediaRef, path string) MediaRef {
	ref.ArtifactPath = path
	return ref
}

func withMediaType(ref MediaRef, mediaType string) MediaRef {
	ref.MediaType = mediaType
	return ref
}

func withSHA(ref MediaRef, sha string) MediaRef {
	ref.SHA256 = sha
	return ref
}
