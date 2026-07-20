package web

import (
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
	"github.com/juex-ai/juex/internal/statusapi"
	"github.com/juex-ai/juex/internal/usermedia"
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
	infos, err = session.MarkActive(s.opts.Cfg.HistoryPath(), infos)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
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
	info, ok := as.app.SessionInfo(as.StartedAt.UTC())
	if !ok {
		writeErr(w, http.StatusInternalServerError, "general_error", app.ErrSessionUnavailable.Error())
		return
	}
	writeJSON(w, http.StatusCreated, info)
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
	Messages        []sessionMessageResponse    `json:"messages"`
	Model           string                      `json:"model,omitempty"`
	HasMoreBefore   bool                        `json:"has_more_before"`
	OldestMessageID string                      `json:"oldest_message_id,omitempty"`
	Goal            *runtime.GoalStatusSnapshot `json:"goal,omitempty"`
	Notes           *runtime.NotesSnapshot      `json:"notes,omitempty"`
}

const (
	defaultSessionMessageLimit = 80
	maxSessionMessageLimit     = 200
)

type sessionMessageWindow struct {
	Before string
	Limit  int
}

type sessionMessageResponse struct {
	llm.Message
	CreatedAt string `json:"created_at,omitempty"`
}

func messagesForSessionResponse(msgs []llm.Message) []sessionMessageResponse {
	if msgs == nil {
		return []sessionMessageResponse{}
	}
	mapped := make([]sessionMessageResponse, 0, len(msgs))
	for _, msg := range msgs {
		response := sessionMessageResponse{Message: msg}
		if createdAt, ok := session.MessageCreatedAt(msg.ID); ok {
			response.CreatedAt = createdAt.UTC().Format(time.RFC3339)
		}
		mapped = append(mapped, response)
	}
	return mapped
}

