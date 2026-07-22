package fleetservice

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
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
			return errors.New("simulated parent sync failure")
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
