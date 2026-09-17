package fleethttp

import (
	"context"
	"net"
	"net/http"
	"strings"

	"github.com/juex-ai/juex/internal/fleet"
	"github.com/juex-ai/juex/internal/foundation/fleetclient"
	"github.com/juex-ai/juex/internal/foundation/serviceendpoint"
)

type managementBackend interface {
	AuthorizeManagement(fleetclient.Caller) error
	ManagedAgents(context.Context, fleetclient.Caller) ([]fleetclient.Agent, error)
	ManagedCreate(context.Context, fleetclient.Caller, fleetclient.CreateRequest) (fleetclient.Result, error)
	ManagedConfig(context.Context, fleetclient.Caller, string) (fleetclient.Config, error)
	ManagedConfigure(context.Context, fleetclient.Caller, string, fleetclient.ConfigRequest) (fleetclient.Result, error)
	ManagedLifecycle(context.Context, fleetclient.Caller, string, fleetclient.LifecycleRequest) (fleetclient.Result, error)
}

func (s *Server) initManagement(manager *fleet.Manager) error {
	id, err := serviceendpoint.FleetID(manager.HomeDir())
	if err != nil {
		return err
	}
	s.managementHome = manager.HomeDir()
	s.managementIdentity = fleetclient.Endpoint{FleetID: id, InstanceID: serviceendpoint.NewID()}
	return nil
}

func (s *Server) publishManagement(address string) error {
	if s.managementHome == "" {
		return nil
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	identity := s.managementIdentity
	identity.URL = "http://" + net.JoinHostPort(host, port)
	return fleetclient.Publish(s.managementHome, identity)
}

func (s *Server) handleManagement(w http.ResponseWriter, r *http.Request) {
	identity := s.managementIdentity
	w.Header().Set(fleetclient.FleetHeader, identity.FleetID)
	w.Header().Set(fleetclient.InstanceHeader, identity.InstanceID)
	manager, ok := s.manager.(managementBackend)
	if !ok || identity.FleetID == "" {
		writeError(w, 503, "unavailable", "management unavailable")
		return
	}
	if r.Header.Get(fleetclient.FleetHeader) != identity.FleetID || r.Header.Get(fleetclient.InstanceHeader) != identity.InstanceID {
		writeError(w, 409, "identity_mismatch", "Fleet instance changed; rediscover endpoint")
		return
	}
	caller := fleetclient.Caller{Profile: r.Header.Get(fleetclient.ProfileHeader), AgentID: r.Header.Get(fleetclient.AgentHeader)}
	if err := manager.AuthorizeManagement(caller); err != nil {
		writeFleetError(w, err)
		return
	}
	suffix := strings.TrimPrefix(r.URL.Path, fleetclient.Path)
	var result any
	var err error
	switch {
	case suffix == "" && r.Method == http.MethodGet:
		result, err = manager.ManagedAgents(r.Context(), caller)
	case suffix == "" && r.Method == http.MethodPost:
		var body fleetclient.CreateRequest
		if !decodeJSONBody(w, r, maxAgentMutationRequestBytes, &body, "Agent creation request required") {
			return
		}
		result, err = manager.ManagedCreate(r.Context(), caller, body)
	default:
		parts := strings.Split(strings.TrimPrefix(suffix, "/"), "/")
		if len(parts) != 2 || parts[0] == "" {
			writeError(w, 404, "not_found", "management route not found")
			return
		}
		switch {
		case parts[1] == "config" && r.Method == http.MethodGet:
			result, err = manager.ManagedConfig(r.Context(), caller, parts[0])
		case parts[1] == "config" && r.Method == http.MethodPut:
			var body fleetclient.ConfigRequest
			if !decodeJSONBody(w, r, maxConfigRequestBytes, &body, "configuration request required") {
				return
			}
			result, err = manager.ManagedConfigure(r.Context(), caller, parts[0], body)
		case parts[1] == "lifecycle" && r.Method == http.MethodPost:
			var body fleetclient.LifecycleRequest
			if !decodeJSONBody(w, r, maxAgentMutationRequestBytes, &body, "lifecycle request required") {
				return
			}
			result, err = manager.ManagedLifecycle(r.Context(), caller, parts[0], body)
		default:
			writeError(w, 405, "method_not_allowed", "unsupported management operation")
			return
		}
	}
	if err != nil {
		writeFleetError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleSupervisor(w http.ResponseWriter, r *http.Request) {
	manager, ok := s.manager.(*fleet.Manager)
	if !ok {
		writeError(w, 503, "unavailable", "Supervisor management unavailable")
		return
	}
	var status fleet.SupervisorStatus
	var err error
	switch r.Method {
	case http.MethodGet:
		status, err = manager.Supervisor(r.Context())
	case http.MethodPost:
		var body struct {
			Action string `json:"action"`
		}
		if !decodeJSONBody(w, r, maxAgentMutationRequestBytes, &body, "Supervisor action required") {
			return
		}
		status, err = manager.SupervisorAction(r.Context(), body.Action)
	default:
		writeError(w, 405, "method_not_allowed", "GET or POST required")
		return
	}
	if err != nil {
		writeFleetError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}
