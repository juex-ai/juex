package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAppendBlockedPathsTrimsAndDeduplicates(t *testing.T) {
	got, err := AppendBlockedPaths([]string{" ~/.ssh ", ".env"}, []string{".env", " ~/.aws "})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"~/.ssh", ".env", "~/.aws"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("blocked paths = %#v, want %#v", got, want)
	}
	if _, err := AppendBlockedPaths(nil, []string{" "}); err == nil || !strings.Contains(err.Error(), "blocked_paths") {
		t.Fatalf("err = %v, want blocked_paths validation error", err)
	}
}

func TestPathGuardBlocksRelativeAbsoluteAndSymlinkTargets(t *testing.T) {
	if os.Getenv("CI_WINDOWS_SYMLINK_SKIP") != "" {
		t.Skip("symlink availability varies on Windows")
	}
	work := t.TempDir()
	outside := t.TempDir()
	blockedOutside := filepath.Join(outside, "secret")
	if err := os.MkdirAll(blockedOutside, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(work, "link")
	if err := os.Symlink(blockedOutside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	policy := LegacyDefaultPolicy()
	policy.Enabled = true
	policy.FileSystem.BlockedPaths = []string{"private", blockedOutside}
	guard := NewPathGuard(work, policy)

	for _, path := range []string{
		filepath.Join(work, "private"),
		filepath.Join(work, "private", "token.txt"),
		filepath.Join(blockedOutside, "token.txt"),
		filepath.Join(link, "token.txt"),
	} {
		if err := guard.Check(path); err == nil || !strings.Contains(err.Error(), "blocked path") {
			t.Fatalf("Check(%q) err = %v, want blocked path", path, err)
		}
	}
	if err := guard.Check(filepath.Join(work, "public", "note.txt")); err != nil {
		t.Fatalf("public path blocked: %v", err)
	}
}

func TestPathGuardBlocksCaseVariantOnCaseInsensitivePlatforms(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("case-insensitive guard behavior is platform-specific")
	}
	work := t.TempDir()
	policy := LegacyDefaultPolicy()
	policy.Enabled = true
	policy.FileSystem.BlockedPaths = []string{"private"}
	guard := NewPathGuard(work, policy)
	if err := guard.Check(filepath.Join(work, "PRIVATE", "secret.txt")); err == nil || !strings.Contains(err.Error(), "blocked path") {
		t.Fatalf("case variant err = %v, want blocked path", err)
	}
}

func TestFilePolicyAllowsOnlyCanonicalWritableRoots(t *testing.T) {
	work := t.TempDir()
	agentState := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(work, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	policy := DefaultPolicyForOS("linux")
	guard := NewFilePolicy(FilePolicyOptions{Policy: policy, WorkDir: work, AgentStateDir: agentState})

	for _, path := range []string{
		filepath.Join(work, "inside.txt"),
		filepath.Join(agentState, "memory", "inside.txt"),
	} {
		if err := guard.CheckWrite(path); err != nil {
			t.Fatalf("CheckWrite(%q) = %v, want allowed", path, err)
		}
	}
	for _, path := range []string{
		filepath.Join(outside, "outside.txt"),
		filepath.Join(link, "existing-or-new.txt"),
	} {
		if err := guard.CheckWrite(path); err == nil || !strings.Contains(err.Error(), "outside writable roots") {
			t.Fatalf("CheckWrite(%q) = %v, want outside writable roots", path, err)
		}
	}
	if err := guard.CheckRead(filepath.Join(outside, "readable.txt")); err != nil {
		t.Fatalf("outside read = %v, want allowed", err)
	}
}

func TestFilePolicyReadOnlyRootsAllowReadsAndRejectWrites(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "work")
	agentState := filepath.Join(root, "agent")
	artifactDir := filepath.Join(agentState, "artifacts")
	for _, path := range []string{work, artifactDir} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	artifactPath := filepath.Join(artifactDir, "sessions", "result.txt")
	guard := NewFilePolicy(FilePolicyOptions{
		Policy:        DefaultPolicyForOS("linux"),
		WorkDir:       work,
		AgentStateDir: agentState,
		ReadOnlyPaths: []string{artifactDir},
	})
	if err := guard.CheckRead(artifactPath); err != nil {
		t.Fatalf("Artifact read = %v, want allowed", err)
	}
	if err := guard.CheckWrite(artifactPath); err == nil || !strings.Contains(err.Error(), "read-only root") {
		t.Fatalf("Artifact write = %v, want read-only root rejection", err)
	}
	if err := guard.CheckWrite(filepath.Join(agentState, "memory", "note.txt")); err != nil {
		t.Fatalf("other Agent state write = %v, want allowed", err)
	}
	roots := guard.ReadOnlyRoots()
	wantRoot, ok := canonicalPath(work, artifactDir)
	if !ok {
		t.Fatalf("canonicalPath(%q) failed", artifactDir)
	}
	if len(roots) != 1 || roots[0] != wantRoot {
		t.Fatalf("read-only roots = %#v, want %q", roots, wantRoot)
	}

	disabled := NewFilePolicy(FilePolicyOptions{
		Policy:        LegacyDefaultPolicy(),
		WorkDir:       work,
		ReadOnlyPaths: []string{artifactDir},
	})
	if err := disabled.CheckWrite(artifactPath); err == nil || !strings.Contains(err.Error(), "read-only root") {
		t.Fatalf("disabled sandbox Artifact write = %v, want builtin read-only root rejection", err)
	}
}

func TestFilePolicyReadOnlyRootsRejectHardLinkAliasesWhenWritesAreUnrestricted(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("hard-link count enforcement is supported by the sandbox platforms")
	}
	root := t.TempDir()
	work := filepath.Join(root, "work")
	artifactDir := filepath.Join(root, "agent", "artifacts")
	for _, path := range []string{work, artifactDir} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	artifactPath := filepath.Join(artifactDir, "result.txt")
	if err := os.WriteFile(artifactPath, []byte("durable result"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(work, "artifact-alias.txt")
	if err := os.Link(artifactPath, alias); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}

	readWrite := DefaultPolicyForOS(runtime.GOOS)
	readWrite.FileSystem.OutsideWorkspace = OutsideWorkspaceReadWrite
	for name, policy := range map[string]Policy{
		"disabled":   LegacyDefaultPolicy(),
		"read_write": readWrite,
	} {
		t.Run(name, func(t *testing.T) {
			guard := NewFilePolicy(FilePolicyOptions{
				Policy:        policy,
				WorkDir:       work,
				ReadOnlyPaths: []string{artifactDir},
			})
			if err := guard.CheckWrite(alias); err == nil || !strings.Contains(err.Error(), "hard-link alias of read-only root") {
				t.Fatalf("CheckWrite(%q) = %v, want read-only hard-link rejection", alias, err)
			}
		})
	}
}

func TestFilePolicyRejectsExistingFilesWithMultipleHardLinks(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("hard-link count enforcement is supported by the sandbox platforms")
	}
	work := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(work, "inside.txt")
	if err := os.Link(outside, inside); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}

	guard := NewFilePolicy(FilePolicyOptions{
		Policy:  DefaultPolicyForOS(runtime.GOOS),
		WorkDir: work,
	})
	if err := guard.CheckWrite(inside); err == nil || !strings.Contains(err.Error(), "multiple hard links") {
		t.Fatalf("CheckWrite(%q) = %v, want multiple hard links rejection", inside, err)
	}

	readWrite := DefaultPolicyForOS(runtime.GOOS)
	readWrite.FileSystem.OutsideWorkspace = OutsideWorkspaceReadWrite
	guard = NewFilePolicy(FilePolicyOptions{Policy: readWrite, WorkDir: work})
	if err := guard.CheckWrite(inside); err != nil {
		t.Fatalf("read_write CheckWrite(%q) = %v, want allowed", inside, err)
	}
}

