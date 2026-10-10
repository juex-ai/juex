package managementhttp

import (
	"net/http"

	"github.com/juex-ai/juex/internal/management"
)

type ConfigureAgentRequest struct {
	management.AgentConfig
	Version int64 `json:"version"`
}

type ArchiveAgentRequest struct {
	Version  int64 `json:"version"`
	Archived bool  `json:"archived"`
}

type AgentManagementRequest struct {
	Version int64 `json:"version"`
	Enabled bool  `json:"enabled"`
}

func (s *Server) agentManagement(w http.ResponseWriter, r *http.Request, user management.User) {
	var body AgentManagementRequest
	if err := decode(r, &body); err != nil {
		respond(w, nil, err)
		return
	}
	value, err := s.options.Directory.SetAgentManagement(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"), body.Version, body.Enabled)
	respond(w, value, err)
}

func (s *Server) tenantSettings(w http.ResponseWriter, r *http.Request, user management.User) {
	value, err := s.options.Directory.TenantSettings(r.Context(), user.ID, r.PathValue("tenant"))
	respond(w, value, err)
}

func (s *Server) configureTenantSettings(w http.ResponseWriter, r *http.Request, user management.User) {
	var body management.ConfigurationLayer
	if err := decode(r, &body); err != nil {
		respond(w, nil, err)
		return
	}
	value, err := s.options.Directory.ConfigureTenantSettings(r.Context(), user.ID, r.PathValue("tenant"), body)
	respond(w, value, err)
}

func (s *Server) agentDetail(w http.ResponseWriter, r *http.Request, user management.User) {
	a, err := s.options.Directory.ReadAgent(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"))
	respond(w, management.AgentDetail{Agent: a.Agent, OwnerID: a.Fleet.UserID, CanExecute: a.CanExecute, Layers: a.Layers, Effective: a.Effective}, err)
}

func (s *Server) configureFleet(w http.ResponseWriter, r *http.Request, user management.User) {
	var body management.FleetSettings
	if err := decode(r, &body); err != nil {
		respond(w, nil, err)
		return
	}
	v, err := s.options.Directory.ConfigureFleet(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("owner"), body)
	respond(w, v, err)
}

func (s *Server) models(w http.ResponseWriter, r *http.Request, user management.User) {
	v, err := s.options.Directory.Models(r.Context(), user.ID, r.PathValue("tenant"))
	respond(w, v, err)
}

func (s *Server) createAgent(w http.ResponseWriter, r *http.Request, user management.User) {
	var body management.AgentConfig
	if err := decode(r, &body); err != nil {
		respond(w, nil, err)
		return
	}
	v, err := s.options.Directory.CreateAgent(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("owner"), body)
	respond(w, v, err)
}

func (s *Server) configureAgent(w http.ResponseWriter, r *http.Request, user management.User) {
	var body ConfigureAgentRequest
	if err := decode(r, &body); err != nil {
		respond(w, nil, err)
		return
	}
	v, err := s.options.Directory.ConfigureAgent(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"), body.Version, body.AgentConfig)
	respond(w, v, err)
}

func (s *Server) archiveAgent(w http.ResponseWriter, r *http.Request, user management.User) {
	var body ArchiveAgentRequest
	if err := decode(r, &body); err != nil {
		respond(w, nil, err)
		return
	}
	v, err := s.options.Directory.SetAgentArchived(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"), body.Version, body.Archived)
	respond(w, v, err)
}
