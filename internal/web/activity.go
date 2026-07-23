package web

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/juex-ai/juex/internal/statusapi"
)

const (
	agentActivityIdle    = statusapi.ActivityIdle
	agentActivityWorking = statusapi.ActivityWorking
)

type agentActivityResponse = statusapi.AgentActivity

type agentStatusStreamEvent struct {
	Type     string                `json:"type"`
	Activity agentActivityResponse `json:"activity"`
}

func (s *Server) handleAgentStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	writeJSON(w, http.StatusOK, s.agentActivity())
}

func (s *Server) handleAgentStatusEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	if _, ok := w.(http.Flusher); !ok {
		writeErr(w, http.StatusInternalServerError, "general_error", "streaming not supported")
		return
	}
	since := sseResumeCursor(r)
	stream := s.statusStream.OpenStream(statusapi.ActivityStreamOptions{
		After:  since,
		Follow: true,
	})
	defer stream.Close()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	for {
		status, ok := stream.Next(r.Context())
		if !ok {
			return
		}
		if err := writeAgentStatusSSE(w, status); err != nil {
			return
		}
	}
}

func (s *Server) agentActivity() agentActivityResponse {
	response := agentActivityResponse{State: agentActivityIdle}
	var latest *activeSession
	var latestWorking *activeSession
	s.sessions.Range(func(_, value any) bool {
		active, ok := value.(*activeSession)
		if !ok || active == nil || active.app == nil || active.app.Status == nil {
			return true
		}
		snapshot := active.app.Status.Snapshot()
		if snapshot.Session.State.IsWorking() {
			response.State = agentActivityWorking
			response.PendingInputCount += snapshot.Session.PendingCount
			if latestWorking == nil || active.StartedAt.After(latestWorking.StartedAt) {
				latestWorking = active
			}
		}
		if latest == nil || active.StartedAt.After(latest.StartedAt) {
			latest = active
		}
		return true
	})
	selected := latestWorking
	if selected == nil {
		selected = latest
	}
	if selected != nil && selected.app != nil && selected.app.Status != nil {
		snapshot := selected.app.Status.Snapshot()
		publicStatus := statusapi.FromRuntime(snapshot)
		response.SelectedStatus = &publicStatus
	}
	return response
}

func writeAgentStatusSSE(w http.ResponseWriter, activity agentActivityResponse) error {
	body, err := json.Marshal(agentStatusStreamEvent{Type: "agent.status", Activity: activity})
	if err != nil {
		return err
	}
	cursor := ""
	if activity.SelectedStatus != nil {
		cursor = activity.SelectedStatus.Cursor
	}
	if cursor != "" {
		if _, err := fmt.Fprintf(w, "id: %s\n", cursor); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", body); err != nil {
		return err
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}
