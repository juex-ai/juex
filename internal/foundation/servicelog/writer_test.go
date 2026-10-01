package servicelog

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRetentionBoundsAgeAndSizeWhileIdle(t *testing.T) {
	w, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := w.Close(); err != nil {
			t.Error(err)
		}
	})
	now := time.Now()
	w.now = func() time.Time { now = now.Add(time.Second); return now }
	w.maxBytes = 4
	w.maxFiles = 2
	if _, err := w.Write([]byte("abcdefghijkl")); err != nil {
		t.Fatal(err)
	}
	paths, _ := filepath.Glob(filepath.Join(w.directory, "*.log"))
	if len(paths) != 2 {
		t.Fatal(paths)
	}
	for _, p := range paths {
		info, _ := os.Stat(p)
		if info.Size() > 4 {
			t.Fatal("unbounded", info.Size())
		}
	}
	now = now.Add(8 * 24 * time.Hour)
	if err := w.Prune(); err != nil {
		t.Fatal(err)
	}
	paths, _ = filepath.Glob(filepath.Join(w.directory, "*.log"))
	if len(paths) != 0 {
		t.Fatal("idle logs outlived retention", paths)
	}
	now = time.Now()
	if _, err := w.Write([]byte("new")); err != nil {
		t.Fatal("writer did not reopen", err)
	}
}

func TestLowVolumeLogsCannotRefreshOldContentPastSevenDays(t *testing.T) {
	w, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := w.Close(); err != nil {
			t.Error(err)
		}
	})
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	w.now = func() time.Time { return now }
	for range 9 {
		if _, err := w.Write([]byte("one daily line\n")); err != nil {
			t.Fatal(err)
		}
		now = now.Add(24 * time.Hour)
	}
	if err := w.Prune(); err != nil {
		t.Fatal(err)
	}
	paths, _ := filepath.Glob(filepath.Join(w.directory, "*.log"))
	if len(paths) > 7 {
		t.Fatal("capacity", paths)
	}
	for _, path := range paths {
		created, err := time.Parse("juex-20060102T150405.000000000.log", filepath.Base(path))
		if err != nil {
			t.Fatal(err)
		}
		if !created.After(now.Add(-7 * 24 * time.Hour)) {
			t.Fatal("old content retained", path)
		}
	}
}
