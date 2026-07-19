package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
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

func filesystemSkills(loader *Loader) []Skill {
	var out []Skill
	for _, skill := range loader.All() {
		if skill.Source != SourceBuiltin {
			out = append(out, skill)
		}
	}
	return out
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
	all := filesystemSkills(l)
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

func TestLoader_PromptSectionUsesSkillTools(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "mySkill", "---\nname: mySkill\ndescription: do x\n---\nbody here")
	l := NewLoader(dir)
	if err := l.Load(); err != nil {
		t.Fatal(err)
	}

	section := l.PromptSection()
	if !strings.Contains(section, "do x") {
		t.Fatalf("prompt section missing description in:\n%s", section)
	}
	for _, want := range []string{"`skill_load`", "`skill_search`"} {
		if !strings.Contains(section, want) {
			t.Fatalf("prompt should tell the model to use %s; got:\n%s", want, section)
		}
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
	if all := filesystemSkills(l); len(all) != 1 || all[0].Name != "real" {
		t.Fatalf("got %+v", all)
	}
}

func TestLoader_SkipsEmptyDirRef(t *testing.T) {
	l := NewLoaderFromDirs([]Dir{{Path: "", Source: "empty"}})
	if err := l.Load(); err != nil {
		t.Fatal(err)
	}
	if all := filesystemSkills(l); len(all) != 0 {
		t.Fatalf("loaded filesystem skills from empty dir ref: %+v", all)
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
	all := filesystemSkills(l)
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
	if len(filesystemSkills(l)) != 0 {
		t.Fatal("expected no filesystem skills")
	}
}

func TestLoader_AppliesIncludeAfterScopeMerge(t *testing.T) {
	user := t.TempDir()
	project := t.TempDir()
	writeSkill(t, user, "alpha", "---\nname: alpha\ndescription: user\n---\nUSER")
	writeSkill(t, project, "alpha", "---\nname: alpha\ndescription: project\n---\nPROJECT")
	writeSkill(t, project, "beta", "---\nname: beta\ndescription: project\n---\nBETA")

	l := NewLoaderFromDirsWithOptions([]Dir{{Path: user, Source: "user"}, {Path: project, Source: "project"}}, LoaderOptions{
		Policy: Policy{Include: []string{"alpha"}},
	})
	if err := l.Load(); err != nil {
		t.Fatal(err)
	}
	all := filesystemSkills(l)
	if len(all) != 1 || all[0].Name != "alpha" || all[0].Source != "project" || all[0].Body != "PROJECT" {
		t.Fatalf("loaded skills = %+v", all)
	}
	filtered := l.Filtered()
	if len(filtered) != 1 || filtered[0].Name != "beta" || filtered[0].Reason != "not included" {
		t.Fatalf("filtered = %+v", filtered)
	}
}

func TestLoader_ExcludeRemovesSkillFromCatalog(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "alpha", "---\nname: alpha\ndescription: a\n---\nA")
	writeSkill(t, dir, "beta", "---\nname: beta\ndescription: b\n---\nB")

	l := NewLoaderFromDirsWithOptions([]Dir{{Path: dir, Source: "project"}}, LoaderOptions{
		Policy: Policy{Exclude: []string{"beta"}},
	})
	if err := l.Load(); err != nil {
		t.Fatal(err)
	}
	if _, ok := l.Get("beta"); ok {
		t.Fatal("excluded skill should not load")
	}
	if results := l.Search("beta", 10); len(results) != 0 {
		t.Fatalf("excluded skill should not be searchable: %+v", results)
	}
}

func TestLoader_PromptBudgetCompactsAndOmitsLowerPrioritySkills(t *testing.T) {
	user := t.TempDir()
	project := t.TempDir()
	writeSkill(t, user, "user-skill", "---\nname: user-skill\ndescription: "+strings.Repeat("u", 300)+"\n---\nUSER")
	writeSkill(t, project, "project-skill", "---\nname: project-skill\ndescription: "+strings.Repeat("p", 300)+"\n---\nPROJECT")

	l := NewLoaderFromDirsWithOptions([]Dir{{Path: user, Source: "user"}, {Path: project, Source: "project"}}, LoaderOptions{
		Policy: Policy{PromptBudgetChars: 260},
	})
	if err := l.Load(); err != nil {
		t.Fatal(err)
	}
	section := l.PromptSection()
	report := l.PromptReport()
	if len(section) > 260 {
		t.Fatalf("section length = %d, want <= budget; section:\n%s", len(section), section)
	}
	if !report.Compacted || len(report.Omitted) == 0 {
		t.Fatalf("report = %+v, want compacted omission", report)
	}
	if !strings.Contains(section, "project-skill") {
		t.Fatalf("project skill should be kept before user skill:\n%s", section)
	}
	if !strings.Contains(section, "skill_search") {
		t.Fatalf("prompt should mention search for omitted skills:\n%s", section)
	}
}

func TestLoader_PromptBudgetUsesMinimalHeaderWhenBudgetIsTiny(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "alpha", "---\nname: alpha\ndescription: "+strings.Repeat("a", 120)+"\n---\nA")
	writeSkill(t, dir, "beta", "---\nname: beta\ndescription: "+strings.Repeat("b", 120)+"\n---\nB")

	l := NewLoaderFromDirsWithOptions([]Dir{{Path: dir, Source: "project"}}, LoaderOptions{
		Policy: Policy{PromptBudgetChars: 80},
	})
	if err := l.Load(); err != nil {
		t.Fatal(err)
	}
	section := l.PromptSection()
	report := l.PromptReport()
	if len(section) > 80 {
		t.Fatalf("section length = %d, want <= budget; section:\n%s", len(section), section)
	}
	if report.UsedChars > report.BudgetChars {
		t.Fatalf("report = %+v, want used <= budget", report)
	}
	if !report.Compacted || len(report.Omitted) == 0 {
		t.Fatalf("report = %+v, want compacted omissions", report)
	}
	for _, want := range []string{"skill_search", "skill_load"} {
		if !strings.Contains(section, want) {
			t.Fatalf("section should keep %s hint under tiny budget; got:\n%s", want, section)
		}
	}
}

