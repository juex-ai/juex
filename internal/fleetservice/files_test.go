package fleetservice

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/juex-ai/juex/internal/homestore"
)

func TestPublishFilesRollsBackCurrentFileAfterPostReplaceFailure(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first")
	second := filepath.Join(dir, "second")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	attempt := 0
	err := publishFilesWith([]definitionFile{
		{path: first, data: []byte("new-first"), mode: 0o600},
		{path: second, data: []byte("new-second"), mode: 0o600},
	}, func(path string, data []byte, mode os.FileMode) error {
		attempt++
		if err := publishDefinitionFile(path, data, mode); err != nil {
			return err
		}
		if attempt == 2 {
			return &homestore.AtomicWriteError{
				Replaced: true,
				Err:      errors.New("simulated parent sync failure"),
			}
		}
		return nil
	})
	if err == nil {
		t.Fatal("publishFilesWith succeeded despite post-replace failure")
	}
	for _, path := range []string{first, second} {
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(body) != "old" {
			t.Fatalf("%s = %q after rollback, want old", path, body)
		}
	}
}

func TestPublishFilesDoesNotRollBackCurrentFileAfterPreReplaceFailure(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first")
	second := filepath.Join(dir, "second")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	attempt := 0
	err := publishFilesWith([]definitionFile{
		{path: first, data: []byte("new-first"), mode: 0o600},
		{path: second, data: []byte("new-second"), mode: 0o600},
	}, func(path string, data []byte, mode os.FileMode) error {
		attempt++
		if attempt == 2 {
			if err := os.WriteFile(path, []byte("concurrent"), mode); err != nil {
				return err
			}
			return errors.New("simulated pre-replace failure")
		}
		return publishDefinitionFile(path, data, mode)
	})
	if err == nil {
		t.Fatal("publishFilesWith succeeded despite pre-replace failure")
	}
	assertFileBody(t, first, "old")
	assertFileBody(t, second, "concurrent")
}

func TestRemoveDurablySyncsParentAfterRemoval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "definition")
	var removed, synced string
	err := removeDurablyWith(path, func(got string) error {
		removed = got
		return nil
	}, func(got string) error {
		synced = got
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if removed != path || synced != filepath.Dir(path) {
		t.Fatalf("remove=%q sync=%q, want %q and parent", removed, synced, path)
	}
}

func assertFileBody(t *testing.T, path, want string) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != want {
		t.Fatalf("%s = %q, want %q", path, body, want)
	}
}
