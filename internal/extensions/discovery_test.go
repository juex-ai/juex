package extensions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverFindsUserAndProjectExtensions(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeExtensionFile(t, filepath.Join(home, "extensions", "user-ext", "mcp.json"), "{}")
	writeExtensionFile(t, filepath.Join(home, "extensions", "user-ext", "skills", "alpha", "SKILL.md"), "---\nname: alpha\n---\n")
	writeExtensionFile(t, filepath.Join(work, ".juex", "extensions", "project-ext", "hooks.yaml"), "trusted: true\n")

	resources, err := Discover(DiscoverOptions{
		HomeJuexDir: home,
		WorkDir:     work,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resources.Extensions) != 2 {
		t.Fatalf("extensions = %+v", resources.Extensions)
	}
	if resources.Extensions[0].Name != "user-ext" || resources.Extensions[0].Source != "ext:user-ext" || resources.Extensions[0].Scope != ScopeUser {
		t.Fatalf("user extension = %+v", resources.Extensions[0])
	}
	if resources.Extensions[1].Name != "project-ext" || resources.Extensions[1].Source != "ext:project-ext" || resources.Extensions[1].Scope != ScopeProject {
		t.Fatalf("project extension = %+v", resources.Extensions[1])
	}
	if len(resources.MCPConfigs) != 1 || resources.MCPConfigs[0].Source != "ext:user-ext" {
		t.Fatalf("mcp refs = %+v", resources.MCPConfigs)
	}
	if len(resources.SkillDirs) != 1 || resources.SkillDirs[0].Source != "ext:user-ext" {
		t.Fatalf("skill refs = %+v", resources.SkillDirs)
	}
	if len(resources.HookFiles) != 1 || resources.HookFiles[0].Source != "ext:project-ext" || !resources.HookFiles[0].RequireTrust {
		t.Fatalf("hook refs = %+v", resources.HookFiles)
	}
}

func TestDiscoverAlwaysLoadsHomeExtensions(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeExtensionFile(t, filepath.Join(home, "extensions", "user-ext", "mcp.json"), "{}")
	writeExtensionFile(t, filepath.Join(work, ".juex", "extensions", "project-ext", "mcp.json"), "{}")

	resources, err := Discover(DiscoverOptions{
		HomeJuexDir: home,
		WorkDir:     work,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resources.Extensions) != 2 ||
		resources.Extensions[0].Name != "user-ext" ||
		resources.Extensions[1].Name != "project-ext" {
		t.Fatalf("extensions = %+v", resources.Extensions)
	}
}

func TestDiscoverDeduplicatesOverlappingHomeAndProjectRoots(t *testing.T) {
	work := t.TempDir()
	homeJuex := filepath.Join(work, ".juex")
	writeExtensionFile(t, filepath.Join(homeJuex, "extensions", "shared", "hooks.yaml"), "commands: {}\n")

	resources, err := Discover(DiscoverOptions{
		HomeJuexDir: homeJuex,
		WorkDir:     work,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resources.Extensions) != 1 ||
		resources.Extensions[0].Name != "shared" ||
		resources.Extensions[0].Scope != ScopeUser {
		t.Fatalf("extensions = %+v, want one home-scoped extension", resources.Extensions)
	}
	if len(resources.HookFiles) != 1 || resources.HookFiles[0].RequireTrust {
		t.Fatalf("hook refs = %+v, want trusted-by-home-location hook", resources.HookFiles)
	}
}

func TestDiscoverDeduplicatesSymlinkedRoots(t *testing.T) {
	work := t.TempDir()
	homeJuex := filepath.Join(work, ".juex")
	writeExtensionFile(t, filepath.Join(homeJuex, "extensions", "shared", "mcp.json"), "{}")
	alias := filepath.Join(t.TempDir(), "home-juex")
	if err := os.Symlink(homeJuex, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	resources, err := Discover(DiscoverOptions{
		HomeJuexDir: alias,
		WorkDir:     work,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resources.Extensions) != 1 ||
		resources.Extensions[0].Name != "shared" ||
		resources.Extensions[0].Scope != ScopeUser {
		t.Fatalf("extensions = %+v, want one home-scoped extension", resources.Extensions)
	}
}

func TestDiscoverRejectsDuplicateExtensionNames(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeExtensionFile(t, filepath.Join(home, "extensions", "shared", "mcp.json"), "{}")
	writeExtensionFile(t, filepath.Join(work, ".juex", "extensions", "shared", "mcp.json"), "{}")

	_, err := Discover(DiscoverOptions{
		HomeJuexDir: home,
		WorkDir:     work,
	})
	if err == nil || !strings.Contains(err.Error(), `duplicate extension "shared"`) {
		t.Fatalf("err = %v, want duplicate extension error", err)
	}
}

func TestDiscoverErrorsWhenSkillsResourceIsNotDirectory(t *testing.T) {
	work := t.TempDir()
	writeExtensionFile(t, filepath.Join(work, ".juex", "extensions", "bad", "skills"), "not a directory")

	_, err := Discover(DiscoverOptions{WorkDir: work})
	if err == nil || !strings.Contains(err.Error(), "is not a directory") {
		t.Fatalf("err = %v, want invalid skills resource error", err)
	}
}

func writeExtensionFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
