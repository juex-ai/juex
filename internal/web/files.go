package web

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/juex-ai/juex/internal/session"
)

const maxFilePreviewBytes = 256 * 1024
const maxFileTreeDepth = 12
const scratchpadLogicalRoot = ".juex/sessions"

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
	Kind      string `json:"kind"`
	MediaType string `json:"media_type,omitempty"`
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
	return buildFileTreeWithSkip(root, relPath, depth, shouldSkipTreeEntry)
}

func buildFileTreeWithSkip(root, relPath string, depth int, skip func(string) bool) (*FileNode, error) {
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
			if skip != nil && skip(name) {
				continue
			}
			childRel := filepath.Join(relPath, name)
			child, err := buildFileTreeWithSkip(root, childRel, depth+1, skip)
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

func (s *Server) handleSessionScratchpad(w http.ResponseWriter, r *http.Request, id string) {
	dir, ok := s.sessionScratchpadDir(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "session not found: "+id)
		return
	}

	root := s.opts.Cfg.SessionsDir()
	root, relPath, err := resolveScratchpadTreePath(root, dir)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}

	tree, err := buildFileTreeWithSkip(root, relPath, 0, nil)
	if os.IsNotExist(err) {
		tree = &FileNode{
			Name:  scratchpadName(dir),
			Path:  filepath.ToSlash(relPath),
			IsDir: true,
		}
		err = nil
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
	prefixFileTreePaths(tree, scratchpadLogicalRoot)
	writeJSON(w, http.StatusOK, tree)
}

func prefixFileTreePaths(node *FileNode, prefix string) {
	if node == nil {
		return
	}
	node.Path = filepath.ToSlash(filepath.Join(filepath.FromSlash(prefix), filepath.FromSlash(node.Path)))
	for _, child := range node.Children {
		prefixFileTreePaths(child, prefix)
	}
}

func resolveScratchpadTreePath(root, dir string) (string, string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return "", "", err
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return "", "", err
	}
	relPath, err := relativeInside(root, dir)
	if err != nil {
		return "", "", errors.New("scratchpad is outside workspace")
	}

	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return root, relPath, nil
	}
	root = resolvedRoot
	if err := rejectScratchpadTreeSymlinks(root, relPath); err != nil {
		return "", "", err
	}
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		// Lazy sessions intentionally have no directory yet. Their lexical path
		// remains valid relative to the resolved workspace root.
		return root, relPath, nil
	}
	if _, err := relativeInside(root, resolvedDir); err != nil {
		return "", "", errors.New("scratchpad is outside workspace")
	}
	return root, relPath, nil
}

func rejectScratchpadTreeSymlinks(root, relPath string) error {
	current := root
	for _, part := range strings.Split(filepath.Clean(relPath), string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("scratchpad path contains a symlink")
		}
	}
	return nil
}

func (s *Server) sessionScratchpadDir(id string) (string, bool) {
	if active, ok := s.sessions.Load(id); ok {
		return active.(*activeSession).app.Session.ScratchpadDir(), true
	}
	if id == "" || id == "." || id == ".." || filepath.Base(id) != id {
		return "", false
	}
	dir := filepath.Join(s.opts.Cfg.SessionsDir(), id)
	if !session.HasConversation(dir) {
		return "", false
	}
	return session.ScratchpadDir(dir), true
}

func scratchpadName(dir string) string {
	name := filepath.Base(dir)
	if name == "." || name == string(filepath.Separator) || name == "" {
		return "scratchpad"
	}
	return name
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

	file, reqErr := s.resolveFileRequest(r)
	if reqErr != nil {
		reqErr.write(w)
		return
	}

	f, err := os.Open(file.resolvedPath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
	defer f.Close()

	sample := make([]byte, 512)
	n, err := f.Read(sample)
	if err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
	if mediaType, ok := imagePreviewMediaType(sample[:n], file.relPath); ok {
		writeJSON(w, http.StatusOK, FileContent{
			Path:      file.relPath,
			Kind:      "image",
			MediaType: mediaType,
			Size:      file.info.Size(),
		})
		return
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}

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
		Path:      file.relPath,
		Content:   string(buf),
		Kind:      "text",
		Size:      file.info.Size(),
		Truncated: truncated,
	})
}

func (s *Server) handleFilesRaw(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET")
		return
	}

	file, reqErr := s.resolveFileRequest(r)
	if reqErr != nil {
		reqErr.write(w)
		return
	}

	f, err := os.Open(file.resolvedPath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
	defer f.Close()

	sample := make([]byte, 512)
	n, err := f.Read(sample)
	if err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
	mediaType, ok := imagePreviewMediaType(sample[:n], file.relPath)
	if !ok {
		writeErr(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "raw preview is only supported for images")
		return
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}

	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, file.relPath, file.info.ModTime(), f)
}

