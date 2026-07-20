package fleetweb

import (
	"errors"
	"fmt"
	"mime"
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
	switch r.Method {
	case http.MethodGet:
		s.handleListDirectories(w, r)
	case http.MethodPost:
		s.handleCreateDirectory(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET or POST required")
	}
}

func (s *Server) handleListDirectories(w http.ResponseWriter, r *http.Request) {
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

func (s *Server) handleCreateDirectory(w http.ResponseWriter, r *http.Request) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(
			w,
			http.StatusUnsupportedMediaType,
			"unsupported_media_type",
			"Content-Type must be application/json",
		)
		return
	}
	var body struct {
		Parent *string `json:"parent"`
		Name   *string `json:"name"`
	}
	if !decodeJSONBody(
		w,
		r,
		maxAgentMutationRequestBytes,
		&body,
		"request body must describe a parent and directory name",
	) {
		return
	}
	if body.Parent == nil || body.Name == nil {
		writeError(w, http.StatusBadRequest, "bad_request", "parent and name are required")
		return
	}
	entry, err := createDirectory(*body.Parent, *body.Name)
	if err != nil {
		var validation *directoryValidationError
		switch {
		case errors.As(err, &validation):
			writeError(w, http.StatusBadRequest, "bad_request", validation.Error())
		case errors.Is(err, os.ErrExist):
			writeError(w, http.StatusConflict, "conflict", "a file or directory with that name already exists")
		default:
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to create directory")
		}
		return
	}
	writeJSON(w, http.StatusCreated, entry)
}

type directoryValidationError struct {
	message string
}

func (e *directoryValidationError) Error() string {
	return e.message
}

func createDirectory(parent, name string) (DirectoryEntry, error) {
	if parent == "" {
		return DirectoryEntry{}, &directoryValidationError{message: "parent is required"}
	}
	if !filepath.IsAbs(parent) {
		return DirectoryEntry{}, &directoryValidationError{message: "parent must be an absolute directory"}
	}
	parent = filepath.Clean(parent)
	info, err := os.Lstat(parent)
	if err != nil {
		return DirectoryEntry{}, &directoryValidationError{
			message: fmt.Sprintf("inspect parent directory: %v", err),
		}
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return DirectoryEntry{}, &directoryValidationError{
			message: "parent must be a real directory",
		}
	}

	name = strings.TrimSpace(name)
	if name == "" {
		return DirectoryEntry{}, &directoryValidationError{message: "directory name is required"}
	}
	if name == "." ||
		name == ".." ||
		strings.ContainsAny(name, `/\`) ||
		strings.ContainsRune(name, '\x00') {
		return DirectoryEntry{}, &directoryValidationError{
			message: "directory name must be one path component",
		}
	}

	path := filepath.Join(parent, name)
	if err := os.Mkdir(path, 0o755); err != nil {
		return DirectoryEntry{}, fmt.Errorf("create directory: %w", err)
	}
	return DirectoryEntry{
		Name: name,
		Path: path,
	}, nil
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
