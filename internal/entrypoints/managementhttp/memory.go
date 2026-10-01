package managementhttp

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/juex-ai/juex/internal/foundation/application"
	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
	"github.com/juex-ai/juex/internal/management"
	"github.com/juex-ai/juex/internal/memory"
)

type Memory interface {
	Status(context.Context, application.Access) (memory.Status, error)
	Configure(context.Context, application.Access, int64, bool, string) (memory.Status, error)
	Search(context.Context, application.Access, mc.Query) (mc.Page, error)
	Read(context.Context, application.Access, mc.ReadRequest) (mc.Entry, error)
	Facts(context.Context, application.Access, mc.Query) (mc.FactPage, error)
	Reviews(context.Context, application.Access, int, int) (memory.ReviewPage, error)
	StorageRules(context.Context, application.Access, int, int) (memory.StorageRules, error)
	Administer(context.Context, application.Access, mc.AdminRequest) (mc.Receipt, error)
}
type MemoryConfiguration struct {
	Version  int64  `json:"version"`
	Enabled  bool   `json:"enabled"`
	Strategy string `json:"strategy"`
}

func appAccess(r *http.Request, user management.User) application.Access {
	return application.Access{ActorID: user.ID, TenantID: r.PathValue("tenant"), UserID: r.PathValue("owner")}
}
func (s *Server) memoryStatus(w http.ResponseWriter, r *http.Request, u management.User) {
	v, e := s.options.Memory.Status(r.Context(), appAccess(r, u))
	respond(w, v, e)
}
func (s *Server) memoryConfigure(w http.ResponseWriter, r *http.Request, u management.User) {
	var body MemoryConfiguration
	if err := decode(r, &body); err != nil {
		respond(w, nil, err)
		return
	}
	v, e := s.options.Memory.Configure(r.Context(), appAccess(r, u), body.Version, body.Enabled, body.Strategy)
	respond(w, v, e)
}
func memoryQuery(r *http.Request) (mc.Query, error) {
	values := r.URL.Query()
	q := mc.Query{Text: values.Get("text"), Domain: values.Get("domain"), Entity: values.Get("entity"), Subject: values.Get("subject"), Predicate: values.Get("predicate"), View: values.Get("view"), Status: values.Get("status"), SourceAgentID: values.Get("source_agent_id"), Workspace: values.Get("workspace"), Project: values.Get("project"), Limit: 50}
	for key, target := range map[string]*int{"offset": &q.Offset, "limit": &q.Limit} {
		if raw := values.Get(key); raw != "" {
			v, err := strconv.Atoi(raw)
			if err != nil || v < 0 || v > 1<<30 {
				return q, application.ErrInvalid
			}
			*target = v
		}
	}
	if raw := values.Get("at"); raw != "" {
		value, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return q, application.ErrInvalid
		}
		q.At = &value
	}
	return q, nil
}
func (s *Server) memorySearch(w http.ResponseWriter, r *http.Request, u management.User) {
	q, e := memoryQuery(r)
	if e != nil {
		respond(w, nil, e)
		return
	}
	v, e := s.options.Memory.Search(r.Context(), appAccess(r, u), q)
	respond(w, v, e)
}
func (s *Server) memoryFacts(w http.ResponseWriter, r *http.Request, u management.User) {
	q, e := memoryQuery(r)
	if e != nil {
		respond(w, nil, e)
		return
	}
	v, e := s.options.Memory.Facts(r.Context(), appAccess(r, u), q)
	respond(w, v, e)
}
func (s *Server) memoryRead(w http.ResponseWriter, r *http.Request, u management.User) {
	q, e := memoryQuery(r)
	if e != nil {
		respond(w, nil, e)
		return
	}
	v, e := s.options.Memory.Read(r.Context(), appAccess(r, u), mc.ReadRequest{ID: r.PathValue("entry"), View: q.View, At: q.At})
	respond(w, v, e)
}
func (s *Server) memoryReviews(w http.ResponseWriter, r *http.Request, u management.User) {
	q, e := memoryQuery(r)
	if e != nil {
		respond(w, nil, e)
		return
	}
	v, e := s.options.Memory.Reviews(r.Context(), appAccess(r, u), q.Offset, q.Limit)
	respond(w, v, e)
}
func (s *Server) memoryRules(w http.ResponseWriter, r *http.Request, u management.User) {
	q, e := memoryQuery(r)
	if e != nil {
		respond(w, nil, e)
		return
	}
	v, e := s.options.Memory.StorageRules(r.Context(), appAccess(r, u), q.Offset, q.Limit)
	respond(w, v, e)
}
func (s *Server) memoryAdminister(w http.ResponseWriter, r *http.Request, u management.User) {
	var body mc.AdminRequest
	if e := decode(r, &body); e != nil {
		respond(w, nil, e)
		return
	}
	v, e := s.options.Memory.Administer(r.Context(), appAccess(r, u), body)
	respond(w, v, e)
}
