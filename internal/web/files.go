package web

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const maxFilePreviewBytes = 256 * 1024
const maxFileTreeDepth = 12

type FileNode struct {
	Name              string      `json:"name"`
	Path              string      `json:"path"`
	IsDir             bool        `json:"is_dir"`
	Children          []*FileNode `json:"children,omitempty"`
	ChildrenTruncated bool        `json:"children_truncated,omitempty"`
}

type FileContent struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	Size      int64  `json:"size"`
	Truncated bool   `json:"truncated"`
}

func (s *Server) handleFilesTree(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET")
		return
	}

	root := s.opts.Cfg.WorkDir
	if root == "" {
		root = "."
	}
	root, err := filepath.Abs(root)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
	if resolvedRoot, err := filepath.EvalSymlinks(root); err == nil {
		root = resolvedRoot
	}

	tree, err := buildFileTree(root, "", 0)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, tree)
}

func buildFileTree(root, relPath string, depth int) (*FileNode, error) {
	absPath := filepath.Join(root, relPath)
	info, err := os.Lstat(absPath)
	if err != nil {
		return nil, err
	}

	node := &FileNode{
		Name:  filepath.Base(absPath),
		Path:  filepath.ToSlash(relPath),
		IsDir: info.IsDir(),
	}
	if relPath == "" {
		node.Name = filepath.Base(root)
		node.Path = "/"
	}

	if node.IsDir {
		if depth >= maxFileTreeDepth {
			node.ChildrenTruncated = true
			return node, nil
		}
		entries, err := os.ReadDir(absPath)
		if err != nil {
			return nil, err
		}
		var children []*FileNode
		for _, e := range entries {
			name := e.Name()
			if shouldSkipTreeEntry(name) {
				continue
			}
			childRel := filepath.Join(relPath, name)
			child, err := buildFileTree(root, childRel, depth+1)
			if err == nil && child != nil {
				children = append(children, child)
			}
		}
		sort.Slice(children, func(i, j int) bool {
			if children[i].IsDir != children[j].IsDir {
				return children[i].IsDir // dirs first
			}
			return children[i].Name < children[j].Name
		})
		node.Children = children
	}

	return node, nil
}

func shouldSkipTreeEntry(name string) bool {
	switch name {
	case ".git", ".juex", "node_modules", "dist":
		return true
	default:
		return false
	}
}

func (s *Server) handleFilesContent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET")
		return
	}

	root := s.opts.Cfg.WorkDir
	if root == "" {
		root = "."
	}
	root, err := filepath.Abs(root)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
	if resolvedRoot, err := filepath.EvalSymlinks(root); err == nil {
		root = resolvedRoot
	}

	reqPath := r.URL.Query().Get("path")
	if reqPath == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "missing path parameter")
		return
	}

	relPath, absPath, err := resolveWorkPath(root, reqPath)
	if err != nil {
		writeErr(w, http.StatusForbidden, "forbidden", "path outside work directory")
		return
	}

	resolved, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			writeErr(w, http.StatusNotFound, "not_found", "file not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
	if _, err := relativeInside(root, resolved); err != nil {
		writeErr(w, http.StatusForbidden, "forbidden", "path outside work directory")
		return
	}

	info, err := os.Stat(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			writeErr(w, http.StatusNotFound, "not_found", "file not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
	if info.IsDir() {
		writeErr(w, http.StatusBadRequest, "bad_request", "path is a directory")
		return
	}

	f, err := os.Open(resolved)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
	defer f.Close()

	buf, err := io.ReadAll(io.LimitReader(f, maxFilePreviewBytes+1))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
	truncated := len(buf) > maxFilePreviewBytes
	if truncated {
		buf = buf[:maxFilePreviewBytes]
	}
	if isBinary(buf) {
		writeErr(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "binary file preview is not supported")
		return
	}

	writeJSON(w, http.StatusOK, FileContent{
		Path:      relPath,
		Content:   string(buf),
		Size:      info.Size(),
		Truncated: truncated,
	})
}

func resolveWorkPath(root, reqPath string) (string, string, error) {
	if strings.HasPrefix(reqPath, "/") {
		return "", "", errors.New("invalid path")
	}
	clean := filepath.Clean(filepath.FromSlash(reqPath))
	if clean == "." || clean == "" || filepath.IsAbs(clean) {
		return "", "", errors.New("invalid path")
	}
	if _, err := relativeInside(root, filepath.Join(root, clean)); err != nil {
		return "", "", err
	}
	return filepath.ToSlash(clean), filepath.Join(root, clean), nil
}

func relativeInside(root, path string) (string, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", errors.New("path escapes root")
	}
	return rel, nil
}

func isBinary(data []byte) bool {
	sample := data
	if len(sample) > 512 {
		sample = sample[:512]
	}
	for _, b := range sample {
		if b == 0 {
			return true
		}
	}
	return false
}
