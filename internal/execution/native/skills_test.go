package native

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/extensionpolicy"
)

func TestSkillSourcesFreezeOnlyEntrypoints(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"one", "two"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: |\n  多行指导\n  第二行\n---\nFrozen guidance\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name, "script.sh"), []byte("must not be read or run"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if err := inspectSkills(context.Background(), dir, &out); err != nil {
		t.Fatal(err)
	}
	var catalog extensionpolicy.Catalog
	if json.Unmarshal(out.Bytes(), &catalog) != nil || catalog.Validate() != nil || catalog.SourceKind != "skills" || len(catalog.Skills) != 2 || catalog.Manifest.Skills[0].Description != "多行指导\n第二行\n" || bytes.Contains(out.Bytes(), []byte("must not")) {
		t.Fatal(out.String())
	}
	before := catalog.Revision
	if err := os.WriteFile(filepath.Join(dir, "one", "SKILL.md"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if catalog.Validate() != nil || catalog.Revision != before {
		t.Fatal("snapshot changed with source")
	}
	catalog.Manifest.Environment = map[string]string{"SECRET": "value"}
	catalog.Revision = catalog.Digest()
	if catalog.Validate() == nil {
		t.Fatal("skills source smuggled executable environment")
	}
	if err := os.Remove(filepath.Join(dir, "two", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "two", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if err := inspectSkills(context.Background(), dir, &bytes.Buffer{}); err == nil {
		t.Fatal("escaping source accepted")
	}
}

func TestSkillSourceSkipsLinksAndRejectsAmbiguousMetadata(t *testing.T) {
	dir := t.TempDir()
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "SKILL.md"), []byte("private target guidance"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "linked")); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := inspectSkills(context.Background(), dir, &output); err != nil {
		t.Fatal(err)
	}
	var catalog extensionpolicy.Catalog
	if json.Unmarshal(output.Bytes(), &catalog) != nil || len(catalog.Skills) != 0 || len(catalog.Skipped) != 1 || catalog.Skipped[0].Target != target || bytes.Contains(output.Bytes(), []byte("private target guidance")) {
		t.Fatal(output.String())
	}
	for _, test := range []struct{ name, content string }{
		{"broken-fence", "---\nname: one\nmissing close"},
		{"malformed", "---\nname: [broken\n---\nbody"},
		{"duplicate-key", "---\nname: first\nname: second\n---\nbody"},
	} {
		t.Run(test.name, func(t *testing.T) {
			single := t.TempDir()
			if err := os.WriteFile(filepath.Join(single, "SKILL.md"), []byte(test.content), 0600); err != nil {
				t.Fatal(err)
			}
			if err := inspectSkills(context.Background(), single, &bytes.Buffer{}); err == nil {
				t.Fatal("malformed source accepted")
			}
		})
	}
	for _, name := range []string{"a", "b"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name, "SKILL.md"), []byte("---\nname: duplicate\n---\nbody"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := inspectSkills(context.Background(), dir, &bytes.Buffer{}); err == nil {
		t.Fatal("duplicate skill identities accepted")
	}
}
