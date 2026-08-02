package extensions

import (
	"fmt"
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
			Scope: ScopeDefaultHome,
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
		t.Fatalf("resources = %+v, want no extension resources", resources)
	}
}

func TestDiscoverLoadsSelectedExtensionManifest(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "demo")
	writeExtensionFile(t, filepath.Join(dir, manifestFilename), `{
  "manifest_version": 1,
  "name": "demo",
  "version": "1.2.3-beta.1+build.7",
  "description": "Demo integration",
  "display_name": "Demo",
  "author": "JueX",
  "homepage": "https://example.com/demo",
  "repository": "https://example.com/demo.git",
  "license": "MIT"
}`)

	resources, err := Discover(DiscoverOptions{
		Roots:        []Root{{Path: root, Scope: ScopeInstanceHome}},
		AllowedNames: []string{"demo"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resources.Extensions) != 1 {
		t.Fatalf("extensions = %+v", resources.Extensions)
	}
	ext := resources.Extensions[0]
	if ext.Scope != ScopeInstanceHome || ext.Manifest.Name != "demo" || ext.Manifest.Version != "1.2.3-beta.1+build.7" || ext.Manifest.Description != "Demo integration" || ext.Manifest.DisplayName != "Demo" || ext.Manifest.Author != "JueX" || ext.Manifest.Homepage != "https://example.com/demo" || ext.Manifest.Repository != "https://example.com/demo.git" || ext.Manifest.License != "MIT" {
		t.Fatalf("extension = %+v", ext)
	}
}

func TestDiscoverValidatesOnlySelectedWinningManifest(t *testing.T) {
	lower := t.TempDir()
	higher := t.TempDir()
	writeRawExtensionFile(t, filepath.Join(lower, "shared", manifestFilename), "not json")
	writeExtensionFile(t, filepath.Join(higher, "shared", "mcp.json"), "{}")
	writeRawExtensionFile(t, filepath.Join(higher, "blocked", manifestFilename), "not json")

	resources, err := Discover(DiscoverOptions{
		Roots: []Root{
			{Path: lower, Scope: ScopeDefaultHome},
			{Path: higher, Scope: ScopeProject, RequireTrust: true},
		},
		AllowedNames: []string{"shared"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resources.Extensions) != 1 || resources.Extensions[0].Dir != filepath.Join(higher, "shared") {
		t.Fatalf("extensions = %+v", resources.Extensions)
	}
}

func TestDiscoverInvalidWinningManifestNeverFallsBack(t *testing.T) {
	lower := t.TempDir()
	higher := t.TempDir()
	writeExtensionFile(t, filepath.Join(lower, "shared", "mcp.json"), "{}")
	writeRawExtensionFile(t, filepath.Join(higher, "shared", manifestFilename), "not json")

	_, err := Discover(DiscoverOptions{
		Roots: []Root{
			{Path: lower, Scope: ScopeDefaultHome},
			{Path: higher, Scope: ScopeProject, RequireTrust: true},
		},
		AllowedNames: []string{"shared"},
	})
	if err == nil || !strings.Contains(err.Error(), "shared") || !strings.Contains(err.Error(), filepath.Join(higher, "shared", manifestFilename)) {
		t.Fatalf("err = %v, want winning manifest diagnostic", err)
	}
}

func TestDiscoverRejectsInvalidSelectedManifests(t *testing.T) {
	tests := []struct {
		name     string
		dirName  string
		manifest string
		want     string
	}{
		{name: "missing", dirName: "demo", want: manifestFilename},
		{name: "malformed json", dirName: "demo", manifest: `{`, want: "parse"},
		{name: "unsupported manifest version", dirName: "demo", manifest: `{"manifest_version":2,"name":"demo","version":"1.0.0"}`, want: "manifest_version"},
		{name: "directory mismatch", dirName: "demo", manifest: `{"manifest_version":1,"name":"other","version":"1.0.0"}`, want: "directory"},
		{name: "missing version", dirName: "demo", manifest: `{"manifest_version":1,"name":"demo"}`, want: "version"},
		{name: "invalid semver", dirName: "demo", manifest: `{"manifest_version":1,"name":"demo","version":"01.2.3"}`, want: "SemVer"},
		{name: "null metadata", dirName: "demo", manifest: `{"manifest_version":1,"name":"demo","version":"1.0.0","description":null}`, want: "description"},
		{name: "duplicate key", dirName: "demo", manifest: `{"manifest_version":1,"name":"demo","name":"other","version":"1.0.0"}`, want: "duplicate"},
		{name: "unknown field", dirName: "demo", manifest: `{"manifest_version":1,"name":"demo","version":"1.0.0","descripton":"typo"}`, want: "unknown field"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, tt.dirName)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if tt.manifest != "" {
				writeRawExtensionFile(t, filepath.Join(dir, manifestFilename), tt.manifest)
			}
			_, err := Discover(DiscoverOptions{
				Roots:        []Root{{Path: root, Scope: ScopeDefaultHome}},
				AllowedNames: []string{tt.dirName},
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), tt.dirName) || !strings.Contains(err.Error(), filepath.Join(dir, manifestFilename)) {
				t.Fatalf("err = %v, want %q with extension and path", err, tt.want)
			}
		})
	}
}

func TestDiscoverRequiresExactManifestFilenameCase(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "demo")
	writeRawExtensionFile(t, filepath.Join(dir, "JUEX.EXTENSION.JSON"), `{"manifest_version":1,"name":"demo","version":"1.0.0"}`)

	_, err := Discover(DiscoverOptions{
		Roots:        []Root{{Path: root, Scope: ScopeDefaultHome}},
		AllowedNames: []string{"demo"},
	})
	if err == nil || !strings.Contains(err.Error(), manifestFilename) {
		t.Fatalf("err = %v, want exact-case manifest filename diagnostic", err)
	}
}

