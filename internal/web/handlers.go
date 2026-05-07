package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/session"
)

// errorJSON is the wire shape for every error response.
type errorJSON struct {
	Error      string `json:"error"`
	Message    string `json:"message"`
	Suggestion string `json:"suggestion,omitempty"`
	Retryable  bool   `json:"retryable"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, status int, kind, msg string) {
	writeJSON(w, status, errorJSON{Error: kind, Message: msg})
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listSessions(w, r)
	case http.MethodPost:
		s.createSession(w, r)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET or POST")
	}
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	infos, err := session.List(s.opts.Cfg.SessionsDir())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
	if infos == nil {
		infos = []session.Info{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": infos})
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	as, err := s.openSession(r.Context(), "")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         as.app.Session.ID,
		"dir":        as.app.Session.Dir,
		"started_at": as.StartedAt.UTC().Format(time.RFC3339),
	})
}

// sessionPathID extracts <id> from /api/sessions/<id>[/<rest>].
// Returns ("", "") when the URL doesn't match the expected prefix.
func sessionPathID(p string) (id, rest string) {
	const prefix = "/api/sessions/"
	if !strings.HasPrefix(p, prefix) {
		return "", ""
	}
	tail := p[len(prefix):]
	if i := strings.IndexByte(tail, '/'); i >= 0 {
		return tail[:i], tail[i+1:]
	}
	return tail, ""
}

type sessionShowResponse struct {
	session.Info
	Messages []llm.Message `json:"messages"`
}

func (s *Server) handleSessionShow(w http.ResponseWriter, r *http.Request, id string) {
	dir := filepath.Join(s.opts.Cfg.SessionsDir(), id)
	info, msgs, err := session.LoadInfo(dir)
	if err != nil {
		if os.IsNotExist(err) {
			writeErr(w, http.StatusNotFound, "not_found", "session not found: "+id)
			return
		}
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sessionShowResponse{Info: info, Messages: msgs})
}

// turnRequest is the wire shape for POST /turns.
type turnRequest struct {
	Prompt string `json:"prompt"`
}

func (s *Server) handleStartTurn(w http.ResponseWriter, r *http.Request, id string) {
	as, err := s.getActiveSession(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "session not found: "+id)
		return
	}

	var req turnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Prompt == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "expected JSON body with non-empty prompt")
		return
	}

	as.cancelMu.Lock()
	if as.cancel != nil {
		as.cancelMu.Unlock()
		writeErr(w, http.StatusConflict, "conflict", "turn in progress")
		return
	}
	turnID := fmt.Sprintf("turn-%d", s.nextTurn.Add(1))
	ctx, cancel := context.WithCancel(context.Background())
	as.cancel = cancel
	as.turnsMu.Lock()
	as.turns[turnID] = &turnState{ID: turnID, State: "running"}
	as.turnsMu.Unlock()
	as.cancelMu.Unlock()

	go s.runTurn(ctx, as, turnID, req.Prompt)

	writeJSON(w, http.StatusAccepted, map[string]any{"turn_id": turnID})
}

func (s *Server) handleTurnStatus(w http.ResponseWriter, r *http.Request, id, turnID string) {
	as, err := s.getActiveSession(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "session not found: "+id)
		return
	}
	as.turnsMu.Lock()
	t, ok := as.turns[turnID]
	var state, errStr string
	if ok {
		state, errStr = t.State, t.Err
	}
	as.turnsMu.Unlock()
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "turn not found: "+turnID)
		return
	}
	resp := map[string]any{"state": state}
	if errStr != "" {
		resp["error"] = errStr
	}
	writeJSON(w, http.StatusOK, resp)
}

// runTurn executes one engine turn and updates state machine + cancel
// bookkeeping when it finishes.
func (s *Server) runTurn(ctx context.Context, as *activeSession, turnID, prompt string) {
	_, err := as.app.Engine.Turn(ctx, prompt)
	as.cancelMu.Lock()
	as.cancel = nil
	as.cancelMu.Unlock()
	as.turnsMu.Lock()
	if t, ok := as.turns[turnID]; ok {
		if err != nil {
			t.State = "errored"
			t.Err = err.Error()
		} else {
			t.State = "done"
		}
	}
	as.turnsMu.Unlock()
}
