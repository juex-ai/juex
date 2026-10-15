package applypatch

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/command"
	"github.com/juex-ai/juex/internal/foundation/sandbox"
	toolcore "github.com/juex-ai/juex/internal/foundation/tools"
)

func TestRegisterBuiltinsApplyPatchSchemaAndDisable(t *testing.T) {
	r := toolcore.NewRegistry()
	applypatchToolstestRegisterTestBuiltins(r, t.TempDir())
	tool, ok := r.Get("apply_patch")
	if !ok {
		t.Fatal("apply_patch should be registered by default")
	}
	props, ok := tool.Schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %+v", tool.Schema["properties"])
	}
	if _, ok := props["patch_text"]; !ok {
		t.Fatalf("patch_text property missing from schema: %+v", props)
	}
	if !strings.Contains(tool.Description, "absolute paths inside the workspace") {
		t.Fatalf("apply_patch description missing accepted absolute path contract: %q", tool.Description)
	}
	required, ok := tool.Schema["required"].([]string)
	if !ok || len(required) != 1 || required[0] != "patch_text" {
		t.Fatalf("required = %+v, want [patch_text]", tool.Schema["required"])
	}

}

func TestBuiltins_ApplyPatchAddUpdateDeleteMove(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "src.txt"), []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "delete.txt"), []byte("remove me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "move.txt"), []byte("old name\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := toolcore.NewRegistry()
	applypatchToolstestRegisterTestBuiltins(r, workDir)

	out, err := r.Call(context.Background(), "apply_patch", map[string]any{"patch_text": strings.Join([]string{
		"*** Begin Patch",
		"*** Add File: notes/new.txt",
		"+line one",
		"+line two",
		"*** Update File: src.txt",
		"@@",
		" alpha",
		"-beta",
		"+BETTER",
		" gamma",
		"*** Update File: move.txt",
		"*** Move to: moved/renamed.txt",
		"@@",
		"-old name",
		"+new name",
		"*** Delete File: delete.txt",
		"*** End Patch",
	}, "\n")})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"applied patch", "add=1", "update=2", "delete=1", "move=1", "notes/new.txt", "src.txt", "moved/renamed.txt"} {
		if !strings.Contains(out, want) {
			t.Fatalf("apply_patch output = %q, want substring %q", out, want)
		}
	}
	if data := applypatchToolstestMustReadFile(t, filepath.Join(workDir, "notes", "new.txt")); string(data) != "line one\nline two\n" {
		t.Fatalf("new file = %q", data)
	}
	if data := applypatchToolstestMustReadFile(t, filepath.Join(workDir, "src.txt")); string(data) != "alpha\nBETTER\ngamma\n" {
		t.Fatalf("updated file = %q", data)
	}
	if _, err := os.Stat(filepath.Join(workDir, "delete.txt")); !os.IsNotExist(err) {
		t.Fatalf("delete.txt should be removed, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(workDir, "move.txt")); !os.IsNotExist(err) {
		t.Fatalf("move.txt should be removed after move, stat err=%v", err)
	}
	if data := applypatchToolstestMustReadFile(t, filepath.Join(workDir, "moved", "renamed.txt")); string(data) != "new name\n" {
		t.Fatalf("moved file = %q", data)
	}
}

