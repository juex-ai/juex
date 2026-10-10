package native

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestGlobRecursivelyMatchesRelativePathsAndBasenames(t *testing.T) {
	root := t.TempDir()
	names := []string{"root.py", "notes.md", "nested/plan.py", "nested/deeper/last.py", "nested/run.log"}
	for _, name := range names {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		pattern string
		want    []string
	}{
		{"*", []string{"nested/deeper/last.py", "nested/plan.py", "nested/run.log", "notes.md", "root.py"}},
		{"*.py", []string{"nested/deeper/last.py", "nested/plan.py", "root.py"}},
		{"nested/*.py", []string{"nested/plan.py"}},
	} {
		t.Run(tc.pattern, func(t *testing.T) {
			var output bytes.Buffer
			if err := RunFileOperation(context.Background(), root, "glob", FileArguments{Pattern: tc.pattern}, &output); err != nil {
				t.Fatal(err)
			}
			var got []string
			decoder := json.NewDecoder(&output)
			for {
				var row struct {
					Path string `json:"path"`
				}
				if err := decoder.Decode(&row); err == io.EOF {
					break
				} else if err != nil {
					t.Fatal(err)
				}
				relative, err := filepath.Rel(root, row.Path)
				if err != nil {
					t.Fatal(err)
				}
				got = append(got, filepath.ToSlash(relative))
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("pattern %q: got %v, want %v", tc.pattern, got, tc.want)
			}
		})
	}
}
