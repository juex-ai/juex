package managementhttp

import (
	"context"
	"net/http"
	"time"

	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/management"
)

type TransferAPI interface {
	BeginTransfer(context.Context, string, string, string, execution.TransferRequest) (execution.Transfer, error)
	Transfer(context.Context, string, string, string, string) (execution.Transfer, error)
	ListTransfers(context.Context, string, string, string, string, int) ([]execution.Transfer, error)
	CancelTransfer(context.Context, string, string, string, string) error
	ExtendTransfer(context.Context, string, string, string, string, time.Duration) error
}

func (s *Server) beginTransfer(w http.ResponseWriter, r *http.Request, user management.User) {
	var body execution.TransferRequest
	if err := decode(r, &body); err != nil {
		respond(w, nil, err)
		return
	}
	v, err := s.options.Execution.BeginTransfer(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"), body)
	respond(w, v, err)
}

func (s *Server) transfer(w http.ResponseWriter, r *http.Request, user management.User) {
	v, err := s.options.Execution.Transfer(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"), r.PathValue("transfer"))
	respond(w, v, err)
}

func (s *Server) transfers(w http.ResponseWriter, r *http.Request, user management.User) {
	v, err := s.options.Execution.ListTransfers(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"), r.URL.Query().Get("after"), 100)
	respond(w, v, err)
}

func (s *Server) cancelTransfer(w http.ResponseWriter, r *http.Request, user management.User) {
	var body struct{}
	if err := decode(r, &body); err != nil {
		respond(w, nil, err)
		return
	}
	respond(w, nil, s.options.Execution.CancelTransfer(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"), r.PathValue("transfer")))
}

type ExtendTransferRequest struct {
	WaitHours int `json:"wait_hours"`
}

func (s *Server) extendTransfer(w http.ResponseWriter, r *http.Request, user management.User) {
	var body ExtendTransferRequest
	if err := decode(r, &body); err != nil {
		respond(w, nil, err)
		return
	}
	if body.WaitHours < 1 || body.WaitHours > 720 {
		respond(w, nil, execprotocol.ErrInvalid)
		return
	}
	respond(w, nil, s.options.Execution.ExtendTransfer(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"), r.PathValue("transfer"), time.Duration(body.WaitHours)*time.Hour))
}