func (s *Server) handleSessionShow(w http.ResponseWriter, r *http.Request, id string) {
	window, err := parseSessionMessageWindow(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if v, ok := s.sessions.Load(id); ok {
		as := v.(*activeSession)
		var (
			info  session.Info
			page  session.MessagePage
			goal  *runtime.GoalStatusSnapshot
			notes *runtime.NotesSnapshot
		)
		err := as.app.ReadSessionID(id, func(sess *session.Session) error {
			info = sess.Info(time.Now().UTC())
			var err error
			page, err = sess.TranscriptMessagePage(window.Before, window.Limit)
			if err != nil {
				return err
			}
			goal, notes = as.app.Engine.SessionStateStatus()
			return nil
		})
		if err == nil {
			info, err = session.MarkActiveInfo(s.opts.Cfg.HistoryPath(), info)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
				return
			}
			writeJSON(w, http.StatusOK, sessionShowResponse{
				Info:            info,
				Messages:        messagesForSessionResponse(page.Messages),
				Model:           s.opts.Cfg.Model,
				HasMoreBefore:   page.HasMoreBefore,
				OldestMessageID: page.OldestMessageID,
				Goal:            goal,
				Notes:           notes,
			})
			return
		}
		if !errors.Is(err, app.ErrSessionChanged) && !errors.Is(err, app.ErrSessionUnavailable) {
			if errors.Is(err, session.ErrBeforeMessageNotFound) {
				writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
				return
			}
			writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
			return
		}
	}
	dir := filepath.Join(s.opts.Cfg.SessionsDir(), id)
	info, page, err := session.LoadInfoPage(dir, window.Before, window.Limit)
	if err != nil {
		if os.IsNotExist(err) {
			writeErr(w, http.StatusNotFound, "not_found", "session not found: "+id)
			return
		}
		if errors.Is(err, session.ErrBeforeMessageNotFound) {
			writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
	info, err = session.MarkActiveInfo(s.opts.Cfg.HistoryPath(), info)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
	goal, notes := s.sessionStateStatus(dir, nil)
	writeJSON(w, http.StatusOK, sessionShowResponse{
		Info:            info,
		Messages:        messagesForSessionResponse(page.Messages),
		Model:           s.opts.Cfg.Model,
		HasMoreBefore:   page.HasMoreBefore,
		OldestMessageID: page.OldestMessageID,
		Goal:            goal,
		Notes:           notes,
	})
}

func (s *Server) sessionStateStatus(dir string, as *activeSession) (*runtime.GoalStatusSnapshot, *runtime.NotesSnapshot) {
	if as != nil && as.app != nil && as.app.Engine != nil {
		return as.app.SessionStateStatus()
	}
	goal, _ := runtime.NewGoalStateStore(dir, runtime.GoalStateOptions{}).StatusSnapshot()
	notes, _ := runtime.NewNotesStore(dir).StatusSnapshot()
	return goal, notes
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
	info, err := session.Activate(s.opts.Cfg.SessionsDir(), s.opts.Cfg.HistoryPath(), id)
	if err != nil {
		if os.IsNotExist(err) {
			writeErr(w, http.StatusNotFound, "not_found", "session not found: "+id)
			return
		}
		if errors.Is(err, session.ErrCannotActivateSide) {
			writeErr(w, http.StatusBadRequest, "bad_request", "side sessions cannot become active")
			return
		}
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, info)
}

type compactRequest struct {
	Reason       string `json:"reason"`
	Instructions string `json:"instructions"`
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

	compactTurnID := s.nextTurnID("compact")
	if err := as.app.BeginCompactAdmission(compactTurnID); err != nil {
		writeErr(w, http.StatusConflict, "conflict", "session busy")
		return
	}
	result, err := as.app.CompactAdmittedWithInstructions(r.Context(), compactTurnID, req.Reason, false, req.Instructions)
	if start := as.app.FinishCompactAdmission(compactTurnID, app.TurnIDFunc(s.nextTurnID)); start != nil {
		as.turns.start(start.TurnID, start.Message)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleSessionContext(w http.ResponseWriter, r *http.Request, id string) {
	if v, ok := s.sessions.Load(id); ok {
		as := v.(*activeSession)
		if snapshot, ok := as.app.ActiveContextForSession(id); ok {
			writeJSON(w, http.StatusOK, snapshot)
			return
		}
	}
	dir := filepath.Join(s.opts.Cfg.SessionsDir(), id)
	msgs, err := session.LoadActiveMessages(dir)
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
		if info, ok := as.app.SessionInfo(now); ok {
			byID[info.ID] = info
		}
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
		var info session.Info
		err := as.app.ReadSessionID(id, func(sess *session.Session) error {
			info = sess.Info(time.Now().UTC())
			return nil
		})
		if err == nil {
			return session.MarkActiveInfo(s.opts.Cfg.HistoryPath(), info)
		}
		if !errors.Is(err, app.ErrSessionChanged) && !errors.Is(err, app.ErrSessionUnavailable) {
			return session.Info{}, err
		}
	}
	info, _, err := session.LoadInfo(filepath.Join(s.opts.Cfg.SessionsDir(), id))
	if err != nil {
		return session.Info{}, err
	}
	return session.MarkActiveInfo(s.opts.Cfg.HistoryPath(), info)
}

// turnRequest is the wire shape for POST /turns.
type turnRequest struct {
	Prompt      string         `json:"prompt"`
	Kind        string         `json:"kind,omitempty"`
	Attachments []llm.MediaRef `json:"attachments,omitempty"`
}

type startTurnResponse struct {
	TurnID           string                  `json:"turn_id,omitempty"`
	Queued           bool                    `json:"queued,omitempty"`
	PendingCount     int                     `json:"pending_count,omitempty"`
	MaxPendingInputs int                     `json:"max_pending_inputs,omitempty"`
	Command          *app.SlashCommandResult `json:"command,omitempty"`
	Warnings         []app.TurnWarning       `json:"warnings,omitempty"`
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "expected JSON body")
		return
	}
	if len(req.Attachments) > 0 {
		if err := usermedia.ValidateSessionMediaRefs(s.opts.Cfg.WorkDir, id, req.Attachments, usermedia.Limits{}); err != nil {
			writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
	}
	if strings.TrimSpace(req.Prompt) == "" && len(req.Attachments) == 0 {
		writeErr(w, http.StatusBadRequest, "bad_request", "expected JSON body with non-empty prompt or attachments")
		return
	}
	if req.Kind != "" && req.Kind != llm.MessageKindSystemNotice {
		writeErr(w, http.StatusBadRequest, "bad_request", "unsupported turn kind")
		return
	}
	if req.Kind == llm.MessageKindSystemNotice && len(req.Attachments) > 0 {
		writeErr(w, http.StatusBadRequest, "bad_request", "system notices cannot include attachments")
		return
	}

	result := as.app.AdmitTurn(r.Context(), app.TurnAdmissionRequest{
		Prompt:      req.Prompt,
		Kind:        req.Kind,
		Attachments: req.Attachments,
		IDs:         app.TurnIDFunc(s.nextTurnID),
	})
	s.applyTurnAdmissionResult(as, result)
	s.writeTurnAdmissionResult(w, result)
}

func (s *Server) handleSessionAttachmentUpload(w http.ResponseWriter, r *http.Request, id string) {
	if _, ok, msg := s.webTurnAllowed(id); !ok {
		if msg == "" {
			writeErr(w, http.StatusNotFound, "not_found", "session not found: "+id)
		} else {
			writeErr(w, http.StatusConflict, "conflict", msg)
		}
		return
	}
	if _, err := s.getActiveSession(r.Context(), id); err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "session not found: "+id)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, usermedia.DefaultMaxBytes+1024*1024)
	file, header, err := r.FormFile("file")
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeErr(w, http.StatusRequestEntityTooLarge, "payload_too_large", "request body too large")
			return
		}
		writeErr(w, http.StatusBadRequest, "bad_request", "expected multipart file field named file")
		return
	}
	defer func() { _ = file.Close() }()

	filename := ""
	if header != nil {
		filename = header.Filename
	}
	ref, err := usermedia.StoreUpload(s.opts.Cfg.WorkDir, id, filename, file, usermedia.Limits{})
	if err != nil {
		status := http.StatusBadRequest
		kind := "bad_request"
		msg := err.Error()
		switch {
		case strings.Contains(msg, "unsupported image type"):
			status = http.StatusUnsupportedMediaType
			kind = "unsupported_media_type"
		case strings.Contains(msg, "exceeds"):
			status = http.StatusRequestEntityTooLarge
			kind = "payload_too_large"
		}
		writeErr(w, status, kind, msg)
		return
	}
	writeJSON(w, http.StatusOK, ref)
}

