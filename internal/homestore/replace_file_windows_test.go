//go:build windows

package homestore

import (
	"testing"

	"golang.org/x/sys/windows"
)

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