func TestBuiltins_ApplyPatchAcceptsAbsoluteWorkspacePaths(t *testing.T) {
	workDir := t.TempDir()
	for name, content := range map[string]string{
		"src.txt":    "alpha\nbeta\n",
		"move.txt":   "old name\n",
		"delete.txt": "remove me\n",
	} {
		if err := os.WriteFile(filepath.Join(workDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r := toolcore.NewRegistry()
	applypatchToolstestRegisterTestBuiltins(r, workDir)

	out, err := r.Call(context.Background(), "apply_patch", map[string]any{"patch_text": strings.Join([]string{
		"*** Begin Patch",
		"*** Add File: " + filepath.Join(workDir, "nested", "new.txt"),
		"+new file",
		"*** Update File: " + filepath.Join(workDir, "src.txt"),
		"@@",
		" alpha",
		"-beta",
		"+BETA",
		"*** Update File: " + filepath.Join(workDir, "move.txt"),
		"*** Move to: " + filepath.Join(workDir, "moved", "renamed.txt"),
		"@@",
		"-old name",
		"+new name",
		"*** Delete File: " + filepath.Join(workDir, "delete.txt"),
		"*** End Patch",
	}, "\n")})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"nested/new.txt", "src.txt", "moved/renamed.txt"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output = %q, want normalized path %q", out, want)
		}
	}
	if strings.Contains(out, workDir) {
		t.Fatalf("output leaked absolute workspace path: %q", out)
	}
	if got := string(applypatchToolstestMustReadFile(t, filepath.Join(workDir, "nested", "new.txt"))); got != "new file\n" {
		t.Fatalf("new file = %q", got)
	}
	if got := string(applypatchToolstestMustReadFile(t, filepath.Join(workDir, "src.txt"))); got != "alpha\nBETA\n" {
		t.Fatalf("updated file = %q", got)
	}
	if got := string(applypatchToolstestMustReadFile(t, filepath.Join(workDir, "moved", "renamed.txt"))); got != "new name\n" {
		t.Fatalf("moved file = %q", got)
	}
	if _, err := os.Stat(filepath.Join(workDir, "delete.txt")); !os.IsNotExist(err) {
		t.Fatalf("delete target still exists: %v", err)
	}
}

func TestBuiltins_ApplyPatchCanonicalizesAbsolutePathsBeforeValidation(t *testing.T) {
	workDir := t.TempDir()
	r := toolcore.NewRegistry()
	applypatchToolstestRegisterTestBuiltins(r, workDir)
	target := filepath.Join(workDir, "same.txt")

	_, err := r.Call(context.Background(), "apply_patch", map[string]any{"patch_text": strings.Join([]string{
		"*** Begin Patch",
		"*** Add File: same.txt",
		"+first",
		"*** Add File: " + target,
		"+second",
		"*** End Patch",
	}, "\n")})
	if err == nil || !strings.Contains(err.Error(), "duplicate operation") {
		t.Fatalf("mixed-spelling duplicate err = %v", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("duplicate patch wrote target: %v", statErr)
	}

	if err := os.WriteFile(target, []byte("same\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = r.Call(context.Background(), "apply_patch", map[string]any{"patch_text": strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: same.txt",
		"*** Move to: " + target,
		"*** End Patch",
	}, "\n")})
	if err == nil || !strings.Contains(err.Error(), "move target matches source") {
		t.Fatalf("mixed-spelling same move err = %v", err)
	}
}

func TestBuiltins_ApplyPatchRejectsCaseVariantDuplicate(t *testing.T) {
	workDir := t.TempDir()
	paths, err := sandbox.NewWorkspacePathResolver(workDir)
	if err != nil {
		t.Fatal(err)
	}
	if paths.Identity("case") != paths.Identity("CASE") {
		t.Skip("workspace volume is case-sensitive")
	}
	r := toolcore.NewRegistry()
	applypatchToolstestRegisterTestBuiltins(r, workDir)
	target := filepath.Join(workDir, "same.txt")
	_, err = r.Call(context.Background(), "apply_patch", map[string]any{"patch_text": strings.Join([]string{
		"*** Begin Patch",
		"*** Add File: same.txt",
		"+first",
		"*** Add File: SAME.TXT",
		"+second",
		"*** End Patch",
	}, "\n")})
	if err == nil || !strings.Contains(err.Error(), "duplicate operation") {
		t.Fatalf("case-variant duplicate err = %v", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("duplicate patch wrote target: %v", statErr)
	}
}

func TestBuiltins_ApplyPatchRejectsOutsideAbsolutePathBeforeWriting(t *testing.T) {
	workDir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	r := toolcore.NewRegistry()
	applypatchToolstestRegisterTestBuiltins(r, workDir)

	_, err := r.Call(context.Background(), "apply_patch", map[string]any{"patch_text": strings.Join([]string{
		"*** Begin Patch",
		"*** Add File: valid.txt",
		"+must not be written",
		"*** Add File: " + outside,
		"+outside",
		"*** End Patch",
	}, "\n")})
	if err == nil || !strings.Contains(err.Error(), "path escapes workspace") {
		t.Fatalf("outside absolute err = %v", err)
	}
	for _, path := range []string{filepath.Join(workDir, "valid.txt"), outside} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("failed patch wrote %s: %v", path, statErr)
		}
	}
}

