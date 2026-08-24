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
	"github.com/juex-ai/juex/internal/eventcatalog"
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/runtime/workmem"
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

func writeActiveSessionLookupError(w http.ResponseWriter, id string, err error) {
	if errors.Is(err, errSessionInactive) {
		writeErr(w, http.StatusConflict, "conflict", "activate this primary session before continuing")
		return
	}
	if os.IsNotExist(err) {
		writeErr(w, http.StatusNotFound, "not_found", "session not found: "+id)
		return
	}
	writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
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
	infos, err := session.ListWithHistory(s.opts.Cfg.SessionsDir(), s.opts.Cfg.HistoryPath())
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

type activeSessionResponse struct {
	SessionID string `json:"session_id,omitempty"`
}

func (s *Server) handleActiveSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	id, ok, err := s.webActiveSessionID()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
	if !ok {
		writeJSON(w, http.StatusOK, activeSessionResponse{})
		return
	}
	writeJSON(w, http.StatusOK, activeSessionResponse{SessionID: id})
}

func (s *Server) webActiveSessionID() (string, bool, error) {
	s.activeSelectionMu.Lock()
	defer s.activeSelectionMu.Unlock()

	id, ok, err := s.activePrimarySessionID()
	if err != nil || !ok {
		return "", ok, err
	}
	if v, live := s.sessions.Load(id); live {
		as := v.(*activeSession)
		if info, available := as.app.SessionInfo(); available && info.Kind == session.KindPrimary {
			return id, true, nil
		}
	}
	if !session.ValidID(id) {
		return "", false, nil
	}
	dir := filepath.Join(s.opts.Cfg.SessionsDir(), id)
	if !session.HasConversation(dir) {
		return "", false, nil
	}
	kind, err := session.LoadKind(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, session.ErrSessionTimeUnavailable) {
			return "", false, nil
		}
		return "", false, err
	}
	if session.NormalizeKind(kind) != session.KindPrimary {
		return "", false, nil
	}
	return id, true, nil
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
	info, ok := as.app.SessionInfo()
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
	EventCursor     string                      `json:"event_cursor"`
	HasMoreBefore   bool                        `json:"has_more_before"`
	OldestMessageID string                      `json:"oldest_message_id,omitempty"`
	Goal            *workmem.GoalStatusSnapshot `json:"goal,omitempty"`
	Notes           *workmem.NotesSnapshot      `json:"notes,omitempty"`
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
			info   session.Info
			page   session.MessagePage
			cursor string
			goal   *workmem.GoalStatusSnapshot
			notes  *workmem.NotesSnapshot
		)
		// Capture the resume cursor before the transcript page, so a concurrent
		// commit may replay but can never be skipped. Both cursor sources run
		// behind the durable commit barrier: the status projection advances its
		// cursor before the later browser projection queues the matching frame,
		// while a raw journal read can observe the appended event before either
		// projection finishes. Reporting either ID mid-commit would advance the
		// browser past an event it never receives — including ones absent from
		// the transcript page, such as pending_input.queued.
		//
		// The barrier is taken inside the session read, never around it: a
		// session switch holds the session lifecycle lock and then takes the
		// commit barrier (App.replaceSession -> DurableSink.SetJournal), so
		// acquiring them in the other order deadlocks.
		err := as.app.ReadSessionID(id, func(sess *session.Session) error {
			return as.app.ReadCommittedEvents(func() error {
				if as.app.Status != nil {
					cursor = as.app.Status.Snapshot().Cursor
				}
				if cursor != "" {
					return nil
				}
				journalCursor, cursorErr := session.ReadLatestCommittedEventID(sess.Dir)
				if cursorErr != nil {
					return cursorErr
				}
				cursor = journalCursor
				return nil
			})
		})
		if err == nil {
			err = as.app.ReadSessionID(id, func(sess *session.Session) error {
				info = sess.Info()
				var pageErr error
				page, pageErr = sess.TranscriptMessagePage(window.Before, window.Limit)
				if pageErr != nil {
					return pageErr
				}
				goal, notes = as.app.Engine.SessionStateStatus()
				return nil
			})
		}
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
				EventCursor:     cursor,
				HasMoreBefore:   page.HasMoreBefore,
				OldestMessageID: page.OldestMessageID,
				Goal:            goal,
				Notes:           notes,
			})
			return
		}
		// A closed durable sink means the runtime is no longer serving this
		// session; the on-disk branch below still answers correctly, so treat it
		// like the other liveness errors rather than failing the request.
		if !errors.Is(err, app.ErrSessionChanged) &&
			!errors.Is(err, app.ErrSessionUnavailable) &&
			!errors.Is(err, events.ErrDurableSinkClosed) {
			if errors.Is(err, session.ErrBeforeMessageNotFound) {
				writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
				return
			}
			writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
			return
		}
	}
	dir := filepath.Join(s.opts.Cfg.SessionsDir(), id)
	cursor, err := session.ReadLatestCommittedEventID(dir)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
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
		EventCursor:     cursor,
		HasMoreBefore:   page.HasMoreBefore,
		OldestMessageID: page.OldestMessageID,
		Goal:            goal,
		Notes:           notes,
	})
}

