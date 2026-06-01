package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/tools"
)

func TestStore_WriteLoadDelete(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Write(Entry{
		Name:        "no-emoji",
		Description: "Don't use emoji in code",
		Type:        "feedback",
		Body:        "Reason: user said so on 2026-04-15.",
	}); err != nil {
		t.Fatal(err)
	}

	all, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Name != "no-emoji" {
		t.Fatalf("got %+v", all)
	}
	if all[0].CreatedAt.IsZero() || all[0].UpdatedAt.IsZero() {
		t.Fatal("timestamps not set")
	}

	indexPath := filepath.Join(s.dir, indexFile)
	indexData, _ := os.ReadFile(indexPath)
	if !strings.Contains(string(indexData), "no-emoji") {
		t.Fatalf("index missing entry: %s", indexData)
	}

	if err := s.Delete("no-emoji"); err != nil {
		t.Fatal(err)
	}
	all, _ = s.Load()
	if len(all) != 0 {
		t.Fatalf("after delete: %+v", all)
	}
}

func TestStore_Search(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Write(Entry{Name: "a", Description: "apple seed", Type: "user", Body: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(Entry{Name: "b", Description: "banana", Type: "feedback", Body: "apple in body"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(Entry{Name: "c", Description: "carrot", Type: "user", Body: "z"}); err != nil {
		t.Fatal(err)
	}

	hits, _ := s.Search("apple")
	if len(hits) != 2 {
		t.Fatalf("got %d hits: %+v", len(hits), hits)
	}
	hits, _ = s.Search("")
	if len(hits) != 3 {
		t.Fatalf("empty search: %d", len(hits))
	}
}

func TestStore_RejectsBadType(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Write(Entry{Name: "x", Description: "", Type: "bogus", Body: ""}); err == nil {
		t.Fatal("expected type validation error")
	}
}

func TestStore_PromptSection(t *testing.T) {
	s := NewStore(t.TempDir())
	if section, _ := s.PromptSection(); section != "" {
		t.Fatalf("empty store should yield empty section, got %q", section)
	}
	if err := s.Write(Entry{Name: "x", Description: "desc", Type: "user", Body: "body"}); err != nil {
		t.Fatal(err)
	}
	section, _ := s.PromptSection()
	if !strings.Contains(section, "## Memory") || !strings.Contains(section, "desc") {
		t.Fatalf("section = %q", section)
	}
}

func TestStore_Tools(t *testing.T) {
	s := NewStore(t.TempDir())
	r := tools.NewRegistry()
	if err := s.RegisterTools(r); err != nil {
		t.Fatal(err)
	}

	out, err := r.Call(context.Background(), "memory_write", map[string]any{
		"name": "fb1", "description": "d1", "type": "feedback", "body": "b1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "saved") {
		t.Fatalf("write out: %q", out)
	}
	out, _ = r.Call(context.Background(), "memory_search", map[string]any{"query": "d1"})
	if !strings.Contains(out, "fb1") {
		t.Fatalf("search out: %q", out)
	}
	if _, err := r.Call(context.Background(), "memory_delete", map[string]any{"name": "fb1"}); err != nil {
		t.Fatal(err)
	}
	out, _ = r.Call(context.Background(), "memory_search", map[string]any{"query": "d1"})
	if !strings.Contains(out, "no matches") {
		t.Fatalf("after delete: %q", out)
	}
}

func TestLoadAgentsMD(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir1, "AGENTS.md"), []byte("rule one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir2, "AGENTS.md"), []byte("rule two"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := LoadAgentsMD("", []string{dir1, dir2})
	if !strings.Contains(out, "rule one") || !strings.Contains(out, "rule two") {
		t.Fatalf("got: %s", out)
	}
}

func TestStore_WriteThenLoadRoundTripsAllFields(t *testing.T) {
	s := NewStore(t.TempDir())
	in := Entry{
		Name:        "round-trip",
		Description: "A description with: colons, dashes - and \"quotes\"",
		Type:        "project",
		Body:        "First line\nSecond line\nThird line",
	}
	if err := s.Write(in); err != nil {
		t.Fatal(err)
	}
	all, _ := s.Load()
	if len(all) != 1 {
		t.Fatalf("got %d entries", len(all))
	}
	got := all[0]
	if got.Name != in.Name || got.Description != in.Description || got.Type != in.Type || got.Body != in.Body {
		t.Fatalf("round-trip mismatch:\nwant=%+v\ngot =%+v", in, got)
	}
}

func TestStore_BodyContainingFrontmatterFenceIsPreserved(t *testing.T) {
	// A memory body that itself contains "---" must round-trip without
	// being interpreted as the closing fence.
	s := NewStore(t.TempDir())
	body := "explanation here\n---\nthis is part of the body, not a fence\n"
	if err := s.Write(Entry{
		Name:        "with-fence",
		Description: "d",
		Type:        "feedback",
		Body:        body,
	}); err != nil {
		t.Fatal(err)
	}
	all, _ := s.Load()
	if len(all) != 1 {
		t.Fatalf("got %d", len(all))
	}
	if !strings.Contains(all[0].Body, "this is part of the body") {
		t.Fatalf("body lost fence content: %q", all[0].Body)
	}
}

func TestStore_WriteSameNameTwiceUpdates(t *testing.T) {
	// Writing an entry with an existing name overwrites; CreatedAt is preserved.
	s := NewStore(t.TempDir())
	if err := s.Write(Entry{Name: "x", Description: "first", Type: "feedback", Body: "b1"}); err != nil {
		t.Fatal(err)
	}
	all1, _ := s.Load()
	created := all1[0].CreatedAt

	time.Sleep(10 * time.Millisecond) // ensure UpdatedAt differs
	if err := s.Write(Entry{Name: "x", Description: "second", Type: "feedback", Body: "b2"}); err != nil {
		t.Fatal(err)
	}
	all2, _ := s.Load()
	if len(all2) != 1 {
		t.Fatalf("got %d entries; expected overwrite", len(all2))
	}
	if all2[0].Description != "second" || all2[0].Body != "b2" {
		t.Fatalf("not overwritten: %+v", all2[0])
	}
	if !all2[0].UpdatedAt.After(created) {
		t.Fatalf("UpdatedAt not bumped: created=%v updated=%v", created, all2[0].UpdatedAt)
	}
}

func TestStore_DeleteNonexistentIsNoError(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Delete("never-existed"); err != nil {
		t.Fatalf("delete-missing should be idempotent, got %v", err)
	}
}

func TestStore_SearchCaseInsensitive(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Write(Entry{Name: "foo", Description: "MIXED Case Description", Type: "user", Body: "some BODY here"}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"mixed", "MIXED", "case", "body", "BODY"} {
		hits, _ := s.Search(q)
		if len(hits) != 1 {
			t.Errorf("search(%q) hits = %d", q, len(hits))
		}
	}
}

func TestStore_IndexFileShape(t *testing.T) {
	// Index file lists entries with name + type + description; format is
	// stable enough for humans to scan.
	s := NewStore(t.TempDir())
	if err := s.Write(Entry{Name: "alpha", Description: "first entry", Type: "feedback", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(Entry{Name: "beta", Description: "second entry", Type: "user", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	idx, err := os.ReadFile(filepath.Join(s.dir, indexFile))
	if err != nil {
		t.Fatal(err)
	}
	body := string(idx)
	for _, want := range []string{"# Memory Index", "alpha", "beta", "feedback", "user", "first entry", "second entry"} {
		if !strings.Contains(body, want) {
			t.Errorf("index missing %q in:\n%s", want, body)
		}
	}
}

func TestLoadAgentsMD_FullThreeLayer(t *testing.T) {
	homeDir := t.TempDir()
	root := t.TempDir()
	subAgents := filepath.Join(root, ".agents")
	if err := os.MkdirAll(subAgents, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(homeDir, "AGENTS.md"), []byte("global rule"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("project rule"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subAgents, "AGENTS.md"), []byte("subdir rule"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := LoadAgentsMD(filepath.Join(homeDir, "AGENTS.md"), []string{root, subAgents})
	for _, want := range []string{"global rule", "project rule", "subdir rule"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q in:\n%s", want, out)
		}
	}
	// All three should appear with their path headers.
	if strings.Count(out, "# AGENTS.md") < 3 {
		t.Errorf("expected 3 path headers, got:\n%s", out)
	}
}

func TestLoadAgentsMD_GlobalComesBeforeWorkspaceAgents(t *testing.T) {
	homeDir := t.TempDir()
	root := t.TempDir()
	subAgents := filepath.Join(root, ".agents")
	if err := os.MkdirAll(subAgents, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(homeDir, "AGENTS.md"), []byte("global rule"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("project root rule"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subAgents, "AGENTS.md"), []byte("project agents rule"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := LoadAgentsMD(filepath.Join(homeDir, "AGENTS.md"), []string{root, subAgents})
	globalPos := strings.Index(out, "global rule")
	rootPos := strings.Index(out, "project root rule")
	agentsPos := strings.Index(out, "project agents rule")
	if globalPos < 0 || rootPos < 0 || agentsPos < 0 {
		t.Fatalf("missing expected rule in:\n%s", out)
	}
	if !(globalPos < rootPos && rootPos < agentsPos) {
		t.Fatalf("AGENTS.md order = global:%d root:%d .agents:%d\n%s", globalPos, rootPos, agentsPos, out)
	}
}

func TestLoadAgentsMD_EmptyFileSkipped(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	out := LoadAgentsMD("", []string{root})
	if out != "" {
		t.Fatalf("empty file should yield empty output, got %q", out)
	}
}

func TestLoadAgentsMD_MissingFilesSkipped(t *testing.T) {
	out := LoadAgentsMD("/no/such/AGENTS.md", []string{"/no/such/dir1", "/no/such/dir2"})
	if out != "" {
		t.Fatalf("expected empty, got %q", out)
	}
}

func TestSanitize(t *testing.T) {
	cases := map[string]string{
		"Hello World": "hello_world",
		"a/b/c":       "a_b_c",
		"file.name":   "file.name",
		"with-dash":   "with-dash",
		"under_score": "under_score",
		"日本":          "__",
	}
	for in, want := range cases {
		if got := sanitize(in); got != want {
			t.Errorf("sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}