func TestBuiltins_ApplyPatchAbsolutePathRespectsSandboxBlockedPaths(t *testing.T) {
	workDir := t.TempDir()
	blockedDir := filepath.Join(workDir, "private")
	if err := os.MkdirAll(blockedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(blockedDir, "secret.txt")
	if err := os.WriteFile(target, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := toolcore.NewRegistry()
	applypatchToolstestRegisterSandboxedTestBuiltins(r, workDir, []string{"private"})

	_, err := r.Call(context.Background(), "apply_patch", map[string]any{"patch_text": strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: " + target,
		"@@",
		"-secret",
		"+open",
		"*** End Patch",
	}, "\n")})
	if err == nil || !strings.Contains(err.Error(), "blocked path") {
		t.Fatalf("absolute blocked path err = %v", err)
	}
	if got := string(applypatchToolstestMustReadFile(t, target)); got != "secret\n" {
		t.Fatalf("blocked file changed: %q", got)
	}
}

func TestBuiltins_ApplyPatchTrimsEnvelopeWhitespace(t *testing.T) {
	workDir := t.TempDir()
	target := filepath.Join(workDir, "src.txt")
	if err := os.WriteFile(target, []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := toolcore.NewRegistry()
	applypatchToolstestRegisterTestBuiltins(r, workDir)

	_, err := r.Call(context.Background(), "apply_patch", map[string]any{"patch_text": "\n \t*** Begin Patch\n*** Update File: src.txt\n@@\n-one\n+ONE\n two\n*** End Patch\n\n"})
	if err != nil {
		t.Fatal(err)
	}
	if data := applypatchToolstestMustReadFile(t, target); string(data) != "ONE\ntwo\n" {
		t.Fatalf("updated file = %q", data)
	}
}

func TestBuiltins_ApplyPatchTreatsBlankUpdateLinesAsContext(t *testing.T) {
	workDir := t.TempDir()
	target := filepath.Join(workDir, "src.txt")
	if err := os.WriteFile(target, []byte("alpha\n\nbeta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := toolcore.NewRegistry()
	applypatchToolstestRegisterTestBuiltins(r, workDir)

	_, err := r.Call(context.Background(), "apply_patch", map[string]any{"patch_text": strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: src.txt",
		"@@",
		" alpha",
		"",
		"-beta",
		"+BETA",
		"",
		"*** End Patch",
	}, "\n")})
	if err != nil {
		t.Fatal(err)
	}
	if data := applypatchToolstestMustReadFile(t, target); string(data) != "alpha\n\nBETA\n" {
		t.Fatalf("updated file = %q", data)
	}
}

func TestBuiltins_ApplyPatchMissingContextPreservesFile(t *testing.T) {
	workDir := t.TempDir()
	target := filepath.Join(workDir, "src.txt")
	original := "one\ntwo\n"
	if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	r := toolcore.NewRegistry()
	applypatchToolstestRegisterTestBuiltins(r, workDir)

	_, err := r.Call(context.Background(), "apply_patch", map[string]any{"patch_text": strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: src.txt",
		"@@",
		" three",
		"-missing",
		"+replacement",
		"*** End Patch",
	}, "\n")})
	if err == nil || !strings.Contains(err.Error(), "context not found") {
		t.Fatalf("err = %v, want context not found", err)
	}
	if data := applypatchToolstestMustReadFile(t, target); string(data) != original {
		t.Fatalf("file changed after failed patch: %q", data)
	}
}

