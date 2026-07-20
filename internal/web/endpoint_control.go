package web

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/juex-ai/juex/internal/cancellation"
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
	var request endpoint.ShutdownRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&request); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid runtime identity")
		return
	}
	if request.Reason != "" && request.Reason != endpoint.ShutdownReasonRuntimeRestart {
		writeErr(w, http.StatusBadRequest, "bad_request", "unsupported shutdown reason")
		return
	}
	actual, shutdown, ok := s.endpointControl()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "endpoint_unavailable", "agent endpoint is not running")
		return
	}
	if !actual.Matches(request.Runtime) {
		writeErr(w, http.StatusConflict, "identity_mismatch", "runtime identity does not match")
		return
	}
	response := endpoint.ShutdownResponse{Status: "stopping"}
	if request.Reason == endpoint.ShutdownReasonRuntimeRestart {
		s.sessions.Range(func(_, value any) bool {
			active, _ := value.(*activeSession)
			if active != nil && active.turns != nil {
				active.turns.interruptWithCause(cancellation.ErrRuntimeRestart)
			}
			return true
		})
		response.RestartIntent = endpoint.ShutdownReasonRuntimeRestart
	}
	writeJSON(w, http.StatusAccepted, response)
	select {
	case shutdown <- struct{}{}:
	default:
	}
}
