package chunkedwrite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/foundation/command"
	"github.com/juex-ai/juex/internal/foundation/sandbox"
	toolcore "github.com/juex-ai/juex/internal/foundation/tools"
)

func TestBuiltins_ChunkedWriteBeginReportsRecommendedChunkSize(t *testing.T) {
	r := toolcore.NewRegistry()
	chunkedwriteToolstestRegisterTestBuiltins(r, t.TempDir())

	out, info, err := r.CallWithInfo(context.Background(), "write_begin", map[string]any{"path": "long.md"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"max_chunk_bytes=4000", "max_chunk_chars=2000", "recommended_chunk_bytes=4000", "recommended_chunk_chars=2000"} {
		if !strings.Contains(out, want) {
			t.Fatalf("write_begin output = %q, missing %q", out, want)
		}
	}
	event, ok := info.StructuredResult.(Event)
	if !ok {
		t.Fatalf("structured result = %#v, want chunkedwrite.Event", info.StructuredResult)
	}
	if event.Kind != EventBegin || event.WriteID == "" || event.Path != "long.md" || event.Mode != ModeOverwrite {
		t.Fatalf("begin event = %+v", event)
	}
}

func TestBuiltins_ChunkedWriteRejectsProviderUnsafeChunkSize(t *testing.T) {
	r := toolcore.NewRegistry()
	chunkedwriteToolstestRegisterTestBuiltins(r, t.TempDir())

	beginOut, err := r.Call(context.Background(), "write_begin", map[string]any{"path": "long.md"})
	if err != nil {
		t.Fatal(err)
	}
	writeID := chunkedwriteToolstestChunkWriteIDFromResult(t, beginOut)
	_, err = r.Call(context.Background(), "write_chunk", map[string]any{
		"write_id": writeID,
		"index":    0,
		"content":  strings.Repeat("x", chunkWriteMaxChunkChars+1),
	})
	if err == nil {
		t.Fatal("expected provider-unsafe chunk size to fail")
	}
	for _, want := range []string{"content exceeds max chunk limits", "2000 chars", "4000 bytes", "split into smaller chunks"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, missing %q", err.Error(), want)
		}
	}
}

func TestBuiltins_ChunkedWriteRejectsProjectedMetadataAsInput(t *testing.T) {
	r := toolcore.NewRegistry()
	chunkedwriteToolstestRegisterTestBuiltins(r, t.TempDir())

	_, err := r.Call(context.Background(), "write_chunk", map[string]any{
		"write_id":        "w_demo",
		"index":           0,
		"content_omitted": true,
		"content_bytes":   123,
		"content_chars":   100,
		"content_sha256":  "abc123",
	})
	if err == nil {
		t.Fatal("expected missing content error")
	}
	msg := err.Error()
	for _, want := range []string{"summary metadata", "actual content string", "2000 chars", "4000 bytes"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error = %q, missing %q", msg, want)
		}
	}
}

func TestBuiltins_ChunkedWriteMalformedRawArgumentsMentionsSafeChunkSize(t *testing.T) {
	r := toolcore.NewRegistry()
	chunkedwriteToolstestRegisterTestBuiltins(r, t.TempDir())

	_, err := r.Call(context.Background(), "write_chunk", map[string]any{
		"_raw_arguments": `{"write_id":"w_demo","index":0,"content":"unterminated`,
	})
	if err == nil {
		t.Fatal("expected malformed raw arguments error")
	}
	msg := err.Error()
	for _, want := range []string{"smaller write_chunk content", "2000 chars", "4000 bytes"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error = %q, missing %q", msg, want)
		}
	}
}

