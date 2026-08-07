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
	inputs := []string{"nested/file.txt", "./nested/file.txt"}
	if runtime.GOOS != "windows" {
		inputs = append(inputs, wantAbs)
	}
	for _, input := range inputs {
		got, err := resolver.Resolve(input)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", input, err)
		}
		if got.Relative != "nested/file.txt" || got.Absolute != wantAbs {
			t.Fatalf("Resolve(%q) = %+v, want relative nested/file.txt and absolute %s", input, got, wantAbs)
		}
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
	if runtime.GOOS != "windows" {
		cases["absolute sibling"] = filepath.Join(filepath.Dir(root), filepath.Base(root)+"-sibling", "file.txt")
	}
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
