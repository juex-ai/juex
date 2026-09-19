package fleethttp

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
	"github.com/juex-ai/juex/internal/foundation/serviceendpoint"
)

type memoryBackend interface {
	Status(context.Context, mc.Caller) (mc.Status, error)
	Search(context.Context, mc.Caller, mc.Query) (mc.Page, error)
	Read(context.Context, mc.Caller, mc.ReadRequest) (mc.Entry, error)
	Admin(context.Context, mc.Caller, mc.AdminRequest) (mc.Receipt, error)
}

func (s *Server) initMemory() {
	s.memoryCaller = mc.Caller{FleetID: s.managementIdentity.FleetID, AgentID: "user", ThreadID: "0", Profile: mc.ProfileUser}
	s.memory = mc.New(serviceendpoint.FileResolver{Home: s.managementHome, Fleet: s.memoryCaller.FleetID}, "memory", s.memoryCaller)
}

// Web is a trusted user client, like the CLI. Fleet's process manager does not
// own Memory data or participate in its transactions.
func (s *Server) handleMemory(w http.ResponseWriter, r *http.Request) {
	if s.memory == nil {
		writeError(w, 503, "unavailable", "Memory service is unavailable")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/memory/")
	var result any
	var err error
	switch {
	case path == "status":
		if r.Method != http.MethodGet {
			writeError(w, 405, "method_not_allowed", "GET required")
			return
		}
		result, err = s.memory.Status(r.Context(), s.memoryCaller)
	case path == "entries":
		if r.Method != http.MethodGet {
			writeError(w, 405, "method_not_allowed", "GET required")
			return
		}
		q := mc.Query{Text: r.URL.Query().Get("q"), Limit: 20}
		for name, dest := range map[string]*int{"offset": &q.Offset, "limit": &q.Limit} {
			if value := r.URL.Query().Get(name); value != "" {
				*dest, err = strconv.Atoi(value)
				if err != nil {
					writeError(w, 400, "bad_request", "Invalid pagination")
					return
				}
			}
		}
		if q.Offset < 0 || q.Limit < 1 || q.Limit > 50 || len(q.Text) > 2048 {
			writeError(w, 400, "bad_request", "Search requires offset >= 0, limit 1-50 and a query up to 2048 bytes")
			return
		}
		result, err = s.memory.Search(r.Context(), s.memoryCaller, q)
	case strings.HasPrefix(path, "entries/"):
		id := strings.TrimPrefix(path, "entries/")
		if err := mc.ValidateEntryID(id); err != nil {
			writeError(w, 400, "bad_request", err.Error())
			return
		}
		switch r.Method {
		case http.MethodGet:
			result, err = s.memory.Read(r.Context(), s.memoryCaller, mc.ReadRequest{ID: id})
		case http.MethodPut, http.MethodDelete:
			var body struct {
				Key              string    `json:"key"`
				Entry            *mc.Entry `json:"entry,omitempty"`
				ExpectedRevision uint64    `json:"expected_revision"`
				Confirm          string    `json:"confirm,omitempty"`
			}
			if !decodeJSONBody(w, r, 256*1024, &body, "A Memory change is required") {
				return
			}
			if strings.TrimSpace(body.Key) == "" || len(body.Key) > 128 || body.ExpectedRevision == 0 {
				writeError(w, 400, "bad_request", "An operation key and current revision are required")
				return
			}
			change := mc.Change{Entry: mc.Entry{ID: id}, ExpectedRevision: body.ExpectedRevision, Delete: r.Method == http.MethodDelete}
			if change.Delete {
				if body.Confirm != id || body.Entry != nil {
					writeError(w, 400, "bad_request", "Confirm the Memory entry ID to delete")
					return
				}
			} else {
				if body.Entry == nil || body.Entry.ID != id || body.Entry.Revision != body.ExpectedRevision || body.Confirm != "" {
					writeError(w, 400, "bad_request", "The entry must match the selected ID and revision")
					return
				}
				change.Entry = *body.Entry
			}
			// Keep the browser's frozen snapshot intact: a read/merge here would change
			// the request fingerprint when retrying after a lost successful response.
			// correct also supports version-guarded deletion with normal suppression.
			result, err = s.memory.Admin(r.Context(), s.memoryCaller, mc.AdminRequest{Key: body.Key, Action: "correct", Changes: []mc.Change{change}})
		default:
			writeError(w, 405, "method_not_allowed", "GET, PUT or DELETE required")
			return
		}
	default:
		writeError(w, 404, "not_found", "Memory route not found")
		return
	}
	if err != nil {
		writeMemoryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func writeMemoryError(w http.ResponseWriter, err error) {
	message := err.Error()
	switch {
	case strings.Contains(message, "revision conflict"), strings.Contains(message, "idempotency conflict"):
		writeError(w, 409, "conflict", message)
	case strings.Contains(message, "memory entry unavailable in caller scope"):
		writeError(w, 404, "not_found", message)
	case strings.Contains(message, "biz error:"):
		writeError(w, 422, "invalid_memory", message)
	default:
		writeError(w, 503, "memory_unavailable", message)
	}
}
