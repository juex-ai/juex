package fleetweb

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/juex-ai/juex/internal/agentstate"
)

type DirectoryEntry struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Registered bool   `json:"registered"`
}

type DirectoryListing struct {
	Path   string           `json:"path"`
	Parent string           `json:"parent"`
	Dirs   []DirectoryEntry `json:"dirs"`
}

func (s *Server) handleDirectories(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	showHidden := false
	if raw := r.URL.Query().Get("show_hidden"); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "show_hidden must be true or false")
			return
		}
		showHidden = parsed
	}
	listing, err := listDirectories(r.URL.Query().Get("path"), showHidden)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, listing)
}

func listDirectories(path string, showHidden bool) (DirectoryListing, error) {
	if path == "" {
		var err error
		path, err = os.UserHomeDir()
		if err != nil {
			return DirectoryListing{}, fmt.Errorf("fleet web: resolve user home: %w", err)
		}
	}
	if !filepath.IsAbs(path) {
		return DirectoryListing{}, errors.New("fleet web: directory path must be absolute")
	}
	path = filepath.Clean(path)
	info, err := os.Lstat(path)
	if err != nil {
		return DirectoryListing{}, fmt.Errorf("fleet web: inspect directory %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return DirectoryListing{}, fmt.Errorf("fleet web: %s is not a real directory", path)
	}
	children, err := os.ReadDir(path)
	if err != nil {
		return DirectoryListing{}, fmt.Errorf("fleet web: read directory %s: %w", path, err)
	}

	dirs := make([]DirectoryEntry, 0, len(children))
	for _, child := range children {
		if !showHidden && strings.HasPrefix(child.Name(), ".") {
			continue
		}
		if child.Type()&os.ModeSymlink != 0 {
			continue
		}
		childInfo, err := child.Info()
		if err != nil || !childInfo.IsDir() || childInfo.Mode()&os.ModeSymlink != 0 {
			continue
		}
		childPath := filepath.Join(path, child.Name())
		registered, err := agentstate.WorkspaceHasMarker(childPath)
		if err != nil {
			registered = false
		}
		dirs = append(dirs, DirectoryEntry{
			Name:       child.Name(),
			Path:       childPath,
			Registered: registered,
		})
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Name < dirs[j].Name })
	return DirectoryListing{
		Path:   path,
		Parent: filepath.Dir(path),
		Dirs:   dirs,
	}, nil
}