func (s *Server) nextTurnID(prefix string) string {
	if prefix == "" {
		prefix = "turn"
	}
	return fmt.Sprintf("%s-%d", prefix, s.nextTurn.Add(1))
}

func (s *Server) applyTurnAdmissionResult(as *activeSession, result app.TurnAdmissionResult) {
	if change := result.SessionChanged; change != nil && change.OldID != "" && change.NewID != "" {
		s.sessions.Delete(change.OldID)
		as.StartedAt = time.Now()
		as.turns.reset()
		s.sessions.Store(change.NewID, as)
	}
	if result.Start != nil {
		as.turns.start(result.Start.TurnID, result.Start.Message)
	}
}

func (s *Server) writeTurnAdmissionResult(w http.ResponseWriter, result app.TurnAdmissionResult) {
	switch result.Kind {
	case app.TurnAdmissionStarted:
		writeJSON(w, http.StatusAccepted, startTurnResponse{TurnID: result.TurnID, Warnings: result.Warnings})
	case app.TurnAdmissionQueued:
		writeJSON(w, http.StatusAccepted, startTurnResponse{
			TurnID:           result.TurnID,
			Queued:           result.Queued,
			PendingCount:     result.PendingCount,
			MaxPendingInputs: result.MaxPendingInputs,
			Warnings:         result.Warnings,
		})
	case app.TurnAdmissionCommandCompleted:
		writeJSON(w, http.StatusOK, startTurnResponse{TurnID: result.TurnID, Command: result.Command, Warnings: result.Warnings})
	case app.TurnAdmissionRejected:
		status := http.StatusBadRequest
		if result.Error.Kind == "pending_input_full" {
			status = http.StatusTooManyRequests
		}
		writeJSON(w, status, errorJSON{
			Error:      result.Error.Kind,
			Message:    result.Error.Message,
			Suggestion: result.Error.Suggestion,
			Retryable:  result.Error.Retryable,
		})
	case app.TurnAdmissionConflict:
		writeErr(w, http.StatusConflict, result.Error.Kind, result.Error.Message)
	case app.TurnAdmissionError:
		writeErr(w, http.StatusInternalServerError, result.Error.Kind, result.Error.Message)
	default:
		writeErr(w, http.StatusInternalServerError, "general_error", "unknown turn admission result")
	}
}

