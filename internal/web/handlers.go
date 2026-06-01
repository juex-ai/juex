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
	"strconv"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/app"
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
	infos = s.markActiveInfos(infos)
	writeJSON(w, http.StatusOK, map[string]any{"sessions": infos})
}

type createSessionRequest struct {
	Kind string `json:"kind"`
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	var req createSessionRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeErr(w, http.StatusBadRequest, "bad_request", "expected JSON body")
			return
		}
	}
	mode := app.SessionModeNewPrimary
	if req.Kind == session.KindSide {
		mode = app.SessionModeNewSide
	} else if req.Kind != "" && req.Kind != session.KindPrimary {
		writeErr(w, http.StatusBadRequest, "bad_request", "kind must be primary or side")
		return
	}
	as, err := s.openSession(r.Context(), "", mode)
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
	Messages        []llm.Message `json:"messages"`
	Model           string        `json:"model,omitempty"`
	HasMoreBefore   bool          `json:"has_more_before"`
	OldestMessageID string        `json:"oldest_message_id,omitempty"`
}

const (
	defaultSessionMessageLimit = 80
	maxSessionMessageLimit     = 200
)

type sessionMessageWindow struct {
	Before string
	Limit  int
}

type sessionMessagePage struct {
	Messages        []llm.Message
	HasMoreBefore   bool
	OldestMessageID string
}

func messagesForSessionResponse(msgs []llm.Message) []llm.Message {
	if msgs == nil {
		return []llm.Message{}
	}
	return msgs
}

func (s *Server) handleSessionShow(w http.ResponseWriter, r *http.Request, id string) {
	window, err := parseSessionMessageWindow(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if v, ok := s.sessions.Load(id); ok {
		as := v.(*activeSession)
		info, msgs := as.app.Session.Snapshot(time.Now().UTC())
		info = s.markActiveInfo(info)
		page, err := selectSessionMessagePage(msgs, window)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, sessionShowResponse{
			Info:            info,
			Messages:        messagesForSessionResponse(page.Messages),
			Model:           s.opts.Cfg.Model,
			HasMoreBefore:   page.HasMoreBefore,
			OldestMessageID: page.OldestMessageID,
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
	page, err := selectSessionMessagePage(msgs, window)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	info = s.markActiveInfo(info)
	writeJSON(w, http.StatusOK, sessionShowResponse{
		Info:            info,
		Messages:        messagesForSessionResponse(page.Messages),
		Model:           s.opts.Cfg.Model,
		HasMoreBefore:   page.HasMoreBefore,
		OldestMessageID: page.OldestMessageID,
	})
}

func parseSessionMessageWindow(r *http.Request) (sessionMessageWindow, error) {
	q := r.URL.Query()
	window := sessionMessageWindow{Limit: defaultSessionMessageLimit}
	if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 {
			return sessionMessageWindow{}, fmt.Errorf("limit must be a positive integer")
		}
		if limit > maxSessionMessageLimit {
			limit = maxSessionMessageLimit
		}
		window.Limit = limit
	}
	window.Before = strings.TrimSpace(q.Get("before"))
	return window, nil
}

func selectSessionMessagePage(msgs []llm.Message, window sessionMessageWindow) (sessionMessagePage, error) {
	if msgs == nil {
		msgs = []llm.Message{}
	}
	start := 0
	end := len(msgs)
	if window.Before != "" {
		index := sessionMessageIndex(msgs, window.Before)
		if index < 0 {
			return sessionMessagePage{}, fmt.Errorf("before message not found: %s", window.Before)
		}
		end = index
	} else if compactIndex := latestCompactMessageIndex(msgs); compactIndex >= 0 {
		start = compactIndex
	}
	if window.Limit > 0 && end-start > window.Limit {
		start = end - window.Limit
	}
	if start < 0 {
		start = 0
	}
	if end < start {
		end = start
	}
	pageMessages := msgs[start:end]
	oldestID := ""
	if len(pageMessages) > 0 {
		oldestID = pageMessages[0].ID
	}
	return sessionMessagePage{
		Messages:        pageMessages,
		HasMoreBefore:   start > 0,
		OldestMessageID: oldestID,
	}, nil
}

func latestCompactMessageIndex(msgs []llm.Message) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Kind == llm.MessageKindCompact {
			return i
		}
	}
	return -1
}

