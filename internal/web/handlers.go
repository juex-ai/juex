package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/runtime"
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
	enc.Encode(v)
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
	infos = s.mergeActiveSessionInfos(infos)
	writeJSON(w, http.StatusOK, map[string]any{"sessions": infos})
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	as, err := s.openSession(r.Context(), "")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, as.app.Session.Info(as.StartedAt.UTC()))
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
	Model    string        `json:"model,omitempty"`
}

func messagesForSessionResponse(msgs []llm.Message) []llm.Message {
	if msgs == nil {
		return []llm.Message{}
	}
	return msgs
}

func (s *Server) handleSessionShow(w http.ResponseWriter, r *http.Request, id string) {
	if v, ok := s.sessions.Load(id); ok {
		as := v.(*activeSession)
		info, msgs := as.app.Session.Snapshot(time.Now().UTC())
		if msgs == nil {
			msgs = []llm.Message{}
		}
		writeJSON(w, http.StatusOK, sessionShowResponse{
			Info:     info,
			Messages: messagesForSessionResponse(msgs),
			Model:    s.opts.Cfg.Model,
		})
		return
	}
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
	if msgs == nil {
		msgs = []llm.Message{}
	}
	writeJSON(w, http.StatusOK, sessionShowResponse{
		Info:     info,
		Messages: messagesForSessionResponse(msgs),
		Model:    s.opts.Cfg.Model,
	})
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request, id string) {
	s.createMu.Lock()
	defer s.createMu.Unlock()

	closedActive := s.closeActiveSession(id)
	if err := session.Delete(s.opts.Cfg.SessionsDir(), s.opts.Cfg.HistoryPath(), id); err != nil {
		if os.IsNotExist(err) {
			if !closedActive {
				writeErr(w, http.StatusNotFound, "not_found", "session not found: "+id)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": id})
			return
		}
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": id})
}

type compactRequest struct {
	Reason string `json:"reason"`
}

func (s *Server) handleCompactSession(w http.ResponseWriter, r *http.Request, id string) {
	as, err := s.getActiveSession(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "session not found: "+id)
		return
	}
	var req compactRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeErr(w, http.StatusBadRequest, "bad_request", "expected JSON body")
			return
		}
	}
	if req.Reason == "" {
		req.Reason = "manual"
	}

	as.cancelMu.Lock()
	if as.cancel != nil || as.compacting {
		as.cancelMu.Unlock()
		writeErr(w, http.StatusConflict, "conflict", "session busy")
		return
	}
	as.compacting = true
	as.cancelMu.Unlock()

	result, err := as.app.Compact(r.Context(), req.Reason, false)
	as.cancelMu.Lock()
	as.compacting = false
	as.cancelMu.Unlock()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleSessionContext(w http.ResponseWriter, r *http.Request, id string) {
	if v, ok := s.sessions.Load(id); ok {
		as := v.(*activeSession)
		writeJSON(w, http.StatusOK, as.app.Engine.ActiveContext())
		return
	}
	dir := filepath.Join(s.opts.Cfg.SessionsDir(), id)
	_, msgs, err := session.LoadInfo(dir)
	if err != nil {
		if os.IsNotExist(err) {
			writeErr(w, http.StatusNotFound, "not_found", "session not found: "+id)
			return
		}
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, runtime.ActiveContextFromHistory(msgs))
}

