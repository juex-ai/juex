package managementhttp

import (
	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/management"
	"net/http"
	"strconv"
)

func (s *Server) observationContent(w http.ResponseWriter, r *http.Request, user management.User) {
	offset, limit := 0, 16384
	for key, target := range map[string]*int{"offset": &offset, "limit": &limit} {
		if raw := r.URL.Query().Get(key); raw != "" {
			value, err := strconv.ParseInt(raw, 10, 32)
			if err != nil {
				respond(w, nil, managedruntime.ErrInvalid)
				return
			}
			*target = int(value)
		}
	}
	value, err := s.options.Runtime.ObservationContent(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"), r.PathValue("observation"), offset, limit)
	respond(w, value, err)
}

func (s *Server) observationSources(w http.ResponseWriter, r *http.Request, user management.User) {
	value, err := s.options.Runtime.ObservationSources(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"), r.URL.Query().Get("after"))
	respond(w, value, err)
}
func (s *Server) observedEvents(w http.ResponseWriter, r *http.Request, user management.User) {
	value, err := s.options.Runtime.ObservedEvents(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"), r.PathValue("source"), r.URL.Query().Get("after"))
	respond(w, value, err)
}
func (s *Server) startObserver(w http.ResponseWriter, r *http.Request, user management.User) {
	var request managedruntime.ObserverStart
	if err := decode(r, &request); err != nil {
		respond(w, nil, err)
		return
	}
	value, err := s.options.Runtime.StartObserver(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"), request)
	respond(w, value, err)
}
func (s *Server) stopObserver(w http.ResponseWriter, r *http.Request, user management.User) {
	var request struct{}
	if err := decode(r, &request); err != nil {
		respond(w, nil, err)
		return
	}
	err := s.options.Runtime.StopObserver(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"), r.PathValue("source"))
	respond(w, map[string]bool{"stop_requested": err == nil}, err)
}

type SourceSubscriptionChange struct {
	Enabled bool `json:"enabled"`
}

func (s *Server) sourceSubscription(w http.ResponseWriter, r *http.Request, user management.User) {
	var request SourceSubscriptionChange
	if err := decode(r, &request); err != nil {
		respond(w, nil, err)
		return
	}
	value, err := s.options.Runtime.SetSourceSubscription(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"), r.PathValue("source"), r.PathValue("thread"), request.Enabled)
	respond(w, value, err)
}
