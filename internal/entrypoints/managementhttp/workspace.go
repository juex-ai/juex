package managementhttp

import (
	"context"
	"net/http"

	"github.com/juex-ai/juex/internal/management"
)

type WorkspaceAPI interface {
	Read(context.Context, string, string, string, management.WorkspaceReadRequest) (management.WorkspaceReadReceipt, error)
	Receipt(context.Context, string, string, string, string, string) (management.WorkspaceReadReceipt, error)
	PreviewConfiguration(context.Context, string, string, string, string, string) (management.WorkspaceConfigurationPreview, error)
	Configure(context.Context, string, string, string, management.WorkspaceConfigurationChange) (management.Agent, error)
}

func (s *Server) workspaceConfigurationPreview(w http.ResponseWriter, r *http.Request, user management.User) {
	value, err := s.options.Workspace.PreviewConfiguration(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"), r.PathValue("environment"), r.PathValue("operation"))
	respond(w, value, err)
}

func (s *Server) configureWorkspace(w http.ResponseWriter, r *http.Request, user management.User) {
	var change management.WorkspaceConfigurationChange
	if err := decode(r, &change); err != nil {
		respond(w, nil, err)
		return
	}
	value, err := s.options.Workspace.Configure(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"), change)
	respond(w, value, err)
}

func (s *Server) readWorkspace(w http.ResponseWriter, r *http.Request, user management.User) {
	var body management.WorkspaceReadRequest
	if err := decode(r, &body); err != nil {
		respond(w, nil, err)
		return
	}
	v, err := s.options.Workspace.Read(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"), body)
	respond(w, v, err)
}

func (s *Server) workspaceReceipt(w http.ResponseWriter, r *http.Request, user management.User) {
	v, err := s.options.Workspace.Receipt(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"), r.PathValue("environment"), r.PathValue("operation"))
	respond(w, v, err)
}
