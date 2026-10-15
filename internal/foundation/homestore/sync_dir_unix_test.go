//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly || solaris

package homestore

import (
	"errors"
	"os"
	"syscall"
	"testing"
)

func TestSyncDirIgnoresOnlyUnsupportedFilesystemErrors(t *testing.T) {
	for _, ignored := range []error{syscall.EINVAL, syscall.ENOTSUP, syscall.ENOSYS} {
		t.Run(ignored.Error(), func(t *testing.T) {
			err := syncDirWith(t.TempDir(), func(*os.File) error { return ignored })
			if err != nil {
				t.Fatalf("syncDirWith(%v) = %v, want nil", ignored, err)
			}
		})
	}
	want := syscall.EIO
	err := syncDirWith(t.TempDir(), func(*os.File) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("syncDirWith(EIO) = %v, want EIO", err)
	}
}
