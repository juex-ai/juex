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
	policy := DefaultPolicy()
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
	policy := DefaultPolicy()
	policy.Enabled = true
	policy.FileSystem.BlockedPaths = []string{"private"}
	guard := NewPathGuard(work, policy)
	if err := guard.Check(filepath.Join(work, "PRIVATE", "secret.txt")); err == nil || !strings.Contains(err.Error(), "blocked path") {
		t.Fatalf("case variant err = %v, want blocked path", err)
	}
}