func TestBuiltins_ChunkedWriteCommitOverwrite(t *testing.T) {
	workDir := t.TempDir()
	target := filepath.Join(workDir, "long.md")
	if err := os.WriteFile(target, []byte("old content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := toolcore.NewRegistry()
	chunkedwriteToolstestRegisterTestBuiltins(r, workDir)

	beginOut, err := r.Call(context.Background(), "write_begin", map[string]any{"path": "long.md"})
	if err != nil {
		t.Fatal(err)
	}
	writeID := chunkedwriteToolstestChunkWriteIDFromResult(t, beginOut)
	for index, content := range []string{"alpha\n", "beta\n", "gamma\n"} {
		out, err := r.Call(context.Background(), "write_chunk", map[string]any{"write_id": writeID, "index": index, "content": content})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, content) {
			t.Fatalf("chunk result echoed content: %q", out)
		}
	}
	full := "alpha\nbeta\ngamma\n"
	out, err := r.Call(context.Background(), "write_commit", map[string]any{"write_id": writeID, "expected_chunks": 3, "sha256": chunkedwriteToolstestSha256HexForTest(full)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "write_commit") || !strings.Contains(out, "chunks=3") {
		t.Fatalf("commit output = %q", out)
	}
	if data := chunkedwriteToolstestMustReadFile(t, target); string(data) != full {
		t.Fatalf("target = %q", data)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
	if _, err := r.Call(context.Background(), "write_commit", map[string]any{"write_id": writeID}); err == nil || !strings.Contains(err.Error(), "unknown write_id") {
		t.Fatalf("commit after success err = %v, want unknown write_id", err)
	}
}

func TestBuiltins_ChunkedWriteAcceptsAbsoluteWorkspacePath(t *testing.T) {
	workDir := t.TempDir()
	target := filepath.Join(workDir, "nested", "absolute.md")
	r := toolcore.NewRegistry()
	chunkedwriteToolstestRegisterTestBuiltins(r, workDir)

	beginOut, beginInfo, err := r.CallWithInfo(context.Background(), "write_begin", map[string]any{"path": target, "mode": "create"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(beginOut, workDir) || !strings.Contains(beginOut, "path=nested/absolute.md") {
		t.Fatalf("begin output = %q, want normalized relative path", beginOut)
	}
	beginEvent, ok := beginInfo.StructuredResult.(Event)
	if !ok || beginEvent.Path != "nested/absolute.md" {
		t.Fatalf("begin event = %#v, want relative path", beginInfo.StructuredResult)
	}
	writeID := chunkedwriteToolstestChunkWriteIDFromResult(t, beginOut)
	if _, err := r.Call(context.Background(), "write_chunk", map[string]any{"write_id": writeID, "index": 0, "content": "absolute\n"}); err != nil {
		t.Fatal(err)
	}
	commitOut, commitInfo, err := r.CallWithInfo(context.Background(), "write_commit", map[string]any{"write_id": writeID, "expected_chunks": 1})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(commitOut, workDir) || !strings.Contains(commitOut, "path=nested/absolute.md") {
		t.Fatalf("commit output = %q, want normalized relative path", commitOut)
	}
	commitEvent, ok := commitInfo.StructuredResult.(Event)
	if !ok || commitEvent.Path != "nested/absolute.md" {
		t.Fatalf("commit event = %#v, want relative path", commitInfo.StructuredResult)
	}
	if got := string(chunkedwriteToolstestMustReadFile(t, target)); got != "absolute\n" {
		t.Fatalf("target = %q", got)
	}
}

func TestBuiltins_ChunkedWriteCreateMode(t *testing.T) {
	workDir := t.TempDir()
	r := toolcore.NewRegistry()
	chunkedwriteToolstestRegisterTestBuiltins(r, workDir)

	beginOut, err := r.Call(context.Background(), "write_begin", map[string]any{"path": "new/report.md", "mode": "create"})
	if err != nil {
		t.Fatal(err)
	}
	writeID := chunkedwriteToolstestChunkWriteIDFromResult(t, beginOut)
	if _, err := r.Call(context.Background(), "write_chunk", map[string]any{"write_id": writeID, "index": 0, "content": "# Report\n"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Call(context.Background(), "write_commit", map[string]any{"write_id": writeID, "expected_chunks": 1}); err != nil {
		t.Fatal(err)
	}
	if data := chunkedwriteToolstestMustReadFile(t, filepath.Join(workDir, "new", "report.md")); string(data) != "# Report\n" {
		t.Fatalf("created file = %q", data)
	}

	if _, err := r.Call(context.Background(), "write_begin", map[string]any{"path": "new/report.md", "mode": "create"}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("create existing err = %v, want already exists", err)
	}
}

func TestBuiltins_ChunkedWriteDuplicateChunkRules(t *testing.T) {
	workDir := t.TempDir()
	r := toolcore.NewRegistry()
	chunkedwriteToolstestRegisterTestBuiltins(r, workDir)
	beginOut, err := r.Call(context.Background(), "write_begin", map[string]any{"path": "dup.txt"})
	if err != nil {
		t.Fatal(err)
	}
	writeID := chunkedwriteToolstestChunkWriteIDFromResult(t, beginOut)
	input := map[string]any{"write_id": writeID, "index": 0, "content": "same", "sha256": chunkedwriteToolstestSha256HexForTest("same")}
	if _, err := r.Call(context.Background(), "write_chunk", input); err != nil {
		t.Fatal(err)
	}
	out, err := r.Call(context.Background(), "write_chunk", input)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "duplicate=true") {
		t.Fatalf("duplicate output = %q, want idempotent duplicate", out)
	}
	if _, err := r.Call(context.Background(), "write_chunk", map[string]any{"write_id": writeID, "index": 0, "content": "different"}); err == nil || !strings.Contains(err.Error(), "conflicting duplicate chunk") {
		t.Fatalf("conflict err = %v, want conflicting duplicate chunk", err)
	}
}

func TestBuiltins_ChunkedWriteValidationFailuresPreserveTarget(t *testing.T) {
	workDir := t.TempDir()
	target := filepath.Join(workDir, "target.txt")
	original := "original\n"
	if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	r := toolcore.NewRegistry()
	chunkedwriteToolstestRegisterTestBuiltins(r, workDir)
	beginOut, err := r.Call(context.Background(), "write_begin", map[string]any{"path": "target.txt"})
	if err != nil {
		t.Fatal(err)
	}
	writeID := chunkedwriteToolstestChunkWriteIDFromResult(t, beginOut)
	if _, err := r.Call(context.Background(), "write_chunk", map[string]any{"write_id": writeID, "index": 1, "content": "later"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Call(context.Background(), "write_commit", map[string]any{"write_id": writeID}); err == nil || !strings.Contains(err.Error(), "missing chunk 0") {
		t.Fatalf("missing chunk err = %v, want missing chunk 0", err)
	}
	if data := chunkedwriteToolstestMustReadFile(t, target); string(data) != original {
		t.Fatalf("target changed after missing chunk: %q", data)
	}
	if _, err := r.Call(context.Background(), "write_chunk", map[string]any{"write_id": writeID, "index": 0, "content": "first", "sha256": "bad"}); err == nil || !strings.Contains(err.Error(), "chunk checksum mismatch") {
		t.Fatalf("chunk checksum err = %v, want mismatch", err)
	}
	if _, err := r.Call(context.Background(), "write_chunk", map[string]any{"write_id": writeID, "index": 0, "content": "first"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Call(context.Background(), "write_commit", map[string]any{"write_id": writeID, "expected_chunks": 2, "sha256": "bad"}); err == nil || !strings.Contains(err.Error(), "full checksum mismatch") {
		t.Fatalf("full checksum err = %v, want mismatch", err)
	}
	if data := chunkedwriteToolstestMustReadFile(t, target); string(data) != original {
		t.Fatalf("target changed after checksum mismatch: %q", data)
	}
}

func TestBuiltins_ChunkedWriteAbort(t *testing.T) {
	workDir := t.TempDir()
	r := toolcore.NewRegistry()
	chunkedwriteToolstestRegisterTestBuiltins(r, workDir)
	beginOut, err := r.Call(context.Background(), "write_begin", map[string]any{"path": "aborted.txt"})
	if err != nil {
		t.Fatal(err)
	}
	writeID := chunkedwriteToolstestChunkWriteIDFromResult(t, beginOut)
	if _, err := r.Call(context.Background(), "write_chunk", map[string]any{"write_id": writeID, "index": 0, "content": "discard"}); err != nil {
		t.Fatal(err)
	}
	if out, err := r.Call(context.Background(), "write_abort", map[string]any{"write_id": writeID}); err != nil || !strings.Contains(out, "aborted") {
		t.Fatalf("abort out=%q err=%v", out, err)
	}
	if _, err := os.Stat(filepath.Join(workDir, "aborted.txt")); !os.IsNotExist(err) {
		t.Fatalf("aborted file should not exist, stat err=%v", err)
	}
	if _, err := r.Call(context.Background(), "write_commit", map[string]any{"write_id": writeID}); err == nil || !strings.Contains(err.Error(), "unknown write_id") {
		t.Fatalf("commit aborted err = %v, want unknown write_id", err)
	}
}

func TestBuiltins_ChunkedWriteStaleSession(t *testing.T) {
	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	manager := newChunkWriteManager(t.TempDir())
	manager.now = func() time.Time { return now }

	session, err := manager.begin("stale.txt", "")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(chunkWriteSessionTTL + time.Second)
	if _, _, _, err := manager.chunk(session.id, 0, "late", ""); err == nil || !strings.Contains(err.Error(), "stale write_id") {
		t.Fatalf("stale chunk err = %v, want stale write_id", err)
	}
	if _, _, _, err := manager.chunk(session.id, 0, "late", ""); err == nil || !strings.Contains(err.Error(), "unknown write_id") {
		t.Fatalf("second stale chunk err = %v, want unknown write_id", err)
	}
}

func TestChunkedWriteManager_RestoreActiveSessionsWithoutGuard(t *testing.T) {
	workDir := t.TempDir()
	manager := NewChunkedWriteManager(workDir)
	manager.RestoreActiveSessions([]ChunkedWriteRecoverySession{{WriteID: "restored", Path: "restored.txt", Mode: "overwrite", Chunks: []ChunkedWriteRecoveryChunk{{Index: 0, Content: "restored"}}}})
	if _, err := manager.commit("restored", 1, ""); err != nil {
		t.Fatal(err)
	}
	if data := chunkedwriteToolstestMustReadFile(t, filepath.Join(workDir, "restored.txt")); string(data) != "restored" {
		t.Fatalf("restored file=%q", data)
	}
}

func TestBuiltins_ChunkedWriteRejectsConcurrentTargetSession(t *testing.T) {
	workDir := t.TempDir()
	r := toolcore.NewRegistry()
	chunkedwriteToolstestRegisterTestBuiltins(r, workDir)
	beginOut, err := r.Call(context.Background(), "write_begin", map[string]any{"path": "same.txt"})
	if err != nil {
		t.Fatal(err)
	}
	writeID := chunkedwriteToolstestChunkWriteIDFromResult(t, beginOut)
	if _, err := r.Call(context.Background(), "write_begin", map[string]any{"path": "same.txt"}); err == nil || !strings.Contains(err.Error(), "already active") {
		t.Fatalf("second begin err = %v, want already active", err)
	}
	if _, err := r.Call(context.Background(), "write_abort", map[string]any{"write_id": writeID}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Call(context.Background(), "write_begin", map[string]any{"path": "same.txt"}); err != nil {
		t.Fatalf("begin after abort: %v", err)
	}
}

func TestBuiltins_ChunkedWriteCanonicalizesAbsoluteTargetForActiveSession(t *testing.T) {
	workDir := t.TempDir()
	r := toolcore.NewRegistry()
	chunkedwriteToolstestRegisterTestBuiltins(r, workDir)
	beginOut, err := r.Call(context.Background(), "write_begin", map[string]any{"path": filepath.Join(workDir, "same.txt")})
	if err != nil {
		t.Fatal(err)
	}
	writeID := chunkedwriteToolstestChunkWriteIDFromResult(t, beginOut)
	if _, err := r.Call(context.Background(), "write_begin", map[string]any{"path": "same.txt"}); err == nil || !strings.Contains(err.Error(), "already active") {
		t.Fatalf("relative duplicate begin err = %v", err)
	}
	if _, err := r.Call(context.Background(), "write_abort", map[string]any{"write_id": writeID}); err != nil {
		t.Fatal(err)
	}
}

func TestBuiltins_ChunkedWriteRejectsCaseVariantActiveSession(t *testing.T) {
	workDir := t.TempDir()
	paths, err := sandbox.NewWorkspacePathResolver(workDir)
	if err != nil {
		t.Fatal(err)
	}
	if paths.Identity("case") != paths.Identity("CASE") {
		t.Skip("workspace volume is case-sensitive")
	}
	r := toolcore.NewRegistry()
	chunkedwriteToolstestRegisterTestBuiltins(r, workDir)
	beginOut, err := r.Call(context.Background(), "write_begin", map[string]any{"path": "same.txt"})
	if err != nil {
		t.Fatal(err)
	}
	writeID := chunkedwriteToolstestChunkWriteIDFromResult(t, beginOut)
	if _, err := r.Call(context.Background(), "write_begin", map[string]any{"path": "SAME.TXT"}); err == nil || !strings.Contains(err.Error(), "already active") {
		t.Fatalf("case-variant duplicate begin err = %v", err)
	}
	if _, err := r.Call(context.Background(), "write_abort", map[string]any{"write_id": writeID}); err != nil {
		t.Fatal(err)
	}
}

func TestBuiltins_ChunkedWriteRejectsUnsafePath(t *testing.T) {
	workDir := t.TempDir()
	r := toolcore.NewRegistry()
	chunkedwriteToolstestRegisterTestBuiltins(r, workDir)
	for name, unsafePath := range map[string]string{
		"parent escape": "../escape.txt",
		"windows drive": "C:/escape.txt",
		"unc slash":     "//server/share/escape.txt",
		"unc backslash": `\\server\share\escape.txt`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := r.Call(context.Background(), "write_begin", map[string]any{"path": unsafePath}); err == nil || !strings.Contains(err.Error(), "unsafe path") {
				t.Fatalf("err = %v, want unsafe path", err)
			}
		})
	}
}

func TestBuiltins_ChunkedWriteRespectsSandboxBlockedPaths(t *testing.T) {
	workDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workDir, "private"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := toolcore.NewRegistry()
	chunkedwriteToolstestRegisterSandboxedTestBuiltins(r, workDir, []string{"private"})

	if _, err := r.Call(context.Background(), "write_begin", map[string]any{"path": "private/long.md"}); err == nil || !strings.Contains(err.Error(), "blocked path") {
		t.Fatalf("write_begin err = %v, want blocked path", err)
	}
	if _, err := os.Stat(filepath.Join(workDir, "private", "long.md")); !os.IsNotExist(err) {
		t.Fatalf("blocked chunked write created file, stat err=%v", err)
	}
}

func TestBuiltins_ChunkedWriteAbsolutePathRespectsSandboxBlockedPaths(t *testing.T) {
	workDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workDir, "private"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := toolcore.NewRegistry()
	chunkedwriteToolstestRegisterSandboxedTestBuiltins(r, workDir, []string{"private"})
	target := filepath.Join(workDir, "private", "long.md")

	if _, err := r.Call(context.Background(), "write_begin", map[string]any{"path": target}); err == nil || !strings.Contains(err.Error(), "blocked path") {
		t.Fatalf("absolute write_begin err = %v, want blocked path", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("blocked chunked write created file, stat err=%v", err)
	}
}

func TestBuiltins_ChunkedWriteCommitRejectsSymlinkSwap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink privileges vary on Windows")
	}
	workDir := t.TempDir()
	dir := filepath.Join(workDir, "swap")
	outside := t.TempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	r := toolcore.NewRegistry()
	chunkedwriteToolstestRegisterTestBuiltins(r, workDir)
	beginOut, err := r.Call(context.Background(), "write_begin", map[string]any{"path": "swap/escaped.txt", "mode": "create"})
	if err != nil {
		t.Fatal(err)
	}
	writeID := chunkedwriteToolstestChunkWriteIDFromResult(t, beginOut)
	if _, err := r.Call(context.Background(), "write_chunk", map[string]any{"write_id": writeID, "index": 0, "content": "must stay inside\n"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, dir); err != nil {
		t.Fatal(err)
	}
	_, err = r.Call(context.Background(), "write_commit", map[string]any{"write_id": writeID, "expected_chunks": 1})
	if err == nil || !strings.Contains(err.Error(), "symlink escapes workspace") {
		t.Fatalf("commit after symlink swap err = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "escaped.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("commit wrote outside workspace: %v", statErr)
	}
}

func chunkedwriteToolstestChunkWriteIDFromResult(t *testing.T, out string) string {
	t.Helper()
	const marker = "write_id="
	start := strings.Index(out, marker)
	if start < 0 {
		t.Fatalf("output %q missing write_id", out)
	}
	start += len(marker)
	end := strings.IndexAny(out[start:], " \n")
	if end < 0 {
		return out[start:]
	}
	return out[start : start+end]
}

type chunkedwriteToolstestFakeSandboxRunner struct {
	calls    int
	specs    []sandbox.ExecSpec
	requests []sandbox.Request
	err      error
	prepare  func(context.Context, sandbox.Request) (sandbox.ExecSpec, error)
}

func chunkedwriteToolstestMustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func chunkedwriteToolstestRegisterSandboxedTestBuiltins(r *toolcore.Registry, workDir string, blockedPaths []string) {
	policy := sandbox.DefaultPolicy()
	policy.Enabled = true
	policy.FileSystem.BlockedPaths = blockedPaths
	registerTestTools(r, testToolOptions{
		WorkDir:       workDir,
		Shell:         command.DefaultShellProfile(),
		Sandbox:       policy,
		SandboxRunner: &chunkedwriteToolstestFakeSandboxRunner{},
	})
}

func chunkedwriteToolstestRegisterTestBuiltins(r *toolcore.Registry, workDir string) {
	registerTestTools(r, testToolOptions{WorkDir: workDir, MediaDir: chunkedwriteToolstestTestMediaDir(workDir), Shell: command.DefaultShellProfile()})
}

func chunkedwriteToolstestSha256HexForTest(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func chunkedwriteToolstestTestMediaDir(workDir string) string { return filepath.Join(workDir, "media") }

func (r *chunkedwriteToolstestFakeSandboxRunner) Prepare(ctx context.Context, req sandbox.Request) (sandbox.ExecSpec, error) {
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