func (s *Server) sessionStateStatus(dir string, as *activeSession) (*workmem.GoalStatusSnapshot, *workmem.NotesSnapshot) {
	if as != nil && as.app != nil && as.app.Engine != nil {
		return as.app.SessionStateStatus()
	}
	goal, _ := workmem.NewGoalStateStore(dir, workmem.GoalStateOptions{}).StatusSnapshot()
	notes, _ := workmem.NewNotesStore(dir).StatusSnapshot()
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
	s.activeSelectionMu.Lock()
	defer s.activeSelectionMu.Unlock()

	_, runtimeActive := s.sessions.Load(id)
	plan, err := app.PrepareSessionDelete(s.opts.Cfg, id, app.SessionDeleteOptions{AllowMissingSession: runtimeActive})
	if err != nil {
		if os.IsNotExist(err) {
			writeErr(w, http.StatusNotFound, "not_found", "session not found: "+id)
			return
		}
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
	if active, ok := s.deferCloseActiveSession(id); ok {
		if err := active.app.WaitSessionReleased(r.Context()); err != nil {
			writeErr(w, http.StatusRequestTimeout, "request_cancelled", err.Error())
			return
		}
	}
	if err := plan.Commit(); err != nil {
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": id})
}

func (s *Server) handleActivateSession(w http.ResponseWriter, r *http.Request, id string) {
	s.createMu.Lock()
	s.activeSelectionMu.Lock()
	var info session.Info
	var err error
	if id == "" || filepath.Base(id) != id {
		err = os.ErrNotExist
	} else {
		var candidate session.Info
		candidate, _, err = session.LoadInfo(filepath.Join(s.opts.Cfg.SessionsDir(), id))
		if err == nil && session.NormalizeKind(candidate.Kind) != session.KindPrimary {
			err = fmt.Errorf("%w: %s", session.ErrCannotActivateSide, id)
		}
	}
	if err == nil {
		// Keep the old Primary selected until its App, managed children, and
		// result deliveries have drained. This makes selection the ownership
		// handoff instead of leaving an inactive resident App behind.
		s.closeOtherPrimarySessions(id)
		info, err = session.Activate(s.opts.Cfg.SessionsDir(), s.opts.Cfg.HistoryPath(), id)
	}
	s.activeSelectionMu.Unlock()
	s.createMu.Unlock()
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
		writeActiveSessionLookupError(w, id, err)
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

	compactTurnID, err := as.app.BeginCompactAdmission(r.Context())
	if err != nil {
		writeErr(w, http.StatusConflict, "conflict", "session busy")
		return
	}
	result, err := as.app.CompactAdmittedWithInstructions(r.Context(), compactTurnID, req.Reason, false, req.Instructions)
	start, promotionErr := as.app.FinishCompactAdmission(compactTurnID)
	if start != nil {
		as.turns.start(start.TurnID, start.Message)
	}
	if err = errors.Join(err, promotionErr); err != nil {
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
	s.sessions.Range(func(_, v any) bool {
		as := v.(*activeSession)
		if info, ok := as.app.SessionInfo(); ok {
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
			info = sess.Info()
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
		writeActiveSessionLookupError(w, id, err)
		return
	}

	var req turnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "expected JSON body")
		return
	}
	if len(req.Attachments) > 0 {
		if err := usermedia.ValidateSessionMediaRefs(s.opts.Cfg.ArtifactDir(), id, req.Attachments, usermedia.Limits{}); err != nil {
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

	admission := app.TurnAdmissionRequest{
		Prompt:      req.Prompt,
		Kind:        req.Kind,
		Attachments: req.Attachments,
	}
	var result app.TurnAdmissionResult
	if isNewSessionCommand(req.Prompt) {
		result, err = s.admitNewSessionTurn(r.Context(), as, id, admission)
		if err != nil {
			writeActiveSessionLookupError(w, id, err)
			return
		}
	} else {
		result = as.app.AdmitTurn(r.Context(), admission)
		s.applyTurnAdmissionResult(as, result)
	}
	s.writeTurnAdmissionResult(w, result)
}

func isNewSessionCommand(prompt string) bool {
	cmd, handled, err := app.ParseSlashCommand(prompt)
	return err == nil && handled && cmd.Name == app.SlashNew
}

func (s *Server) admitNewSessionTurn(ctx context.Context, as *activeSession, id string, admission app.TurnAdmissionRequest) (app.TurnAdmissionResult, error) {
	// SwitchToNewPrimarySession persists the new active id before the Web
	// registry key changes. Keep both steps in the same critical section used
	// by live-session restoration so stale EventSource reconnects cannot enter
	// between them.
	s.createMu.Lock()
	defer s.createMu.Unlock()
	s.activeSelectionMu.Lock()
	defer s.activeSelectionMu.Unlock()
	activeID, ok, err := s.activePrimarySessionID()
	if err != nil {
		return app.TurnAdmissionResult{}, err
	}
	if !ok || activeID != id || !activeSessionMatches(as, id) {
		return app.TurnAdmissionResult{}, errSessionInactive
	}
	result := as.app.AdmitTurn(ctx, admission)
	s.applyTurnAdmissionResult(as, result)
	return result, nil
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
	if err := s.ensureMCPStarted(r.Context()); err != nil {
		writeActiveSessionLookupError(w, id, err)
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
	ref, err := s.storeSessionAttachment(r.Context(), id, filename, file)
	if err != nil {
		if errors.Is(err, errSessionInactive) || os.IsNotExist(err) {
			writeActiveSessionLookupError(w, id, err)
			return
		}
		writeSessionAttachmentStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ref)
}

func (s *Server) storeSessionAttachment(ctx context.Context, id, filename string, file io.Reader) (usermedia.MediaRef, error) {
	s.createMu.Lock()
	defer s.createMu.Unlock()
	s.activeSelectionMu.Lock()
	defer s.activeSelectionMu.Unlock()
	if _, err := s.getActiveSessionLocked(ctx, id); err != nil {
		return usermedia.MediaRef{}, err
	}
	return usermedia.StoreUpload(s.opts.Cfg.ArtifactDir(), id, filename, file, usermedia.Limits{})
}

func writeSessionAttachmentStoreError(w http.ResponseWriter, err error) {
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
}

func (s *Server) applyTurnAdmissionResult(as *activeSession, result app.TurnAdmissionResult) {
	if change := result.SessionChanged; change != nil && change.OldID != "" && change.NewID != "" {
		s.sessions.Delete(change.OldID)
		as.StartedAt = time.Now()
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
		writeJSON(w, http.StatusConflict, errorJSON{
			Error:      result.Error.Kind,
			Message:    result.Error.Message,
			Suggestion: result.Error.Suggestion,
			Retryable:  result.Error.Retryable,
		})
	case app.TurnAdmissionError:
		writeErr(w, http.StatusInternalServerError, result.Error.Kind, result.Error.Message)
	default:
		writeErr(w, http.StatusInternalServerError, "general_error", "unknown turn admission result")
	}
}

func (s *Server) handleInterrupt(w http.ResponseWriter, r *http.Request, id string) {
	as, err := s.getActiveSession(r.Context(), id)
	if err != nil {
		writeActiveSessionLookupError(w, id, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cancelled": as.turns.interrupt()})
}

func (s *Server) handleEventsSSE(w http.ResponseWriter, r *http.Request, id string) {
	as, err := s.getActiveSession(r.Context(), id)
	if err != nil {
		writeActiveSessionLookupError(w, id, err)
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

	var replayDeduper *browserReplayDeduplicator
	since, replayRequested := sseResumeCursorWithPresence(r)
	if replayRequested {
		replay, err := captureCommittedEventReplay(as.app, id)
		if err == nil {
			journal, replayErr := replay.readJournal()
			if closeErr := replay.Close(); closeErr != nil {
				log.Printf("web: close events replay for %s: %v", id, closeErr)
			}
			if replayErr != nil {
				log.Printf("web: events replay for %s: %v", id, replayErr)
			}
			replayed, projectionErr := projectBrowserEvents(
				replay.seed,
				journal,
				since,
				replay.authoritative,
			)
			if projectionErr != nil {
				log.Printf("web: browser projection replay for %s: %v", id, projectionErr)
			}
			replayBoundary := as.bcast.replayBoundary(sub, replayed)
			replayDeduper = newBrowserReplayDeduplicator(replayed, replayBoundary)
			for _, event := range replayed {
				if err := writeBrowserSSEFrame(w, event); err != nil {
					return
				}
			}
		}
	}

	ctx := r.Context()
	for {
		select {
		case event, ok := <-sub.ch:
			if !ok {
				return
			}
			if replayDeduper != nil && replayDeduper.skip(event) {
				continue
			}
			if err := as.app.ReadSessionID(id, func(*session.Session) error { return nil }); err != nil {
				return
			}
			if err := writeBrowserSSEFrame(w, event); err != nil {
				return
			}
		case <-sub.done:
			return
		case <-ctx.Done():
			return
		}
	}
}

type browserReplayDeduplicator struct {
	durableIDs     map[string]struct{}
	replayBoundary uint64
}

func newBrowserReplayDeduplicator(
	replayed []BrowserEvent,
	replayBoundary uint64,
) *browserReplayDeduplicator {
	if len(replayed) == 0 {
		return nil
	}
	// The handler subscribes before replay, and the subscriber channel is the
	// only place duplicates can wait. If more than this many visible events
	// arrive during replay, the bounded broadcaster drops the subscriber.
	start := max(0, len(replayed)-broadcasterBufferSize)
	ids := make(map[string]struct{}, len(replayed)-start)
	for _, event := range replayed[start:] {
		if !event.transient && event.ID != "" {
			ids[event.ID] = struct{}{}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	return &browserReplayDeduplicator{
		durableIDs:     ids,
		replayBoundary: replayBoundary,
	}
}

func (d *browserReplayDeduplicator) skip(event BrowserEvent) bool {
	if d == nil || len(d.durableIDs) == 0 {
		return false
	}
	// A queued transient may predate a durable terminal event already emitted
	// by replay. Drop it until the durable handoff boundary is known, otherwise
	// its older status could roll the browser back after terminal replay.
	if event.transient {
		return event.sequence <= d.replayBoundary
	}
	if _, duplicate := d.durableIDs[event.ID]; duplicate {
		delete(d.durableIDs, event.ID)
		return true
	}
	// Broadcaster delivery is ordered. The first unseen durable event is past
	// the replay tail, so no older duplicate can appear after it.
	d.durableIDs = nil
	return false
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
	stream, err := s.statusStreamForSession(id, since)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "session not found: "+id)
		return
	}
	defer stream.Close()
	if _, ok := w.(http.Flusher); !ok {
		writeErr(w, http.StatusInternalServerError, "general_error", "streaming not supported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	for {
		snapshot, ok := stream.Next(r.Context())
		if !ok {
			return
		}
		if snapshot.Session.ID != id {
			return
		}
		if err := writeStatusSSE(w, statusapi.FromRuntime(snapshot)); err != nil {
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

func (s *Server) statusStreamForSession(id, since string) (*runtime.StatusStream, error) {
	if id == "" || filepath.Base(id) != id {
		return nil, os.ErrNotExist
	}
	if value, ok := s.sessions.Load(id); ok {
		active := value.(*activeSession)
		if active.app == nil || active.app.Status == nil {
			return nil, os.ErrNotExist
		}
		var stream *runtime.StatusStream
		err := active.app.ReadSessionID(id, func(*session.Session) error {
			stream = active.app.Status.OpenStream(runtime.StatusStreamOptions{
				After:  since,
				Follow: true,
			})
			return nil
		})
		if err == nil {
			return stream, nil
		}
		if !errors.Is(err, app.ErrSessionChanged) && !errors.Is(err, app.ErrSessionUnavailable) {
			return nil, err
		}
	}

	status, err := s.historicalStatusStore(id)
	if err != nil {
		return nil, err
	}
	return status.OpenStream(runtime.StatusStreamOptions{After: since}), nil
}

func (s *Server) historicalStatusStore(id string) (*runtime.StatusStore, error) {
	dir := filepath.Join(s.opts.Cfg.SessionsDir(), id)
	info, _, err := session.LoadInfoPage(dir, "", 1)
	if err != nil {
		return nil, err
	}
	seed := runtime.StatusSeed{
		SessionID:        info.ID,
		SessionAlias:     info.Alias,
		MaxPendingInputs: runtime.DefaultMaxPendingInput,
		TokenUsage:       info.TokenUsage,
		ContextUsage:     info.ContextUsage,
	}
	// Historical reads preserve the valid-prefix projection when replay repairs
	// and reports a malformed journal suffix.
	status, _ := runtime.NewStatusStoreFromReplay(seed, func(visit func(events.Event)) error {
		return session.ReplayEventsWithCatalog(dir, eventcatalog.Default(), visit)
	})
	status.RecoverAfterRestart()
	return status, nil
}
