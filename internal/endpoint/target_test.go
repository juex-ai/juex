package endpoint

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"runtime"
	"testing"
)

func TestParseTargets(t *testing.T) {
	unixPath := filepath.Join(t.TempDir(), "agent api.sock")
	unixTarget, err := Parse(unixURI(unixPath))
	if err != nil {
		t.Fatalf("parse unix target: %v", err)
	}
	if unixTarget.Network() != "unix" || unixTarget.Address() != unixPath {
		t.Fatalf("unix target = %s %q, want unix %q", unixTarget.Network(), unixTarget.Address(), unixPath)
	}
	if got := unixTarget.URI(); got != unixURI(unixPath) {
		t.Fatalf("unix URI = %q, want %q", got, unixURI(unixPath))
	}

	tcpTarget, err := Parse("tcp://127.0.0.1:43123")
	if err != nil {
		t.Fatalf("parse tcp target: %v", err)
	}
	if tcpTarget.Network() != "tcp" || tcpTarget.Address() != "127.0.0.1:43123" {
		t.Fatalf("tcp target = %s %q", tcpTarget.Network(), tcpTarget.Address())
	}

	for _, raw := range []string{
		"http://127.0.0.1:43123",
		"tcp://192.0.2.1:43123",
		"tcp://localhost:43123",
		"tcp://127.0.0.1:0",
		"tcp://user@127.0.0.1:43123",
		"tcp://127.0.0.1:43123/path",
		"tcp://127.0.0.1:43123?x=1",
		"unix://relative.sock",
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := Parse(raw); err == nil {
				t.Fatalf("Parse(%q) succeeded, want error", raw)
			}
		})
	}

	if runtime.GOOS == "windows" {
		target, err := Parse("unix:///C:/Users/test/agent/api.sock")
		if err != nil {
			t.Fatalf("parse Windows unix target: %v", err)
		}
		if target.URI() != "unix:///C:/Users/test/agent/api.sock" {
			t.Fatalf("Windows unix URI = %q", target.URI())
		}
	}
}

func TestTargetHTTPClientDialsParsedEndpoint(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "endpoint-ok")
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	target, err := Parse("tcp://" + listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	client := target.NewClient()
	if client.Timeout != 0 {
		t.Fatalf("client timeout = %s, want no global timeout", client.Timeout)
	}
	response, err := client.Get(target.URL("/healthz"))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "endpoint-ok\n" {
		t.Fatalf("body = %q", body)
	}
}