func TestFilePolicyAllowsHardLinksContainedInWritableRoots(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("hard-link count enforcement is supported by the sandbox platforms")
	}
	work := t.TempDir()
	first := filepath.Join(work, "first.txt")
	second := filepath.Join(work, "second.txt")
	if err := os.WriteFile(first, []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(first, second); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}

	guard := NewFilePolicy(FilePolicyOptions{
		Policy:  DefaultPolicyForOS(runtime.GOOS),
		WorkDir: work,
	})
	for _, path := range []string{first, second} {
		if err := guard.CheckWrite(path); err != nil {
			t.Fatalf("CheckWrite(%q) = %v, want contained hard link allowed", path, err)
		}
	}
	if err := guard.CheckCommandWrites(context.Background()); err != nil {
		t.Fatalf("CheckCommandWrites() = %v, want contained hard links allowed", err)
	}
}

func TestFilePolicyRejectsCommandWritesWhenWritableRootContainsHardLinks(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("hard-link count enforcement is supported by the sandbox platforms")
	}
	work := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(work, "inside.txt")
	if err := os.Link(outside, inside); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}

	guard := NewFilePolicy(FilePolicyOptions{
		Policy:  DefaultPolicyForOS(runtime.GOOS),
		WorkDir: work,
	})
	if err := guard.CheckCommandWrites(context.Background()); err == nil || !strings.Contains(err.Error(), "multiple hard links") {
		t.Fatalf("CheckCommandWrites() = %v, want multiple hard links rejection", err)
	}

	readWrite := DefaultPolicyForOS(runtime.GOOS)
	readWrite.FileSystem.OutsideWorkspace = OutsideWorkspaceReadWrite
	guard = NewFilePolicy(FilePolicyOptions{Policy: readWrite, WorkDir: work})
	if err := guard.CheckCommandWrites(context.Background()); err != nil {
		t.Fatalf("read_write CheckCommandWrites() = %v, want allowed", err)
	}
}

