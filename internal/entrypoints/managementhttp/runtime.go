package managementhttp

import (
	"context"
	"net/http"
	"strconv"

	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/management"
)

type Runtime interface {
	AgentRunState(context.Context, string, string, string) (managedruntime.AgentRunState, error)
	ChangeAgentLifecycle(context.Context, string, string, string, managedruntime.AgentLifecycleChange) (managedruntime.AgentLifecycleReceipt, error)
	InputChecks(context.Context, string, string, string, string, managedruntime.InputCheckQuery) (managedruntime.InputCheckPage, error)
	ObservationSources(context.Context, string, string, string, string) (managedruntime.ObservationPage, error)
	ObservedEvents(context.Context, string, string, string, string, string) (managedruntime.ObservedEvents, error)
	ObservationContent(context.Context, string, string, string, string, int, int) (managedruntime.ObservationContent, error)
	StartObserver(context.Context, string, string, string, managedruntime.ObserverStart) (managedruntime.ObserverControl, error)
	StopObserver(context.Context, string, string, string, string) error
	SetSourceSubscription(context.Context, string, string, string, string, string, bool) (managedruntime.Subscription, error)
	Status(context.Context, string, string, string) (managedruntime.RuntimeStatus, error)
	History(context.Context, string, string, string, string, int64, int) (managedruntime.Timeline, error)
	Inspection(context.Context, string, string, string, string) (managedruntime.ThreadInspection, error)
	ResetContext(context.Context, string, string, string, string, string) (managedruntime.Thread, error)
	Usage(context.Context, string, managedruntime.UsageQuery) (managedruntime.UsageReport, error)
	DeleteThread(context.Context, string, string, string, string) (managedruntime.ThreadDeletionReceipt, error)
	Archive(context.Context, string, string, string, string, bool) (managedruntime.Thread, error)
	Compact(context.Context, string, string, string, string, managedruntime.CompactionRequest) (managedruntime.InputReceipt, error)
	Submit(context.Context, string, string, string, managedruntime.InputRequest) (managedruntime.InputReceipt, error)
	Threads(context.Context, string, string, string) ([]managedruntime.Thread, error)
	Events(context.Context, string, string, string, string, int64, int) (managedruntime.Timeline, error)
	Cancel(context.Context, string, string, string, string) error
	Worker(context.Context, string, string, string, string, string, string) (managedruntime.Thread, error)
}

func (s *Server) inputChecks(w http.ResponseWriter, r *http.Request, user management.User) {
	var query managedruntime.InputCheckQuery
	if err := decode(r, &query); err != nil {
		respond(w, nil, err)
		return
	}
	v, err := s.options.Runtime.InputChecks(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"), r.PathValue("thread"), query)
	respond(w, v, err)
}

func (s *Server) runtimeStatus(w http.ResponseWriter, r *http.Request, user management.User) {
	v, err := s.options.Runtime.Status(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"))
	respond(w, v, err)
}

func (s *Server) threadInspection(w http.ResponseWriter, r *http.Request, user management.User) {
	v, err := s.options.Runtime.Inspection(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"), r.PathValue("thread"))
	respond(w, v, err)
}

type ResetContextRequest struct {
	RequestID string `json:"request_id"`
}

func (s *Server) resetThreadContext(w http.ResponseWriter, r *http.Request, user management.User) {
	var body ResetContextRequest
	if err := decode(r, &body); err != nil {
		respond(w, nil, err)
		return
	}
	v, err := s.options.Runtime.ResetContext(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"), r.PathValue("thread"), body.RequestID)
	respond(w, v, err)
}

type WorkerRequest struct {
	RequestID string `json:"request_id"`
	Name      string `json:"name"`
}

func (s *Server) threads(w http.ResponseWriter, r *http.Request, user management.User) {
	v, err := s.options.Runtime.Threads(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"))
	respond(w, v, err)
}

func (s *Server) submitInput(w http.ResponseWriter, r *http.Request, user management.User) {
	var input managedruntime.InputRequest
	if err := decode(r, &input); err != nil {
		respond(w, nil, err)
		return
	}
	v, err := s.options.Runtime.Submit(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"), input)
	respond(w, v, err)
}

func (s *Server) threadEvents(w http.ResponseWriter, r *http.Request, user management.User) {
	after := int64(0)
	limit := 200
	if raw := r.URL.Query().Get("after"); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			respond(w, nil, managedruntime.ErrInvalid)
			return
		}
		after = v
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil {
			respond(w, nil, managedruntime.ErrInvalid)
			return
		}
		limit = v
	}
	if r.URL.Query().Has("before") {
		before, err := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
		if err != nil || r.URL.Query().Has("after") {
			respond(w, nil, managedruntime.ErrInvalid)
			return
		}
		v, err := s.options.Runtime.History(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"), r.PathValue("thread"), before, limit)
		respond(w, v, err)
		return
	}
	v, err := s.options.Runtime.Events(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"), r.PathValue("thread"), after, limit)
	respond(w, v, err)
}

func (s *Server) cancelThread(w http.ResponseWriter, r *http.Request, user management.User) {
	var body struct{}
	if err := decode(r, &body); err != nil {
		respond(w, nil, err)
		return
	}
	err := s.options.Runtime.Cancel(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"), r.PathValue("thread"))
	respond(w, map[string]bool{"cancelled": err == nil}, err)
}

func (s *Server) createWorker(w http.ResponseWriter, r *http.Request, user management.User) {
	var body WorkerRequest
	if err := decode(r, &body); err != nil {
		respond(w, nil, err)
		return
	}
	v, err := s.options.Runtime.Worker(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"), r.PathValue("thread"), body.RequestID, body.Name)
	respond(w, v, err)
}

func (s *Server) compactThread(w http.ResponseWriter, r *http.Request, user management.User) {
	var body managedruntime.CompactionRequest
	if err := decode(r, &body); err != nil {
		respond(w, nil, err)
		return
	}
	v, err := s.options.Runtime.Compact(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"), r.PathValue("thread"), body)
	respond(w, v, err)
}

type ArchiveThreadRequest struct {
	Archived bool `json:"archived"`
}

func (s *Server) archiveThread(w http.ResponseWriter, r *http.Request, user management.User) {
	var body ArchiveThreadRequest
	if err := decode(r, &body); err != nil {
		respond(w, nil, err)
		return
	}
	result, err := s.options.Runtime.Archive(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"), r.PathValue("thread"), body.Archived)
	respond(w, result, err)
}

func (s *Server) deleteThread(w http.ResponseWriter, r *http.Request, user management.User) {
	result, err := s.options.Runtime.DeleteThread(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"), r.PathValue("thread"))
	respond(w, result, err)
}
