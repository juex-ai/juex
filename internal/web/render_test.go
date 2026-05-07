package web

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/session"
)

func TestRenderer_IndexShowsSessions(t *testing.T) {
	r, err := newRenderer()
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	data := struct {
		Title    string
		Sessions []session.Info
	}{
		Title: "sessions",
		Sessions: []session.Info{
			{ID: "20260507T101010-aaaa", Turns: 2, Preview: "hello", LastActiveAt: time.Now()},
		},
	}
	if err := r.Render(&buf, "index.html", data); err != nil {
		t.Fatal(err)
	}
	body := buf.String()
	for _, want := range []string{"juex", "20260507T101010-aaaa", "hello", "/sessions/new"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
}

func TestStaticFileServer_ServesEmbeddedCSS(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", staticFileServer()))
	ts := httptest.NewServer(mux)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "role-user") {
		t.Errorf("body did not contain role-user; got: %s", body)
	}
}
