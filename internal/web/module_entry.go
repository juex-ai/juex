package web

import "net/http"

func (s *Server) requireModule(w http.ResponseWriter, id string) bool {
	if s.opts.Cfg.ModuleEnabled(id) {
		return true
	}
	writeErr(w, http.StatusForbidden, "module_disabled", id+" module is disabled")
	return false
}
