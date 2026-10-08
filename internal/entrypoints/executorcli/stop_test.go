package executorcli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestBackgroundStopMarkerExitsBeforeOpeningEnrollmentOrJournal(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "service-stop"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Execute(context.Background(), []string{"--state", directory, "run", "--background-log"}, nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 || entries[0].Name() != "service-stop" {
		t.Fatalf("stopped service opened runtime state: %v %v", entries, err)
	}
	if err := Execute(context.Background(), []string{"--state", directory, "run"}, nil, io.Discard, io.Discard); err == nil {
		t.Fatal("background stop marker suppressed explicit foreground execution")
	}
}
