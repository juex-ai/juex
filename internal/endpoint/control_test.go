package endpoint

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
