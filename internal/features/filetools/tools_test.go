package filetools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/foundation/artifact"
	"github.com/juex-ai/juex/internal/foundation/command"
	"github.com/juex-ai/juex/internal/foundation/sandbox"
	toolcore "github.com/juex-ai/juex/internal/foundation/tools"
)

func TestReadToolDefinitionAdvertisesArtifactURI(t *testing.T) {
	definition := readToolDefinition()
	properties, ok := definition.Schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("read properties = %#v", definition.Schema["properties"])
	}
	path, ok := properties["path"].(map[string]any)
	if !ok {
		t.Fatalf("read path schema = %#v", properties["path"])
	}
	pathDescription, _ := path["description"].(string)
	for label, description := range map[string]string{
		"tool": definition.Description,
		"path": pathDescription,
	} {
		if !strings.Contains(description, "artifact://") || !strings.Contains(description, "read-only") {
			t.Fatalf("read %s description does not advertise read-only Artifact URIs: %q", label, description)
		}
	}
}

func TestBuiltins_ReadWriteEdit(t *testing.T) {
	r := toolcore.NewRegistry()
	filetoolsToolstestRegisterTestBuiltins(r, "")

	dir := t.TempDir()
	path := filepath.Join(dir, "foo.txt")

	out, err := r.Call(context.Background(), "write", map[string]any{"path": path, "content": "hello world"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "wrote") {
		t.Fatalf("write output: %s", out)
	}

	out, err = r.Call(context.Background(), "read", map[string]any{"path": path})
	if err != nil {
		t.Fatal(err)
	}
	if out != "hello world" {
		t.Fatalf("read output: %q", out)
	}

	if _, err := r.Call(context.Background(), "edit", map[string]any{"path": path, "old": "world", "new": "Juex"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "hello Juex" {
		t.Fatalf("after edit: %q", string(data))
	}
}

func TestBuiltins_ArtifactReadURIIsReadableAndReadOnly(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "work")
	mediaDir := filepath.Join(root, "media")
	if err := os.Mkdir(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := artifact.NewStore(mediaDir)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := store.Put("threads/123456/spool/call-1.txt", []byte("complete tool output"))
	if err != nil {
		t.Fatal(err)
	}
	uri, err := artifact.FormatReadURI(ref.Path)
	if err != nil {
		t.Fatal(err)
	}

	r := toolcore.NewRegistry()
	registerTestTools(r, testToolOptions{WorkDir: workDir, MediaDir: mediaDir, Shell: command.DefaultShellProfile()})
	out, err := r.Call(context.Background(), "read", map[string]any{"path": uri})
	if err != nil {
		t.Fatal(err)
	}
	if out != "complete tool output" {
		t.Fatalf("read output = %q, want complete Artifact content", out)
	}
	for _, call := range []struct {
		name  string
		input map[string]any
	}{
		{name: "write", input: map[string]any{"path": uri, "content": "overwritten"}},
		{name: "edit", input: map[string]any{"path": uri, "old": "complete", "new": "overwritten"}},
	} {
		if _, err := r.Call(context.Background(), call.name, call.input); err == nil || !strings.Contains(err.Error(), "artifact references are read-only") {
			t.Fatalf("%s error = %v, want read-only Artifact rejection", call.name, err)
		}
	}
	for _, call := range []struct {
		name  string
		input map[string]any
	}{
		{name: "write", input: map[string]any{"path": filepath.Join(mediaDir, filepath.FromSlash(ref.Path)), "content": "overwritten"}},
		{name: "edit", input: map[string]any{"path": filepath.Join(mediaDir, filepath.FromSlash(ref.Path)), "old": "complete", "new": "overwritten"}},
	} {
		if _, err := r.Call(context.Background(), call.name, call.input); err == nil || !strings.Contains(err.Error(), "read-only root") {
			t.Fatalf("%s physical Artifact error = %v, want read-only root rejection", call.name, err)
		}
	}
	data, err := store.Read(ref)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "complete tool output" {
		t.Fatalf("Artifact changed after rejected writes: %q", data)
	}
}

func TestBuiltins_ArtifactReadURIRespectsSandboxBlockedPaths(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "work")
	agentStateDir := filepath.Join(root, "agent")
	mediaDir := filepath.Join(agentStateDir, "media")
	for _, path := range []string{workDir, agentStateDir} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	store, err := artifact.NewStore(mediaDir)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := store.Put("threads/123456/spool/call-1.txt", []byte("blocked tool output"))
	if err != nil {
		t.Fatal(err)
	}
	uri, err := artifact.FormatReadURI(ref.Path)
	if err != nil {
		t.Fatal(err)
	}
	policy := sandbox.DefaultPolicyForOS("linux")
	policy.FileSystem.BlockedPaths = []string{mediaDir}
	r := toolcore.NewRegistry()
	registerTestTools(r, testToolOptions{
		WorkDir:       workDir,
		AgentStateDir: agentStateDir,
		MediaDir:      mediaDir,
		Shell:         command.DefaultShellProfile(),
		Sandbox:       policy,
	})
	if _, err := r.Call(context.Background(), "read", map[string]any{"path": uri}); err == nil || !strings.Contains(err.Error(), "blocked path") {
		t.Fatalf("Artifact URI read = %v, want blocked path rejection", err)
	}
}

func TestBuiltins_ReadImageReturnsMediaResult(t *testing.T) {
	workDir := t.TempDir()
	imagePath := filepath.Join(workDir, "shot.png")
	source := filetoolsToolstestTestPNG(t, 2, 1)
	if err := os.WriteFile(imagePath, source, 0o644); err != nil {
		t.Fatal(err)
	}

	r := toolcore.NewRegistry()
	filetoolsToolstestRegisterTestBuiltins(r, workDir)

	out, info, err := r.CallWithInfo(context.Background(), "read", map[string]any{"path": "shot.png"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[image 2x1", "image/png", "bytes"} {
		if !strings.Contains(out, want) {
			t.Fatalf("read output = %q, want substring %q", out, want)
		}
	}
	media, ok := toolcore.MediaRefFromStructuredResult(info.StructuredResult)
	if !ok || media == nil {
		t.Fatalf("structured result = %#v, want media", info.StructuredResult)
	}
	if media.MediaType != "image/png" || media.Width != 2 || media.Height != 1 {
		t.Fatalf("media = %+v, want png 2x1", media)
	}
	if media.OriginalBytes != len(source) {
		t.Fatalf("original bytes = %d, want %d", media.OriginalBytes, len(source))
	}
	if !strings.HasPrefix(filepath.ToSlash(media.ArtifactPath), "read-media/") {
		t.Fatalf("media path = %q, want stored read image", media.ArtifactPath)
	}
	cached, err := os.ReadFile(filepath.Join(filetoolsToolstestTestMediaDir(workDir), filepath.FromSlash(media.ArtifactPath)))
	if err != nil {
		t.Fatalf("read cached media: %v", err)
	}
	sum := sha256.Sum256(cached)
	if got := hex.EncodeToString(sum[:]); got != media.SHA256 {
		t.Fatalf("sha = %q, want cached file sha %q", media.SHA256, got)
	}
	sentinel := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	artifactPath := filepath.Join(filetoolsToolstestTestMediaDir(workDir), filepath.FromSlash(media.ArtifactPath))
	if err := os.Chtimes(artifactPath, sentinel, sentinel); err != nil {
		t.Fatalf("set cached media time: %v", err)
	}
	if _, _, err := r.CallWithInfo(context.Background(), "read", map[string]any{"path": "shot.png"}); err != nil {
		t.Fatal(err)
	}
	stat, err := os.Stat(artifactPath)
	if err != nil {
		t.Fatalf("stat cached media: %v", err)
	}
	if !stat.ModTime().Equal(sentinel) {
		t.Fatalf("cached media mtime = %s, want unchanged %s", stat.ModTime(), sentinel)
	}

	if err := os.WriteFile(artifactPath, []byte("stale artifact bytes"), 0o644); err != nil {
		t.Fatalf("poison cached media: %v", err)
	}
	if _, _, err := r.CallWithInfo(context.Background(), "read", map[string]any{"path": "shot.png"}); err != nil {
		t.Fatal(err)
	}
	repaired, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatalf("read repaired media: %v", err)
	}
	if !bytes.Equal(repaired, cached) {
		t.Fatalf("cached media was not repaired")
	}
}

func TestBuiltins_ReadImageRejectsSymlinkedMediaArtifactRoots(t *testing.T) {
	source := filetoolsToolstestTestPNG(t, 2, 1)
	cases := []string{
		"media",
		filepath.Join("media", "read-media"),
	}
	for _, linkRel := range cases {
		t.Run(linkRel, func(t *testing.T) {
			workDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(workDir, "shot.png"), source, 0o644); err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			linkPath := filepath.Join(workDir, linkRel)
			if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, linkPath); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}

			r := toolcore.NewRegistry()
			filetoolsToolstestRegisterTestBuiltins(r, workDir)
			_, _, err := r.CallWithInfo(context.Background(), "read", map[string]any{"path": "shot.png"})
			if err == nil {
				t.Fatalf("read accepted symlinked media root %s", linkRel)
			}
			entries, readErr := os.ReadDir(outside)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if len(entries) != 0 {
				t.Fatalf("read wrote through symlinked media root %s into %s", linkRel, outside)
			}
		})
	}
}

