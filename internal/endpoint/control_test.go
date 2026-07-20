package endpoint

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

func TestProbeRequiresExactRuntimeIdentity(t *testing.T) {
	runtimeState := Runtime{
		AgentID:    "abcdefghijklmnop",
		InstanceID: "instance-one",
		PID:        42,
		StartedAt:  time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC),
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != identityPath {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(runtimeState)
	}))
	defer server.Close()
	runtimeState.Endpoint = "tcp://" + server.Listener.Addr().String()

	if err := Probe(context.Background(), runtimeState); err != nil {
		t.Fatalf("Probe exact identity: %v", err)
	}
	mismatch := runtimeState
	mismatch.InstanceID = "reused-pid-or-endpoint"
	err := Probe(context.Background(), mismatch)
	var identityErr *IdentityMismatchError
	if !errors.As(err, &identityErr) {
		t.Fatalf("Probe mismatch error = %T %v, want IdentityMismatchError", err, err)
	}
	if identityErr.Actual != runtimeState {
		t.Fatalf("actual runtime = %+v, want %+v", identityErr.Actual, runtimeState)
	}
}

func TestProbeDoesNotRetainUnixConnections(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix sockets are unavailable on Windows")
	}

	socketDir, err := os.MkdirTemp("", "juex-probe-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(socketDir); err != nil {
			t.Errorf("remove socket directory: %v", err)
		}
	})
	socketPath := filepath.Join(socketDir, "agent.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}

	expected := Runtime{
		AgentID:    "abcdefghijklmnop",
		InstanceID: "instance-one",
		PID:        42,
		Endpoint:   (&url.URL{Scheme: "unix", Path: socketPath}).String(),
		StartedAt:  time.Date(2026, 7, 19, 8, 0, 0, 0, time.UTC),
	}
	var connections endpointConnectionCounter
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != identityPath {
				http.NotFound(w, r)
				return
			}
			if err := json.NewEncoder(w).Encode(expected); err != nil {
				t.Errorf("encode identity: %v", err)
			}
		}),
		ConnState: connections.track,
	}
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- server.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = server.Close()
		if err := <-serveDone; err != nil && err != http.ErrServerClosed {
			t.Errorf("serve identity endpoint: %v", err)
		}
	})

	for range 32 {
		if err := Probe(context.Background(), expected); err != nil {
			t.Fatal(err)
		}
	}

	waitForEndpointConnectionCount(t, &connections.open, 0)
}

func TestProbeDoesNotRetainTCPConnections(t *testing.T) {
	expected := Runtime{
		AgentID:    "abcdefghijklmnop",
		InstanceID: "instance-one",
		PID:        42,
		StartedAt:  time.Date(2026, 7, 19, 8, 0, 0, 0, time.UTC),
	}
	var connections endpointConnectionCounter
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != identityPath {
			http.NotFound(w, r)
			return
		}
		if err := json.NewEncoder(w).Encode(expected); err != nil {
			t.Errorf("encode identity: %v", err)
		}
	}))
	server.Config.ConnState = connections.track
	server.Start()
	t.Cleanup(server.Close)
	expected.Endpoint = "tcp://" + server.Listener.Addr().String()

	for range 32 {
		if err := Probe(context.Background(), expected); err != nil {
			t.Fatal(err)
		}
	}

	waitForEndpointConnectionCount(t, &connections.open, 0)
}

func TestRequestShutdownSendsExpectedRuntime(t *testing.T) {
	expected := Runtime{
		AgentID:    "abcdefghijklmnop",
		InstanceID: "instance-one",
		PID:        42,
		StartedAt:  time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC),
	}
	received := make(chan Runtime, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != shutdownPath || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var request Runtime
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		received <- request
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	expected.Endpoint = "tcp://" + server.Listener.Addr().String()

	if err := RequestShutdown(context.Background(), expected); err != nil {
		t.Fatal(err)
	}
	if got := <-received; got != expected {
		t.Fatalf("shutdown runtime = %+v, want %+v", got, expected)
	}
}

func TestRequestRestartNegotiatesRestartIntent(t *testing.T) {
	expected := Runtime{
		AgentID:    "abcdefghijklmnop",
		InstanceID: "instance-one",
		PID:        42,
		StartedAt:  time.Date(2026, 7, 20, 8, 0, 0, 0, time.UTC),
	}
	var received struct {
		Runtime
		Reason ShutdownReason `json:"reason"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != shutdownPath || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]ShutdownReason{
			"restart_intent": ShutdownReasonRuntimeRestart,
		})
	}))
	defer server.Close()
	expected.Endpoint = "tcp://" + server.Listener.Addr().String()

	acknowledged, err := RequestRestart(context.Background(), expected)
	if err != nil {
		t.Fatal(err)
	}
	if !acknowledged {
		t.Fatal("restart intent was not acknowledged")
	}
	if received.Runtime != expected || received.Reason != ShutdownReasonRuntimeRestart {
		t.Fatalf("restart request = %+v, want runtime %+v and reason %q", received, expected, ShutdownReasonRuntimeRestart)
	}
}

func TestRequestRestartRequiresRestartIntentAcknowledgement(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"stopping"}`))
	}))
	defer server.Close()

	acknowledged, err := RequestRestart(context.Background(), Runtime{
		AgentID:    "abcdefghijklmnop",
		InstanceID: "instance-one",
		Endpoint:   "tcp://" + server.Listener.Addr().String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if acknowledged {
		t.Fatal("response without restart intent unexpectedly acknowledged restart")
	}
}

func waitForEndpointConnectionCount(t *testing.T, count *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for count.Load() != want && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := count.Load(); got != want {
		t.Fatalf("open connections = %d, want %d", got, want)
	}
}

type endpointConnectionCounter struct {
	open atomic.Int32
}

func (c *endpointConnectionCounter) track(_ net.Conn, state http.ConnState) {
	switch state {
	case http.StateNew:
		c.open.Add(1)
	case http.StateHijacked, http.StateClosed:
		c.open.Add(-1)
	}
}
