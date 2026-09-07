package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/app"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/thread"
)

type ThreadModulesSnapshot struct {
	AgentID             string `json:"agent_id"`
	ThreadID            string `json:"thread_id"`
	CompositionRevision string `json:"composition_revision"`
	Revision            string `json:"revision"`
	ReadOnly            bool   `json:"read_only"`
	// ObservedCursor is the metadata cursor observed before reading module files.
	// It is a lower bound, not an as-of position for independently written state.
	ObservedCursor thread.EventCursor                             `json:"observed_cursor"`
	Modules        map[runtimemodule.ID]runtimemodule.ModuleState `json:"modules"`
	UI             []runtimemodule.UIContribution                 `json:"ui"`
}

func (s *Server) inspectionCatalog() (*runtimemodule.InspectionCatalog, error) {
	return app.ThreadInspectionCatalog(s.opts.Cfg, s.inspectionFactories...)
}

func (s *Server) readModuleSnapshot(r *http.Request, id string, catalog *runtimemodule.InspectionCatalog) (ThreadModulesSnapshot, error) {
	metadata, err := thread.NewStore(s.opts.Cfg.RuntimePaths().StateDir).Inspect(id)
	if err != nil {
		return ThreadModulesSnapshot{}, err
	}
	states, ui := catalog.Snapshot(r.Context(), runtimemodule.ThreadContext{ID: id, Dir: metadata.Dir})
	snapshot := ThreadModulesSnapshot{
		AgentID: s.opts.Cfg.AgentID, ThreadID: id,
		CompositionRevision: runtimemodule.ContentRevision([]any{s.startedAt, catalog.Revision(), s.opts.Cfg.RuntimePaths().StateDir}),
		ReadOnly:            s.readOnly || metadata.Projection.RetentionState == thread.RetentionArchived,
		ObservedCursor:      metadata.Projection.EventCursor, Modules: states, UI: ui,
	}
	snapshot.Revision = runtimemodule.ContentRevision([]any{snapshot.AgentID, id, snapshot.CompositionRevision, snapshot.ReadOnly, states, ui})
	return snapshot, nil
}

func (s *Server) dispatchModuleInspection(w http.ResponseWriter, r *http.Request, id, rest string) {
	if !thread.ValidID(id) {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid Thread id")
		return
	}
	catalog, err := s.inspectionCatalog()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
	if rest == "modules" && r.Method == http.MethodGet {
		snapshot, err := s.readModuleSnapshot(r, id, catalog)
		if err != nil {
			writeThreadLookupError(w, id, err)
			return
		}
		writeJSON(w, http.StatusOK, snapshot)
		return
	}
	if rest == "modules/events" && r.Method == http.MethodGet {
		s.handleModuleEvents(w, r, id, catalog)
		return
	}
	parts := strings.Split(rest, "/")
	if len(parts) < 4 {
		writeErr(w, http.StatusNotFound, "not_found", "module route not found")
		return
	}
	entry, ok := catalog.Lookup(runtimemodule.ID(parts[1]))
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "module is not registered")
		return
	}
	metadata, err := thread.NewStore(s.opts.Cfg.RuntimePaths().StateDir).Inspect(id)
	if err != nil {
		writeThreadLookupError(w, id, err)
		return
	}
	scope := runtimemodule.ThreadContext{ID: id, Dir: metadata.Dir}
	if parts[2] == "operations" && len(parts) == 4 && r.Method == http.MethodPost {
		if s.readOnly || metadata.Projection.RetentionState == thread.RetentionArchived {
			writeErr(w, http.StatusForbidden, "forbidden", "Thread is read-only")
			return
		}
		operation, ok := entry.Operations[parts[3]]
		if !ok {
			writeErr(w, http.StatusNotFound, "not_found", "operation is not registered")
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil || !json.Valid(body) {
			writeErr(w, http.StatusBadRequest, "bad_request", "valid JSON body required")
			return
		}
		// Recheck after reading the request body: a slow upload may span archive.
		current, err := thread.NewStore(s.opts.Cfg.RuntimePaths().StateDir).Inspect(id)
		if err != nil {
			writeThreadLookupError(w, id, err)
			return
		}
		if current.Projection.RetentionState == thread.RetentionArchived {
			writeErr(w, http.StatusForbidden, "forbidden", "Thread is read-only")
			return
		}
		scope.Dir = current.Dir
		value, err := operation(r.Context(), scope, body)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, value)
		return
	}
	if parts[2] != "resources" || len(parts) != 5 || r.Method != http.MethodGet {
		writeErr(w, http.StatusNotFound, "not_found", "module route not found")
		return
	}
	rootFunc, ok := entry.Files[parts[3]]
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "resource is not registered")
		return
	}
	root, relative, err := resolveModuleTreePath(metadata.Dir, rootFunc(scope))
	if err != nil {
		writeErr(w, http.StatusForbidden, "forbidden", err.Error())
		return
	}
	resourceRoot := filepath.Join(root, relative)
	if parts[4] == "events" {
		s.handleModuleResourceEvents(w, r, root, resourceRoot)
		return
	}
	if parts[4] == "tree" {
		tree, err := buildFileTreeWithSkip(resourceRoot, "", 0, nil)
		if os.IsNotExist(err) {
			tree = &FileNode{Name: parts[3], Path: "/", IsDir: true}
			err = nil
		}
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, tree)
		return
	}
	file, reqErr := resolveFileAtRoot(resourceRoot, r.URL.Query().Get("path"), "")
	if reqErr != nil {
		reqErr.write(w)
		return
	}
	switch parts[4] {
	case "content":
		serveFileContent(w, file)
	case "raw":
		serveFileRaw(w, r, file)
	default:
		writeErr(w, http.StatusNotFound, "not_found", "resource route not found")
	}
}

