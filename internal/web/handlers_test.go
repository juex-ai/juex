package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// seedSession writes a minimal conversation.jsonl under
// <work>/.agents/sessions/<id>/ so session.List can find it.
func seedSession(t *testing.T, work, id, body string) {
	t.Helper()
	dir := filepath.Join(work, ".agents", "sessions", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "conversation.jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGetSessionsList_ReturnsSeededSession(t *testing.T) {
	srv := newTestServer(t)
	seedSession(t, srv.opts.Cfg.WorkDir, "20260507T101010-aaaa11",
		`{"role":"user","blocks":[{"type":"text","text":"hi"}]}`+"\n")

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var parsed struct {
		Sessions []struct {
			ID      string `json:"id"`
			Preview string `json:"preview"`
		} `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Sessions) != 1 || parsed.Sessions[0].ID != "20260507T101010-aaaa11" {
		t.Errorf("got %+v", parsed.Sessions)
	}
	if parsed.Sessions[0].Preview != "hi" {
		t.Errorf("preview = %q", parsed.Sessions[0].Preview)
	}
}
