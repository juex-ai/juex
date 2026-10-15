package mcp

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSecureEndpointHTTPClientRejectsDowngradeRedirect(t *testing.T) {
	redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://mcp.example.test/insecure", http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	transport := &insecureCountingTransport{base: redirect.Client().Transport}
	client := newSecureEndpointHTTPClient(transport)
	request, err := http.NewRequest(http.MethodPost, redirect.URL, strings.NewReader("refresh_token=secret"))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer secret")
	_, err = client.Do(request)
	if err == nil || !strings.Contains(err.Error(), "insecure redirect") {
		t.Fatalf("Do error = %v, want insecure redirect rejection", err)
	}
	if got := transport.insecureRequests.Load(); got != 0 {
		t.Fatalf("downgrade target received %d requests", got)
	}
}

func TestSecureEndpointHTTPClientAllowsHTTPSRedirect(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/target", http.StatusTemporaryRedirect)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := newSecureEndpointHTTPClient(server.Client().Transport)
	response, err := client.Get(server.URL + "/start")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusNoContent)
	}
}

func TestRemoteHeadersStayOnConfiguredOrigin(t *testing.T) {
	const secret = "redirect-secret"
	targetHeader := make(chan string, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHeader <- r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	sourceHeader := make(chan string, 1)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sourceHeader <- r.Header.Get("Authorization")
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	endpoint, err := url.Parse(source.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := newSecureEndpointHTTPClient(&remoteDiagnosticRoundTripper{
		base:     http.DefaultTransport,
		endpoint: endpoint,
		headers: map[string]Credential{
			"Authorization": {value: "Bearer " + secret},
		},
	})
	response, err := client.Get(source.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if got := <-sourceHeader; got != "Bearer "+secret {
		t.Fatalf("configured origin Authorization = %q", got)
	}
	if got := <-targetHeader; got != "" {
		t.Fatalf("redirect target received Authorization = %q", got)
	}
}

type insecureCountingTransport struct {
	base             http.RoundTripper
	insecureRequests atomic.Int32
}

func (t *insecureCountingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme == "http" {
		t.insecureRequests.Add(1)
	}
	return t.base.RoundTrip(request)
}
