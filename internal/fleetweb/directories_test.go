package fleetweb

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/juex-ai/juex/internal/agentstate"
)

func TestDirectoryAPIListsOnlySafeDirectoriesAndMarkers(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"visible", ".hidden", "registered", "restricted"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "visible"), filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	writeDirectoryTestJSON(
		t,
		filepath.Join(root, "registered", ".juex", "juex.local.json"),
		agentstate.Marker{AgentID: "aaaaaaaa"},
	)
	restrictedJuex := filepath.Join(root, "restricted", ".juex")
	if err := os.MkdirAll(restrictedJuex, 0o755); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(restrictedJuex, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(restrictedJuex, 0o755) })
	}

	server := newServer(&fakeBackend{}, Options{Addr: "127.0.0.1:0"})
	path := "/api/fs/dirs?path=" + url.QueryEscape(root)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, http.NoBody))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var listing DirectoryListing
	decodeJSON(t, recorder.Body.Bytes(), &listing)
	if listing.Path != root || listing.Parent != filepath.Dir(root) {
		t.Fatalf("listing location = %+v", listing)
	}
	if len(listing.Dirs) != 3 {
		t.Fatalf("directories = %+v, want visible, registered, restricted", listing.Dirs)
	}
	got := make(map[string]DirectoryEntry, len(listing.Dirs))
	for _, entry := range listing.Dirs {
		got[entry.Name] = entry
	}
	if !got["registered"].Registered ||
		got["visible"].Registered ||
		got["restricted"].Registered {
		t.Fatalf("registered markers = %+v", got)
	}
	for _, excluded := range []string{".hidden", "file.txt", "linked"} {
		if _, ok := got[excluded]; ok {
			t.Fatalf("unsafe or hidden entry %q included: %+v", excluded, listing.Dirs)
		}
	}

	hiddenRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(
		hiddenRecorder,
		httptest.NewRequest(http.MethodGet, path+"&show_hidden=true", http.NoBody),
	)
	if hiddenRecorder.Code != http.StatusOK {
		t.Fatalf("show hidden status = %d, body = %s", hiddenRecorder.Code, hiddenRecorder.Body.String())
	}
	var hidden DirectoryListing
	decodeJSON(t, hiddenRecorder.Body.Bytes(), &hidden)
	foundHidden := false
	for _, entry := range hidden.Dirs {
		if entry.Name == ".hidden" {
			foundHidden = true
		}
	}
	if !foundHidden {
		t.Fatalf("show hidden listing = %+v", hidden.Dirs)
	}
}

func TestDirectoryAPIRejectsRelativeAndSymlinkPaths(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	server := newServer(&fakeBackend{}, Options{Addr: "127.0.0.1:0"})
	for _, rawPath := range []string{
		"/api/fs/dirs?path=relative",
		"/api/fs/dirs?path=" + url.QueryEscape(link),
		"/api/fs/dirs?show_hidden=not-a-bool",
	} {
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(
			recorder,
			httptest.NewRequest(http.MethodGet, rawPath, http.NoBody),
		)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("GET %s status = %d, body = %s", rawPath, recorder.Code, recorder.Body.String())
		}
	}
}

func writeDirectoryTestJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
