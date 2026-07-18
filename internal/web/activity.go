package web

import (
	"net/http"
)

type agentActivityState string

const (
	agentActivityIdle    agentActivityState = "idle"
	agentActivityWorking agentActivityState = "working"
)

type agentActivityResponse struct {
	State        agentActivityState `json:"state"`
	SessionID    string             `json:"session_id,omitempty"`
	SessionAlias string             `json:"session_alias,omitempty"`
	PendingCount int                `json:"pending_count"`
}

func (s *Server) handleAgentActivity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	writeJSON(w, http.StatusOK, s.agentActivity())
}

func (s *Server) agentActivity() agentActivityResponse {
	response := agentActivityResponse{State: agentActivityIdle}
	var latest *activeSession
	s.sessions.Range(func(_, value any) bool {
		active, ok := value.(*activeSession)
		if !ok || active == nil || active.turns == nil {
			return true
		}
		_, status, running := active.turns.activeStatus()
		if !running {
			return true
		}
		response.State = agentActivityWorking
		if status.PendingCount != nil {
			response.PendingCount += *status.PendingCount
		}
		if latest == nil || active.StartedAt.After(latest.StartedAt) {
			latest = active
		}
		return true
	})
	if latest != nil && latest.app != nil && latest.app.Session != nil {
		response.SessionID = latest.app.Session.ID
		response.SessionAlias = latest.app.Session.Alias
	}
	return response
}
