package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveSessionDir_BothFlagsIsUsageError(t *testing.T) {
	_, err := resolveSessionDir(resumeFlags{Resume: true, Session: "abc"}, t.TempDir(), nil, &bytes.Buffer{}, true)
	if err == nil {
		t.Fatal("expected usage error")
	}
	if _, ok := err.(*usageError); !ok {
		t.Errorf("got %T", err)
	}
}

func TestResolveSessionDir_NeitherFlagReturnsEmpty(t *testing.T) {
	dir, err := resolveSessionDir(resumeFlags{}, t.TempDir(), nil, &bytes.Buffer{}, true)
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
	dir := filepath.Join(work, ".agents", "sessions", id)
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "conversation.jsonl"), []byte(""), 0o644)

	got, err := resolveSessionDir(resumeFlags{Session: id}, filepath.Join(work, ".agents", "sessions"), nil, &bytes.Buffer{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Errorf("dir = %s, want %s", got, dir)
	}
}

func TestResolveSessionDir_SessionFlagMissing(t *testing.T) {
	_, err := resolveSessionDir(resumeFlags{Session: "nope"}, t.TempDir(), nil, &bytes.Buffer{}, true)
	if err == nil {
		t.Fatal("expected error")
	}
	if _, ok := err.(*notFoundError); !ok {
		t.Errorf("got %T", err)
	}
}

func TestResolveSessionDir_ResumeNonTTYIsUsageError(t *testing.T) {
	_, err := resolveSessionDir(resumeFlags{Resume: true}, t.TempDir(), nil, &bytes.Buffer{}, false)
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
	dir := filepath.Join(work, ".agents", "sessions", id)
	os.MkdirAll(dir, 0o755)
	body := `{"role":"user","blocks":[{"type":"text","text":"hi"}]}` + "\n"
	os.WriteFile(filepath.Join(dir, "conversation.jsonl"), []byte(body), 0o644)

	in := strings.NewReader("1\n")
	var out bytes.Buffer
	got, err := resolveSessionDir(resumeFlags{Resume: true}, filepath.Join(work, ".agents", "sessions"), in, &out, true)
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Errorf("dir = %s, want %s", got, dir)
	}
}
