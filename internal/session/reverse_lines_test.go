package session

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReverseLineReader(t *testing.T) {
	long := strings.Repeat("x", reverseLineBlockBytes+17)
	path := filepath.Join(t.TempDir(), "events.jsonl")
	if err := os.WriteFile(path, []byte("first\r\n\n"+long+"\nlast"), 0o644); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader, err := newReverseLineReader(file)
	if err != nil {
		t.Fatal(err)
	}

	for index, want := range []string{"last", long, "first"} {
		got, err := reader.next()
		if err != nil {
			t.Fatalf("line %d: %v", index, err)
		}
		if string(got) != want {
			t.Fatalf("line %d = %q, want %q", index, got, want)
		}
	}
	if _, err := reader.next(); !errors.Is(err, io.EOF) {
		t.Fatalf("final error = %v, want EOF", err)
	}
}

func TestUncappedReverseLineReaderReadsMultiBlockLine(t *testing.T) {
	long := strings.Repeat("x", reverseLineBlockBytes*16+17)
	path := filepath.Join(t.TempDir(), "conversation.jsonl")
	if err := os.WriteFile(path, []byte("first\n"+long+"\nlast"), 0o644); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader, err := newUncappedReverseLineReaderAt(file, 0)
	if err != nil {
		t.Fatal(err)
	}

	for index, want := range []string{"last", long, "first"} {
		got, err := reader.next()
		if err != nil {
			t.Fatalf("line %d: %v", index, err)
		}
		if string(got) != want {
			t.Fatalf("line %d length = %d, want %d", index, len(got), len(want))
		}
	}
	if _, err := reader.next(); !errors.Is(err, io.EOF) {
		t.Fatalf("final error = %v, want EOF", err)
	}
}

func TestReverseLineReaderRejectsOversizedLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", maxEventLineBytes+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader, err := newReverseLineReader(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.next(); !errors.Is(err, errEventLineTooLong) {
		t.Fatalf("error = %v, want errEventLineTooLong", err)
	}
}

func TestBoundedReverseLineReaderDiscardsPartialPrefix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	if err := os.WriteFile(path, []byte("outside\npartial-prefix\nlast"), 0o644); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader, err := newBoundedReverseLineReader(file, int64(len("prefix\nlast")))
	if err != nil {
		t.Fatal(err)
	}

	line, err := reader.next()
	if err != nil {
		t.Fatal(err)
	}
	if string(line) != "last" {
		t.Fatalf("line = %q, want last", line)
	}
	if _, err := reader.next(); !errors.Is(err, io.EOF) {
		t.Fatalf("final error = %v, want EOF", err)
	}
}
