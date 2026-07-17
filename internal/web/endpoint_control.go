package web

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/juex-ai/juex/internal/endpoint"
)

func (s *Server) setEndpointControl(runtime endpoint.Runtime) <-chan struct{} {
	s.endpointMu.Lock()
	defer s.endpointMu.Unlock()
	shutdown := make(chan struct{}, 1)
	s.endpointRuntime = runtime
	s.endpointShutdown = shutdown
	return shutdown
}

func (s *Server) clearEndpointControl(runtime endpoint.Runtime) {
	s.endpointMu.Lock()
	defer s.endpointMu.Unlock()
	if s.endpointRuntime.Matches(runtime) {
		s.endpointRuntime = endpoint.Runtime{}
		s.endpointShutdown = nil
	}
}

func (s *Server) endpointControl() (endpoint.Runtime, chan struct{}, bool) {
	s.endpointMu.RLock()
	defer s.endpointMu.RUnlock()
	if s.endpointShutdown == nil {
		return endpoint.Runtime{}, nil, false
	}
	return s.endpointRuntime, s.endpointShutdown, true
}

func (s *Server) handleEndpointIdentity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	runtime, _, ok := s.endpointControl()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "endpoint_unavailable", "agent endpoint is not running")
		return
	}
	writeJSON(w, http.StatusOK, runtime)
}

func (s *Server) handleEndpointShutdown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	var expected endpoint.Runtime
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&expected); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid runtime identity")
		return
	}
	actual, shutdown, ok := s.endpointControl()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "endpoint_unavailable", "agent endpoint is not running")
		return
	}
	if !actual.Matches(expected) {
		writeErr(w, http.StatusConflict, "identity_mismatch", "runtime identity does not match")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "stopping"})
	select {
	case shutdown <- struct{}{}:
	default:
	}
}