func sessionMessageIndex(msgs []llm.Message, id string) int {
	for i, msg := range msgs {
		if msg.ID == id {
			return i
		}
	}
	return -1
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

func (s *Server) handleActivateSession(w http.ResponseWriter, r *http.Request, id string) {
	dir := filepath.Join(s.opts.Cfg.SessionsDir(), id)
	info, _, err := session.LoadInfo(dir)
	if err != nil {
		if os.IsNotExist(err) {
			writeErr(w, http.StatusNotFound, "not_found", "session not found: "+id)
			return
		}
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
	if info.Kind != session.KindPrimary {
		writeErr(w, http.StatusBadRequest, "bad_request", "side sessions cannot become active")
		return
	}
	if err := session.SetActive(s.opts.Cfg.HistoryPath(), info); err != nil {
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
	info.Active = true
	writeJSON(w, http.StatusOK, info)
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

	compactTurnID, ok := s.beginCompactTurn(w, as)
	if !ok {
		return
	}
	result, err := as.app.Compact(r.Context(), req.Reason, false)
	s.finishCompactTurn(as, compactTurnID)
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

func (s *Server) markActiveInfos(infos []session.Info) []session.Info {
	activeID := s.activeSessionID()
	for i := range infos {
		if infos[i].Kind == "" {
			infos[i].Kind = session.KindPrimary
		}
		infos[i].Active = activeID != "" && infos[i].ID == activeID
	}
	return infos
}

func (s *Server) markActiveInfo(info session.Info) session.Info {
	if info.Kind == "" {
		info.Kind = session.KindPrimary
	}
	info.Active = s.activeSessionID() != "" && info.ID == s.activeSessionID()
	return info
}

func (s *Server) activeSessionID() string {
	h, err := session.LoadHistory(s.opts.Cfg.HistoryPath())
	if err != nil || h.Active == nil {
		return ""
	}
	return h.Active.ID
}

func (s *Server) webTurnAllowed(id string) (session.Info, bool, string) {
	info, err := s.sessionInfo(id)
	if err != nil {
		return session.Info{}, false, ""
	}
	if info.Kind == session.KindSide {
		return info, false, "side session cannot be continued from web"
	}
	if !info.Active {
		return info, false, "activate this primary session before continuing"
	}
	return info, true, ""
}

func (s *Server) sessionInfo(id string) (session.Info, error) {
	if v, ok := s.sessions.Load(id); ok {
		as := v.(*activeSession)
		info, _ := as.app.Session.Snapshot(time.Now().UTC())
		return s.markActiveInfo(info), nil
	}
	info, _, err := session.LoadInfo(filepath.Join(s.opts.Cfg.SessionsDir(), id))
	if err != nil {
		return session.Info{}, err
	}
	return s.markActiveInfo(info), nil
}

// turnRequest is the wire shape for POST /turns.
type turnRequest struct {
	Prompt string `json:"prompt"`
}

type startTurnResponse struct {
	TurnID           string                  `json:"turn_id,omitempty"`
	Queued           bool                    `json:"queued,omitempty"`
	PendingCount     int                     `json:"pending_count,omitempty"`
	MaxPendingInputs int                     `json:"max_pending_inputs,omitempty"`
	Command          *app.SlashCommandResult `json:"command,omitempty"`
}

func (s *Server) handleStartTurn(w http.ResponseWriter, r *http.Request, id string) {
	if _, ok, msg := s.webTurnAllowed(id); !ok {
		if msg == "" {
			writeErr(w, http.StatusNotFound, "not_found", "session not found: "+id)
		} else {
			writeErr(w, http.StatusConflict, "conflict", msg)
		}
		return
	}
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

	slashCmd, isSlash, slashErr := app.ParseSlashCommand(req.Prompt)
	if slashErr != nil {
		writeJSON(w, http.StatusBadRequest, errorJSON{
			Error:      "bad_request",
			Message:    slashErr.Error(),
			Suggestion: "available slash commands: " + app.AvailableSlashCommandsText(),
			Retryable:  false,
		})
		return
	}
	if isSlash {
		s.handleSlashTurn(w, r, as, slashCmd)
		return
	}

	as.cancelMu.Lock()
	if as.compacting {
		as.cancelMu.Unlock()
		s.enqueuePendingInput(w, r, as, req.Prompt)
		return
	}
	if as.cancel != nil {
		activeTurn := as.activeTurn
		as.cancelMu.Unlock()
		s.enqueuePendingInputWithTurn(w, r, as, req.Prompt, activeTurn)
		return
	}
	turnID := fmt.Sprintf("turn-%d", s.nextTurn.Add(1))
	if err := as.app.Engine.ReserveTurnID(turnID); err != nil {
		as.cancelMu.Unlock()
		writeErr(w, http.StatusConflict, "conflict", err.Error())
		return
	}
	s.startTurnMessageLocked(as, turnID, llm.TextMessage(llm.RoleUser, req.Prompt))
	as.cancelMu.Unlock()

	writeJSON(w, http.StatusAccepted, startTurnResponse{TurnID: turnID})
}

func (s *Server) handleSlashTurn(w http.ResponseWriter, r *http.Request, as *activeSession, cmd app.SlashCommand) {
	if cmd.Name == app.SlashCompact {
		compactTurnID, ok := s.beginCompactTurn(w, as)
		if !ok {
			return
		}
		result, err := as.app.ExecuteParsedSlashCommand(r.Context(), cmd)
		s.finishCompactTurn(as, compactTurnID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, startTurnResponse{Command: &result})
		return
	}

	if cmd.Name == app.SlashNew {
		as.cancelMu.Lock()
		if as.cancel != nil || as.compacting {
			as.cancelMu.Unlock()
			writeErr(w, http.StatusConflict, "conflict", "session busy")
			return
		}
		as.compacting = true
		as.cancelMu.Unlock()

		defer func() {
			as.cancelMu.Lock()
			as.compacting = false
			as.cancelMu.Unlock()
		}()
	}

	oldID := as.app.Session.ID
	result, err := as.app.ExecuteParsedSlashCommand(r.Context(), cmd)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
	if cmd.Name == app.SlashNew && as.app.Session.ID != oldID {
		s.sessions.Delete(oldID)
		as.StartedAt = time.Now()
		as.turnsMu.Lock()
		as.turns = map[string]*turnState{}
		as.turnsMu.Unlock()
		s.sessions.Store(as.app.Session.ID, as)
	}
	writeJSON(w, http.StatusOK, startTurnResponse{Command: &result})
}

func (s *Server) beginCompactTurn(w http.ResponseWriter, as *activeSession) (string, bool) {
	turnID := fmt.Sprintf("compact-%d", s.nextTurn.Add(1))
	as.cancelMu.Lock()
	if as.cancel != nil || as.compacting {
		as.cancelMu.Unlock()
		writeErr(w, http.StatusConflict, "conflict", "session busy")
		return "", false
	}
	as.compacting = true
	if err := as.app.Engine.ReserveTurnID(turnID); err != nil {
		as.compacting = false
		as.cancelMu.Unlock()
		writeErr(w, http.StatusConflict, "conflict", err.Error())
		return "", false
	}
	as.cancelMu.Unlock()
	return turnID, true
}

func (s *Server) finishCompactTurn(as *activeSession, compactTurnID string) {
	nextTurnID := fmt.Sprintf("turn-%d", s.nextTurn.Add(1))
	as.cancelMu.Lock()
	defer as.cancelMu.Unlock()
	as.compacting = false
	msg, _, promoted := as.app.Engine.PromotePendingInputTurn(compactTurnID, nextTurnID)
	if promoted {
		s.startTurnMessageLocked(as, nextTurnID, msg)
	}
}

func (s *Server) enqueuePendingInput(w http.ResponseWriter, r *http.Request, as *activeSession, prompt string) {
	s.enqueuePendingInputWithTurn(w, r, as, prompt, "")
}

func (s *Server) enqueuePendingInputWithTurn(w http.ResponseWriter, r *http.Request, as *activeSession, prompt, fallbackTurnID string) {
	status, err := as.app.Engine.EnqueuePendingInput(r.Context(), prompt)
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
	turnID := status.TurnID
	if turnID == "" {
		turnID = fallbackTurnID
	}
	writeJSON(w, http.StatusAccepted, startTurnResponse{
		TurnID:           turnID,
		Queued:           true,
		PendingCount:     status.PendingCount,
		MaxPendingInputs: status.MaxPendingInputs,
	})
}

type turnStatusResponse struct {
	State            string `json:"state"`
	Error            string `json:"error,omitempty"`
	PendingCount     *int   `json:"pending_count,omitempty"`
	MaxPendingInputs *int   `json:"max_pending_inputs,omitempty"`
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
	resp := turnStatusResponse{State: state, Error: errStr}
	if state == "running" {
		pending := as.app.Engine.PendingInputStatus()
		resp.PendingCount = &pending.PendingCount
		resp.MaxPendingInputs = &pending.MaxPendingInputs
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

func (s *Server) startTurnMessageLocked(as *activeSession, turnID string, msg llm.Message) {
	ctx, cancel := context.WithCancel(context.Background())
	as.cancel = cancel
	as.activeTurn = turnID
	as.turnsMu.Lock()
	as.turns[turnID] = &turnState{ID: turnID, State: "running"}
	as.turnsMu.Unlock()
	as.turnWG.Add(1)
	go s.runTurnMessage(ctx, as, turnID, msg)
}

// runTurnMessage executes one engine turn and updates state machine + cancel
// bookkeeping when it finishes.
func (s *Server) runTurnMessage(ctx context.Context, as *activeSession, turnID string, msg llm.Message) {
	defer as.turnWG.Done()
	_, err := as.app.Engine.TurnMessageWithID(ctx, msg, turnID)
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
