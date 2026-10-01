package managementhttp

import (
	"context"
	"net/http"
	"strconv"

	"github.com/juex-ai/juex/internal/calendar"
	"github.com/juex-ai/juex/internal/foundation/application"
	"github.com/juex-ai/juex/internal/management"
)

type Calendar interface {
	Status(context.Context, application.Access) (calendar.Status, error)
	Configure(context.Context, application.Access, int64, bool) (calendar.Status, error)
	Schedules(context.Context, application.Access, int, int) (calendar.SchedulePage, error)
	Occurrences(context.Context, application.Access, string, int, int) (calendar.OccurrencePage, error)
	Change(context.Context, application.Access, *application.Scope, string, calendar.Change) (calendar.Receipt, error)
}
type CalendarConfiguration struct {
	Version int64 `json:"version"`
	Enabled bool  `json:"enabled"`
}
type CalendarChange struct {
	RequestID string `json:"request_id"`
	calendar.Change
}

func (s *Server) calendarStatus(w http.ResponseWriter, r *http.Request, u management.User) {
	v, e := s.options.Calendar.Status(r.Context(), appAccess(r, u))
	respond(w, v, e)
}
func (s *Server) calendarConfigure(w http.ResponseWriter, r *http.Request, u management.User) {
	var q CalendarConfiguration
	if err := decode(r, &q); err != nil {
		respond(w, nil, err)
		return
	}
	v, e := s.options.Calendar.Configure(r.Context(), appAccess(r, u), q.Version, q.Enabled)
	respond(w, v, e)
}
func calendarPage(r *http.Request) (int, int, error) {
	offset, limit := 0, 50
	for key, target := range map[string]*int{"offset": &offset, "limit": &limit} {
		if raw := r.URL.Query().Get(key); raw != "" {
			v, e := strconv.Atoi(raw)
			if e != nil || v < 0 || v > 1<<30 {
				return 0, 0, application.ErrInvalid
			}
			*target = v
		}
	}
	return offset, limit, nil
}
func (s *Server) calendarSchedules(w http.ResponseWriter, r *http.Request, u management.User) {
	offset, limit, e := calendarPage(r)
	if e != nil {
		respond(w, nil, e)
		return
	}
	v, e := s.options.Calendar.Schedules(r.Context(), appAccess(r, u), offset, limit)
	respond(w, v, e)
}
func (s *Server) calendarOccurrences(w http.ResponseWriter, r *http.Request, u management.User) {
	offset, limit, e := calendarPage(r)
	if e != nil {
		respond(w, nil, e)
		return
	}
	v, e := s.options.Calendar.Occurrences(r.Context(), appAccess(r, u), r.URL.Query().Get("schedule_id"), offset, limit)
	respond(w, v, e)
}
func (s *Server) calendarChange(w http.ResponseWriter, r *http.Request, u management.User) {
	var q CalendarChange
	if e := decode(r, &q); e != nil {
		respond(w, nil, e)
		return
	}
	v, e := s.options.Calendar.Change(r.Context(), appAccess(r, u), nil, q.RequestID, q.Change)
	respond(w, v, e)
}
