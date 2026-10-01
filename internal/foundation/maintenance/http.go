package maintenance

import (
	"net/http"
	"strings"
)

func (g Gate) HTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Event streams are readers, not admissions. Graceful HTTP shutdown owns
		// their final lifetime; they must not keep the drain barrier occupied.
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		enter := g.Enter
		if r.URL.Path == "/api/auth/login" || r.URL.Path == "/api/auth/logout" || strings.HasSuffix(r.URL.Path, "/cancel") || strings.HasSuffix(r.URL.Path, "/revoke") {
			enter = g.Control
		}
		done, err := enter()
		if err != nil {
			w.Header().Set("Retry-After", "30")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"maintenance"}`))
			return
		}
		defer done()
		next.ServeHTTP(w, r)
	})
}
