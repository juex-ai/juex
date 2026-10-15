package agenthttp

import (
	"context"
	"encoding/json"
	"github.com/juex-ai/juex/internal/framework/runtime"
	"github.com/juex-ai/juex/internal/framework/thread"
	"io"
	"net/http"
	"path/filepath"
	"sync"

	"github.com/juex-ai/juex/internal/foundation/cancellation"
	"github.com/juex-ai/juex/internal/framework/endpoint"
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
	runtime.ConfigRevision = s.opts.Cfg.AgentConfigRevision
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
	var rollback func()
	if r.URL.Path == "/api/control/shutdown-idle" {
		var reserved bool
		rollback, reserved = s.reserveIdleShutdown(r.Context())
		if !reserved {
			if r.Context().Err() == nil {
				writeErr(w, http.StatusConflict, "runtime_busy", "Agent has active or pending work; retry when idle")
			}
			return
		}
		defer func() {
			if rollback != nil {
				rollback()
			}
		}()
		if r.Context().Err() != nil {
			return
		}
		response.IdleReserved = true
	}
	if request.Reason == endpoint.ShutdownReasonRuntimeRestart {
		s.threads.Range(func(_, value any) bool {
			active, _ := value.(*activeThread)
			if active != nil && active.turns != nil {
				active.turns.interruptWithCause(cancellation.ErrRuntimeRestart)
			}
			return true
		})
		response.RestartIntent = endpoint.ShutdownReasonRuntimeRestart
	}
	writeJSON(w, http.StatusAccepted, response)
	if rollback != nil && r.Context().Err() != nil {
		return
	}
	select {
	case shutdown <- struct{}{}:
	default:
	}
	rollback = nil
}

func (s *Server) reserveIdleShutdown(ctx context.Context) (func(), bool) {
	if ctx.Err() != nil {
		return nil, false
	}
	if !s.createMu.TryLock() {
		return nil, false
	}
	defer s.createMu.Unlock()
	if s.isClosed() {
		return nil, false
	}
	if _, ok := s.threads.Load(thread.MainID); !ok {
		return nil, false
	}
	var releases []func()
	release := func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
		s.closeMu.Lock()
		s.draining = false
		s.closeMu.Unlock()
	}
	idle := true
	s.threads.Range(func(_, value any) bool {
		if ctx.Err() != nil {
			idle = false
			return false
		}
		active, _ := value.(*activeThread)
		// Managed Worker entries borrow their parent's Agent tree and are
		// reserved recursively by the owning root.
		if active == nil || !active.ownsAgent {
			return true
		}
		undo, err := active.agent.ReserveIdleMaintenance()
		if err != nil {
			idle = false
			return false
		}
		releases = append(releases, undo)
		return true
	})
	// Dormant Threads can own durable inputs even when they have no live
	// execution object. Active roots are fenced before reading those records.
	if idle {
		store := thread.NewStore(s.opts.Cfg.RuntimePaths().StateDir)
		entries, err := store.List()
		if err != nil {
			idle = false
		}
		for _, entry := range entries {
			if !idle || ctx.Err() != nil {
				break
			}
			if entry.RetentionState != thread.RetentionActive {
				continue
			}
			if _, loaded := s.threads.Load(entry.ThreadID); loaded {
				continue
			}
			if s.managedWorkerAgent(entry.ThreadID) != nil {
				continue
			}
			pending, err := runtime.HasStoredUnsettledInput(filepath.Join(store.ThreadsDir(), entry.ThreadID))
			if err != nil || pending {
				idle = false
			}
		}
	}
	if !idle || ctx.Err() != nil {
		release()
		return nil, false
	}
	s.closeMu.Lock()
	s.draining = true
	s.closeMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.createMu.Lock()
			defer s.createMu.Unlock()
			release()
		})
	}, true
}
