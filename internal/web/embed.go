package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// distSubFS strips the "dist/" prefix so http.FileServer maps URLs
// directly to embedded paths.
func distSubFS() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		// embed.FS construction errors are programmer errors at compile
		// time; if Sub fails it's a structural bug and crashing is fine.
		panic("web: dist embed sub failed: " + err.Error())
	}
	return sub
}

// spaHandler serves files embedded under dist/ with a single-page-app
// fallback: any GET request whose path doesn't match an embedded file
// gets dist/index.html so client-side routing (React Router) takes over.
func spaHandler() http.Handler {
	root := distSubFS()
	fileServer := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Direct embedded asset (CSS, JS, fonts, etc.)
		if r.Method == http.MethodGet && hasEmbeddedFile(root, r.URL.Path) {
			fileServer.ServeHTTP(w, r)
			return
		}
		// SPA fallback: serve index.html for any unknown path so React
		// Router can render the route.
		index, err := fs.ReadFile(root, "index.html")
		if err != nil {
			http.Error(w, "frontend not built", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(index)
	})
}

// SPAHandler returns the embedded frontend with its client-side route
// fallback. Fleet web reuses the same application shell under agent routes.
func SPAHandler() http.Handler {
	return spaHandler()
}

// hasEmbeddedFile checks whether path corresponds to a real file under
// the embed root. Trims a leading slash so paths like "/assets/foo.js"
// match the embed entries directly.
func hasEmbeddedFile(root fs.FS, urlPath string) bool {
	p := strings.TrimPrefix(urlPath, "/")
	if p == "" || p == "/" {
		// Root path is a directory; let the fallback hit index.html.
		return false
	}
	st, err := fs.Stat(root, p)
	if err != nil {
		return false
	}
	return !st.IsDir()
}
