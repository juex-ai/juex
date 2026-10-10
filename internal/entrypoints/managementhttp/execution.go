package managementhttp

import (
	"context"
	"net/http"
	"strconv"

	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/management"
)

type Execution interface {
	InspectEnvironments(context.Context, string, string, string) (execution.EnvironmentInspection, error)
	InspectMCP(context.Context, string, string, string, string) (execution.MCPPage, error)
	RefreshMCPTools(context.Context, string, string, string, string, string, execution.MCPRefresh) (*execution.MCPToolList, error)
	ArtifactAPI
	TransferAPI
	Operation(context.Context, string, string, string, string, string, int64, int) (execution.Operation, error)
	Environments(context.Context, string, string, string) ([]execprotocol.Environment, error)
	DefaultEnvironment(context.Context, string, string, string) (execution.DefaultEnvironment, error)
	SetDefaultEnvironment(context.Context, string, string, string, execution.DefaultEnvironment) (execution.DefaultEnvironment, error)
	PreviewPair(context.Context, string, string, string) (execution.Pairing, error)
	ApprovePair(context.Context, string, string, string, map[string][]execprotocol.Capability) (execution.Pairing, error)
	Devices(context.Context, string, string, string) ([]execution.Device, error)
	Restrict(context.Context, string, string, string, int64, map[string][]execprotocol.Capability) (execution.Device, error)
	Revoke(context.Context, string, string, string) error
}

func (s *Server) mcpInspection(w http.ResponseWriter, r *http.Request, user management.User) {
	v, err := s.options.Execution.InspectMCP(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"), r.URL.Query().Get("after"))
	respond(w, v, err)
}

func (s *Server) refreshMCPTools(w http.ResponseWriter, r *http.Request, user management.User) {
	var change execution.MCPRefresh
	if err := decode(r, &change); err != nil {
		respond(w, nil, err)
		return
	}
	v, err := s.options.Execution.RefreshMCPTools(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"), r.PathValue("environment"), r.PathValue("connection"), change)
	respond(w, v, err)
}

// OperationOutput exposes retained output without the operation's arguments,
// environment variables, authorization snapshot or other execution secrets.
type OperationOutput struct {
	ID            string `json:"id"`
	EnvironmentID string `json:"environment_id"`
	State         string `json:"state"`
	Output        []byte `json:"output"`
	NextCursor    int64  `json:"next_cursor"`
	OutputBytes   int64  `json:"output_bytes"`
	Truncated     bool   `json:"truncated"`
	OutputExpired bool   `json:"output_expired"`
	ExitCode      *int   `json:"exit_code"`
	Error         string `json:"error"`
}

func (s *Server) operationOutput(w http.ResponseWriter, r *http.Request, user management.User) {
	var after int64
	if raw := r.URL.Query().Get("after"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 0 {
			respond(w, nil, management.ErrInvalid)
			return
		}
		after = value
	}
	op, err := s.options.Execution.Operation(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"), r.PathValue("environment"), r.PathValue("operation"), after, 64<<10)
	if err != nil {
		respond(w, nil, err)
		return
	}
	respond(w, OperationOutput{ID: op.ID, EnvironmentID: op.EnvironmentID, State: op.State, Output: op.Snapshot.Output, NextCursor: op.Snapshot.NextCursor, OutputBytes: op.Snapshot.OutputBytes, Truncated: op.Snapshot.Truncated, OutputExpired: op.Snapshot.OutputExpired, ExitCode: op.Snapshot.ExitCode, Error: op.Snapshot.Error}, nil)
}

func (s *Server) environmentInspection(w http.ResponseWriter, r *http.Request, user management.User) {
	v, err := s.options.Execution.InspectEnvironments(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"))
	respond(w, v, err)
}

func (s *Server) environments(w http.ResponseWriter, r *http.Request, user management.User) {
	v, err := s.options.Execution.Environments(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"))
	respond(w, v, err)
}

func (s *Server) defaultEnvironment(w http.ResponseWriter, r *http.Request, user management.User) {
	v, err := s.options.Execution.DefaultEnvironment(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"))
	respond(w, v, err)
}

func (s *Server) setDefaultEnvironment(w http.ResponseWriter, r *http.Request, user management.User) {
	var body execution.DefaultEnvironment
	if err := decode(r, &body); err != nil {
		respond(w, nil, err)
		return
	}
	v, err := s.options.Execution.SetDefaultEnvironment(r.Context(), user.ID, r.PathValue("tenant"), r.PathValue("agent"), body)
	respond(w, v, err)
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
