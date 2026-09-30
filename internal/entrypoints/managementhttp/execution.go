package managementhttp

import (
	"context"
	"net/http"

	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/management"
)

type Execution interface {
	PreviewPair(context.Context, string, string, string) (execution.Pairing, error)
	ApprovePair(context.Context, string, string, string, map[string][]execprotocol.Capability) (execution.Pairing, error)
	Devices(context.Context, string, string, string) ([]execution.Device, error)
	Restrict(context.Context, string, string, string, int64, map[string][]execprotocol.Capability) (execution.Device, error)
	Revoke(context.Context, string, string, string) error
}

func (s *Server) previewPair(w http.ResponseWriter, r *http.Request, user management.User) {
	v, err := s.options.Execution.PreviewPair(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("pair"))
	respond(w, v, err)
}
func (s *Server) approvePair(w http.ResponseWriter, r *http.Request, user management.User) {
	var body struct {
		Grants map[string][]execprotocol.Capability `json:"grants"`
	}
	if err := decode(r, &body); err != nil {
		respond(w, nil, err)
		return
	}
	v, err := s.options.Execution.ApprovePair(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("pair"), body.Grants)
	respond(w, v, err)
}
func (s *Server) devices(w http.ResponseWriter, r *http.Request, user management.User) {
	v, err := s.options.Execution.Devices(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("owner"))
	respond(w, v, err)
}
func (s *Server) deviceGrants(w http.ResponseWriter, r *http.Request, user management.User) {
	var body struct {
		Version int64                                `json:"version"`
		Grants  map[string][]execprotocol.Capability `json:"grants"`
	}
	if err := decode(r, &body); err != nil {
		respond(w, nil, err)
		return
	}
	v, err := s.options.Execution.Restrict(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("device"), body.Version, body.Grants)
	respond(w, v, err)
}
func (s *Server) revokeDevice(w http.ResponseWriter, r *http.Request, user management.User) {
	var body struct{}
	if err := decode(r, &body); err != nil {
		respond(w, nil, err)
		return
	}
	respond(w, nil, s.options.Execution.Revoke(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("device")))
}
