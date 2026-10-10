package managementhttp

import (
	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/management"
	"net/http"
)

func (s *Server) agentRunState(w http.ResponseWriter, r *http.Request, user management.User) {
	value, err := s.options.Runtime.AgentRunState(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"))
	respond(w, value, err)
}
func (s *Server) changeAgentLifecycle(w http.ResponseWriter, r *http.Request, user management.User) {
	var change managedruntime.AgentLifecycleChange
	if err := decode(r, &change); err != nil {
		respond(w, nil, err)
		return
	}
	value, err := s.options.Runtime.ChangeAgentLifecycle(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"), change)
	respond(w, value, err)
}
