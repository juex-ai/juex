package managementhttp

import (
	"context"
	"net/http"
	"strconv"

	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/management"
)

type Runtime interface {
	Submit(context.Context, string, string, string, managedruntime.InputRequest) (managedruntime.InputReceipt, error)
	Threads(context.Context, string, string, string) ([]managedruntime.Thread, error)
	Events(context.Context, string, string, string, string, int64, int) (managedruntime.Timeline, error)
	Cancel(context.Context, string, string, string, string) error
	Worker(context.Context, string, string, string, string, string, string) (managedruntime.Thread, error)
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
