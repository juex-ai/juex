package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBundleCmdCreatesArchiveAndPrintsJSON(t *testing.T) {
	work := t.TempDir()
	sessionID := "20260614T120000-cli0001"
	seedBundleCLISession(t, work, sessionID)
	out := filepath.Join(work, "debug.tar.gz")

	root := newRootCmd()
	var stdout bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stdout)
	root.SetArgs([]string{"-C", work, "bundle", "--session", sessionID, "--out", out})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Path      string `json:"path"`
		SessionID string `json:"session_id"`
		Files     int    `json:"files"`
		Bytes     int64  `json:"bytes"`
		Redacted  bool   `json:"redacted"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &parsed); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, stdout.String())
	}
	if parsed.Path != out || parsed.SessionID != sessionID || parsed.Files == 0 || parsed.Bytes == 0 || !parsed.Redacted {
		t.Fatalf("output = %+v", parsed)
	}
	if !bundleCLIArchiveContains(t, out, "juex-debug-bundle/manifest.json") {
		t.Fatalf("archive missing manifest")
	}
}

func TestBundleCmdUnknownSessionReturnsNotFound(t *testing.T) {
	work := t.TempDir()
	root := newRootCmd()
	var stdout bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stdout)
	root.SetArgs([]string{"-C", work, "bundle", "--session", "missing", "--out", filepath.Join(work, "debug.tar.gz")})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected not found error")
	}
	if _, ok := err.(*notFoundError); !ok {
		t.Fatalf("err = %T %v, want *notFoundError", err, err)
	}
}

func TestBundleCmdExistingOutPathRequiresForce(t *testing.T) {
	work := t.TempDir()
	sessionID := "20260614T120000-cliforce"
	seedBundleCLISession(t, work, sessionID)
	out := filepath.Join(work, "debug.tar.gz")
	if err := os.WriteFile(out, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	root := newRootCmd()
	root.SetArgs([]string{"-C", work, "bundle", "--session", sessionID, "--out", out})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected conflict error")
	}
	if _, ok := err.(*conflictError); !ok {
		t.Fatalf("err = %T %v, want *conflictError", err, err)
	}
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "keep" {
		t.Fatalf("existing output overwritten: %q", body)
	}
}

func seedBundleCLISession(t *testing.T, work, id string) {
	t.Helper()
	dir := filepath.Join(work, ".juex", "sessions", id)
	for name, body := range map[string]string{
		"session.json":       `{"kind":"primary"}`,
		"conversation.jsonl": `{"role":"user","blocks":[{"type":"text","text":"hi api_key=sk-cli-secret"}]}` + "\n",
		"events.jsonl":       `{"type":"x"}` + "\n",
	} {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func bundleCLIArchiveContains(t *testing.T, path, want string) bool {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return false
		}
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(h.Name) == want {
			return true
		}
	}
}
