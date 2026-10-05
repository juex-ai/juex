package legacy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func extensionFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFixture(t, filepath.Join(dir, "juex.extension.json"), []byte(`{"manifest_version":1,"name":"fixture","version":"1.0.0"}`))
	writeFixture(t, filepath.Join(dir, "mcp.json"), []byte(`{"mcpServers":{"fixture":{"command":"fixture","env":{"PRIVATE":"secret-fixture"}}}}`))
	return dir
}

func TestReadExtensionCapturesOnlySelectedResources(t *testing.T) {
	dir := extensionFixture(t)
	writeFixture(t, filepath.Join(dir, "script.py"), []byte("not captured"))
	before := fixtureHashes(t, dir)
	got, err := ReadExtension(dir, ExtensionResources{MCP: true, Hooks: true, Observables: true, Skills: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.Directory != dir || len(got.Files) != 2 || !reflect.DeepEqual(got.AbsentFiles, []string{"hooks.yaml", "observables.json", "skills"}) {
		t.Fatal(got)
	}
	if !reflect.DeepEqual(before, fixtureHashes(t, dir)) {
		t.Fatal("source modified")
	}
	b, err := json.Marshal(got)
	if err != nil || strings.Contains(string(b), "secret-fixture") {
		t.Fatal("private bytes in report", err)
	}
}

func TestReadExtensionDoesNotProbeDisabledCategories(t *testing.T) {
	dir := extensionFixture(t)
	if err := os.Remove(filepath.Join(dir, "mcp.json")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"mcp.json", "hooks.yaml", "observables.json", "skills"} {
		if err := os.Symlink(name, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := ReadExtension(dir, ExtensionResources{})
	if err != nil || len(got.Files) != 1 || len(got.AbsentFiles) != 0 {
		t.Fatal(got, err)
	}
}

func TestReadExtensionCapturesSkillEntrypointsOnly(t *testing.T) {
	dir := extensionFixture(t)
	skill := filepath.Join(dir, "skills", "guide")
	if err := os.MkdirAll(skill, 0700); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(skill, "SKILL.md"), []byte("guide content"))
	writeFixture(t, filepath.Join(skill, "helper.py"), []byte("not captured"))
	got, err := ReadExtension(dir, ExtensionResources{Skills: true})
	if err != nil || len(got.Files) != 2 || got.Files[1].Path != "skills/guide/SKILL.md" {
		t.Fatal(got, err)
	}
}

func TestReadExtensionRejectsUnsafeSelectedPaths(t *testing.T) {
	for _, kind := range []string{"missing manifest", "case manifest", "manifest directory", "manifest symlink", "root symlink", "resource symlink", "resource case", "oversized", "skill symlink"} {
		t.Run(kind, func(t *testing.T) {
			dir := extensionFixture(t)
			switch kind {
			case "missing manifest", "case manifest", "manifest directory", "manifest symlink":
				p := filepath.Join(dir, "juex.extension.json")
				if err := os.Remove(p); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "case manifest":
					writeFixture(t, filepath.Join(dir, "Juex.extension.json"), []byte(`{}`))
				case "manifest directory":
					if err := os.Mkdir(p, 0700); err != nil {
						t.Fatal(err)
					}
				case "manifest symlink":
					if err := os.Symlink("mcp.json", p); err != nil {
						t.Fatal(err)
					}
				}
			case "root symlink":
				link := filepath.Join(t.TempDir(), "extension")
				if err := os.Symlink(dir, link); err != nil {
					t.Fatal(err)
				}
				dir = link
			case "resource symlink":
				p := filepath.Join(dir, "mcp.json")
				if err := os.Remove(p); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("juex.extension.json", p); err != nil {
					t.Fatal(err)
				}
			case "resource case":
				if err := os.Rename(filepath.Join(dir, "mcp.json"), filepath.Join(dir, "MCP.json")); err != nil {
					t.Fatal(err)
				}
			case "oversized":
				if err := os.Truncate(filepath.Join(dir, "mcp.json"), maxSourceFileBytes+1); err != nil {
					t.Fatal(err)
				}
			case "skill symlink":
				skills := filepath.Join(dir, "skills")
				if err := os.Mkdir(skills, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), filepath.Join(skills, "guide")); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := ReadExtension(dir, ExtensionResources{MCP: true, Skills: true}); err == nil {
				t.Fatal("unsafe selected resource accepted")
			}
		})
	}
}

func TestExtensionCaptureRejectsChanges(t *testing.T) {
	for _, kind := range []string{"content", "late resource", "late skill", "root replacement"} {
		t.Run(kind, func(t *testing.T) {
			dir := extensionFixture(t)
			_, reader, err := readExtension(dir, ExtensionResources{MCP: true, Hooks: true, Skills: true})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reader.root.Close() })
			switch kind {
			case "content":
				writeFixture(t, filepath.Join(dir, "mcp.json"), []byte("different content"))
			case "late resource":
				writeFixture(t, filepath.Join(dir, "hooks.yaml"), []byte("created"))
			case "late skill":
				if err := os.Mkdir(filepath.Join(dir, "skills"), 0700); err != nil {
					t.Fatal(err)
				}
			case "root replacement":
				if err := os.Rename(dir, dir+"-old"); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.RemoveAll(dir + "-old") })
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := extensionUnchanged(dir, reader); err == nil {
				t.Fatal("capture mutation accepted")
			}
		})
	}
}

func TestReadExtensionPreservesNonSkillResourceDirectories(t *testing.T) {
	dir := extensionFixture(t)
	for _, name := range []string{"guide", "shared"} {
		if err := os.MkdirAll(filepath.Join(dir, "skills", name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	writeFixture(t, filepath.Join(dir, "skills/guide/SKILL.md"), []byte("valid guide"))
	writeFixture(t, filepath.Join(dir, "skills/shared/helper.py"), []byte("auxiliary resource"))
	got, reader, err := readExtension(dir, ExtensionResources{Skills: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.root.Close() })
	if len(got.Files) != 2 || !reflect.DeepEqual(got.AbsentFiles, []string{"skills/shared/SKILL.md"}) {
		t.Fatal(got)
	}
	if err := extensionUnchanged(dir, reader); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(dir, "skills/shared/SKILL.md"), []byte("late skill"))
	if err := extensionUnchanged(dir, reader); err == nil {
		t.Fatal("late skill entrypoint accepted")
	}
}
