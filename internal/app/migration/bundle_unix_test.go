//go:build darwin || linux

package migration

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestBundleFileReplacementCannotBlockOpeningFIFO(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(name, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	before, err := root.Lstat("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(name, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := readBundleOpenedFile(root, "manifest.json", 1024, before); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("replaced file accepted")
		}
	case <-time.After(time.Second):
		// Release a broken blocking implementation so a failing test never
		// strands the test process. No production FIFO or user path is used.
		fd, err := syscall.Open(name, syscall.O_RDWR|syscall.O_NONBLOCK, 0)
		if err == nil {
			defer func() { _ = syscall.Close(fd) }()
		}
		select {
		case <-done:
		case <-time.After(time.Second):
		}
		t.Fatal("replacement FIFO blocked before inode validation")
	}
}