func TestBuiltins_ApplyPatchAmbiguousContextPreservesFile(t *testing.T) {
	workDir := t.TempDir()
	target := filepath.Join(workDir, "src.txt")
	original := "repeat\nx\nrepeat\nx\n"
	if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	r := toolcore.NewRegistry()
	applypatchToolstestRegisterTestBuiltins(r, workDir)

	_, err := r.Call(context.Background(), "apply_patch", map[string]any{"patch_text": strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: src.txt",
		"@@",
		" repeat",
		"-x",
		"+y",
		"*** End Patch",
	}, "\n")})
	if err == nil || !strings.Contains(err.Error(), "ambiguous context") {
		t.Fatalf("err = %v, want ambiguous context", err)
	}
	if data := applypatchToolstestMustReadFile(t, target); string(data) != original {
		t.Fatalf("file changed after failed patch: %q", data)
	}
}

func TestBuiltins_ApplyPatchRejectsUnsafePath(t *testing.T) {
	workDir := t.TempDir()
	r := toolcore.NewRegistry()
	applypatchToolstestRegisterTestBuiltins(r, workDir)

	unsafePatches := map[string]string{
		"parent escape": "../escape.txt",
		"windows drive": "C:/escape.txt",
		"unc slash":     "//server/share/escape.txt",
		"unc backslash": `\\server\share\escape.txt`,
	}
	for name, unsafePath := range unsafePatches {
		t.Run(name, func(t *testing.T) {
			_, err := r.Call(context.Background(), "apply_patch", map[string]any{"patch_text": strings.Join([]string{
				"*** Begin Patch",
				"*** Add File: " + unsafePath,
				"+nope",
				"*** End Patch",
			}, "\n")})
			if err == nil || !strings.Contains(err.Error(), "unsafe path") {
				t.Fatalf("err = %v, want unsafe path", err)
			}
		})
	}
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(workDir), "escape.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("escape file should not exist, stat err=%v", statErr)
	}
}