func TestBuiltins_ReadImageRequiresMagicBytes(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "fake.png"), []byte("not really an image"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := toolcore.NewRegistry()
	filetoolsToolstestRegisterTestBuiltins(r, workDir)

	out, info, err := r.CallWithInfo(context.Background(), "read", map[string]any{"path": "fake.png"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "not really an image" {
		t.Fatalf("read output = %q, want text fallback", out)
	}
	if media, ok := toolcore.MediaRefFromStructuredResult(info.StructuredResult); ok || media != nil {
		t.Fatalf("structured result = %#v, want no media", info.StructuredResult)
	}
}

func TestBuiltins_ReadImageOmitsUnreadableImagePayload(t *testing.T) {
	workDir := t.TempDir()
	truncatedPNG := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	if err := os.WriteFile(filepath.Join(workDir, "truncated.png"), truncatedPNG, 0o644); err != nil {
		t.Fatal(err)
	}

	r := toolcore.NewRegistry()
	filetoolsToolstestRegisterTestBuiltins(r, workDir)

	out, info, err := r.CallWithInfo(context.Background(), "read", map[string]any{"path": "truncated.png"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"image omitted", "dimensions unknown for image/png"} {
		if !strings.Contains(out, want) {
			t.Fatalf("read output = %q, want substring %q", out, want)
		}
	}
	if media, ok := toolcore.MediaRefFromStructuredResult(info.StructuredResult); ok || media != nil {
		t.Fatalf("structured result = %#v, want no media", info.StructuredResult)
	}
}

func TestBuiltins_ReadImageRejectsOffsetLimit(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "shot.png"), filetoolsToolstestTestPNG(t, 2, 1), 0o644); err != nil {
		t.Fatal(err)
	}

	r := toolcore.NewRegistry()
	filetoolsToolstestRegisterTestBuiltins(r, workDir)

	_, _, err := r.CallWithInfo(context.Background(), "read", map[string]any{"path": "shot.png", "offset": 1})
	if err == nil || !strings.Contains(err.Error(), "offset and limit are not supported for image files") {
		t.Fatalf("err = %v, want image offset error", err)
	}
}

func TestBuiltins_ReadImageDownsamplesLongSide(t *testing.T) {
	workDir := t.TempDir()
	sourcePath := filepath.Join(workDir, "wide.png")
	source := filetoolsToolstestTestPNG(t, 2101, 3)
	if err := os.WriteFile(sourcePath, source, 0o644); err != nil {
		t.Fatal(err)
	}

	r := toolcore.NewRegistry()
	filetoolsToolstestRegisterTestBuiltins(r, workDir)

	_, info, err := r.CallWithInfo(context.Background(), "read", map[string]any{"path": "wide.png"})
	if err != nil {
		t.Fatal(err)
	}
	media, ok := toolcore.MediaRefFromStructuredResult(info.StructuredResult)
	if !ok || media == nil {
		t.Fatalf("structured result = %#v, want media", info.StructuredResult)
	}
	if media.Width > readImageMaxSide || media.Height > readImageMaxSide {
		t.Fatalf("media dimensions = %dx%d, want max side <= %d", media.Width, media.Height, readImageMaxSide)
	}
	cached, err := os.Open(filepath.Join(filetoolsToolstestTestMediaDir(workDir), filepath.FromSlash(media.ArtifactPath)))
	if err != nil {
		t.Fatalf("open cached media: %v", err)
	}
	defer cached.Close()
	cfg, err := png.DecodeConfig(cached)
	if err != nil {
		t.Fatalf("decode cached png: %v", err)
	}
	if cfg.Width > readImageMaxSide || cfg.Height > readImageMaxSide {
		t.Fatalf("cached dimensions = %dx%d, want max side <= %d", cfg.Width, cfg.Height, readImageMaxSide)
	}
	original, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	originalCfg, err := png.DecodeConfig(original)
	if err != nil {
		t.Fatalf("decode original png: %v", err)
	}
	if originalCfg.Width != 2101 {
		t.Fatalf("original width = %d, want source untouched", originalCfg.Width)
	}
}

func TestBuiltins_ReadImageDownsampleFailureStillReturnsMedia(t *testing.T) {
	workDir := t.TempDir()
	source := filetoolsToolstestCorruptLargePNG(2101, 3)
	if err := os.WriteFile(filepath.Join(workDir, "corrupt.png"), source, 0o644); err != nil {
		t.Fatal(err)
	}

	r := toolcore.NewRegistry()
	filetoolsToolstestRegisterTestBuiltins(r, workDir)

	out, info, err := r.CallWithInfo(context.Background(), "read", map[string]any{"path": "corrupt.png"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[image 2101x3") || strings.Contains(out, "\x89PNG") {
		t.Fatalf("read output = %q, want media summary without raw png bytes", out)
	}
	media, ok := toolcore.MediaRefFromStructuredResult(info.StructuredResult)
	if !ok || media == nil {
		t.Fatalf("structured result = %#v, want media", info.StructuredResult)
	}
	cached, err := os.ReadFile(filepath.Join(filetoolsToolstestTestMediaDir(workDir), filepath.FromSlash(media.ArtifactPath)))
	if err != nil {
		t.Fatalf("read cached media: %v", err)
	}
	if string(cached) != string(source) {
		t.Fatalf("cached media changed on downsample failure")
	}
}

func TestBuiltins_ReadImageOmitsUnsafePixelCount(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "huge.png"), filetoolsToolstestCorruptLargePNG(20000, 20000), 0o644); err != nil {
		t.Fatal(err)
	}

	r := toolcore.NewRegistry()
	filetoolsToolstestRegisterTestBuiltins(r, workDir)

	out, info, err := r.CallWithInfo(context.Background(), "read", map[string]any{"path": "huge.png"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "image omitted") || !strings.Contains(out, "pixel count") {
		t.Fatalf("read output = %q, want pixel-limit omission", out)
	}
	if media, ok := toolcore.MediaRefFromStructuredResult(info.StructuredResult); ok || media != nil {
		t.Fatalf("structured result = %#v, want no media for unsafe pixel count", info.StructuredResult)
	}
	if _, err := os.Stat(filetoolsToolstestTestMediaDir(workDir)); !os.IsNotExist(err) {
		t.Fatalf("unsafe image should not create media dir, stat err=%v", err)
	}
}

func TestBuiltins_ReadImageOmitsOversizeNonDownsampledFormat(t *testing.T) {
	workDir := t.TempDir()
	source := append([]byte("RIFF\x00\x00\x00\x00WEBP"), bytes.Repeat([]byte("x"), readImageMaxBytes+1)...)
	if err := os.WriteFile(filepath.Join(workDir, "large.webp"), source, 0o644); err != nil {
		t.Fatal(err)
	}

	r := toolcore.NewRegistry()
	filetoolsToolstestRegisterTestBuiltins(r, workDir)

	out, info, err := r.CallWithInfo(context.Background(), "read", map[string]any{"path": "large.webp"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "image omitted") || !strings.Contains(out, "non-downsampled image/webp") {
		t.Fatalf("read output = %q, want oversize webp omission", out)
	}
	if media, ok := toolcore.MediaRefFromStructuredResult(info.StructuredResult); ok || media != nil {
		t.Fatalf("structured result = %#v, want no media for oversized webp", info.StructuredResult)
	}
}

func TestBuiltins_ReadImageReturnsWebPDimensions(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "small.webp"), filetoolsToolstestTestWebPVP8X(640, 480), 0o644); err != nil {
		t.Fatal(err)
	}

	r := toolcore.NewRegistry()
	filetoolsToolstestRegisterTestBuiltins(r, workDir)

	_, info, err := r.CallWithInfo(context.Background(), "read", map[string]any{"path": "small.webp"})
	if err != nil {
		t.Fatal(err)
	}
	media, ok := toolcore.MediaRefFromStructuredResult(info.StructuredResult)
	if !ok || media == nil {
		t.Fatalf("structured result = %#v, want webp media", info.StructuredResult)
	}
	if media.Width != 640 || media.Height != 480 {
		t.Fatalf("webp dimensions = %dx%d, want 640x480", media.Width, media.Height)
	}
}

func TestBuiltins_ReadImageOmitsLargeWebPDimensions(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "wide.webp"), filetoolsToolstestTestWebPVP8X(readImageMaxSide+1, 10), 0o644); err != nil {
		t.Fatal(err)
	}

	r := toolcore.NewRegistry()
	filetoolsToolstestRegisterTestBuiltins(r, workDir)

	out, info, err := r.CallWithInfo(context.Background(), "read", map[string]any{"path": "wide.webp"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "image omitted") || !strings.Contains(out, "dimensions") {
		t.Fatalf("read output = %q, want webp dimension omission", out)
	}
	if media, ok := toolcore.MediaRefFromStructuredResult(info.StructuredResult); ok || media != nil {
		t.Fatalf("structured result = %#v, want no media for oversized webp dimensions", info.StructuredResult)
	}
}

