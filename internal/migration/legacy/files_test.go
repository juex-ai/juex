package legacy

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSourceReaderRejectsChangesDuringCapture(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		dir := t.TempDir()
		path := filepath.Join(dir, "source.json")
		writeFixture(t, path, []byte(`{"v":1}`))
		root, err := os.OpenRoot(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := root.Close(); err != nil {
				t.Error(err)
			}
		})
		r := sourceReader{root: root, stats: make(map[string]os.FileInfo)}
		if _, err := r.read("source.json"); err != nil {
			t.Fatal(err)
		}
		if replacement {
			newPath := filepath.Join(dir, "replacement")
			writeFixture(t, newPath, []byte(`{"v":2}`))
			if err := os.Rename(newPath, path); err != nil {
				t.Fatal(err)
			}
		} else {
			writeFixture(t, path, []byte(`{"v":2,"changed":true}`))
		}
		if err := r.unchanged(); err == nil {
			t.Fatal("source changed during capture but was accepted")
		}
	}
}

func TestSourceReaderAbsentFileMustStayAbsent(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Error(err)
		}
	})
	r := sourceReader{root: root, stats: make(map[string]os.FileInfo)}
	data, err := r.optionalRead("inputs.json")
	if err != nil || data != nil || len(r.absent) != 1 {
		t.Fatalf("absent file: %v", err)
	}
	if err := r.unchanged(); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(dir, "inputs.json"), []byte(`{"v":1,"records":[]}`))
	if err := r.unchanged(); err == nil {
		t.Fatal("missed queue created during capture")
	}
}
