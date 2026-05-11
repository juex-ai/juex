package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/session"
)

func TestResolveSessionDir_BothFlagsIsUsageError(t *testing.T) {
	_, err := resolveSessionDir(resumeFlags{Resume: "abc", Session: "def"}, t.TempDir(), filepath.Join(t.TempDir(), "history.json"), nil, &bytes.Buffer{}, true)
	if err == nil {
		t.Fatal("expected usage error")
	}
	if _, ok := err.(*usageError); !ok {
		t.Errorf("got %T", err)
	}
}

func TestResolveSessionDir_NeitherFlagReturnsEmpty(t *testing.T) {
	dir, err := resolveSessionDir(resumeFlags{}, t.TempDir(), filepath.Join(t.TempDir(), "history.json"), nil, &bytes.Buffer{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if dir != "" {
		t.Errorf("dir = %q, want empty (= start a new session)", dir)
	}
}

func TestResolveSessionDir_SessionFlagFound(t *testing.T) {
	work := t.TempDir()
	id := "20260506T103500-resolve01"
	dir := filepath.Join(work, ".juex", "sessions", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "conversation.jsonl"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := resolveSessionDir(resumeFlags{Session: id}, filepath.Join(work, ".juex", "sessions"), filepath.Join(work, ".juex", "history.json"), nil, &bytes.Buffer{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Errorf("dir = %s, want %s", got, dir)
	}
}

func TestResolveSessionDir_SessionFlagMissing(t *testing.T) {
	_, err := resolveSessionDir(resumeFlags{Session: "nope"}, t.TempDir(), filepath.Join(t.TempDir(), "history.json"), nil, &bytes.Buffer{}, true)
	if err == nil {
		t.Fatal("expected error")
	}
	if _, ok := err.(*notFoundError); !ok {
		t.Errorf("got %T", err)
	}
}

func TestResolveSessionDir_ResumeNonTTYIsUsageError(t *testing.T) {
	_, err := resolveSessionDir(resumeFlags{Resume: resumePick}, t.TempDir(), filepath.Join(t.TempDir(), "history.json"), nil, &bytes.Buffer{}, false)
	if err == nil {
		t.Fatal("expected usage error")
	}
	if _, ok := err.(*usageError); !ok {
		t.Errorf("got %T", err)
	}
}

func TestResolveSessionDir_ResumeTTYUsesPicker(t *testing.T) {
	work := t.TempDir()
	id := "20260506T103500-pickone01"
	dir := filepath.Join(work, ".juex", "sessions", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"role":"user","blocks":[{"type":"text","text":"hi"}]}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "conversation.jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	in := strings.NewReader("1\n")
	var out bytes.Buffer
	got, err := resolveSessionDir(resumeFlags{Resume: resumePick}, filepath.Join(work, ".juex", "sessions"), filepath.Join(work, ".juex", "history.json"), in, &out, true)
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Errorf("dir = %s, want %s", got, dir)
	}
}

func TestResolveSessionDir_ResumeLastUsesHistory(t *testing.T) {
	work := t.TempDir()
	sessionsRoot := filepath.Join(work, ".juex", "sessions")
	oldDir := seedResumeSession(t, sessionsRoot, "20260506T103500-old00001", "older", "shared", time.Date(2026, 5, 6, 10, 35, 0, 0, time.UTC))
	newDir := seedResumeSession(t, sessionsRoot, "20260506T113500-new00001", "newer", "daily", time.Date(2026, 5, 6, 11, 35, 0, 0, time.UTC))
	for _, dir := range []string{oldDir, newDir} {
		if err := session.RecordHistory(filepath.Join(work, ".juex", "history.json"), mustInfo(t, dir)); err != nil {
			t.Fatal(err)
		}
	}

	got, err := resolveSessionDir(resumeFlags{Resume: "last"}, sessionsRoot, filepath.Join(work, ".juex", "history.json"), nil, &bytes.Buffer{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != newDir {
		t.Errorf("dir = %s, want %s", got, newDir)
	}
}

func TestResolveSessionDir_ResumeAliasChoosesMostRecent(t *testing.T) {
	work := t.TempDir()
	sessionsRoot := filepath.Join(work, ".juex", "sessions")
	olderDir := seedResumeSession(t, sessionsRoot, "20260506T103500-older001", "older", "daily", time.Date(2026, 5, 6, 10, 35, 0, 0, time.UTC))
	newerDir := seedResumeSession(t, sessionsRoot, "20260506T113500-newer001", "newer", "daily", time.Date(2026, 5, 6, 11, 35, 0, 0, time.UTC))
	for _, dir := range []string{olderDir, newerDir} {
		if err := session.RecordHistory(filepath.Join(work, ".juex", "history.json"), mustInfo(t, dir)); err != nil {
			t.Fatal(err)
		}
	}

	got, err := resolveSessionDir(resumeFlags{Resume: "daily"}, sessionsRoot, filepath.Join(work, ".juex", "history.json"), nil, &bytes.Buffer{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != newerDir {
		t.Errorf("dir = %s, want %s", got, newerDir)
	}
}

func seedResumeSession(t *testing.T, root, id, prompt, alias string, mtime time.Time) string {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"role":"user","blocks":[{"type":"text","text":"` + prompt + `"}]}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "conversation.jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(dir, "conversation.jsonl"), mtime, mtime); err != nil {
		t.Fatal(err)
	}
	if err := session.SetAlias(dir, alias); err != nil {
		t.Fatal(err)
	}
	return dir
}

func mustInfo(t *testing.T, dir string) session.Info {
	t.Helper()
	info, _, err := session.LoadInfo(dir)
	if err != nil {
		t.Fatal(err)
	}
	return info
}
