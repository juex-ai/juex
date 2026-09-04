package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/juex-ai/juex/internal/thread"
)

func TestBundleCommandUsesThreadIdentity(t *testing.T) {
	work := t.TempDir()
	cfg := ensureTestWorkspaceAgent(t, work)
	target, err := thread.NewStore(cfg.RuntimePaths().StateDir).EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(work, "debug.tar.gz")
	root := newRootCmd()
	var stdout bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stdout)
	root.SetArgs([]string{"thread", "bundle", thread.MainID, "--cwd", work, "--out", out})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var result struct {
		ThreadID string `json:"thread_id"`
		Path     string `json:"path"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, stdout.String())
	}
	if result.ThreadID != thread.MainID || result.Path != out {
		t.Fatalf("result = %+v", result)
	}
}

func TestBundleCommandReportsUnknownThread(t *testing.T) {
	work := t.TempDir()
	ensureTestWorkspaceAgent(t, work)
	root := newRootCmd()
	root.SetArgs([]string{"thread", "bundle", "abcdef", "--cwd", work, "--out", filepath.Join(work, "debug.tar.gz")})
	err := root.Execute()
	if _, ok := err.(*notFoundError); !ok {
		t.Fatalf("error = %T %v, want notFoundError", err, err)
	}
}
