package web

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/runtime/workmem"
	"github.com/juex-ai/juex/internal/statusapi"
	"github.com/juex-ai/juex/internal/thread"
	"github.com/juex-ai/juex/internal/usermedia"
)

type errorJSON struct {
	Error      string `json:"error"`
	Message    string `json:"message"`
	Suggestion string `json:"suggestion,omitempty"`
	Retryable  bool   `json:"retryable"`
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(value)
}

func writeErr(w http.ResponseWriter, status int, kind, message string) {
	writeJSON(w, status, errorJSON{Error: kind, Message: message})
}

func writeThreadLookupError(w http.ResponseWriter, id string, err error) {
	if errors.Is(err, errThreadInactive) {
		writeErr(w, http.StatusConflict, "conflict", "Thread is archived: "+id)
		return
	}
	if os.IsNotExist(err) {
		writeErr(w, http.StatusNotFound, "not_found", "Thread not found: "+id)
		return
	}
	writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
}

type threadListResponse struct {
	Active   []thread.IndexEntry `json:"active_threads"`
	Archived []thread.IndexEntry `json:"archived_threads"`
}

func (s *Server) handleListThreads(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listThreads(w)
	case http.MethodPost:
		s.createWorkerThread(w, r)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET or POST")
	}
}

func (s *Server) listThreads(w http.ResponseWriter) {
	store := thread.NewStore(s.opts.Cfg.RuntimePaths().StateDir)
	entries, err := store.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
	response := threadListResponse{Active: []thread.IndexEntry{}, Archived: []thread.IndexEntry{}}
	for _, entry := range entries {
		if entry.ArchivedAt == nil {
			response.Active = append(response.Active, entry)
		} else {
			response.Archived = append(response.Archived, entry)
		}
	}
	writeJSON(w, http.StatusOK, response)
}

type createWorkerThreadRequest struct {
	Alias          string `json:"alias,omitempty"`
	ParentThreadID string `json:"parent_thread_id,omitempty"`
}

func (s *Server) createWorkerThread(w http.ResponseWriter, r *http.Request) {
	var request createWorkerThreadRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil && !errors.Is(err, io.EOF) {
			writeErr(w, http.StatusBadRequest, "bad_request", "expected JSON body")
			return
		}
	}
	store := thread.NewStore(s.opts.Cfg.RuntimePaths().StateDir)
	main, err := store.EnsureMain()
	if err == nil {
		err = main.Close()
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
	parentID := strings.TrimSpace(request.ParentThreadID)
	if parentID == "" {
		parentID = thread.MainID
	}
	target, err := store.CreateWorker(parentID, request.Alias)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	info := target.Info()
	_ = target.Close()
	writeJSON(w, http.StatusCreated, info)
}

type threadShowResponse struct {
	thread.Info
	Items          []thread.TimelineItem       `json:"items"`
	EventCursor    string                      `json:"event_cursor,omitempty"`
	HasMoreBefore  bool                        `json:"has_more_before"`
	PreviousCursor string                      `json:"previous_cursor,omitempty"`
	Goal           *workmem.GoalStatusSnapshot `json:"goal,omitempty"`
	Notes          *workmem.NotesSnapshot      `json:"notes,omitempty"`
}

func parseTimelineWindow(r *http.Request) (string, int, error) {
	cursor := strings.TrimSpace(r.URL.Query().Get("before"))
	limit := 80
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 {
			return "", 0, errors.New("limit must be a positive integer")
		}
		limit = min(value, 200)
	}
	return cursor, limit, nil
}

