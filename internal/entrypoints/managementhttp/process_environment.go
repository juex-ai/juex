package managementhttp

import (
	"context"
	"github.com/juex-ai/juex/internal/management"
	"net/http"
)

type ProcessEnvironmentAPI interface {
	Read(context.Context, string, string, string) (management.ProcessEnvironmentView, error)
	Configure(context.Context, string, string, string, string, management.ProcessEnvironmentChange) (management.ProcessEnvironmentView, error)
}

func (s *Server) processEnvironment(w http.ResponseWriter, r *http.Request, user management.User) {
	value, err := s.options.ProcessEnvironment.Read(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"))
	respond(w, value, err)
}
func (s *Server) configureProcessEnvironment(w http.ResponseWriter, r *http.Request, user management.User) {
	var change management.ProcessEnvironmentChange
	if err := decode(r, &change); err != nil {
		respond(w, nil, err)
		return
	}
	value, err := s.options.ProcessEnvironment.Configure(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"), r.PathValue("layer"), change)
	respond(w, value, err)
}
