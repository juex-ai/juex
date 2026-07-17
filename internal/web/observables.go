package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/observable"
)

type observableCreateRequest = observable.Spec

func (s *Server) handleObservables(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		mgr, ok := s.observableManager(w, r)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, mgr.Status())
	case http.MethodPost:
		mgr, ok := s.observableManager(w, r)
		if !ok {
			return
		}
		var req observableCreateRequest
		if r.Body != nil {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
				writeErr(w, http.StatusBadRequest, "bad_request", "expected JSON body")
				return
			}
		}
		status, err := mgr.Create(r.Context(), observable.Spec(req))
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, status)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET or POST")
	}
}

func (s *Server) dispatchObservable(w http.ResponseWriter, r *http.Request) {
	id, rest := observablePathID(r.URL.Path)
	if id == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "missing observable id")
		return
	}
	mgr, ok := s.observableManager(w, r)
	if !ok {
		return
	}
	switch {
	case rest == "" && r.Method == http.MethodGet:
		status, err := mgr.StatusByID(id)
		if err != nil {
			writeErr(w, http.StatusNotFound, "not_found", err.Error())
			return
		}
		records, err := mgr.Observations(observable.ObservationFilter{ObservableID: id, Limit: 50})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"observable": status, "observations": records})
	case rest == "run" && r.Method == http.MethodPost:
		record, err := mgr.RunOnce(r.Context(), id)
		if err != nil {
			writeRunOnceError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, record)
	case rest == "start" && r.Method == http.MethodPost:
		if err := mgr.Start(r.Context(), id); err != nil {
			writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		status, err := mgr.StatusByID(id)
		if err != nil {
			writeErr(w, http.StatusNotFound, "not_found", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, status)
	case rest == "stop" && r.Method == http.MethodPost:
		if err := mgr.Stop(r.Context(), id); err != nil {
			writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		status, err := mgr.StatusByID(id)
		if err != nil {
			writeErr(w, http.StatusNotFound, "not_found", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, status)
	case rest == "" && r.Method == http.MethodDelete:
		if err := mgr.Delete(r.Context(), id); err != nil {
			writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
	case rest == "observations" && r.Method == http.MethodGet:
		limit := parsePositiveInt(r.URL.Query().Get("limit"), 50)
		records, err := mgr.Observations(observable.ObservationFilter{ObservableID: id, Limit: limit})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"observations": records})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "unsupported method or sub-path")
	}
}

func writeRunOnceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, observable.ErrObservableNotFound):
		writeErr(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, observable.ErrManagerClosed),
		errors.Is(err, observable.ErrObservableDeleting),
		errors.Is(err, observable.ErrRunOnceUnsupported):
		writeErr(w, http.StatusConflict, "conflict", err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
	}
}

func observablePathID(path string) (id, rest string) {
	const prefix = "/api/observables/"
	if !strings.HasPrefix(path, prefix) {
		return "", ""
	}
	tail := path[len(prefix):]
	if i := strings.IndexByte(tail, '/'); i >= 0 {
		return tail[:i], tail[i+1:]
	}
	return tail, ""
}

func (s *Server) observableManager(w http.ResponseWriter, r *http.Request) (*observable.Manager, bool) {
	as, err := s.activeObservableSession(r)
	if err != nil {
		if os.IsNotExist(err) {
			writeErr(w, http.StatusNotFound, "not_found", "active session not found")
			return nil, false
		}
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return nil, false
	}
	if as == nil || as.app == nil || as.app.Observables() == nil {
		writeErr(w, http.StatusInternalServerError, "general_error", "observable manager unavailable")
		return nil, false
	}
	return as.app.Observables(), true
}

func (s *Server) activeObservableSession(r *http.Request) (*activeSession, error) {
	id, ok, err := s.activePrimarySessionID()
	if err != nil {
		return nil, err
	}
	if ok {
		if v, exists := s.sessions.Load(id); exists {
			return v.(*activeSession), nil
		}
		as, err := s.getActiveSession(r.Context(), id)
		if err == nil {
			return as, nil
		}
		if !os.IsNotExist(err) {
			return nil, err
		}
	}
	return s.openSession(r.Context(), "", app.SessionModeAttachActive)
}

func parsePositiveInt(raw string, fallback int) int {
	if strings.TrimSpace(raw) == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}
