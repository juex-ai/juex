//go:build windows

package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTranscriptFileChangeIDUsesUSN(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conversation.jsonl")
	if err := os.WriteFile(path, []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	before := transcriptFileChangeID(file, info)
	if before == "" {
		t.Skip("filesystem does not expose per-file USN data")
	}
	if _, err := file.WriteAt([]byte("other"), 0); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	file, err = os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	after := transcriptFileChangeID(file, info)
	if after == "" || after == before {
		t.Fatalf("change identity = %q after rewrite, want non-empty value different from %q", after, before)
	}
}
