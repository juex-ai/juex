package web

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"

	"github.com/juex-ai/juex/internal/llm"
)

//go:embed templates/*.html
var templatesFS embed.FS

//go:embed static
var staticFS embed.FS

// staticFileServer mounts the embedded static dir at /static/<file>.
func staticFileServer() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(fmt.Sprintf("web: static embed: %v", err))
	}
	return http.FileServer(http.FS(sub))
}

// renderer parses every template once at startup. Each entry pairs a
// page template with the layout.
type renderer struct {
	tpls map[string]*template.Template
}

func newRenderer() (*renderer, error) {
	pages := []string{"index.html", "session.html", "new.html"}
	r := &renderer{tpls: make(map[string]*template.Template, len(pages))}
	funcs := template.FuncMap{
		// RoleOf returns "user", "assistant", "tool", etc. — used by
		// session.html to colour each line.
		"RoleOf": func(b llm.Block) string {
			switch b.Type {
			case llm.BlockText:
				return "msg"
			case llm.BlockReasoning:
				return "thinking"
			case llm.BlockToolUse:
				return "tool>"
			case llm.BlockToolResult:
				return "tool<"
			}
			return string(b.Type)
		},
	}
	for _, p := range pages {
		t, err := template.New("layout").Funcs(funcs).ParseFS(templatesFS,
			"templates/layout.html", "templates/"+p)
		if err != nil {
			return nil, fmt.Errorf("web: parse %s: %w", p, err)
		}
		r.tpls[p] = t
	}
	return r, nil
}

// Render executes the page template into a buffer first so an error never
// produces partial output. Callers can rely on: error → nothing written.
func (r *renderer) Render(w io.Writer, page string, data any) error {
	t, ok := r.tpls[page]
	if !ok {
		return fmt.Errorf("web: unknown page %q", page)
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", data); err != nil {
		return err
	}
	_, err := w.Write(buf.Bytes())
	return err
}
