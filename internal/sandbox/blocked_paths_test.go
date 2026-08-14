package sandbox

import (
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
