package tools

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWorkspacePathResolverNormalizesEquivalentPaths(t *testing.T) {
	root := t.TempDir()
	resolver, err := newWorkspacePathResolver(root)
	if err != nil {
		t.Fatal(err)
	}
	wantAbs := filepath.Join(root, "nested", "file.txt")
	inputs := []string{"nested/file.txt", "./nested/file.txt", wantAbs}
	for _, input := range inputs {
		got, err := resolver.Resolve(input)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", input, err)
		}
		if got.Relative != "nested/file.txt" || got.Absolute != wantAbs || got.Identity != "nested/file.txt" {
			t.Fatalf("Resolve(%q) = %+v, want relative/identity nested/file.txt and absolute %s", input, got, wantAbs)
		}
	}
}

func TestWorkspacePathResolverCanonicalizesCaseInsensitiveIdentity(t *testing.T) {
	root := t.TempDir()
	resolver, err := newWorkspacePathResolver(root)
	if err != nil {
		t.Fatal(err)
	}
	if !resolver.caseInsensitive {
		t.Skip("workspace volume is case-sensitive")
	}
	relative, err := resolver.Resolve("nested/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	absolute, err := resolver.Resolve("NESTED/FILE.TXT")
	if err != nil {
		t.Fatal(err)
	}
	if relative.Identity != absolute.Identity {
		t.Fatalf("case-variant identities differ: %q != %q", relative.Identity, absolute.Identity)
	}
}

func TestWorkspacePathResolverPreservesCaseSensitiveIdentity(t *testing.T) {
	resolver := workspacePathResolver{caseInsensitive: false}
	if resolver.identity("Report.txt") == resolver.identity("report.txt") {
		t.Fatal("case-sensitive workspace identities should remain distinct")
	}
}

func TestWorkspacePathResolverDetectsWorkspaceCaseSensitivity(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "CaseProbe"), []byte("probe"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, lookupErr := os.Stat(filepath.Join(root, "caseprobe"))
	wantInsensitive := lookupErr == nil
	if lookupErr != nil && !os.IsNotExist(lookupErr) {
		t.Fatal(lookupErr)
	}
	resolver, err := newWorkspacePathResolver(root)
	if err != nil {
		t.Fatal(err)
	}
	if resolver.caseInsensitive != wantInsensitive {
		t.Fatalf("caseInsensitive = %v, observed filesystem behavior = %v", resolver.caseInsensitive, wantInsensitive)
	}
}

func TestWorkspaceCaseProbeCleansWorkspace(t *testing.T) {
	root := t.TempDir()
	_ = workspaceCaseInsensitive(root)
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("case probe left workspace entries: %v", entries)
	}
}

func TestWorkspaceCaseProbeIgnoresCaseVariantHardLinks(t *testing.T) {
	root := t.TempDir()
	original := filepath.Join(root, "Probe")
	if err := os.WriteFile(original, []byte("probe"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(original, filepath.Join(root, "probe")); err != nil {
		t.Skipf("workspace does not allow distinct case-variant hard links: %v", err)
	}
	if workspaceCaseInsensitive(root) {
		t.Fatal("case-variant hard links must not make a case-sensitive workspace appear insensitive")
	}
}

func TestWorkspacePathResolverDoesNotFoldAbsoluteContainment(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("regression covers case-sensitive Darwin volumes")
	}
	parent := t.TempDir()
	root := filepath.Join(parent, "Work")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	resolver, err := newWorkspacePathResolver(root)
	if err != nil {
		t.Fatal(err)
	}
	resolver.caseInsensitive = false
	sibling := filepath.Join(parent, "work", "file.txt")
	if _, err := resolver.Resolve(sibling); err == nil || !strings.Contains(err.Error(), "path escapes workspace") {
		t.Fatalf("case-variant absolute sibling err = %v, want workspace escape", err)
	}
}

func TestWorkspacePathResolverAcceptsCaseVariantAbsolutePathOnInsensitiveVolume(t *testing.T) {
	root := t.TempDir()
	resolver, err := newWorkspacePathResolver(root)
	if err != nil {
		t.Fatal(err)
	}
	if !resolver.caseInsensitive {
		t.Skip("workspace volume is case-sensitive")
	}
	input := filepath.Join(strings.ToUpper(root), "Nested", "File.txt")
	resolved, err := resolver.Resolve(input)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Relative != "Nested/File.txt" || resolved.Identity != "nested/file.txt" {
		t.Fatalf("resolved = %+v", resolved)
	}
}

func TestWorkspacePathResolverRejectsUnsafeSyntaxAndEscapes(t *testing.T) {
	root := t.TempDir()
	resolver, err := newWorkspacePathResolver(root)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"empty":          "",
		"workspace root": ".",
		"parent escape":  "../escape.txt",
		"drive absolute": "C:/escape.txt",
		"drive relative": "C:escape.txt",
		"alternate data": "file.txt:stream",
		"unc slash":      "//server/share/file.txt",
		"unc backslash":  `\\server\share\file.txt`,
		"device path":    `\\?\C:\file.txt`,
		"rooted slash":   `\rooted\file.txt`,
	}
	cases["absolute sibling"] = filepath.Join(filepath.Dir(root), filepath.Base(root)+"-sibling", "file.txt")
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := resolver.Resolve(input); err == nil || !strings.Contains(err.Error(), "unsafe path") {
				t.Fatalf("Resolve(%q) err = %v, want unsafe path", input, err)
			}
		})
	}
}

func TestWorkspacePathResolverChecksExistingSymlinkBoundary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink privileges vary on Windows")
	}
	root := t.TempDir()
	inside := filepath.Join(root, "inside")
	outside := t.TempDir()
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(inside, filepath.Join(root, "inside-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "outside-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "missing"), filepath.Join(root, "broken-link")); err != nil {
		t.Fatal(err)
	}
	resolver, err := newWorkspacePathResolver(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve("inside-link/new/file.txt"); err != nil {
		t.Fatalf("inside symlink: %v", err)
	}
	if _, err := resolver.Resolve("outside-link/new/file.txt"); err == nil || !strings.Contains(err.Error(), "symlink escapes workspace") {
		t.Fatalf("outside symlink err = %v", err)
	}
	if _, err := resolver.Resolve("broken-link/file.txt"); err == nil {
		t.Fatal("broken symlink should be rejected")
	}
}

func TestWorkspacePathResolverRejectsMissingWorkspace(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing")
	if _, err := newWorkspacePathResolver(root); err == nil {
		t.Fatal("missing workspace should fail resolver construction")
	}
}