func TestBuiltins_ReadImageOmitsReencodedOversizeImage(t *testing.T) {
	workDir := t.TempDir()
	source := filetoolsToolstestHighEntropyPNG(t, 1500, 1500)
	if len(source) <= readImageMaxBytes {
		t.Fatalf("test image size = %d, want > %d", len(source), readImageMaxBytes)
	}
	if err := os.WriteFile(filepath.Join(workDir, "noise.png"), source, 0o644); err != nil {
		t.Fatal(err)
	}

	r := toolcore.NewRegistry()
	filetoolsToolstestRegisterTestBuiltins(r, workDir)

	out, info, err := r.CallWithInfo(context.Background(), "read", map[string]any{"path": "noise.png"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "image omitted") || !strings.Contains(out, "after downsampling") {
		t.Fatalf("read output = %q, want re-encoded byte-limit omission", out)
	}
	if media, ok := toolcore.MediaRefFromStructuredResult(info.StructuredResult); ok || media != nil {
		t.Fatalf("structured result = %#v, want no media for oversized re-encode", info.StructuredResult)
	}
	if _, err := os.Stat(filetoolsToolstestTestMediaDir(workDir)); !os.IsNotExist(err) {
		t.Fatalf("oversized re-encode should not create media dir, stat err=%v", err)
	}
}

func TestBuiltins_EditMissingRequiredArgumentsReportsReceivedKeys(t *testing.T) {
	r := toolcore.NewRegistry()
	filetoolsToolstestRegisterTestBuiltins(r, "")

	dir := t.TempDir()
	path := filepath.Join(dir, "foo.txt")
	if err := os.WriteFile(path, []byte("source_ai: Claude"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := r.Call(context.Background(), "edit", map[string]any{
		"path":       path,
		"old_string": "source_ai: Claude",
		"new_string": "source_ai: Juex",
	})
	if err == nil {
		t.Fatal("expected missing required arguments error")
	}
	for _, want := range []string{
		"missing required argument(s): old, new",
		"expected keys: path, old, new",
		"received keys: new_string, old_string, path",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want substring %q", err.Error(), want)
		}
	}
}

func TestBuiltins_FileToolsResolveRelativePathsFromWorkDir(t *testing.T) {
	processDir := t.TempDir()
	t.Chdir(processDir)
	workDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workDir, "music"), 0o755); err != nil {
		t.Fatal(err)
	}
	readmePath := filepath.Join(workDir, "music", "README.md")
	if err := os.WriteFile(readmePath, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := toolcore.NewRegistry()
	filetoolsToolstestRegisterTestBuiltins(r, workDir)

	out, err := r.Call(context.Background(), "read", map[string]any{"path": "music/README.md"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "hello world" {
		t.Fatalf("read output = %q, want workdir file contents", out)
	}

	if _, err := r.Call(context.Background(), "edit", map[string]any{
		"path": "music/README.md",
		"old":  "world",
		"new":  "Juex",
	}); err != nil {
		t.Fatal(err)
	}
	edited, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(edited) != "hello Juex" {
		t.Fatalf("edited file = %q, want workdir file updated", edited)
	}

	if _, err := r.Call(context.Background(), "write", map[string]any{
		"path":    "music/transposed.json",
		"content": `{"ok":true}`,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workDir, "music", "transposed.json")); err != nil {
		t.Fatalf("write did not create file under workdir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(processDir, "music", "transposed.json")); !os.IsNotExist(err) {
		t.Fatalf("write used process cwd; stat err = %v", err)
	}
}

func TestBuiltins_FileToolsRespectSandboxBlockedPaths(t *testing.T) {
	workDir := t.TempDir()
	blockedDir := filepath.Join(workDir, "private")
	if err := os.MkdirAll(blockedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	blockedFile := filepath.Join(blockedDir, "secret.txt")
	if err := os.WriteFile(blockedFile, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	publicFile := filepath.Join(workDir, "public.txt")
	if err := os.WriteFile(publicFile, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := toolcore.NewRegistry()
	filetoolsToolstestRegisterSandboxedTestBuiltins(r, workDir, []string{"private"})

	if _, err := r.Call(context.Background(), "read", map[string]any{"path": "private/secret.txt"}); err == nil || !strings.Contains(err.Error(), "blocked path") {
		t.Fatalf("read blocked err = %v, want blocked path", err)
	}
	if _, err := r.Call(context.Background(), "write", map[string]any{"path": "private/new.txt", "content": "nope"}); err == nil || !strings.Contains(err.Error(), "blocked path") {
		t.Fatalf("write blocked err = %v, want blocked path", err)
	}
	if _, err := os.Stat(filepath.Join(blockedDir, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("blocked write created file, stat err=%v", err)
	}
	if _, err := r.Call(context.Background(), "edit", map[string]any{"path": "private/secret.txt", "old": "secret", "new": "open"}); err == nil || !strings.Contains(err.Error(), "blocked path") {
		t.Fatalf("edit blocked err = %v, want blocked path", err)
	}
	if data := filetoolsToolstestMustReadFile(t, blockedFile); string(data) != "secret" {
		t.Fatalf("blocked file changed: %q", data)
	}
	out, err := r.Call(context.Background(), "read", map[string]any{"path": "public.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "hello" {
		t.Fatalf("public read = %q", out)
	}
}

func TestBuiltins_FileWritesUseSandboxWritableRoots(t *testing.T) {
	workDir := t.TempDir()
	agentStateDir := t.TempDir()
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "outside.txt")
	if err := os.WriteFile(outsideFile, []byte("readable"), 0o644); err != nil {
		t.Fatal(err)
	}
	policy := sandbox.DefaultPolicyForOS("linux")
	r := toolcore.NewRegistry()
	registerTestTools(r, testToolOptions{
		WorkDir:       workDir,
		AgentStateDir: agentStateDir,
		Shell:         filetoolsToolstestFakeShellProfile(),
		Sandbox:       policy,
		SandboxRunner: &filetoolsToolstestFakeSandboxRunner{},
	})

	for _, path := range []string{
		filepath.Join(workDir, "workspace.txt"),
		filepath.Join(agentStateDir, "memory", "agent.txt"),
	} {
		if _, err := r.Call(context.Background(), "write", map[string]any{"path": path, "content": "ok"}); err != nil {
			t.Fatalf("write(%q) = %v", path, err)
		}
	}
	if out, err := r.Call(context.Background(), "read", map[string]any{"path": outsideFile}); err != nil || out != "readable" {
		t.Fatalf("outside read = %q, %v", out, err)
	}
	if _, err := r.Call(context.Background(), "write", map[string]any{"path": outsideFile, "content": "denied"}); err == nil || !strings.Contains(err.Error(), "outside writable roots") {
		t.Fatalf("outside write = %v, want writable roots rejection", err)
	}
	if _, err := r.Call(context.Background(), "edit", map[string]any{"path": outsideFile, "old": "readable", "new": "denied"}); err == nil || !strings.Contains(err.Error(), "outside writable roots") {
		t.Fatalf("outside edit = %v, want writable roots rejection", err)
	}
	if got := string(filetoolsToolstestMustReadFile(t, outsideFile)); got != "readable" {
		t.Fatalf("outside file = %q, want unchanged", got)
	}
}

func TestBuiltins_EditAmbiguous(t *testing.T) {
	r := toolcore.NewRegistry()
	filetoolsToolstestRegisterTestBuiltins(r, "")

	dir := t.TempDir()
	path := filepath.Join(dir, "foo.txt")
	if err := os.WriteFile(path, []byte("a a a"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := r.Call(context.Background(), "edit", map[string]any{"path": path, "old": "a", "new": "b"}); err == nil {
		t.Fatal("expected ambiguity error")
	}
}

func TestBuiltins_EditReplaceAll(t *testing.T) {
	r := toolcore.NewRegistry()
	filetoolsToolstestRegisterTestBuiltins(r, "")

	dir := t.TempDir()
	path := filepath.Join(dir, "foo.txt")
	if err := os.WriteFile(path, []byte("alpha beta alpha alpha"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := r.Call(context.Background(), "edit", map[string]any{
		"path":        path,
		"old":         "alpha",
		"new":         "omega",
		"replace_all": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "3 replacements") {
		t.Fatalf("edit output: %s", out)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "omega beta omega omega" {
		t.Fatalf("after edit: %q", string(data))
	}
}

func TestBuiltins_EditExpectedReplacementsMismatchPreservesFile(t *testing.T) {
	r := toolcore.NewRegistry()
	filetoolsToolstestRegisterTestBuiltins(r, "")

	dir := t.TempDir()
	path := filepath.Join(dir, "foo.txt")
	original := "one two one"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := r.Call(context.Background(), "edit", map[string]any{
		"path":                  path,
		"old":                   "one",
		"new":                   "three",
		"replace_all":           true,
		"expected_replacements": 3,
	})
	if err == nil {
		t.Fatal("expected replacement count error")
	}
	if !strings.Contains(err.Error(), "expected 3 replacements, found 2") {
		t.Fatalf("unexpected error: %v", err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != original {
		t.Fatalf("file changed after failed edit: %q", string(data))
	}
}

func TestBuiltins_EditExpectedReplacementsNullIsIgnored(t *testing.T) {
	r := toolcore.NewRegistry()
	filetoolsToolstestRegisterTestBuiltins(r, "")

	dir := t.TempDir()
	path := filepath.Join(dir, "foo.txt")
	if err := os.WriteFile(path, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := r.Call(context.Background(), "edit", map[string]any{
		"path":                  path,
		"old":                   "world",
		"new":                   "Juex",
		"expected_replacements": nil,
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello Juex" {
		t.Fatalf("after edit: %q", string(data))
	}
}

func TestBuiltins_ReadOffsetLimit(t *testing.T) {
	r := toolcore.NewRegistry()
	filetoolsToolstestRegisterTestBuiltins(r, "")
	dir := t.TempDir()
	path := filepath.Join(dir, "lines.txt")
	if err := os.WriteFile(path, []byte("a\nb\nc\nd\ne"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := r.Call(context.Background(), "read", map[string]any{"path": path, "offset": 2, "limit": 2})
	if err != nil {
		t.Fatal(err)
	}
	if out != "b\nc" {
		t.Fatalf("got %q", out)
	}
}

func TestBuiltins_EditMissingFile(t *testing.T) {
	r := toolcore.NewRegistry()
	filetoolsToolstestRegisterTestBuiltins(r, "")
	_, err := r.Call(context.Background(), "edit", map[string]any{"path": "/tmp/__nope__.txt", "old": "x", "new": "y"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func filetoolsToolstestCorruptLargePNG(width, height int) []byte {
	var b bytes.Buffer
	b.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	filetoolsToolstestWritePNGChunk(&b, []byte("IHDR"), []byte{
		byte(width >> 24), byte(width >> 16), byte(width >> 8), byte(width),
		byte(height >> 24), byte(height >> 16), byte(height >> 8), byte(height),
		8, 2, 0, 0, 0,
	})
	filetoolsToolstestWritePNGChunk(&b, []byte("IDAT"), []byte("not-zlib-data"))
	filetoolsToolstestWritePNGChunk(&b, []byte("IEND"), nil)
	return b.Bytes()
}

type filetoolsToolstestFakeSandboxRunner struct {
	calls    int
	specs    []sandbox.ExecSpec
	requests []sandbox.Request
	err      error
	prepare  func(context.Context, sandbox.Request) (sandbox.ExecSpec, error)
}

func filetoolsToolstestFakeShellProfile() command.ShellProfile {
	return command.ShellProfile{
		Profile:   "fake",
		Family:    "posix",
		Binary:    os.Args[0],
		Args:      []string{"-test.run=TestShellHelperProcess", "--"},
		PathStyle: "posix",
	}
}

func filetoolsToolstestHighEntropyPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	state := uint32(1)
	for i := 0; i < len(img.Pix); i += 4 {
		state = state*1664525 + 1013904223
		img.Pix[i] = byte(state >> 24)
		state = state*1664525 + 1013904223
		img.Pix[i+1] = byte(state >> 24)
		state = state*1664525 + 1013904223
		img.Pix[i+2] = byte(state >> 24)
		img.Pix[i+3] = 0xff
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func filetoolsToolstestMustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func filetoolsToolstestRegisterSandboxedTestBuiltins(r *toolcore.Registry, workDir string, blockedPaths []string) {
	policy := sandbox.DefaultPolicy()
	policy.Enabled = true
	policy.FileSystem.BlockedPaths = blockedPaths
	registerTestTools(r, testToolOptions{
		WorkDir:       workDir,
		Shell:         command.DefaultShellProfile(),
		Sandbox:       policy,
		SandboxRunner: &filetoolsToolstestFakeSandboxRunner{},
	})
}

func filetoolsToolstestRegisterTestBuiltins(r *toolcore.Registry, workDir string) {
	registerTestTools(r, testToolOptions{WorkDir: workDir, MediaDir: filetoolsToolstestTestMediaDir(workDir), Shell: command.DefaultShellProfile()})
}

func filetoolsToolstestTestMediaDir(workDir string) string { return filepath.Join(workDir, "media") }

func filetoolsToolstestTestPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 255), G: uint8(y % 255), B: 0x88, A: 0xff})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func filetoolsToolstestTestWebPVP8X(width, height int) []byte {
	payload := make([]byte, 10)
	filetoolsToolstestWriteLittleEndian24(payload[4:7], uint32(width-1))
	filetoolsToolstestWriteLittleEndian24(payload[7:10], uint32(height-1))
	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(4+8+len(payload)))
	b.WriteString("WEBP")
	b.WriteString("VP8X")
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(payload)))
	b.Write(payload)
	return b.Bytes()
}

func filetoolsToolstestWriteLittleEndian24(dst []byte, value uint32) {
	dst[0] = byte(value)
	dst[1] = byte(value >> 8)
	dst[2] = byte(value >> 16)
}

func filetoolsToolstestWritePNGChunk(b *bytes.Buffer, kind []byte, data []byte) {
	_ = binary.Write(b, binary.BigEndian, uint32(len(data)))
	b.Write(kind)
	b.Write(data)
	sum := crc32.ChecksumIEEE(append(append([]byte{}, kind...), data...))
	_ = binary.Write(b, binary.BigEndian, sum)
}

func (r *filetoolsToolstestFakeSandboxRunner) Prepare(ctx context.Context, req sandbox.Request) (sandbox.ExecSpec, error) {
	r.calls++
	r.specs = append(r.specs, req.Spec)
	r.requests = append(r.requests, req)
	if r.prepare != nil {
		return r.prepare(ctx, req)
	}
	if r.err != nil {
		return sandbox.ExecSpec{}, r.err
	}
	return req.Spec, nil
}
