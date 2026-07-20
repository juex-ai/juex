package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

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

type agentStatusHub struct {
	mu          sync.Mutex
	current     agentActivityResponse
	subscribers map[uint64]chan agentActivityResponse
	nextID      uint64
}

type agentStatusSubscription struct {
	initial []agentActivityResponse
	updates <-chan agentActivityResponse
	cancel  func()
}

func newAgentStatusHub() *agentStatusHub {
	return &agentStatusHub{
		current:     agentActivityResponse{State: agentActivityIdle},
		subscribers: map[uint64]chan agentActivityResponse{},
	}
}

func (h *agentStatusHub) publish(status agentActivityResponse) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.current = status
	for _, channel := range h.subscribers {
		select {
		case channel <- status:
		default:
			select {
			case <-channel:
			default:
			}
			select {
			case channel <- status:
			default:
			}
		}
	}
	h.mu.Unlock()
}

func (h *agentStatusHub) subscribe(_ string) agentStatusSubscription {
	h.mu.Lock()
	h.nextID++
	id := h.nextID
	updates := make(chan agentActivityResponse, 16)
	h.subscribers[id] = updates
	// Presentation can change without advancing the durable event cursor, so a
	// same-cursor reconnect still receives the current full snapshot.
	initial := []agentActivityResponse{h.current}
	h.mu.Unlock()
	return agentStatusSubscription{
		initial: initial,
		updates: updates,
		cancel: func() {
			h.mu.Lock()
			delete(h.subscribers, id)
			h.mu.Unlock()
		},
	}
}

func (s *Server) handleAgentActivity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	writeJSON(w, http.StatusOK, s.agentActivity())
}

func (s *Server) handleAgentStatus(w http.ResponseWriter, r *http.Request) {
	s.handleAgentActivity(w, r)
}

func (s *Server) handleAgentStatusEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "general_error", "streaming not supported")
		return
	}
	since := sseResumeCursor(r)
	subscription := s.statusHub.subscribe(since)
	defer subscription.cancel()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	for _, status := range subscription.initial {
		if err := writeAgentStatusSSE(w, status); err != nil {
			return
		}
	}
	flusher.Flush()
	for {
		select {
		case status, ok := <-subscription.updates:
			if !ok {
				return
			}
			if err := writeAgentStatusSSE(w, status); err != nil {
				return
			}
		case <-r.Context().Done():
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