func TestFilePolicyCommandWriteCheckHonorsCancellation(t *testing.T) {
	guard := NewFilePolicy(FilePolicyOptions{
		Policy:  DefaultPolicyForOS("linux"),
		WorkDir: t.TempDir(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := guard.CheckCommandWrites(ctx); err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Fatalf("CheckCommandWrites(cancelled) = %v, want context cancellation", err)
	}
}

func TestFilePolicyCachesSuccessfulCommandWriteCheck(t *testing.T) {
	guard := NewFilePolicy(FilePolicyOptions{
		Policy:  DefaultPolicyForOS("linux"),
		WorkDir: t.TempDir(),
	})
	if err := guard.CheckCommandWrites(context.Background()); err != nil {
		t.Fatal(err)
	}
	guard.commandWriteCheck.mu.Lock()
	safe := guard.commandWriteCheck.safe
	guard.commandWriteCheck.mu.Unlock()
	if !safe {
		t.Fatal("successful command write check was not cached")
	}
}

func TestFilePolicyDoesNotCacheFailedCommandWriteCheck(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("hard-link count enforcement is supported by the sandbox platforms")
	}
	work := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(work, "inside.txt")
	if err := os.Link(outside, inside); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	guard := NewFilePolicy(FilePolicyOptions{
		Policy:  DefaultPolicyForOS(runtime.GOOS),
		WorkDir: work,
	})
	if err := guard.CheckCommandWrites(context.Background()); err == nil {
		t.Fatal("expected initial external hard-link rejection")
	}
	if err := os.Remove(inside); err != nil {
		t.Fatal(err)
	}
	if err := guard.CheckCommandWrites(context.Background()); err != nil {
		t.Fatalf("CheckCommandWrites() after repair = %v, want allowed", err)
	}
	guard.commandWriteCheck.mu.Lock()
	safe := guard.commandWriteCheck.safe
	guard.commandWriteCheck.mu.Unlock()
	if !safe {
		t.Fatal("repaired command write check was not cached")
	}
}

func TestFilePolicyCommandWritableRootsRemoveNestedDuplicates(t *testing.T) {
	work := t.TempDir()
	agentState := filepath.Join(work, ".juex", "agent")
	if err := os.MkdirAll(agentState, 0o700); err != nil {
		t.Fatal(err)
	}
	guard := NewFilePolicy(FilePolicyOptions{
		Policy:        DefaultPolicyForOS("linux"),
		WorkDir:       work,
		AgentStateDir: agentState,
	})
	roots := guard.commandWritableRoots()
	want, ok := canonicalPath(work, work)
	if !ok {
		t.Fatalf("canonicalPath(%q) failed", work)
	}
	if len(roots) != 1 || roots[0] != want {
		t.Fatalf("command writable roots = %#v, want only %q", roots, want)
	}
}

func TestFilePolicyAllowsWritableRootCaseVariantOnCaseInsensitiveFilesystem(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("case-insensitive writable-root behavior is exercised on macOS")
	}
	parent := t.TempDir()
	work := filepath.Join(parent, "MixedCase")
	if err := os.Mkdir(work, 0o755); err != nil {
		t.Fatal(err)
	}
	variant := filepath.Join(parent, "mixedcase")
	workInfo, workErr := os.Stat(work)
	variantInfo, variantErr := os.Stat(variant)
	if workErr != nil || variantErr != nil || !os.SameFile(workInfo, variantInfo) {
		t.Skip("test volume is case-sensitive")
	}
	guard := NewFilePolicy(FilePolicyOptions{
		Policy:  DefaultPolicyForOS("darwin"),
		WorkDir: work,
	})
	if err := guard.CheckWrite(filepath.Join(variant, "new.txt")); err != nil {
		t.Fatalf("case-variant Workspace write = %v, want allowed", err)
	}
}

