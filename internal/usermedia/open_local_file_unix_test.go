//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package usermedia

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestOpenLocalFileDoesNotBlockOnFIFO(t *testing.T) {
	workDir := t.TempDir()
	fifoPath := filepath.Join(workDir, "image.pipe")
	if err := unix.Mkfifo(fifoPath, 0o600); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	f, err := openLocalFile(fifoPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("non-blocking FIFO open took %s", elapsed)
	}
	stat, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if stat.Mode().IsRegular() {
		t.Fatal("FIFO reported as regular file")
	}

	if _, err := InspectFiles(workDir, []string{"image.pipe"}, Limits{}); !errors.Is(err, ErrInvalidInput) || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("InspectFiles FIFO error = %v", err)
	}
}