func (s *Server) mergeActiveSessionInfos(persisted []session.Info) []session.Info {
	byID := make(map[string]session.Info, len(persisted))
	for _, info := range persisted {
		byID[info.ID] = info
	}
	now := time.Now().UTC()
	s.sessions.Range(func(_, v any) bool {
		as := v.(*activeSession)
		info := as.app.Session.Info(now)
		byID[info.ID] = info
		return true
	})
	infos := make([]session.Info, 0, len(byID))
	for _, info := range byID {
		infos = append(infos, info)
	}
	sort.SliceStable(infos, func(i, j int) bool {
		if !infos[i].LastActiveAt.Equal(infos[j].LastActiveAt) {
			return infos[i].LastActiveAt.After(infos[j].LastActiveAt)
		}
		return infos[i].StartedAt.After(infos[j].StartedAt)
	})
	return infos
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
	if as.compacting {
		as.cancelMu.Unlock()
		writeErr(w, http.StatusConflict, "conflict", "session busy")
		return
	}
	if as.cancel != nil {
		activeTurn := as.activeTurn
		as.cancelMu.Unlock()
		status, err := as.app.Engine.EnqueuePendingInput(r.Context(), req.Prompt)
		if err != nil {
			if errors.Is(err, runtime.ErrPendingInputQueueFull) {
				writeJSON(w, http.StatusTooManyRequests, errorJSON{
					Error:      "pending_input_full",
					Message:    fmt.Sprintf("pending input queue full (%d/%d)", status.PendingCount, status.MaxPendingInputs),
					Suggestion: "wait for the active turn to drain pending input before sending more",
					Retryable:  true,
				})
				return
			}
			if errors.Is(err, runtime.ErrNoActiveTurn) {
				writeErr(w, http.StatusConflict, "conflict", "turn is not accepting pending input")
				return
			}
			writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{
			"turn_id":            activeTurn,
			"queued":             true,
			"pending_count":      status.PendingCount,
			"max_pending_inputs": status.MaxPendingInputs,
		})
		return
	}
	turnID := fmt.Sprintf("turn-%d", s.nextTurn.Add(1))
	if err := as.app.Engine.ReserveTurnID(turnID); err != nil {
		as.cancelMu.Unlock()
		writeErr(w, http.StatusConflict, "conflict", err.Error())
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	as.cancel = cancel
	as.activeTurn = turnID
	as.turnsMu.Lock()
	as.turns[turnID] = &turnState{ID: turnID, State: "running"}
	as.turnsMu.Unlock()
	as.turnWG.Add(1)
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
	if state == "running" {
		pending := as.app.Engine.PendingInputStatus()
		resp["pending_count"] = pending.PendingCount
		resp["max_pending_inputs"] = pending.MaxPendingInputs
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleInterrupt(w http.ResponseWriter, r *http.Request, id string) {
	as, err := s.getActiveSession(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "session not found: "+id)
		return
	}
	as.cancelMu.Lock()
	cancelled := false
	if as.cancel != nil {
		as.cancel()
		as.cancel = nil
		as.activeTurn = ""
		cancelled = true
	}
	as.cancelMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"cancelled": cancelled})
}

func (s *Server) handleEventsSSE(w http.ResponseWriter, r *http.Request, id string) {
	as, err := s.getActiveSession(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "session not found: "+id)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "general_error", "streaming not supported")
		return
	}
	sub := as.bcast.subscribe()
	defer sub.unsubscribe()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher.Flush()

	since := r.URL.Query().Get("since")
	if since == "" {
		since = r.Header.Get("Last-Event-ID")
	}
	if since != "" {
		// Replay missed events from events.jsonl. The path comes from the
		// session record so we never read outside the sessions dir.
		f, err := os.Open(filepath.Join(as.app.Session.Dir, "events.jsonl"))
		if err == nil {
			replayed, replayErr := replaySince(f, since)
			f.Close()
			if replayErr != nil {
				log.Printf("web: events replay for %s: %v", id, replayErr)
			}
			for _, e := range replayed {
				if err := writeSSEFrame(w, e); err != nil {
					return
				}
			}
		}
	}

	ctx := r.Context()
	for {
		select {
		case e, ok := <-sub.ch:
			if !ok {
				return
			}
			if err := writeSSEFrame(w, e); err != nil {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// runTurn executes one engine turn and updates state machine + cancel
// bookkeeping when it finishes.
func (s *Server) runTurn(ctx context.Context, as *activeSession, turnID, prompt string) {
	defer as.turnWG.Done()
	_, err := as.app.Engine.TurnWithID(ctx, prompt, turnID)
	as.cancelMu.Lock()
	as.cancel = nil
	as.activeTurn = ""
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