func TestBuiltins_ApplyPatchRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink privileges vary on Windows")
	}
	workDir := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(workDir, "link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	r := toolcore.NewRegistry()
	applypatchToolstestRegisterTestBuiltins(r, workDir)

	_, err := r.Call(context.Background(), "apply_patch", map[string]any{"patch_text": strings.Join([]string{
		"*** Begin Patch",
		"*** Add File: link/escape.txt",
		"+nope",
		"*** End Patch",
	}, "\n")})
	if err == nil || !strings.Contains(err.Error(), "symlink escapes workspace") {
		t.Fatalf("err = %v, want symlink escape error", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "escape.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("escape file should not exist outside workspace, stat err=%v", statErr)
	}
}

func TestBuiltins_ApplyPatchRespectsSandboxBlockedPathsBeforeReading(t *testing.T) {
	workDir := t.TempDir()
	blockedDir := filepath.Join(workDir, "private")
	if err := os.MkdirAll(blockedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(blockedDir, "secret.txt")
	original := "secret\n"
	if err := os.WriteFile(target, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	r := toolcore.NewRegistry()
	applypatchToolstestRegisterSandboxedTestBuiltins(r, workDir, []string{"private"})

	_, err := r.Call(context.Background(), "apply_patch", map[string]any{"patch_text": strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: private/secret.txt",
		"@@",
		"-secret",
		"+open",
		"*** End Patch",
	}, "\n")})
	if err == nil || !strings.Contains(err.Error(), "blocked path") {
		t.Fatalf("err = %v, want blocked path", err)
	}
	if data := applypatchToolstestMustReadFile(t, target); string(data) != original {
		t.Fatalf("blocked file changed: %q", data)
	}
}

func TestBuiltins_ApplyPatchRejectsMalformedPatch(t *testing.T) {
	workDir := t.TempDir()
	r := toolcore.NewRegistry()
	applypatchToolstestRegisterTestBuiltins(r, workDir)

	_, err := r.Call(context.Background(), "apply_patch", map[string]any{"patch_text": strings.Join([]string{
		"*** Add File: a.txt",
		"+missing envelope",
		"*** End Patch",
	}, "\n")})
	if err == nil || !strings.Contains(err.Error(), "must start with") {
		t.Fatalf("err = %v, want missing begin patch error", err)
	}
}

func TestBuiltins_ApplyPatchRejectsDuplicateOperationsBeforeWriting(t *testing.T) {
	workDir := t.TempDir()
	r := toolcore.NewRegistry()
	applypatchToolstestRegisterTestBuiltins(r, workDir)

	_, err := r.Call(context.Background(), "apply_patch", map[string]any{"patch_text": strings.Join([]string{
		"*** Begin Patch",
		"*** Add File: a.txt",
		"+first",
		"*** Add File: a.txt",
		"+second",
		"*** End Patch",
	}, "\n")})
	if err == nil || !strings.Contains(err.Error(), "duplicate operation") {
		t.Fatalf("err = %v, want duplicate operation", err)
	}
	if _, statErr := os.Stat(filepath.Join(workDir, "a.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("a.txt should not exist after failed patch, stat err=%v", statErr)
	}
}

func TestBuiltins_ApplyPatchValidatesWholePatchBeforeWriting(t *testing.T) {
	workDir := t.TempDir()
	target := filepath.Join(workDir, "src.txt")
	if err := os.WriteFile(target, []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := toolcore.NewRegistry()
	applypatchToolstestRegisterTestBuiltins(r, workDir)

	_, err := r.Call(context.Background(), "apply_patch", map[string]any{"patch_text": strings.Join([]string{
		"*** Begin Patch",
		"*** Add File: created.txt",
		"+created",
		"*** Update File: src.txt",
		"@@",
		" missing",
		"-line",
		"+replacement",
		"*** End Patch",
	}, "\n")})
	if err == nil || !strings.Contains(err.Error(), "context not found") {
		t.Fatalf("err = %v, want context not found", err)
	}
	if _, statErr := os.Stat(filepath.Join(workDir, "created.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("created.txt should not exist after validation failure, stat err=%v", statErr)
	}
	if data := applypatchToolstestMustReadFile(t, target); string(data) != "one\ntwo\n" {
		t.Fatalf("src.txt changed after failed patch: %q", data)
	}
}

type applypatchToolstestFakeSandboxRunner struct {
	calls    int
	specs    []sandbox.ExecSpec
	requests []sandbox.Request
	err      error
	prepare  func(context.Context, sandbox.Request) (sandbox.ExecSpec, error)
}

func applypatchToolstestMustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func applypatchToolstestRegisterSandboxedTestBuiltins(r *toolcore.Registry, workDir string, blockedPaths []string) {
	policy := sandbox.DefaultPolicy()
	policy.Enabled = true
	policy.FileSystem.BlockedPaths = blockedPaths
	registerTestTools(r, testToolOptions{
		WorkDir:       workDir,
		Shell:         command.DefaultShellProfile(),
		Sandbox:       policy,
		SandboxRunner: &applypatchToolstestFakeSandboxRunner{},
	})
}

func applypatchToolstestRegisterTestBuiltins(r *toolcore.Registry, workDir string) {
	registerTestTools(r, testToolOptions{WorkDir: workDir, MediaDir: applypatchToolstestTestMediaDir(workDir), Shell: command.DefaultShellProfile()})
}

func applypatchToolstestTestMediaDir(workDir string) string { return filepath.Join(workDir, "media") }

func (r *applypatchToolstestFakeSandboxRunner) Prepare(ctx context.Context, req sandbox.Request) (sandbox.ExecSpec, error) {
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
