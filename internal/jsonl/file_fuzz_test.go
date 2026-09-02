package jsonl

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func FuzzOpenRepairsArbitraryTornTail(f *testing.F) {
	f.Add([]byte(`{"id":2`))
	f.Add([]byte{0, 1, 2, 0xff})
	f.Fuzz(func(t *testing.T, tail []byte) {
		tail = bytes.ReplaceAll(tail, []byte{'\n'}, []byte{'x'})
		if len(tail) == 0 {
			tail = []byte{'{'}
		}
		path := filepath.Join(t.TempDir(), "events.jsonl")
		prefix := []byte("{\"id\":1}\n")
		body := append(append([]byte(nil), prefix...), tail...)
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
		file, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, prefix) {
			t.Fatalf("file = %q, want %q", got, prefix)
		}
	})
}
