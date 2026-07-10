//go:build windows

package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestReplaceFileRetriesSharingViolation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.json")
	tmpName := filepath.Join(dir, "history.tmp")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tmpName, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}

	pathp, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(
		pathp,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	releaseErr := make(chan error, 1)
	go func() {
		time.Sleep(50 * time.Millisecond)
		releaseErr <- windows.CloseHandle(handle)
	}()

	if err := replaceFile(tmpName, path); err != nil {
		t.Fatal(err)
	}
	if err := <-releaseErr; err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "new" {
		t.Fatalf("replaced content = %q, want new", got)
	}
}