func (s *Server) handleMedia(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET")
		return
	}

	file, reqErr := s.resolveFileRequest(r)
	if reqErr != nil {
		reqErr.write(w)
		return
	}

	f, err := os.Open(file.resolvedPath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
	defer f.Close()

	sample := make([]byte, 512)
	n, err := f.Read(sample)
	if err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
	mediaType, ok := imagePreviewMediaType(sample[:n], file.relPath)
	if !ok {
		writeErr(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "media is only supported for images")
		return
	}
	etag, cacheControl := mediaCacheHeaders(file)
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}

	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Cache-Control", cacheControl)
	w.Header().Set("ETag", etag)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, file.relPath, file.info.ModTime(), f)
}

func mediaCacheHeaders(file resolvedFileRequest) (etag, cacheControl string) {
	relPath := filepath.ToSlash(file.relPath)
	name := filepath.Base(filepath.FromSlash(relPath))
	digest := strings.TrimSuffix(name, filepath.Ext(name))
	if strings.HasPrefix(relPath, ".juex/artifacts/") && len(digest) == 64 {
		if _, err := hex.DecodeString(digest); err == nil {
			return `"` + strings.ToLower(digest) + `"`, "public, max-age=31536000, immutable"
		}
	}
	return fmt.Sprintf(`W/"%x-%x"`, file.info.ModTime().UnixNano(), file.info.Size()), "no-cache"
}

type resolvedFileRequest struct {
	relPath      string
	resolvedPath string
	info         os.FileInfo
}

type fileRequestError struct {
	status  int
	code    string
	message string
}

func (e fileRequestError) write(w http.ResponseWriter) {
	writeErr(w, e.status, e.code, e.message)
}

func (s *Server) resolveFileRequest(r *http.Request) (resolvedFileRequest, *fileRequestError) {
	reqPath := r.URL.Query().Get("path")
	if reqPath == "" {
		return resolvedFileRequest{}, &fileRequestError{status: http.StatusBadRequest, code: "bad_request", message: "missing path parameter"}
	}

	root := s.opts.Cfg.WorkDir
	if root == "" {
		root = "."
	}
	displayPath := ""
	if scratchpadPath, logicalPath, ok := resolveScratchpadRequestPath(reqPath); ok {
		root = s.opts.Cfg.SessionsDir()
		reqPath = scratchpadPath
		displayPath = logicalPath
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return resolvedFileRequest{}, &fileRequestError{status: http.StatusInternalServerError, code: "general_error", message: err.Error()}
	}
	if resolvedRoot, err := filepath.EvalSymlinks(root); err == nil {
		root = resolvedRoot
	}

	relPath, absPath, err := resolveWorkPath(root, reqPath)
	if err != nil {
		return resolvedFileRequest{}, &fileRequestError{status: http.StatusForbidden, code: "forbidden", message: "path outside work directory"}
	}

	resolved, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return resolvedFileRequest{}, &fileRequestError{status: http.StatusNotFound, code: "not_found", message: "file not found"}
		}
		return resolvedFileRequest{}, &fileRequestError{status: http.StatusInternalServerError, code: "general_error", message: err.Error()}
	}
	if _, err := relativeInside(root, resolved); err != nil {
		return resolvedFileRequest{}, &fileRequestError{status: http.StatusForbidden, code: "forbidden", message: "path outside work directory"}
	}

	info, err := os.Stat(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			return resolvedFileRequest{}, &fileRequestError{status: http.StatusNotFound, code: "not_found", message: "file not found"}
		}
		return resolvedFileRequest{}, &fileRequestError{status: http.StatusInternalServerError, code: "general_error", message: err.Error()}
	}
	if info.IsDir() {
		return resolvedFileRequest{}, &fileRequestError{status: http.StatusBadRequest, code: "bad_request", message: "path is a directory"}
	}
	if displayPath != "" {
		relPath = displayPath
	}

	return resolvedFileRequest{relPath: relPath, resolvedPath: resolved, info: info}, nil
}

func resolveScratchpadRequestPath(reqPath string) (physicalPath, logicalPath string, ok bool) {
	if strings.HasPrefix(reqPath, "/") {
		return "", "", false
	}
	clean := filepath.Clean(filepath.FromSlash(reqPath))
	parts := strings.Split(clean, string(filepath.Separator))
	if len(parts) < 5 ||
		parts[0] != ".juex" ||
		parts[1] != "sessions" ||
		parts[2] == "" ||
		parts[2] == "." ||
		parts[2] == ".." ||
		parts[3] != "scratchpad" {
		return "", "", false
	}
	return filepath.Join(parts[2:]...), filepath.ToSlash(clean), true
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

func imagePreviewMediaType(data []byte, path string) (string, bool) {
	detected := mediaTypeBase(http.DetectContentType(data))
	if isSupportedImagePreviewType(detected) {
		return detected, true
	}
	extType := mediaTypeBase(mime.TypeByExtension(strings.ToLower(filepath.Ext(path))))
	if isSupportedImagePreviewType(extType) {
		return extType, true
	}
	return "", false
}

func mediaTypeBase(value string) string {
	if i := strings.Index(value, ";"); i >= 0 {
		value = value[:i]
	}
	return strings.ToLower(strings.TrimSpace(value))
}

func isSupportedImagePreviewType(mediaType string) bool {
	switch mediaType {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/bmp":
		return true
	default:
		return false
	}
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
