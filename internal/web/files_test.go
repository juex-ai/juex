package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/config"
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

func TestSessionScratchpadTreeReturnsScopedFiles(t *testing.T) {
	srv := newTestServer(t)
	id := "20260507T101010-pad001"
	seedSession(t, srv.opts.Cfg.WorkDir, id,
		`{"role":"user","blocks":[{"type":"text","text":"hi"}]}`+"\n")
	root := filepath.Join(srv.opts.Cfg.SessionsDir(), id, "scratchpad")
	mustWriteFile(t, filepath.Join(root, "draft.md"), "draft")
	mustWriteFile(t, filepath.Join(root, "dist", "result.txt"), "kept")

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/sessions/" + id + "/scratchpad")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body = %s", resp.StatusCode, body)
	}
	var tree FileNode
	if err := json.NewDecoder(resp.Body).Decode(&tree); err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.ToSlash(filepath.Join(".juex", "sessions", id, "scratchpad"))
	if tree.Name != "scratchpad" || tree.Path != wantPath || !tree.IsDir {
		t.Fatalf("root = %+v, want scoped scratchpad %q", tree, wantPath)
	}
	if got, want := strings.Join(childNames(tree.Children), ","), "dist,draft.md"; got != want {
		t.Fatalf("children = %q, want %q", got, want)
	}
	if got := tree.Children[0].Children[0].Path; got != wantPath+"/dist/result.txt" {
		t.Fatalf("nested path = %q", got)
	}
}

func TestSessionScratchpadTreeAndPreviewUseAgentStateDir(t *testing.T) {
	srv := newTestServer(t)
	srv.opts.Cfg.AgentStateDir = filepath.Join(t.TempDir(), "agent")
	id := "20260717T131800-agenthome"
	sessionDir := filepath.Join(srv.opts.Cfg.SessionsDir(), id)
	mustWriteFile(t, filepath.Join(sessionDir, "conversation.jsonl"),
		`{"role":"user","blocks":[{"type":"text","text":"hi"}]}`+"\n")
	mustWriteFile(t, filepath.Join(sessionDir, "scratchpad", "draft.md"), "agent-home draft")

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/sessions/" + id + "/scratchpad")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("tree status = %d body = %s", resp.StatusCode, body)
	}
	var tree FileNode
	if err := json.NewDecoder(resp.Body).Decode(&tree); err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.ToSlash(filepath.Join(".juex", "sessions", id, "scratchpad"))
	if tree.Path != wantPath || len(tree.Children) != 1 || tree.Children[0].Path != wantPath+"/draft.md" {
		t.Fatalf("tree = %+v, want logical path %q with draft.md", tree, wantPath)
	}

	preview, err := http.Get(ts.URL + "/api/files/content?path=" + url.QueryEscape(tree.Children[0].Path))
	if err != nil {
		t.Fatal(err)
	}
	defer preview.Body.Close()
	if preview.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(preview.Body)
		t.Fatalf("preview status = %d body = %s", preview.StatusCode, body)
	}
	var content FileContent
	if err := json.NewDecoder(preview.Body).Decode(&content); err != nil {
		t.Fatal(err)
	}
	if content.Path != wantPath+"/draft.md" || content.Content != "agent-home draft" {
		t.Fatalf("preview = %+v", content)
	}
}

func TestSessionScratchpadTreeDoesNotPersistLazySession(t *testing.T) {
	srv := newTestServer(t)
	as, err := srv.openSession(t.Context(), "", app.SessionModeNewPrimary)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(as.app.Session.Dir, "conversation.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("lazy conversation stat err = %v, want not exist", err)
	}
	if _, err := os.Stat(filepath.Join(as.app.Session.Dir, "scratchpad")); !os.IsNotExist(err) {
		t.Fatalf("lazy scratchpad stat err = %v, want not exist", err)
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/api/sessions/" + as.app.Session.ID + "/scratchpad")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body = %s", resp.StatusCode, body)
	}
	var tree FileNode
	if err := json.NewDecoder(resp.Body).Decode(&tree); err != nil {
		t.Fatal(err)
	}
	if tree.Name != "scratchpad" || !tree.IsDir || len(tree.Children) != 0 {
		t.Fatalf("lazy scratchpad tree = %+v", tree)
	}
	if _, err := os.Stat(filepath.Join(as.app.Session.Dir, "conversation.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("scratchpad read persisted lazy conversation: %v", err)
	}
	if _, err := os.Stat(filepath.Join(as.app.Session.Dir, "scratchpad")); !os.IsNotExist(err) {
		t.Fatalf("scratchpad read created lazy directory: %v", err)
	}
}

