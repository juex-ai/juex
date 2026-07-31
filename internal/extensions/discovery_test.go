package extensions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverLoadsNoExtensionsWithoutAllowedNames(t *testing.T) {
	root := t.TempDir()
	writeExtensionFile(t, filepath.Join(root, "demo", "mcp.json"), "{}")

	resources, err := Discover(DiscoverOptions{
		Roots: []Root{{
			Path:  root,
			Scope: ScopeUser,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resources.Extensions) != 0 ||
		len(resources.SkillDirs) != 0 ||
		len(resources.MCPConfigs) != 0 ||
		len(resources.HookFiles) != 0 ||
		len(resources.ObservableConfigs) != 0 {
		t.Fatalf("resources = %+v, want no plugin resources", resources)
	}
}

func TestDiscoverIgnoresInvalidResourcesFromUnallowedExtensions(t *testing.T) {
	root := t.TempDir()
	writeExtensionFile(t, filepath.Join(root, "allowed", "mcp.json"), "{}")
	writeExtensionFile(t, filepath.Join(root, "blocked", "skills"), "not a directory")

	resources, err := Discover(DiscoverOptions{
		Roots:        []Root{{Path: root, Scope: ScopeUser}},
		AllowedNames: []string{"allowed"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resources.Extensions) != 1 || resources.Extensions[0].Name != "allowed" {
		t.Fatalf("extensions = %+v, want only allowed", resources.Extensions)
	}
	if len(resources.MCPConfigs) != 1 || resources.MCPConfigs[0].ExtensionName != "allowed" {
		t.Fatalf("mcp configs = %+v, want only allowed", resources.MCPConfigs)
	}
}

func TestDiscoverOverlappingRootsKeepHigherPrecedenceScopeAndTrust(t *testing.T) {
	root := t.TempDir()
	middle := t.TempDir()
	writeExtensionFile(t, filepath.Join(root, "shared", "hooks.yaml"), "trusted: true\n")
	writeExtensionFile(t, filepath.Join(middle, "shared", "mcp.json"), "{}")

	resources, err := Discover(DiscoverOptions{
		Roots: []Root{
			{Path: root, Scope: ScopeUser},
			{Path: middle, Scope: ScopeUser},
			{Path: root, Scope: ScopeProject, RequireTrust: true},
		},
		AllowedNames: []string{"shared"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resources.Extensions) != 1 || resources.Extensions[0].Scope != ScopeProject {
		t.Fatalf("extensions = %+v, want higher-precedence project scope", resources.Extensions)
	}
	if len(resources.HookFiles) != 1 || !resources.HookFiles[0].RequireTrust {
		t.Fatalf("hooks = %+v, want higher-precedence trust requirement", resources.HookFiles)
	}
}

func TestDiscoverFindsUserAndProjectExtensions(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeExtensionFile(t, filepath.Join(home, "extensions", "user-ext", "mcp.json"), "{}")
	writeExtensionFile(t, filepath.Join(home, "extensions", "user-ext", "observables.json"), `{"observables":[]}`)
	writeExtensionFile(t, filepath.Join(home, "extensions", "user-ext", "skills", "alpha", "SKILL.md"), "---\nname: alpha\n---\n")
	writeExtensionFile(t, filepath.Join(work, ".juex", "extensions", "project-ext", "hooks.yaml"), "trusted: true\n")
	writeExtensionFile(t, filepath.Join(work, ".juex", "extensions", "project-ext", "observables.json"), `{"observables":[]}`)

	resources, err := Discover(DiscoverOptions{
		Roots: []Root{
			{Path: filepath.Join(home, "extensions"), Scope: ScopeUser},
			{Path: filepath.Join(work, ".juex", "extensions"), Scope: ScopeProject, RequireTrust: true},
		},
		AllowedNames: []string{"user-ext", "project-ext"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resources.Extensions) != 2 {
		t.Fatalf("extensions = %+v", resources.Extensions)
	}
	if resources.Extensions[0].Name != "project-ext" || resources.Extensions[0].Source != "ext:project-ext" || resources.Extensions[0].Scope != ScopeProject {
		t.Fatalf("project extension = %+v", resources.Extensions[0])
	}
	if resources.Extensions[1].Name != "user-ext" || resources.Extensions[1].Source != "ext:user-ext" || resources.Extensions[1].Scope != ScopeUser {
		t.Fatalf("user extension = %+v", resources.Extensions[1])
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
	if len(resources.ObservableConfigs) != 2 ||
		resources.ObservableConfigs[0].Source != "ext:project-ext" ||
		!resources.ObservableConfigs[0].RequireTrust ||
		resources.ObservableConfigs[1].Source != "ext:user-ext" {
		t.Fatalf("observable refs = %+v", resources.ObservableConfigs)
	}
}

func TestDiscoverObservableConfigRequiresRegularFile(t *testing.T) {
	work := t.TempDir()
	if err := os.MkdirAll(filepath.Join(work, "bad", "observables.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Discover(DiscoverOptions{
		Roots:        []Root{{Path: work, Scope: ScopeUser}},
		AllowedNames: []string{"bad"},
	})
	if err == nil || !strings.Contains(err.Error(), "observables.json") || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("err = %v, want regular-file diagnostic", err)
	}
}

func TestDiscoverDeduplicatesOverlappingHomeAndProjectRoots(t *testing.T) {
	work := t.TempDir()
	homeJuex := filepath.Join(work, ".juex")
	writeExtensionFile(t, filepath.Join(homeJuex, "extensions", "shared", "hooks.yaml"), "commands: {}\n")

	resources, err := Discover(DiscoverOptions{
		Roots: []Root{
			{Path: filepath.Join(homeJuex, "extensions"), Scope: ScopeUser},
			{Path: filepath.Join(work, ".juex", "extensions"), Scope: ScopeProject, RequireTrust: true},
		},
		AllowedNames: []string{"shared"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resources.Extensions) != 1 ||
		resources.Extensions[0].Name != "shared" ||
		resources.Extensions[0].Scope != ScopeProject {
		t.Fatalf("extensions = %+v, want one project-scoped extension", resources.Extensions)
	}
	if len(resources.HookFiles) != 1 || !resources.HookFiles[0].RequireTrust {
		t.Fatalf("hook refs = %+v, want project trust requirement", resources.HookFiles)
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
		Roots: []Root{
			{Path: filepath.Join(alias, "extensions"), Scope: ScopeUser},
			{Path: filepath.Join(work, ".juex", "extensions"), Scope: ScopeProject, RequireTrust: true},
		},
		AllowedNames: []string{"shared"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resources.Extensions) != 1 ||
		resources.Extensions[0].Name != "shared" ||
		resources.Extensions[0].Scope != ScopeProject {
		t.Fatalf("extensions = %+v, want one project-scoped extension", resources.Extensions)
	}
}

func TestDiscoverHigherPrecedenceExtensionReplacesLowerBundle(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeExtensionFile(t, filepath.Join(home, "shared", "skills"), "invalid lower resource")
	projectDir := filepath.Join(work, "shared")
	writeExtensionFile(t, filepath.Join(projectDir, "mcp.json"), "{}")

	resources, err := Discover(DiscoverOptions{
		Roots: []Root{
			{Path: home, Scope: ScopeUser},
			{Path: work, Scope: ScopeProject, RequireTrust: true},
		},
		AllowedNames: []string{"shared"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resources.Extensions) != 1 ||
		resources.Extensions[0].Dir != projectDir ||
		resources.Extensions[0].Scope != ScopeProject {
		t.Fatalf("extensions = %+v, want project winner %s", resources.Extensions, projectDir)
	}
	if len(resources.MCPConfigs) != 1 || resources.MCPConfigs[0].ExtensionDir != projectDir {
		t.Fatalf("mcp configs = %+v, want project winner", resources.MCPConfigs)
	}
}

func TestDiscoverErrorsWhenSkillsResourceIsNotDirectory(t *testing.T) {
	work := t.TempDir()
	writeExtensionFile(t, filepath.Join(work, "bad", "skills"), "not a directory")

	_, err := Discover(DiscoverOptions{
		Roots:        []Root{{Path: work, Scope: ScopeProject, RequireTrust: true}},
		AllowedNames: []string{"bad"},
	})
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
