package managed

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBlobStorageCannotBeInsideUserWritableHostedStorage(t *testing.T) {
	root := t.TempDir()
	blobs, workspace := filepath.Join(root, "blobs"), filepath.Join(root, "workspace")
	for _, path := range []string{blobs, workspace, filepath.Join(workspace, "agent", "files")} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := separateBlobWorkspace(blobs, workspace); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(filepath.Join(workspace, "agent", "files"), alias); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{workspace, workspace}, {alias, workspace}, {root, workspace}} {
		if err := separateBlobWorkspace(pair[0], pair[1]); err == nil {
			t.Fatal("overlapping storage accepted", pair)
		}
	}
}
