package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkill(t *testing.T, root, name, body string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoader_LoadsBothScopes(t *testing.T) {
	user := t.TempDir()
	project := t.TempDir()
	writeSkill(t, user, "alpha", "---\nname: alpha\ndescription: user version\n---\nUSER BODY")
	writeSkill(t, project, "beta", "---\nname: beta\ndescription: project only\n---\nPROJECT BODY")

	l := NewLoader(user, project)
	if err := l.Load(); err != nil {
		t.Fatal(err)
	}
	all := l.All()
	if len(all) != 2 {
		t.Fatalf("got %d", len(all))
	}
	names := []string{all[0].Name, all[1].Name}
	if names[0] != "alpha" || names[1] != "beta" {
		t.Fatalf("names = %v", names)
	}
	if all[0].Source != "user" || all[1].Source != "project" {
		t.Fatalf("sources = %s, %s", all[0].Source, all[1].Source)
	}
}

func TestLoader_LoadsSymlinkedSkillDirectory(t *testing.T) {
	root := t.TempDir()
	targetRoot := t.TempDir()
	writeSkill(t, targetRoot, "taskline-management", "---\nname: taskline-management\ndescription: task queue\n---\nbody")
	if err := os.Symlink(filepath.Join(targetRoot, "taskline-management"), filepath.Join(root, "taskline-management")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	l := NewLoader(root)
	if err := l.Load(); err != nil {
		t.Fatal(err)
	}
	s, ok := l.Get("taskline-management")
	if !ok {
		t.Fatalf("loaded skills: %+v", l.All())
	}
	if s.Description != "task queue" {
		t.Fatalf("description = %q", s.Description)
	}
}

func TestLoader_ProjectOverridesUser(t *testing.T) {
	user := t.TempDir()
	project := t.TempDir()
	writeSkill(t, user, "alpha", "---\nname: alpha\ndescription: user\n---\nUSER")
	writeSkill(t, project, "alpha", "---\nname: alpha\ndescription: project\n---\nPROJECT")

	l := NewLoader(user, project)
	if err := l.Load(); err != nil {
		t.Fatal(err)
	}
	s, _ := l.Get("alpha")
	if s.Description != "project" || s.Source != "project" || s.Body != "PROJECT" {
		t.Fatalf("override failed: %+v", s)
	}
}

func TestLoader_ExplicitSourceDirsLoadExtensionSource(t *testing.T) {
	ext := t.TempDir()
	writeSkill(t, ext, "alpha", "---\nname: alpha\ndescription: extension\n---\nEXT")

	l := NewLoaderFromDirs([]Dir{{Path: ext, Source: "ext:demo", StrictConflicts: true}})
	if err := l.Load(); err != nil {
		t.Fatal(err)
	}
	s, ok := l.Get("alpha")
	if !ok {
		t.Fatalf("loaded skills: %+v", l.All())
	}
	if s.Source != "ext:demo" || s.Description != "extension" {
		t.Fatalf("skill = %+v", s)
	}
}

func TestLoader_StrictSourceRejectsDuplicateSkill(t *testing.T) {
	project := t.TempDir()
	ext := t.TempDir()
	writeSkill(t, project, "alpha", "---\nname: alpha\ndescription: project\n---\nPROJECT")
	writeSkill(t, ext, "alpha", "---\nname: alpha\ndescription: extension\n---\nEXT")

	l := NewLoaderFromDirs([]Dir{
		{Path: project, Source: "project"},
		{Path: ext, Source: "ext:demo", StrictConflicts: true},
	})
	err := l.Load()
	if err == nil || !strings.Contains(err.Error(), `duplicate skill "alpha"`) {
		t.Fatalf("err = %v, want duplicate skill error", err)
	}
}

func TestLoader_PromptSection(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "x", "---\nname: x\ndescription: do x\n---\n")
	l := NewLoader(dir)
	if err := l.Load(); err != nil {
		t.Fatal(err)
	}
	got := l.PromptSection()
	if !strings.Contains(got, "Available Skills") || !strings.Contains(got, "do x") {
		t.Fatalf("section = %q", got)
	}
}

func TestLoader_PromptSectionEmpty(t *testing.T) {
	l := NewLoader(t.TempDir())
	if err := l.Load(); err != nil {
		t.Fatal(err)
	}
	if l.PromptSection() != "" {
		t.Fatalf("expected empty section")
	}
}

func TestLoader_PromptSectionExposesAbsolutePath(t *testing.T) {
	// The prompt section must give the model the absolute path so it can
	// load the body with the standard `read` builtin (no dedicated tool).
	dir := t.TempDir()
	writeSkill(t, dir, "mySkill", "---\nname: mySkill\ndescription: do x\n---\nbody here")
	l := NewLoader(dir)
	if err := l.Load(); err != nil {
		t.Fatal(err)
	}

	section := l.PromptSection()
	want := filepath.Join(dir, "mySkill", "SKILL.md")
	if !strings.Contains(section, want) {
		t.Fatalf("prompt section missing absolute path %q in:\n%s", want, section)
	}
	if !strings.Contains(section, "do x") {
		t.Fatalf("prompt section missing description in:\n%s", section)
	}
	// Instruction line must point the model at the `read` builtin.
	if !strings.Contains(section, "`read`") {
		t.Fatalf("prompt should tell the model to use `read` against the path; got:\n%s", section)
	}
}

func TestLoader_NameFallsBackToDirName(t *testing.T) {
	// SKILL.md without a name field falls back to directory name.
	dir := t.TempDir()
	writeSkill(t, dir, "implicit-name", "---\ndescription: no name field\n---\nbody")
	l := NewLoader(dir)
	if err := l.Load(); err != nil {
		t.Fatal(err)
	}
	s, ok := l.Get("implicit-name")
	if !ok {
		t.Fatalf("loaded skills: %+v", l.All())
	}
	if s.Description != "no name field" {
		t.Fatalf("desc = %q", s.Description)
	}
}

func TestLoader_MalformedFrontmatterSilentlySkipped(t *testing.T) {
	// Bad SKILL.md (missing closing ---) should not abort the loader; other
	// skills must still load.
	dir := t.TempDir()
	writeSkill(t, dir, "bad", "---\nname: bad\ndescription: no closing fence\nthis never closes")
	writeSkill(t, dir, "good", "---\nname: good\ndescription: ok\n---\nbody")

	l := NewLoader(dir)
	if err := l.Load(); err != nil {
		t.Fatal(err)
	}
	if _, ok := l.Get("good"); !ok {
		t.Fatalf("good skill should have loaded; got %+v", l.All())
	}
	if _, ok := l.Get("bad"); ok {
		t.Fatalf("bad skill should be skipped, got it loaded")
	}
}

func TestLoader_DirectoryWithoutSkillMD(t *testing.T) {
	// A subdir missing SKILL.md must not crash; it's just ignored.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "lonely"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeSkill(t, dir, "real", "---\nname: real\ndescription: r\n---\nbody")

	l := NewLoader(dir)
	if err := l.Load(); err != nil {
		t.Fatal(err)
	}
	if len(l.All()) != 1 || l.All()[0].Name != "real" {
		t.Fatalf("got %+v", l.All())
	}
}

func TestLoader_AllSortedByName(t *testing.T) {
	// All() must return entries sorted by name for deterministic prompt assembly.
	dir := t.TempDir()
	for _, n := range []string{"zebra", "alpha", "mike", "bravo"} {
		writeSkill(t, dir, n, "---\nname: "+n+"\ndescription: "+n+"\n---\nbody")
	}
	l := NewLoader(dir)
	if err := l.Load(); err != nil {
		t.Fatal(err)
	}
	all := l.All()
	if len(all) != 4 {
		t.Fatalf("len = %d", len(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i-1].Name >= all[i].Name {
			t.Fatalf("not sorted: %v", []string{all[0].Name, all[1].Name, all[2].Name, all[3].Name})
		}
	}
}

func TestLoader_PromptSectionListsAllSkills(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "one", "---\nname: one\ndescription: ONE_DESC\n---\n")
	writeSkill(t, dir, "two", "---\nname: two\ndescription: TWO_DESC\n---\n")
	writeSkill(t, dir, "three", "---\nname: three\ndescription: THREE_DESC\n---\n")
	l := NewLoader(dir)
	if err := l.Load(); err != nil {
		t.Fatal(err)
	}
	s := l.PromptSection()
	for _, want := range []string{"ONE_DESC", "TWO_DESC", "THREE_DESC", "one", "two", "three"} {
		if !strings.Contains(s, want) {
			t.Errorf("section missing %q in:\n%s", want, s)
		}
	}
}

func TestLoader_ReloadDoesNotLeakStaleSkills(t *testing.T) {
	// Calling Load() twice must reflect the current filesystem state.
	dir := t.TempDir()
	writeSkill(t, dir, "first", "---\nname: first\ndescription: d\n---\nbody")
	l := NewLoader(dir)
	if err := l.Load(); err != nil {
		t.Fatal(err)
	}
	if _, ok := l.Get("first"); !ok {
		t.Fatal("first not loaded")
	}
	if err := os.RemoveAll(filepath.Join(dir, "first")); err != nil {
		t.Fatal(err)
	}
	writeSkill(t, dir, "second", "---\nname: second\ndescription: d\n---\nbody")
	if err := l.Load(); err != nil {
		t.Fatal(err)
	}
	if _, ok := l.Get("first"); ok {
		t.Fatal("first should have been dropped on reload")
	}
	if _, ok := l.Get("second"); !ok {
		t.Fatal("second not loaded")
	}
}

func TestLoader_LoadMissingDirIsOK(t *testing.T) {
	// Pointing the loader at a directory that does not exist must not error.
	l := NewLoader("/definitely/does/not/exist/__juex_nope__")
	if err := l.Load(); err != nil {
		t.Fatalf("missing dir should be ok, got %v", err)
	}
	if len(l.All()) != 0 {
		t.Fatal("expected no skills")
	}
}
