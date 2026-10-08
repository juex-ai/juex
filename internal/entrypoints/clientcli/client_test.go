package clientcli

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientPrivateCAStillVerifiesServerIdentity(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	call := func(origin, caFile string) error {
		c, err := (options{server: origin, caFile: caFile, sessionFile: filepath.Join(dir, "session")}).open(false)
		if err != nil {
			return err
		}
		_, err = c.request(context.Background(), "GET", "/tenants", nil)
		return err
	}
	if err := call(server.URL, ""); err == nil {
		t.Fatal("private CA was implicitly trusted")
	}
	if err := call(server.URL, ca); err != nil {
		t.Fatal(err)
	}
	wrongName, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	wrongName.Host = "localhost:" + wrongName.Port()
	if err := call(wrongName.String(), ca); err == nil {
		t.Fatal("CA bypassed hostname verification")
	}
	if err := os.WriteFile(ca, []byte("invalid certificate"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := call(server.URL, ca); err == nil {
		t.Fatal("invalid CA accepted")
	}
}

func TestClientOriginAndCredentialProtection(t *testing.T) {
	var leaked atomic.Bool
	outside := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true) }))
	defer outside.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer private-token" || r.Header.Get("X-Juex-Client") != "cli" {
			t.Error("missing CLI authentication")
		}
		http.Redirect(w, r, outside.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session.json")
	session := loginSession{Origin: server.URL, Token: "private-token", ExpiresAt: time.Now().Add(time.Hour)}
	data, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, server string
		insecure     bool
	}{
		{"http requires opt-in", server.URL, false},
		{"origin mismatch", outside.URL, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := (options{server: tc.server, insecure: tc.insecure, sessionFile: path}).open(true); err == nil {
				t.Fatal("accepted unsafe session")
			}
		})
	}
	c, err := (options{server: server.URL, insecure: true, sessionFile: path}).open(true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.request(context.Background(), "GET", "/tenants", nil); err == nil || !strings.Contains(err.Error(), "307") {
		t.Fatal(err)
	}
	for _, target := range []string{outside.URL, "//outside.test", "/tenants#fragment"} {
		if _, err := c.request(context.Background(), "GET", target, nil); err == nil {
			t.Fatal("accepted non-API target", target)
		}
	}
	if leaked.Load() {
		t.Fatal("credentials followed a redirect")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readSession(path, server.URL); err == nil {
		t.Fatal("accepted public credentials")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readSession(link, server.URL); err == nil {
		t.Fatal("accepted symlink credentials")
	}
}

func TestClientReportsStableInputIdentityOnUncertainResponse(t *testing.T) {
	const tenant = "27708230-bc92-44f9-89bc-bbd07cc75041"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tenants" {
			_, _ = w.Write([]byte(`[{"id":"` + tenant + `"}]`))
			return
		}
		http.Error(w, `{"error":"temporarily unavailable"}`, http.StatusServiceUnavailable)
	}))
	defer server.Close()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	c := client{options: options{sessionFile: filepath.Join(dir, "session.json")}, origin: server.URL, session: loginSession{Origin: server.URL, Token: "secret", ExpiresAt: time.Now().Add(time.Hour)}}
	if err := c.saveSession(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := Execute(context.Background(), []string{"--server", server.URL, "--insecure-http", "--session-file", c.sessionFile, "thread", "--agent", tenant, "send", tenant, "--request-id", "retry-this-input"}, strings.NewReader("message"), &out, &out)
	if err == nil || !strings.Contains(err.Error(), "retry-this-input") || !strings.Contains(err.Error(), "503") {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatal("failed command emitted success", out.String())
	}
}
