package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/thread"
)

func bundleThreadFixture(t *testing.T) (config.Config, *thread.Thread) {
	t.Helper()
	work := t.TempDir()
	state := filepath.Join(work, ".juex")
	cfg := config.Config{WorkDir: work, AgentStateDir: state}
	target, err := thread.NewStore(state).EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = target.Close() })
	return cfg, target
}

func TestCreateIncludesThreadJournalAndRedacts(t *testing.T) {
	cfg, target := bundleThreadFixture(t)
	if err := target.Append(llm.TextMessage(llm.RoleUser, "api_key=sk-bundle-secret")); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "thread.tar.gz")
	result, err := Create(Options{WorkDir: cfg.WorkDir, ThreadID: target.ID, OutPath: out, Redact: true, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if result.ThreadID != thread.MainID || !result.Redacted || result.Files < 2 {
		t.Fatalf("result = %+v", result)
	}
	files := readBundle(t, out)
	journal := files["juex-debug-bundle/thread/journal.jsonl"]
	if len(journal) == 0 {
		t.Fatalf("journal missing: %v", files)
	}
	if bytes.Contains(journal, []byte("sk-bundle-secret")) {
		t.Fatalf("journal was not redacted: %s", journal)
	}
}

func TestCreateReadsArchivedThread(t *testing.T) {
	cfg, main := bundleThreadFixture(t)
	store := thread.NewStore(cfg.AgentStateDir)
	worker, err := store.CreateWorker(main.ID, "archived")
	if err != nil {
		t.Fatal(err)
	}
	id := worker.ID
	if err := store.Archive(worker); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "archived.tar.gz")
	if _, err := Create(Options{WorkDir: cfg.WorkDir, ThreadID: id, OutPath: out, Config: cfg}); err != nil {
		t.Fatal(err)
	}
}

func TestCreateRejectsMissingThreadAndExistingOutput(t *testing.T) {
	cfg, target := bundleThreadFixture(t)
	out := filepath.Join(t.TempDir(), "bundle.tar.gz")
	if _, err := Create(Options{WorkDir: cfg.WorkDir, ThreadID: "abcdef", OutPath: out, Config: cfg}); !errors.Is(err, ErrThreadNotFound) {
		t.Fatalf("missing error = %v", err)
	}
	if err := os.WriteFile(out, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(Options{WorkDir: cfg.WorkDir, ThreadID: target.ID, OutPath: out, Config: cfg}); !errors.Is(err, ErrOutputExists) {
		t.Fatalf("existing output error = %v", err)
	}
}

func readBundle(t *testing.T, path string) map[string][]byte {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	files := map[string][]byte{}
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return files
		}
		if err != nil {
			t.Fatal(err)
		}
		files[header.Name], err = io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
	}
}
