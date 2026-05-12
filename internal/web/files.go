package web

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type FileNode struct {
	Name     string      `json:"name"`
	Path     string      `json:"path"`
	IsDir    bool        `json:"is_dir"`
	Children []*FileNode `json:"children,omitempty"`
}

func (s *Server) handleFilesTree(w http.ResponseWriter, r *http.Request) {
	root := s.opts.Cfg.WorkDir
	if root == "" {
		root = "."
	}
	root, err := filepath.Abs(root)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}

	tree, err := buildFileTree(root, "")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, tree)
}

func buildFileTree(root, relPath string) (*FileNode, error) {
	absPath := filepath.Join(root, relPath)
	info, err := os.Stat(absPath)
	if err != nil {
		return nil, err
	}

	node := &FileNode{
		Name:  filepath.Base(absPath),
		Path:  relPath,
		IsDir: info.IsDir(),
	}
	if relPath == "" {
		node.Name = filepath.Base(root)
		node.Path = "/"
	}

	if node.IsDir {
		entries, err := os.ReadDir(absPath)
		if err != nil {
			return nil, err
		}
		var children []*FileNode
		for _, e := range entries {
			name := e.Name()
			if strings.HasPrefix(name, ".") && name != ".agents" {
				continue // skip hidden files except .agents
			}
			if name == "node_modules" || name == "dist" {
				continue
			}
			childRel := filepath.Join(relPath, name)
			child, err := buildFileTree(root, childRel)
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

func (s *Server) handleFilesContent(w http.ResponseWriter, r *http.Request) {
	root := s.opts.Cfg.WorkDir
	if root == "" {
		root = "."
	}
	root, err := filepath.Abs(root)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}

	reqPath := r.URL.Query().Get("path")
	if reqPath == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "missing path parameter")
		return
	}

	absPath := filepath.Join(root, filepath.Clean(reqPath))
	if !strings.HasPrefix(absPath, root) {
		writeErr(w, http.StatusForbidden, "forbidden", "path outside work directory")
		return
	}

	info, err := os.Stat(absPath)
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

	f, err := os.Open(absPath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	io.Copy(w, f)
}