func (s *Server) handleModuleEvents(w http.ResponseWriter, r *http.Request, id string, catalog *runtimemodule.InspectionCatalog) {
	if !thread.ValidID(id) {
		writeThreadLookupError(w, id, thread.ErrInvalidID)
		return
	}
	if _, err := thread.NewStore(s.opts.Cfg.RuntimePaths().StateDir).Inspect(id); err != nil {
		writeThreadLookupError(w, id, err)
		return
	}
	paths := make([]string, 0)
	stateDir := s.opts.Cfg.RuntimePaths().StateDir
	store := thread.NewStore(stateDir)
	for _, root := range []string{store.ThreadsDir(), store.ArchiveDir()} {
		scope := runtimemodule.ThreadContext{ID: id, Dir: filepath.Join(root, id)}
		paths = append(paths, filepath.Join(scope.Dir, "thread.json"))
		paths = append(paths, catalog.StatePaths(scope)...)
	}
	watcher, err := newModuleWatcher(stateDir, paths)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
	defer watcher.Close()
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "general_error", "streaming not supported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	// Subscriptions always replace the baseline, including reconnects with an old
	// Last-Event-ID. This stream is a file view, not durable-event replay.
	epoch := fmt.Sprintf("%x", time.Now().UnixNano())
	sequence := 0
	revision := ""
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		if err := watcher.Refresh(); err != nil {
			return
		}
		snapshot, err := s.readModuleSnapshot(r, id, catalog)
		if err != nil {
			return
		}
		if snapshot.Revision != revision {
			sequence++
			body, err := json.Marshal(snapshot)
			if err != nil {
				return
			}
			if _, err := fmt.Fprintf(w, "id: %s:%d\ndata: %s\n\n", epoch, sequence, body); err != nil {
				return
			}
			flusher.Flush()
			if s.readOnly {
				return
			} // Reconnect through Fleet to revalidate config and endpoint selection.
			revision = snapshot.Revision
		}
		select {
		case <-watcher.Changed():
		case <-watcher.Done():
			return
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-s.inspectionDone:
			return
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) handleModuleResourceEvents(w http.ResponseWriter, r *http.Request, threadDir, resourceRoot string) {
	watcher, err := newScopedModuleWatcher(threadDir, []string{filepath.Join(resourceRoot, ".watch")}, resourceRoot)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
	defer watcher.Close()
	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		if err := watcher.Refresh(); err != nil {
			return
		}
		if _, err := fmt.Fprint(w, "data: {\"type\":\"resource.changed\"}\n\n"); err != nil {
			return
		}
		flusher.Flush()
		if s.readOnly {
			return
		}
		select {
		case <-watcher.Changed():
		case <-watcher.Done():
			return
		case <-s.inspectionDone:
			return
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
		}
	}
}
