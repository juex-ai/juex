package web

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFilesTreeReturnsSortedWorkDir(t *testing.T) {
	srv := newTestServer(t)
	work := srv.opts.Cfg.WorkDir
	mustWriteFile(t, filepath.Join(work, "README.md"), "hello")
	mustWriteFile(t, filepath.Join(work, "cmd", "main.go"), "package main\n")
	mustWriteFile(t, filepath.Join(work, ".agents", "skills", "demo", "SKILL.md"), "skill")
	mustWriteFile(t, filepath.Join(work, ".git", "config"), "ignored")
	mustWriteFile(t, filepath.Join(work, "node_modules", "pkg", "index.js"), "ignored")

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/files/tree")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	var root FileNode
	if err := json.NewDecoder(resp.Body).Decode(&root); err != nil {
		t.Fatal(err)
	}
	if root.Path != "/" || !root.IsDir {
		t.Fatalf("root = %+v", root)
	}
	names := childNames(root.Children)
	want := []string{".agents", "cmd", "README.md"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("children = %v, want %v", names, want)
	}
	if got := root.Children[1].Children[0].Path; got != "cmd/main.go" {
		t.Fatalf("nested path = %q, want forward slash path", got)
	}
}

func TestFilesTreeLimitsDepth(t *testing.T) {
	srv := newTestServer(t)
	deep := srv.opts.Cfg.WorkDir
	for i := 0; i < maxFileTreeDepth+2; i++ {
		deep = filepath.Join(deep, "d")
	}
	mustWriteFile(t, filepath.Join(deep, "too-deep.txt"), "hidden")

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/files/tree")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	var root FileNode
	if err := json.NewDecoder(resp.Body).Decode(&root); err != nil {
		t.Fatal(err)
	}
	node := &root
	for i := 0; i < maxFileTreeDepth; i++ {
		if len(node.Children) != 1 {
			t.Fatalf("depth %d children = %d", i, len(node.Children))
		}
		node = node.Children[0]
	}
	if !node.ChildrenTruncated || len(node.Children) != 0 {
		t.Fatalf("truncated node = %+v", node)
	}
}

func TestFilesContentReturnsPreview(t *testing.T) {
	srv := newTestServer(t)
	mustWriteFile(t, filepath.Join(srv.opts.Cfg.WorkDir, "notes", "today.txt"), "line one\nline two\n")

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/files/content?path=notes%2Ftoday.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	var got FileContent
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Path != "notes/today.txt" || got.Content != "line one\nline two\n" || got.Truncated {
		t.Fatalf("content = %+v", got)
	}
}

func TestFilesContentReturnsImageMetadata(t *testing.T) {
	srv := newTestServer(t)
	mustWriteBytes(t, filepath.Join(srv.opts.Cfg.WorkDir, "screenshots", "preview.png"), tinyPNG)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/files/content?path=screenshots%2Fpreview.png")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	var got FileContent
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Path != "screenshots/preview.png" || got.Kind != "image" || got.MediaType != "image/png" {
		t.Fatalf("content metadata = %+v", got)
	}
	if got.Content != "" || got.Truncated {
		t.Fatalf("image metadata should not include text content or truncation: %+v", got)
	}
}

func TestFilesRawServesImage(t *testing.T) {
	srv := newTestServer(t)
	mustWriteBytes(t, filepath.Join(srv.opts.Cfg.WorkDir, "screenshots", "preview.png"), tinyPNG)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/files/raw?path=screenshots%2Fpreview.png")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "image/png" {
		t.Fatalf("content type = %q", got)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, tinyPNG) {
		t.Fatalf("image body = %x, want %x", body, tinyPNG)
	}
}

func TestFilesRawRejectsEscapesAndNonImages(t *testing.T) {
	srv := newTestServer(t)
	work := srv.opts.Cfg.WorkDir
	outside := filepath.Join(t.TempDir(), "secret.png")
	mustWriteBytes(t, outside, tinyPNG)
	mustWriteFile(t, filepath.Join(work, "notes.txt"), "hello")
	mustWriteFile(t, filepath.Join(work, "binary.dat"), string([]byte{0, 1, 2}))
	if err := os.Symlink(outside, filepath.Join(work, "secret-link")); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	cases := []struct {
		name string
		path string
		want int
	}{
		{name: "parent traversal", path: "../secret.png", want: http.StatusForbidden},
		{name: "absolute path", path: "/etc/passwd", want: http.StatusForbidden},
		{name: "outside symlink", path: "secret-link", want: http.StatusForbidden},
		{name: "binary", path: "binary.dat", want: http.StatusUnsupportedMediaType},
		{name: "text", path: "notes.txt", want: http.StatusUnsupportedMediaType},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := http.Get(ts.URL + "/api/files/raw?path=" + tc.path)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.want)
			}
		})
	}
}

func TestFilesContentRejectsEscapesAndBinary(t *testing.T) {
	srv := newTestServer(t)
	work := srv.opts.Cfg.WorkDir
	outside := filepath.Join(t.TempDir(), "secret.txt")
	mustWriteFile(t, outside, "secret")
	mustWriteFile(t, filepath.Join(work, "binary.dat"), string([]byte{0, 1, 2}))
	if err := os.Symlink(outside, filepath.Join(work, "secret-link")); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	cases := []struct {
		name string
		path string
		want int
	}{
		{name: "parent traversal", path: "../secret.txt", want: http.StatusForbidden},
		{name: "absolute path", path: "/etc/passwd", want: http.StatusForbidden},
		{name: "outside symlink", path: "secret-link", want: http.StatusForbidden},
		{name: "binary", path: "binary.dat", want: http.StatusUnsupportedMediaType},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := http.Get(ts.URL + "/api/files/content?path=" + tc.path)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.want)
			}
		})
	}
}

func TestFilesContentTruncatesLargeFiles(t *testing.T) {
	srv := newTestServer(t)
	mustWriteFile(t, filepath.Join(srv.opts.Cfg.WorkDir, "large.txt"), strings.Repeat("a", maxFilePreviewBytes+10))

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/files/content?path=large.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	var got FileContent
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if !got.Truncated || len(got.Content) != maxFilePreviewBytes {
		t.Fatalf("truncated = %v len = %d", got.Truncated, len(got.Content))
	}
}

var tinyPNG = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0x00, 0x00, 0x00, 0x00}

func mustWriteFile(t *testing.T, path, body string) {
	t.Helper()
	mustWriteBytes(t, path, []byte(body))
}

func mustWriteBytes(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func childNames(nodes []*FileNode) []string {
	names := make([]string, 0, len(nodes))
	for _, n := range nodes {
		names = append(names, n.Name)
	}
	return names
}
