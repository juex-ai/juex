package managementhttp

import (
	"context"
	"net/http"

	"github.com/juex-ai/juex/internal/management"
)

type ExtensionAPI interface {
	Inspect(context.Context, string, string, string, management.ExtensionInspectionRequest) (management.ExtensionInspection, error)
	Inspection(context.Context, string, string, string, string, string) (management.ExtensionInspection, error)
	Configure(context.Context, string, string, string, string, management.ExtensionChange) (management.Agent, error)
}

func (s *Server) inspectExtension(w http.ResponseWriter, r *http.Request, user management.User) {
	var request management.ExtensionInspectionRequest
	if err := decode(r, &request); err != nil {
		respond(w, nil, err)
		return
	}
	value, err := s.options.Extensions.Inspect(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"), request)
	respond(w, value, err)
}
func (s *Server) extensionInspection(w http.ResponseWriter, r *http.Request, user management.User) {
	value, err := s.options.Extensions.Inspection(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"), r.PathValue("environment"), r.PathValue("operation"))
	respond(w, value, err)
}
func (s *Server) configureExtension(w http.ResponseWriter, r *http.Request, user management.User) {
	var request management.ExtensionChange
	if err := decode(r, &request); err != nil {
		respond(w, nil, err)
		return
	}
	value, err := s.options.Extensions.Configure(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"), r.PathValue("binding"), request)
	respond(w, value, err)
}