func TestLoader_SearchFindsLoadedSkills(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "alpha", "---\nname: alpha\ndescription: read screenshots\n---\nA")
	writeSkill(t, dir, "beta", "---\nname: beta\ndescription: database migrations\n---\nB")

	l := NewLoader(dir)
	if err := l.Load(); err != nil {
		t.Fatal(err)
	}
	results := l.Search("screen", 10)
	if len(results) != 1 || results[0].Name != "alpha" {
		t.Fatalf("results = %+v", results)
	}
}

func TestLoader_LoadsBuiltinGuidesOutsidePromptCatalog(t *testing.T) {
	l := NewLoader(t.TempDir())
	if err := l.Load(); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"juex-chunked-write", "juex-observables", "juex-session-state"} {
		skill, ok := l.Get(name)
		if !ok {
			t.Fatalf("builtin skill %q missing from %+v", name, l.All())
		}
		if skill.Source != "builtin" {
			t.Fatalf("%s source = %q, want builtin", name, skill.Source)
		}
		wantPath := "builtin://skills/" + name + "/SKILL.md"
		if skill.Path != wantPath {
			t.Fatalf("%s path = %q, want %q", name, skill.Path, wantPath)
		}
		if !strings.Contains(skill.raw, "name: "+name) || !strings.Contains(skill.Body, "# ") {
			t.Fatalf("%s embedded content incomplete: %+v", name, skill)
		}
		lowerRaw := strings.ToLower(skill.raw)
		for _, forbidden := range []string{"required guide", "load this guide before"} {
			if strings.Contains(lowerRaw, forbidden) {
				t.Fatalf("%s embedded guide retains mandatory loading text %q", name, forbidden)
			}
		}
		for _, want := range []string{"load this guide when", "do not require a prior guide load"} {
			if !strings.Contains(lowerRaw, want) {
				t.Fatalf("%s embedded guide missing advisory loading text %q", name, want)
			}
		}
		if results := l.Search(name, 10); len(results) != 1 || results[0].Name != name {
			t.Fatalf("search %s = %+v", name, results)
		}
	}

	if section := l.PromptSection(); section != "" {
		t.Fatalf("builtin guides must not enter prompt catalog:\n%s", section)
	}
	if report := l.PromptReport(); report.UsedChars != 0 || len(report.Omitted) != 0 {
		t.Fatalf("builtin guides must not enter prompt report: %+v", report)
	}
}

