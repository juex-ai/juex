package managementhttp

import (
	"net/http"
	"strconv"

	"github.com/juex-ai/juex/internal/management"
)

type NotificationReadRequest struct {
	Read bool `json:"read"`
}

func (s *Server) notifications(w http.ResponseWriter, r *http.Request, u management.User) {
	var before int64
	if raw := r.URL.Query().Get("before"); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || v < 0 {
			respond(w, nil, management.ErrInvalid)
			return
		}
		before = v
	}
	value, err := s.options.Directory.Notifications(r.Context(), u.ID, r.PathValue("tenant"), before, 50)
	respond(w, value, err)
}
func (s *Server) markNotification(w http.ResponseWriter, r *http.Request, u management.User) {
	var body NotificationReadRequest
	if err := decode(r, &body); err != nil {
		respond(w, nil, err)
		return
	}
	respond(w, map[string]bool{"read": body.Read}, s.options.Directory.MarkNotification(r.Context(), u.ID, r.PathValue("tenant"), r.PathValue("notification"), body.Read))
}
func (s *Server) notificationPreferences(w http.ResponseWriter, r *http.Request, u management.User) {
	value, err := s.options.Directory.NotificationPreferences(r.Context(), u.ID, r.PathValue("tenant"))
	respond(w, value, err)
}
func (s *Server) configureNotifications(w http.ResponseWriter, r *http.Request, u management.User) {
	var body management.NotificationPreferences
	if err := decode(r, &body); err != nil {
		respond(w, nil, err)
		return
	}
	value, err := s.options.Directory.ConfigureNotifications(r.Context(), u.ID, r.PathValue("tenant"), body)
	respond(w, value, err)
}