func (s *Server) handleInterrupt(w http.ResponseWriter, r *http.Request, id string) {
	as, err := s.getActiveSession(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "session not found: "+id)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cancelled": as.turns.interrupt()})
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

	since := sseResumeCursor(r)
	if since != "" {
		// Replay missed events from events.jsonl. The path comes from the
		// session record so we never read outside the sessions dir.
		var f *os.File
		err := as.app.ReadSessionID(id, func(sess *session.Session) error {
			var openErr error
			f, openErr = os.Open(filepath.Join(sess.Dir, "events.jsonl"))
			return openErr
		})
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
			if err := as.app.ReadSessionID(id, func(*session.Session) error { return nil }); err != nil {
				return
			}
			if err := writeSSEFrame(w, e); err != nil {
				return
			}
		case <-sub.done:
			return
		case <-ctx.Done():
			return
		}
	}
}

func (s *Server) handleSessionStatus(w http.ResponseWriter, r *http.Request, id string) {
	status, err := s.statusSnapshotForSession(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "session not found: "+id)
		return
	}
	writeJSON(w, http.StatusOK, statusapi.FromRuntime(status))
}

func (s *Server) handleSessionStatusEvents(w http.ResponseWriter, r *http.Request, id string) {
	since := sseResumeCursor(r)
	subscription, live, err := s.statusSubscriptionForSession(id, since)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "session not found: "+id)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "general_error", "streaming not supported")
		return
	}
	defer subscription.Unsubscribe()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	for _, snapshot := range subscription.Snapshots {
		if snapshot.Session.ID != id {
			return
		}
		if err := writeStatusSSE(w, statusapi.FromRuntime(snapshot)); err != nil {
			return
		}
	}
	flusher.Flush()
	if !live {
		return
	}
	for {
		select {
		case snapshot, ok := <-subscription.Updates:
			if !ok {
				return
			}
			if snapshot.Session.ID != id {
				return
			}
			if err := writeStatusSSE(w, statusapi.FromRuntime(snapshot)); err != nil {
				return
			}
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) statusSnapshotForSession(id string) (runtime.StatusSnapshot, error) {
	if id == "" || filepath.Base(id) != id {
		return runtime.StatusSnapshot{}, os.ErrNotExist
	}
	if value, ok := s.sessions.Load(id); ok {
		active := value.(*activeSession)
		if active.app == nil || active.app.Status == nil {
			return runtime.StatusSnapshot{}, os.ErrNotExist
		}
		var snapshot runtime.StatusSnapshot
		err := active.app.ReadSessionID(id, func(*session.Session) error {
			snapshot = active.app.Status.Snapshot()
			return nil
		})
		if err == nil {
			return snapshot, nil
		}
		if !errors.Is(err, app.ErrSessionChanged) && !errors.Is(err, app.ErrSessionUnavailable) {
			return runtime.StatusSnapshot{}, err
		}
	}

	status, err := s.historicalStatusStore(id)
	if err != nil {
		return runtime.StatusSnapshot{}, err
	}
	return status.Snapshot(), nil
}

func (s *Server) statusSubscriptionForSession(id, since string) (*runtime.StatusSubscription, bool, error) {
	if id == "" || filepath.Base(id) != id {
		return nil, false, os.ErrNotExist
	}
	if value, ok := s.sessions.Load(id); ok {
		active := value.(*activeSession)
		if active.app == nil || active.app.Status == nil {
			return nil, false, os.ErrNotExist
		}
		var subscription *runtime.StatusSubscription
		err := active.app.ReadSessionID(id, func(*session.Session) error {
			subscription = active.app.Status.SubscribeFrom(since)
			return nil
		})
		if err == nil {
			return subscription, true, nil
		}
		if !errors.Is(err, app.ErrSessionChanged) && !errors.Is(err, app.ErrSessionUnavailable) {
			return nil, false, err
		}
	}

	status, err := s.historicalStatusStore(id)
	if err != nil {
		return nil, false, err
	}
	return status.SubscribeFrom(since), false, nil
}

func (s *Server) historicalStatusStore(id string) (*runtime.StatusStore, error) {
	dir := filepath.Join(s.opts.Cfg.SessionsDir(), id)
	info, _, err := session.LoadInfoPage(dir, "", 1)
	if err != nil {
		return nil, err
	}
	journal, _ := session.ReadEvents(dir)
	seed := runtime.StatusSeed{
		SessionID:        info.ID,
		SessionAlias:     info.Alias,
		MaxPendingInputs: runtime.DefaultMaxPendingInput,
		TokenUsage:       info.TokenUsage,
		ContextUsage:     info.ContextUsage,
	}
	status := runtime.NewStatusStoreFromJournal(seed, journal)
	status.RecoverAfterRestart()
	return status, nil
}
