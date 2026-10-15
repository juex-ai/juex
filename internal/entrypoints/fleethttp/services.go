package fleethttp

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/juex-ai/juex/internal/fleet/services"
)

type serviceBackend interface {
	Status(context.Context) []services.Status
	Get(context.Context, string) (services.Status, error)
	Start(context.Context, string) (services.Status, error)
	Stop(context.Context, string) (services.Status, error)
	Restart(context.Context, string) (services.Status, error)
	Logs(string, int) ([]byte, error)
}

func (s *Server) handleServices(w http.ResponseWriter, r *http.Request) {
	if s.services == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "service management unavailable")
		return
	}
	suffix := strings.TrimPrefix(r.URL.Path, "/api/services")
	if suffix == "" || suffix == "/" {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
			return
		}
		writeJSON(w, http.StatusOK, s.services.Status(r.Context()))
		return
	}
	parts := strings.Split(strings.TrimPrefix(suffix, "/"), "/")
	if len(parts) > 2 || parts[0] == "" {
		writeError(w, http.StatusNotFound, "not_found", "service route not found")
		return
	}
	id := parts[0]
	status, err := s.services.Get(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
			return
		}
		writeJSON(w, http.StatusOK, status)
		return
	}
	if len(parts) != 2 {
		writeError(w, http.StatusNotFound, "not_found", "service route not found")
		return
	}
	operation := parts[1]
	if operation == "logs" {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
			return
		}
		lines := 100
		if value := r.URL.Query().Get("lines"); value != "" {
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed < 1 || parsed > 10000 {
				writeError(w, http.StatusBadRequest, "invalid_request", "invalid lines")
				return
			}
			lines = parsed
		}
		data, err := s.services.Logs(id, lines)
		if err != nil {
			writeError(w, http.StatusConflict, "unavailable", err.Error())
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write(data)
		return
	}
	if operation != "start" && operation != "stop" && operation != "restart" {
		writeError(w, http.StatusNotFound, "not_found", "service route not found")
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	switch operation {
	case "start":
		status, err = s.services.Start(r.Context(), id)
	case "stop":
		status, err = s.services.Stop(r.Context(), id)
	case "restart":
		status, err = s.services.Restart(r.Context(), id)
	}
	if err != nil {
		writeError(w, http.StatusConflict, "service_lifecycle", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, status)
}