func TestFilesystemContainmentRejectsDifferentCaseFoldedDirectory(t *testing.T) {
	parent := t.TempDir()
	upper := filepath.Join(parent, "Root")
	lower := filepath.Join(parent, "root")
	if err := os.Mkdir(upper, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(lower, 0o755); err != nil {
		t.Skip("test volume does not permit case-distinct directories")
	}
	if pathWithinOrEqualFilesystem(upper, filepath.Join(lower, "new.txt")) {
		t.Fatal("case-folded but physically distinct directory matched writable root")
	}
}

func TestFilePolicyExposesCanonicalWritableRoots(t *testing.T) {
	parent := t.TempDir()
	realWork := filepath.Join(parent, "real-work")
	if err := os.Mkdir(realWork, 0o755); err != nil {
		t.Fatal(err)
	}
	linkedWork := filepath.Join(parent, "linked-work")
	if err := os.Symlink(realWork, linkedWork); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	policy := DefaultPolicyForOS("linux")
	guard := NewFilePolicy(FilePolicyOptions{Policy: policy, WorkDir: linkedWork})
	canonicalWork, err := filepath.EvalSymlinks(realWork)
	if err != nil {
		t.Fatal(err)
	}
	roots := guard.WritableRoots()
	if len(roots) != 1 || roots[0] != canonicalWork {
		t.Fatalf("writable roots = %#v, want canonical root %q", roots, canonicalWork)
	}
	if err := guard.CheckWrite(filepath.Join(linkedWork, "new.txt")); err != nil {
		t.Fatalf("write through workspace symlink = %v, want allowed", err)
	}
}

func TestFilePolicyBlockedPathsOverrideWritableRoots(t *testing.T) {
	work := t.TempDir()
	blocked := filepath.Join(work, "private")
	policy := DefaultPolicyForOS("linux")
	policy.FileSystem.BlockedPaths = []string{blocked}
	guard := NewFilePolicy(FilePolicyOptions{Policy: policy, WorkDir: work})
	if err := guard.CheckWrite(filepath.Join(blocked, "secret.txt")); err == nil || !strings.Contains(err.Error(), "blocked path") {
		t.Fatalf("CheckWrite blocked = %v", err)
	}
}

func TestFilePolicyEscapeHatchesPreserveBlockedPaths(t *testing.T) {
	work := t.TempDir()
	outside := t.TempDir()
	for _, policy := range []Policy{
		LegacyDefaultPolicy(),
		{Enabled: true, FileSystem: FileSystemPolicy{OutsideWorkspace: OutsideWorkspaceReadWrite}, Network: NetworkPolicy{Enabled: true}},
	} {
		policy.FileSystem.BlockedPaths = []string{filepath.Join(outside, "blocked")}
		guard := NewFilePolicy(FilePolicyOptions{Policy: policy, WorkDir: work})
		if err := guard.CheckWrite(filepath.Join(outside, "allowed")); err != nil {
			t.Fatalf("outside read_write write = %v", err)
		}
		if policy.Enabled {
			if err := guard.CheckWrite(filepath.Join(outside, "blocked", "secret")); err == nil {
				t.Fatal("enabled policy did not enforce blocked_paths")
			}
		} else if err := guard.CheckWrite(filepath.Join(outside, "blocked", "secret")); err != nil {
			t.Fatalf("disabled policy did not bypass all checks: %v", err)
		}
	}
}