func (s *Server) handleThreadShow(w http.ResponseWriter, r *http.Request, id string) {
	cursor, limit, err := parseTimelineWindow(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if value, ok := s.threads.Load(id); ok {
		active := value.(*activeThread)
		var response threadShowResponse
		err := active.app.ReadThreadID(id, func(target *thread.Thread) error {
			response.Info = target.Info()
			page, pageErr := target.Timeline(cursor, limit)
			if pageErr != nil {
				return pageErr
			}
			response.Items = page.Items
			response.HasMoreBefore = page.HasMoreBefore
			response.PreviousCursor = page.PreviousCursor
			response.Goal, response.Notes = active.app.ThreadStateStatus()
			if active.app.Status != nil {
				response.EventCursor = active.app.Status.Snapshot().Cursor
			}
			return nil
		})
		if err == nil {
			writeJSON(w, http.StatusOK, response)
			return
		}
	}
	store := thread.NewStore(s.opts.Cfg.RuntimePaths().StateDir)
	target, err := store.OpenActive(id)
	if os.IsNotExist(err) {
		target, err = store.OpenArchived(id)
	}
	if err != nil {
		writeThreadLookupError(w, id, err)
		return
	}
	page, err := target.Timeline(cursor, limit)
	if err != nil {
		_ = target.Close()
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	goal, notes := threadStateStatus(target, nil)
	info := target.Info()
	if err := target.Close(); err != nil {
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, threadShowResponse{
		Info: info, Items: page.Items, HasMoreBefore: page.HasMoreBefore,
		PreviousCursor: page.PreviousCursor, Goal: goal, Notes: notes,
	})
}

func threadStateStatus(target *thread.Thread, active *activeThread) (*workmem.GoalStatusSnapshot, *workmem.NotesSnapshot) {
	if active != nil && active.app != nil {
		return active.app.ThreadStateStatus()
	}
	goal, _ := workmem.NewThreadGoalStateStore(target, workmem.GoalStateOptions{}).StatusSnapshot()
	notes, _ := workmem.NewThreadNotesStore(target).StatusSnapshot()
	return goal, notes
}

func (s *Server) handleArchiveThread(w http.ResponseWriter, r *http.Request, id string) {
	if id == thread.MainID {
		writeErr(w, http.StatusConflict, "conflict", "Main Thread cannot be archived")
		return
	}
	if value, ok := s.threads.Load(id); ok {
		active := value.(*activeThread)
		if active.app.Status != nil {
			status := active.app.Status.Snapshot().Thread
			if status.State.IsWorking() || status.PendingCount != 0 {
				writeErr(w, http.StatusConflict, "conflict", "Thread must be idle without pending input before archive")
				return
			}
		}
	}
	if mainValue, ok := s.threads.Load(thread.MainID); ok {
		managed, err := mainValue.(*activeThread).app.ArchiveManagedWorker(r.Context(), id)
		if managed {
			if err != nil {
				writeErr(w, http.StatusConflict, "conflict", err.Error())
				return
			}
			if active, loaded := s.threads.LoadAndDelete(id); loaded {
				active.(*activeThread).close()
			}
			writeJSON(w, http.StatusOK, map[string]any{"archived": true, "thread_id": id})
			return
		}
	}
	if active, ok := s.deferCloseActiveThread(id); ok {
		if err := active.app.WaitThreadReleased(r.Context()); err != nil {
			writeErr(w, http.StatusRequestTimeout, "request_cancelled", err.Error())
			return
		}
	}
	store := thread.NewStore(s.opts.Cfg.RuntimePaths().StateDir)
	target, err := store.OpenActive(id)
	if err == nil {
		err = store.Archive(target)
	}
	if err != nil {
		writeThreadLookupError(w, id, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"archived": true, "thread_id": id})
}

func (s *Server) handleUnarchiveThread(w http.ResponseWriter, _ *http.Request, id string) {
	target, err := thread.NewStore(s.opts.Cfg.RuntimePaths().StateDir).Unarchive(id)
	if err != nil {
		writeThreadLookupError(w, id, err)
		return
	}
	info := target.Info()
	_ = target.Close()
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) handleRenameThread(w http.ResponseWriter, r *http.Request, id string) {
	if id == thread.MainID {
		writeErr(w, http.StatusConflict, "conflict", "Main Thread alias is immutable")
		return
	}
	var request struct {
		Alias string `json:"alias"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil || strings.TrimSpace(request.Alias) == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "non-empty alias is required")
		return
	}
	var info thread.Info
	var err error
	if value, ok := s.threads.Load(id); ok {
		err = value.(*activeThread).app.ReadThreadID(id, func(target *thread.Thread) error {
			if applyErr := target.ApplyAlias(strings.TrimSpace(request.Alias)); applyErr != nil {
				return applyErr
			}
			info = target.Info()
			return nil
		})
	} else {
		store := thread.NewStore(s.opts.Cfg.RuntimePaths().StateDir)
		var target *thread.Thread
		target, err = store.OpenActive(id)
		if os.IsNotExist(err) {
			target, err = store.OpenArchived(id)
		}
		if err == nil {
			err = target.ApplyAlias(strings.TrimSpace(request.Alias))
			info = target.Info()
			closeErr := target.Close()
			if err == nil {
				err = closeErr
			}
		}
	}
	if err != nil {
		writeErr(w, http.StatusConflict, "conflict", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) handleDeleteThread(w http.ResponseWriter, _ *http.Request, id string) {
	if id == thread.MainID {
		writeErr(w, http.StatusConflict, "conflict", "Main Thread cannot be deleted")
		return
	}
	if err := app.DeleteThread(s.opts.Cfg, id); err != nil {
		writeThreadLookupError(w, id, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "thread_id": id})
}

type turnRequest struct {
	Prompt      string         `json:"prompt"`
	Kind        string         `json:"kind,omitempty"`
	Attachments []llm.MediaRef `json:"attachments,omitempty"`
}

type startTurnResponse struct {
	ThreadID         string                  `json:"thread_id,omitempty"`
	InputID          string                  `json:"input_id,omitempty"`
	AcceptedAt       *thread.Timestamp       `json:"accepted_at,omitempty"`
	State            string                  `json:"state,omitempty"`
	Cursor           string                  `json:"cursor,omitempty"`
	TurnID           string                  `json:"turn_id,omitempty"`
	Queued           bool                    `json:"queued,omitempty"`
	PendingCount     int                     `json:"pending_count,omitempty"`
	MaxPendingInputs int                     `json:"max_pending_inputs,omitempty"`
	Command          *app.SlashCommandResult `json:"command,omitempty"`
	Warnings         []app.TurnWarning       `json:"warnings,omitempty"`
}

func (s *Server) handleStartTurn(w http.ResponseWriter, r *http.Request, id string) {
	active, err := s.getThread(r.Context(), id)
	if err != nil {
		writeThreadLookupError(w, id, err)
		return
	}
	var request turnRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "expected JSON body")
		return
	}
	if len(request.Attachments) > 0 {
		if err := usermedia.ValidateThreadMediaRefs(s.opts.Cfg.MediaDir(), id, request.Attachments, usermedia.Limits{}); err != nil {
			writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
	}
	result := active.app.AdmitTurn(r.Context(), app.TurnAdmissionRequest{
		Prompt: request.Prompt, Kind: request.Kind, Attachments: request.Attachments,
	})
	if result.Start != nil {
		active.turns.start(result.Start.TurnID, result.Start.Message)
	}
	writeTurnAdmissionResult(w, active, id, result)
}

func writeTurnAdmissionResult(w http.ResponseWriter, active *activeThread, threadID string, result app.TurnAdmissionResult) {
	receipt := startTurnResponse{
		ThreadID: threadID,
		InputID:  result.InputID,
		TurnID:   result.TurnID,
	}
	if result.InputID != "" {
		acceptedAt := thread.NewTimestamp(time.Now())
		receipt.AcceptedAt = &acceptedAt
		receipt.State = "queued"
		if result.Kind == app.TurnAdmissionStarted {
			receipt.State = "assigned"
		}
		if active != nil && active.app != nil && active.app.Thread != nil {
			events := active.app.Thread.ReplaySnapshot().Events
			if len(events) > 0 {
				receipt.Cursor = events[len(events)-1].ID
			}
		}
	}
	switch result.Kind {
	case app.TurnAdmissionStarted:
		receipt.Warnings = result.Warnings
		writeJSON(w, http.StatusAccepted, receipt)
	case app.TurnAdmissionQueued:
		receipt.Queued = true
		receipt.PendingCount = result.PendingCount
		receipt.MaxPendingInputs = result.MaxPendingInputs
		receipt.Warnings = result.Warnings
		writeJSON(w, http.StatusAccepted, receipt)
	case app.TurnAdmissionCommandCompleted:
		writeJSON(w, http.StatusOK, startTurnResponse{ThreadID: threadID, Command: result.Command, Warnings: result.Warnings})
	case app.TurnAdmissionRejected:
		status := http.StatusBadRequest
		if result.Error.Kind == "pending_input_full" {
			status = http.StatusTooManyRequests
		}
		writeJSON(w, status, errorJSON{Error: result.Error.Kind, Message: result.Error.Message,
			Suggestion: result.Error.Suggestion, Retryable: result.Error.Retryable})
	case app.TurnAdmissionConflict:
		writeJSON(w, http.StatusConflict, errorJSON{Error: result.Error.Kind, Message: result.Error.Message,
			Suggestion: result.Error.Suggestion, Retryable: result.Error.Retryable})
	default:
		writeErr(w, http.StatusInternalServerError, "general_error", result.Error.Message)
	}
}

func (s *Server) handleThreadAttachmentUpload(w http.ResponseWriter, r *http.Request, id string) {
	if _, err := s.getThread(r.Context(), id); err != nil {
		writeThreadLookupError(w, id, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, usermedia.DefaultMaxBytes+1024*1024)
	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "expected multipart file field named file")
		return
	}
	defer func() { _ = file.Close() }()
	filename := ""
	if header != nil {
		filename = header.Filename
	}
	ref, err := usermedia.StoreUpload(s.opts.Cfg.MediaDir(), id, filename, file, usermedia.Limits{})
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ref)
}

func (s *Server) handleInterrupt(w http.ResponseWriter, r *http.Request, id string) {
	active, err := s.getThread(r.Context(), id)
	if err != nil {
		writeThreadLookupError(w, id, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cancelled": active.turns.interrupt()})
}

type compactRequest struct {
	Reason       string `json:"reason"`
	Instructions string `json:"instructions"`
}

func (s *Server) handleCompactThread(w http.ResponseWriter, r *http.Request, id string) {
	active, err := s.getThread(r.Context(), id)
	if err != nil {
		writeThreadLookupError(w, id, err)
		return
	}
	var request compactRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil && !errors.Is(err, io.EOF) {
			writeErr(w, http.StatusBadRequest, "bad_request", "expected JSON body")
			return
		}
	}
	if request.Reason == "" {
		request.Reason = "manual"
	}
	result, err := active.app.CompactWithInstructions(r.Context(), request.Reason, false, request.Instructions)
	if err != nil {
		writeErr(w, http.StatusConflict, "conflict", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleThreadContext(w http.ResponseWriter, _ *http.Request, id string) {
	if value, ok := s.threads.Load(id); ok {
		if snapshot, ok := value.(*activeThread).app.ActiveContextForThread(id); ok {
			writeJSON(w, http.StatusOK, snapshot)
			return
		}
	}
	store := thread.NewStore(s.opts.Cfg.RuntimePaths().StateDir)
	target, err := store.OpenActive(id)
	if os.IsNotExist(err) {
		target, err = store.OpenArchived(id)
	}
	if err != nil {
		writeThreadLookupError(w, id, err)
		return
	}
	snapshot := runtime.ActiveContextFromHistory(target.History)
	if err := target.Close(); err != nil {
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (s *Server) handleEventsSSE(w http.ResponseWriter, r *http.Request, id string) {
	active, err := s.getThread(r.Context(), id)
	if err != nil {
		writeThreadLookupError(w, id, err)
		return
	}
	if _, ok := w.(http.Flusher); !ok {
		writeErr(w, http.StatusInternalServerError, "general_error", "streaming not supported")
		return
	}
	subscription := active.bcast.subscribe()
	defer subscription.unsubscribe()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	var replayDeduper *browserReplayDeduplicator
	if since, requested := sseResumeCursorWithPresence(r); requested {
		replay, replayErr := captureCommittedEventReplay(active.app, id)
		if replayErr == nil {
			journal, _ := replay.readJournal()
			replayed, projectionErr := projectBrowserEvents(replay.seed, journal, since, replay.authoritative)
			if projectionErr != nil {
				log.Printf("web: browser replay %s: %v", id, projectionErr)
			}
			replayBoundary := active.bcast.replayBoundary(subscription, replayed)
			replayDeduper = newBrowserReplayDeduplicator(replayed, replayBoundary)
			for _, event := range replayed {
				if err := writeBrowserSSEFrame(w, event); err != nil {
					return
				}
			}
		}
	}
	for {
		select {
		case event, ok := <-subscription.ch:
			if !ok {
				return
			}
			if replayDeduper != nil && replayDeduper.skip(event) {
				continue
			}
			if writeBrowserSSEFrame(w, event) != nil {
				return
			}
		case <-subscription.done:
			return
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) handleThreadStatus(w http.ResponseWriter, _ *http.Request, id string) {
	status, err := s.statusSnapshotForThread(id)
	if err != nil {
		writeThreadLookupError(w, id, err)
		return
	}
	writeJSON(w, http.StatusOK, statusapi.FromRuntime(status))
}

func (s *Server) handleThreadStatusEvents(w http.ResponseWriter, r *http.Request, id string) {
	active, err := s.getThread(r.Context(), id)
	if err != nil || active.app.Status == nil {
		writeThreadLookupError(w, id, err)
		return
	}
	stream := active.app.Status.OpenStream(runtime.StatusStreamOptions{After: sseResumeCursor(r), Follow: true})
	defer stream.Close()
	w.Header().Set("Content-Type", "text/event-stream")
	for {
		snapshot, ok := stream.Next(r.Context())
		if !ok || snapshot.Thread.ID != id || writeStatusSSE(w, statusapi.FromRuntime(snapshot)) != nil {
			return
		}
	}
}

func (s *Server) statusSnapshotForThread(id string) (runtime.StatusSnapshot, error) {
	if value, ok := s.threads.Load(id); ok {
		active := value.(*activeThread)
		if active.app.Status != nil {
			return active.app.Status.Snapshot(), nil
		}
	}
	store := thread.NewStore(s.opts.Cfg.RuntimePaths().StateDir)
	target, err := store.OpenActive(id)
	if os.IsNotExist(err) {
		target, err = store.OpenArchived(id)
	}
	if err != nil {
		return runtime.StatusSnapshot{}, err
	}
	status, _ := runtime.NewStatusStoreFromReplay(runtime.StatusSeed{
		ThreadID: target.ID, ThreadAlias: target.Alias,
		MaxPendingInputs: runtime.DefaultMaxPendingInput,
		TokenUsage:       target.TokenUsageSnapshot(), ContextUsage: target.ContextUsageSnapshot(),
	}, func(visit func(events.Event)) error { target.ReplayEvents(visit); return nil })
	status.RecoverAfterRestart()
	snapshot := status.Snapshot()
	if err := target.Close(); err != nil {
		return runtime.StatusSnapshot{}, err
	}
	return snapshot, nil
}