func TestSessionScratchpadTreeRejectsUnknownSession(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/sessions/missing/scratchpad")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body = %s", resp.StatusCode, body)
	}
}

func TestSessionScratchpadTreeSupportsSymlinkedWorkspace(t *testing.T) {
	realWork := t.TempDir()
	linkedWork := filepath.Join(t.TempDir(), "work")
	if err := os.Symlink(realWork, linkedWork); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(Options{
		Cfg: config.Config{
			ProviderID: "openai",
			APIKey:     "x",
			Model:      "m",
			WorkDir:    linkedWork,
			Compaction: config.DefaultCompactionConfig(),
		},
		Provider: stubProvider{},
	})
	t.Cleanup(srv.Close)

	id := "20260507T101010-linked"
	seedSession(t, linkedWork, id,
		`{"role":"user","blocks":[{"type":"text","text":"hi"}]}`+"\n")
	mustWriteFile(t, filepath.Join(srv.opts.Cfg.SessionsDir(), id, "scratchpad", "draft.md"), "draft")

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/api/sessions/" + id + "/scratchpad")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body = %s", resp.StatusCode, body)
	}
	var tree FileNode
	if err := json.NewDecoder(resp.Body).Decode(&tree); err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.ToSlash(filepath.Join(".juex", "sessions", id, "scratchpad"))
	if tree.Path != wantPath || len(tree.Children) != 1 || tree.Children[0].Name != "draft.md" {
		t.Fatalf("tree = %+v, want path %q with draft.md", tree, wantPath)
	}

	as, err := srv.openSession(t.Context(), "", app.SessionModeNewPrimary)
	if err != nil {
		t.Fatal(err)
	}
	lazyResp, err := http.Get(ts.URL + "/api/sessions/" + as.app.Session.ID + "/scratchpad")
	if err != nil {
		t.Fatal(err)
	}
	defer lazyResp.Body.Close()
	if lazyResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(lazyResp.Body)
		t.Fatalf("lazy status = %d body = %s", lazyResp.StatusCode, body)
	}
	if _, err := os.Stat(as.app.Session.ScratchpadDir()); !os.IsNotExist(err) {
		t.Fatalf("scratchpad read created lazy directory: %v", err)
	}
}

func TestSessionScratchpadTreeRejectsOutsideSymlink(t *testing.T) {
	srv := newTestServer(t)
	id := "20260507T101010-linked-out"
	seedSession(t, srv.opts.Cfg.WorkDir, id,
		`{"role":"user","blocks":[{"type":"text","text":"hi"}]}`+"\n")
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(srv.opts.Cfg.SessionsDir(), id, "scratchpad")); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/api/sessions/" + id + "/scratchpad")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body = %s", resp.StatusCode, body)
	}
}

func TestSessionScratchpadTreeRejectsInsideSymlink(t *testing.T) {
	srv := newTestServer(t)
	id := "20260507T101010-linked-in"
	seedSession(t, srv.opts.Cfg.WorkDir, id,
		`{"role":"user","blocks":[{"type":"text","text":"hi"}]}`+"\n")
	if err := os.Symlink(srv.opts.Cfg.WorkDir, filepath.Join(srv.opts.Cfg.SessionsDir(), id, "scratchpad")); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/api/sessions/" + id + "/scratchpad")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body = %s", resp.StatusCode, body)
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

