package native

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteCreatesIndependentThreadDirectories(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	for _, name := range []string{"worker-a", "worker-b"} {
		file := filepath.Join(root, "thread-work", name, "draft", "note.txt")
		var output bytes.Buffer
		if err := RunFileOperation(ctx, root, "read", FileArguments{Path: file}, &output); !os.IsNotExist(err) {
			t.Fatal("read fabricated a directory", err)
		}
		if err := RunFileOperation(ctx, root, "write", FileArguments{Path: file, Content: name}, &output); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"worker-a", "worker-b"} {
		data, err := os.ReadFile(filepath.Join(root, "thread-work", name, "draft", "note.txt"))
		if err != nil || string(data) != name {
			t.Fatal("Thread files shared state", err)
		}
	}
	blocked := filepath.Join(root, "blocked")
	if err := os.WriteFile(blocked, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := RunFileOperation(ctx, root, "write", FileArguments{Path: filepath.Join(blocked, "child"), Content: "bad"}, &output); err == nil || output.Len() != 0 {
		t.Fatal("reported success for invalid parent", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if err := RunFileOperation(ctx, root, "write", FileArguments{Path: filepath.Join(root, "cancelled", "child"), Content: "bad"}, &output); err == nil {
		t.Fatal("cancelled write succeeded")
	}
	if _, err := os.Stat(filepath.Join(root, "cancelled")); !os.IsNotExist(err) {
		t.Fatal("cancelled write created parent")
	}
}
