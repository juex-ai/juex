//go:build windows

package homestore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWriteFileAtomicRecoversAfterWindowsReaderCloses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	original := moveFileEx
	t.Cleanup(func() { moveFileEx = original })
	var firstErr error
	calls := 0
	moveFileEx = func(from, to *uint16, flags uint32) error {
		calls++
		err := original(from, to, flags)
		if calls == 1 {
			firstErr = err
			// Close only after a real replacement attempt observes the reader.
			if closeErr := reader.Close(); closeErr != nil {
				t.Errorf("close blocking reader: %v", closeErr)
			}
		}
		return err
	}
	err = WriteFileAtomic(path, []byte("new\n"), 0o600, 0o700)
	t.Logf("initial replacement with an ordinary reader: %v; attempts: %d", firstErr, calls)
	if !errors.Is(firstErr, windows.ERROR_ACCESS_DENIED) && !errors.Is(firstErr, windows.ERROR_SHARING_VIOLATION) {
		t.Fatalf("initial replacement error = %v, want a Windows sharing conflict", firstErr)
	}
	if err != nil {
		t.Fatalf("publication after reader closed: %v", err)
	}
	if calls < 2 {
		t.Fatalf("replacement attempts = %d, want a retry after closing the reader", calls)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new\n" {
		t.Fatalf("published data = %q, want new", data)
	}
}

func TestReplaceFileUsesDurableWindowsFlags(t *testing.T) {
	original := moveFileEx
	t.Cleanup(func() { moveFileEx = original })
	var gotFlags uint32
	moveFileEx = func(from, to *uint16, flags uint32) error {
		gotFlags = flags
		return nil
	}

	if err := replaceFile("from.tmp", "to.json"); err != nil {
		t.Fatal(err)
	}
	want := uint32(windows.MOVEFILE_REPLACE_EXISTING | windows.MOVEFILE_WRITE_THROUGH)
	if gotFlags != want {
		t.Fatalf("MoveFileEx flags = %#x, want %#x", gotFlags, want)
	}
}
