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
	var firstSource, firstTarget string
	calls := 0
	moveFileEx = func(from, to *uint16, flags uint32) error {
		calls++
		source, target := windows.UTF16PtrToString(from), windows.UTF16PtrToString(to)
		if calls == 1 {
			firstSource, firstTarget = source, target
		} else if source != firstSource || target != firstTarget {
			t.Errorf("retry changed source/target: %q -> %q", source, target)
		}
		if flags != windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH {
			t.Errorf("replacement flags = %#x, want durable replacement", flags)
		}
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

func TestWriteFileAtomicPersistentWindowsReaderPreservesDestination(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.json")
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
	calls := 0
	moveFileEx = func(from, to *uint16, flags uint32) error {
		calls++
		return original(from, to, flags)
	}
	err = WriteFileAtomic(path, []byte("new\n"), 0o600, 0o700)
	var writeErr *AtomicWriteError
	if !errors.As(err, &writeErr) || writeErr.Operation != "replace" || writeErr.Path != path || writeErr.Replaced {
		t.Fatalf("publication error = %v, want a pre-replacement atomic write error", err)
	}
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) && !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		t.Fatalf("publication error = %v, want a Windows sharing conflict", err)
	}
	if calls != 7 {
		t.Fatalf("replacement attempts = %d, want the bounded 7 attempts", calls)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "old\n" {
		t.Fatalf("original destination = %q, %v; want unchanged old data", data, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "history.json" {
		t.Fatalf("directory entries = %v, want only the original destination", entries)
	}
}

func TestReplaceFileWindowsRetryPolicy(t *testing.T) {
	for _, tc := range []struct {
		name     string
		sequence []error
		calls    int
		wantErr  error
	}{
		{name: "success", sequence: []error{nil}, calls: 1},
		{name: "access denied recovers", sequence: []error{windows.ERROR_ACCESS_DENIED, nil}, calls: 2},
		{name: "sharing violation recovers", sequence: []error{windows.ERROR_SHARING_VIOLATION, nil}, calls: 2},
		{name: "unrelated error", sequence: []error{windows.ERROR_DISK_FULL}, calls: 1, wantErr: windows.ERROR_DISK_FULL},
		{name: "retry becomes permanent", sequence: []error{windows.ERROR_ACCESS_DENIED, windows.ERROR_FILE_NOT_FOUND}, calls: 2, wantErr: windows.ERROR_FILE_NOT_FOUND},
		{name: "access denied exhausted", sequence: []error{windows.ERROR_ACCESS_DENIED}, calls: 7, wantErr: windows.ERROR_ACCESS_DENIED},
		{name: "sharing violation exhausted", sequence: []error{windows.ERROR_SHARING_VIOLATION}, calls: 7, wantErr: windows.ERROR_SHARING_VIOLATION},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := moveFileEx
			t.Cleanup(func() { moveFileEx = original })
			calls := 0
			moveFileEx = func(from, to *uint16, flags uint32) error {
				calls++
				if flags != windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH {
					t.Errorf("attempt %d flags = %#x, want durable replacement", calls, flags)
				}
				return tc.sequence[min(calls-1, len(tc.sequence)-1)]
			}
			err := replaceFile("from.tmp", "to.json")
			if !errors.Is(err, tc.wantErr) || calls != tc.calls {
				t.Fatalf("replacement = %v after %d attempts, want %v after %d", err, calls, tc.wantErr, tc.calls)
			}
		})
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
