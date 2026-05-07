package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestGetSessionShow_ReturnsTranscript(t *testing.T) {
	srv := newTestServer(t)
	id := "20260507T101010-show01"
	body := `{"role":"user","blocks":[{"type":"text","text":"hi"}]}` + "\n" +
		`{"role":"assistant","blocks":[{"type":"text","text":"hello"}]}` + "\n"
	seedSession(t, srv.opts.Cfg.WorkDir, id, body)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/sessions/" + id)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var parsed struct {
		ID       string `json:"id"`
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.ID != id || len(parsed.Messages) != 2 {
		t.Errorf("got %+v", parsed)
	}
}

func TestGetSessionShow_NotFound(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, _ := http.Get(ts.URL + "/api/sessions/missing")
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

func TestPostCreateSession_ReturnsIDAndDir(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/sessions", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var parsed struct {
		ID  string `json:"id"`
		Dir string `json:"dir"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.ID == "" || parsed.Dir == "" {
		t.Errorf("got %+v", parsed)
	}
	// The created session must show up in subsequent List call.
	resp2, _ := http.Get(ts.URL + "/api/sessions")
	defer resp2.Body.Close()
	body, _ := io.ReadAll(resp2.Body)
	if !strings.Contains(string(body), parsed.ID) {
		t.Errorf("created id %q not found in list:\n%s", parsed.ID, body)
	}
}

func TestPostTurn_StartsTurnAndPersists(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Create a session first.
	created, _ := http.Post(ts.URL+"/api/sessions", "application/json", nil)
	var c struct{ ID string }
	json.NewDecoder(created.Body).Decode(&c)
	created.Body.Close()

	// Submit a turn.
	body := strings.NewReader(`{"prompt":"hi"}`)
	resp, err := http.Post(ts.URL+"/api/sessions/"+c.ID+"/turns", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 202 {
		t.Errorf("status = %d", resp.StatusCode)
	}
	var got struct {
		TurnID string `json:"turn_id"`
	}
	json.NewDecoder(resp.Body).Decode(&got)
	if got.TurnID == "" {
		t.Errorf("missing turn_id")
	}

	// Wait briefly for the goroutine to finish (stub provider returns immediately).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		show, _ := http.Get(ts.URL + "/api/sessions/" + c.ID)
		body, _ := io.ReadAll(show.Body)
		show.Body.Close()
		if strings.Contains(string(body), `"text":"ack"`) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out waiting for ack to be persisted")
}