func TestLoader_BuiltinGuidesIgnoreFilesystemSkillFilters(t *testing.T) {
	for _, policy := range []Policy{
		{Include: []string{"not-a-builtin"}},
		{Exclude: []string{"juex-observables", "juex-session-state", "juex-chunked-write"}},
	} {
		l := NewLoaderFromDirsWithOptions(nil, LoaderOptions{Policy: policy})
		if err := l.Load(); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"juex-observables", "juex-session-state", "juex-chunked-write"} {
			if _, ok := l.Get(name); !ok {
				t.Fatalf("policy %+v hid required builtin %q", policy, name)
			}
		}
	}
}

func TestLoader_BuiltinGuideNamesAreReserved(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "override", "---\nname: juex-observables\ndescription: override\n---\nbody")

	l := NewLoaderFromDirs([]Dir{{Path: dir, Source: "project"}})
	err := l.Load()
	if err == nil || !strings.Contains(err.Error(), `duplicate skill "juex-observables" from builtin and project`) {
		t.Fatalf("err = %v, want reserved builtin duplicate error", err)
	}
}

func TestLoader_ReloadRetainsBuiltinGuides(t *testing.T) {
	l := NewLoader(t.TempDir())
	for i := 0; i < 2; i++ {
		if err := l.Load(); err != nil {
			t.Fatal(err)
		}
		if skill, ok := l.Get("juex-observables"); !ok || skill.Source != "builtin" || skill.raw == "" {
			t.Fatalf("reload %d builtin = %+v, %v", i, skill, ok)
		}
	}
}

func TestBuiltinCatalogFailsLoudOnInvalidEmbeddedGuide(t *testing.T) {
	valid := func() fstest.MapFS {
		files := fstest.MapFS{}
		for _, name := range builtinSkillNames {
			files["builtin/"+name+"/SKILL.md"] = &fstest.MapFile{Data: []byte("---\nname: " + name + "\ndescription: guide\ntype: builtin-guide\n---\n# Guide\n")}
		}
		return files
	}

	t.Run("malformed frontmatter", func(t *testing.T) {
		files := valid()
		files["builtin/juex-observables/SKILL.md"] = &fstest.MapFile{Data: []byte("---\nname: broken")}
		if _, err := loadBuiltinSkillsFromFS(files); err == nil || !strings.Contains(err.Error(), "parse builtin") {
			t.Fatalf("err = %v, want parse failure", err)
		}
	})

	t.Run("name mismatch", func(t *testing.T) {
		files := valid()
		files["builtin/juex-observables/SKILL.md"] = &fstest.MapFile{Data: []byte("---\nname: other\ndescription: guide\ntype: builtin-guide\n---\n# Guide\n")}
		if _, err := loadBuiltinSkillsFromFS(files); err == nil || !strings.Contains(err.Error(), `declares name "other"`) {
			t.Fatalf("err = %v, want name mismatch", err)
		}
	})

	t.Run("missing expected guide", func(t *testing.T) {
		files := valid()
		delete(files, "builtin/juex-session-state/SKILL.md")
		if _, err := loadBuiltinSkillsFromFS(files); err == nil || !strings.Contains(err.Error(), "has 2 guides, want 3") {
			t.Fatalf("err = %v, want catalog count failure", err)
		}
	})
}

func TestBuiltinCatalogDoesNotDependOnExpectedNameOrder(t *testing.T) {
	files := fstest.MapFS{}
	for _, name := range builtinSkillNames {
		files["builtin/"+name+"/SKILL.md"] = &fstest.MapFile{Data: []byte("---\nname: " + name + "\ndescription: guide\ntype: builtin-guide\n---\n# Guide\n")}
	}
	original := builtinSkillNames
	builtinSkillNames = []string{"juex-session-state", "juex-chunked-write", "juex-observables"}
	t.Cleanup(func() { builtinSkillNames = original })

	loaded, err := loadBuiltinSkillsFromFS(files)
	if err != nil {
		t.Fatal(err)
	}
	for i, skill := range loaded {
		if skill.Name != builtinSkillNames[i] {
			t.Fatalf("loaded[%d].Name = %q, want %q", i, skill.Name, builtinSkillNames[i])
		}
	}
}