func TestSemVerValidation(t *testing.T) {
	valid := []string{
		"0.0.0",
		"1.2.3",
		"1.0.0-alpha",
		"1.0.0-alpha.1",
		"1.0.0-0.3.7",
		"1.0.0-x.7.z.92",
		"1.0.0+20130313144700",
		"1.0.0-beta+exp.sha.5114f85",
	}
	for _, version := range valid {
		if !semVerPattern.MatchString(version) {
			t.Errorf("valid SemVer rejected: %q", version)
		}
	}
	invalid := []string{"", "1", "1.2", "01.2.3", "1.02.3", "1.2.03", "1.0.0-01", "v1.2.3", "1.2.3-", "1.2.3+"}
	for _, version := range invalid {
		if semVerPattern.MatchString(version) {
			t.Errorf("invalid SemVer accepted: %q", version)
		}
	}
}

func TestDiscoverIgnoresInvalidResourcesFromUnallowedExtensions(t *testing.T) {
	root := t.TempDir()
	writeExtensionFile(t, filepath.Join(root, "allowed", "mcp.json"), "{}")
	writeExtensionFile(t, filepath.Join(root, "blocked", "skills"), "not a directory")

	resources, err := Discover(DiscoverOptions{
		Roots:        []Root{{Path: root, Scope: ScopeDefaultHome}},
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
			{Path: root, Scope: ScopeDefaultHome},
			{Path: middle, Scope: ScopeInstanceHome},
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
			{Path: filepath.Join(home, "extensions"), Scope: ScopeInstanceHome},
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
	if resources.Extensions[1].Name != "user-ext" || resources.Extensions[1].Source != "ext:user-ext" || resources.Extensions[1].Scope != ScopeInstanceHome {
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
	writeRawExtensionFile(t, filepath.Join(work, "bad", manifestFilename), `{"manifest_version":1,"name":"bad","version":"1.0.0"}`)
	_, err := Discover(DiscoverOptions{
		Roots:        []Root{{Path: work, Scope: ScopeInstanceHome}},
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
			{Path: filepath.Join(homeJuex, "extensions"), Scope: ScopeInstanceHome},
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
			{Path: filepath.Join(alias, "extensions"), Scope: ScopeInstanceHome},
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
			{Path: home, Scope: ScopeDefaultHome},
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
	if filepath.Base(path) != manifestFilename {
		dir := extensionFixtureDir(path)
		writeRawExtensionFile(t, filepath.Join(dir, manifestFilename), fmt.Sprintf(`{"manifest_version":1,"name":%q,"version":"1.0.0"}`, filepath.Base(dir)))
	}
	writeRawExtensionFile(t, path, body)
}

func writeRawExtensionFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func extensionFixtureDir(path string) string {
	dir := filepath.Dir(path)
	for filepath.Base(dir) == "skills" || filepath.Base(filepath.Dir(dir)) == "skills" {
		dir = filepath.Dir(dir)
	}
	return dir
}
