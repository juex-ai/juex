package managementhttp

import (
	"net/http"
	"strconv"

	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/management"
)

func (s *Server) usage(w http.ResponseWriter, r *http.Request, user management.User) {
	values := r.URL.Query()
	q := managedruntime.UsageQuery{TenantID: r.PathValue("tenant"), UserID: values.Get("user_id"), From: values.Get("from"), Until: values.Get("until"), Group: values.Get("group"), Limit: 100}
	if r.PathValue("owner") != "" {
		q.UserID = r.PathValue("owner")
	}
	if values.Get("offset") != "" {
		var err error
		q.Offset, err = strconv.Atoi(values.Get("offset"))
		if err != nil {
			respond(w, nil, managedruntime.ErrInvalid)
			return
		}
	}
	result, err := s.options.Runtime.Usage(r.Context(), user.ID, q)
	respond(w, result, err)
}
