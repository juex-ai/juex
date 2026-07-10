//go:build windows

package artifact

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestStorePutRetriesWindowsSharingViolation(t *testing.T) {
	workDir := t.TempDir()
	store, err := NewStore(workDir)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := store.Put("context/item.txt", []byte("old"))
	if err != nil {
		t.Fatal(err)
	}
	absPath := filepath.Join(workDir, filepath.FromSlash(ref.Path))
	pathp, err := windows.UTF16PtrFromString(absPath)
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

	if _, err := store.Put("context/item.txt", []byte("new")); err != nil {
		t.Fatal(err)
	}
	if err := <-releaseErr; err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new" {
		t.Fatalf("data = %q, want new", data)
	}
}