func TestFilesContentReturnsBMPMetadata(t *testing.T) {
	srv := newTestServer(t)
	mustWriteBytes(t, filepath.Join(srv.opts.Cfg.WorkDir, "screenshots", "preview.bmp"), tinyBMP)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/files/content?path=screenshots%2Fpreview.bmp")
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
	if got.Kind != "image" || got.MediaType != "image/bmp" || got.Size != int64(len(tinyBMP)) {
		t.Fatalf("content metadata = %+v", got)
	}
}

func TestFilesContentDoesNotTrustImageExtension(t *testing.T) {
	srv := newTestServer(t)
	mustWriteFile(t, filepath.Join(srv.opts.Cfg.WorkDir, "screenshots", "not-an-image.png"), "plain text")

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/files/content?path=screenshots%2Fnot-an-image.png")
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
	if got.Kind != "text" || got.MediaType != "" || got.Content != "plain text" {
		t.Fatalf("content metadata = %+v", got)
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

func TestMediaServesWorkDirImageWithRevalidationCache(t *testing.T) {
	srv := newTestServer(t)
	imagePath := filepath.Join(srv.opts.Cfg.WorkDir, "screenshots", "preview.png")
	mustWriteBytes(t, imagePath, tinyPNG)
	info, err := os.Stat(imagePath)
	if err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/media?path=screenshots%2Fpreview.png")
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
	if got := resp.Header.Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("cache-control = %q", got)
	}
	wantETag := fmt.Sprintf(`W/"%x-%x"`, info.ModTime().UnixNano(), info.Size())
	if got := resp.Header.Get("ETag"); got != wantETag {
		t.Fatalf("etag = %q, want %q", got, wantETag)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, tinyPNG) {
		t.Fatalf("media body = %x, want %x", body, tinyPNG)
	}
}

func TestMediaHeadReturnsImageMetadataWithoutBody(t *testing.T) {
	srv := newTestServer(t)
	mustWriteBytes(t, filepath.Join(srv.opts.Cfg.WorkDir, "screenshots", "preview.png"), tinyPNG)
	mustWriteFile(t, filepath.Join(srv.opts.Cfg.WorkDir, "notes.txt"), "plain text")

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Head(ts.URL + "/api/media?path=screenshots%2Fpreview.png")
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
	if got := resp.Header.Get("Content-Length"); got != strconv.Itoa(len(tinyPNG)) {
		t.Fatalf("content length = %q", got)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != 0 {
		t.Fatalf("HEAD body length = %d", len(body))
	}

	resp, err = http.Head(ts.URL + "/api/media?path=notes.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("non-image status = %d", resp.StatusCode)
	}
}

func TestMediaServesArtifactImage(t *testing.T) {
	srv := newTestServer(t)
	digest := "1b56b50ac4e976f488f128cabdcdffb2fc9331d6974bb9968131a415d14ade24"
	artifactPath := filepath.Join(".juex", "artifacts", "media", "session", digest+".png")
	mustWriteBytes(t, filepath.Join(srv.opts.Cfg.WorkDir, artifactPath), tinyPNG)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/media?path=" + url.QueryEscape(filepath.ToSlash(artifactPath)))
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
	if got := resp.Header.Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("cache-control = %q", got)
	}
	if got := resp.Header.Get("ETag"); got != `"`+digest+`"` {
		t.Fatalf("etag = %q", got)
	}
}

func TestMediaRejectsEscapesAndNonImages(t *testing.T) {
	srv := newTestServer(t)
	work := srv.opts.Cfg.WorkDir
	outside := filepath.Join(t.TempDir(), "secret.png")
	mustWriteBytes(t, outside, tinyPNG)
	mustWriteFile(t, filepath.Join(work, "notes.txt"), "hello")
	mustWriteFile(t, filepath.Join(work, "not-an-image.png"), "plain text")
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
		{name: "text", path: "notes.txt", want: http.StatusUnsupportedMediaType},
		{name: "misleading extension", path: "not-an-image.png", want: http.StatusUnsupportedMediaType},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := http.Get(ts.URL + "/api/media?path=" + tc.path)
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
var tinyBMP = []byte{'B', 'M', 0x00, 0x00, 0x00, 0x00}

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
