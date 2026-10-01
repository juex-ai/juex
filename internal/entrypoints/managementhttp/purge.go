package managementhttp

import (
	"context"
	"github.com/juex-ai/juex/internal/management"
	"net/http"
)

type Purges interface {
	RequestPurge(context.Context, string, string, string, management.PurgeRequest) (management.PurgeJob, error)
	Purges(context.Context, string, string, string, string) ([]management.PurgeJob, error)
}

func (s *Server) requestPurge(w http.ResponseWriter, r *http.Request, user management.User) {
	var request management.PurgeRequest
	if err := decode(r, &request); err != nil {
		respond(w, nil, err)
		return
	}
	v, err := s.options.Directory.(Purges).RequestPurge(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("owner"), request)
	respond(w, v, err)
}

func (s *Server) purges(w http.ResponseWriter, r *http.Request, user management.User) {
	v, err := s.options.Directory.(Purges).Purges(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("owner"), r.URL.Query().Get("after"))
	respond(w, v, err)
}
