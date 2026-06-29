package tools

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	chunkWriteDefaultMode   = "overwrite"
	chunkWriteCreateMode    = "create"
	chunkWriteOverwriteMode = "overwrite"
	chunkWriteMaxChunkBytes = 64 * 1024
	chunkWriteMaxChunkChars = 64 * 1024
	chunkWriteSessionTTL    = 2 * time.Hour
)

type chunkWriteManager struct {
	mu       sync.Mutex
	workDir  string
	sessions map[string]*chunkWriteSession
	now      func() time.Time
}

type chunkWriteSession struct {
	id        string
	rel       string
	abs       string
	mode      string
	fileMode  os.FileMode
	createdAt time.Time
	updatedAt time.Time
	chunks    map[int]chunkWriteChunk
}

type chunkWriteChunk struct {
	content string
	hash    string
	bytes   int
	chars   int
}

func newChunkWriteManager(workDir string) *chunkWriteManager {
	if workDir != "" {
		if abs, err := filepath.Abs(workDir); err == nil {
			workDir = abs
		}
	}
	return &chunkWriteManager{
		workDir:  workDir,
		sessions: map[string]*chunkWriteSession{},
		now:      func() time.Time { return time.Now().UTC() },
	}
}

func writeBeginTool(manager *chunkWriteManager) Tool {
	return Tool{
		Name:        "write_begin",
		Description: "Begin a chunked full-file write session for a long generated file. Use write_chunk to add chunks and write_commit to atomically create or overwrite the final file.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "Working-dir-relative target file path"},
				"mode": map[string]any{"type": "string", "description": "overwrite (default) or create"},
			},
			"required": []string{"path"},
		},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			path, _ := in["path"].(string)
			mode, _ := in["mode"].(string)
			session, err := manager.begin(path, mode)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("write_begin: write_id=%s path=%s mode=%s max_chunk_bytes=%d max_chunk_chars=%d", session.id, session.rel, session.mode, chunkWriteMaxChunkBytes, chunkWriteMaxChunkChars), nil
		},
	}
}

func writeChunkTool(manager *chunkWriteManager) Tool {
	return Tool{
		Name:        "write_chunk",
		Description: "Record one chunk for a chunked write session. The result is a compact acknowledgement and never echoes content.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"write_id": map[string]any{"type": "string"},
				"index":    map[string]any{"type": "integer", "description": "Zero-based chunk index"},
				"content":  map[string]any{"type": "string"},
				"sha256":   map[string]any{"type": "string", "description": "Optional SHA-256 hex digest of this chunk"},
			},
			"required": []string{"write_id", "index", "content"},
		},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			writeID, _ := in["write_id"].(string)
			index, ok := toInt(in["index"])
			if !ok {
				return "", fmt.Errorf("write_chunk: index must be an integer")
			}
			content, contentOK := in["content"].(string)
			if !contentOK {
				return "", fmt.Errorf("write_chunk: missing content")
			}
			expectedHash, _ := in["sha256"].(string)
			chunk, duplicate, count, err := manager.chunk(writeID, index, content, expectedHash)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("write_chunk: write_id=%s index=%d bytes=%d chars=%d sha256=%s chunks=%d duplicate=%t", writeID, index, chunk.bytes, chunk.chars, chunk.hash, count, duplicate), nil
		},
	}
}

func writeCommitTool(manager *chunkWriteManager) Tool {
	return Tool{
		Name:        "write_commit",
		Description: "Validate and commit a chunked write session to its final file using a temporary file plus rename.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"write_id":        map[string]any{"type": "string"},
				"expected_chunks": map[string]any{"type": "integer", "description": "Optional expected number of chunks"},
				"sha256":          map[string]any{"type": "string", "description": "Optional SHA-256 hex digest of the assembled content"},
			},
			"required": []string{"write_id"},
		},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			writeID, _ := in["write_id"].(string)
			expectedChunks := -1
			if value, ok := in["expected_chunks"]; ok && value != nil {
				parsed, parsedOK := toInt(value)
				if !parsedOK || parsed < 0 {
					return "", fmt.Errorf("write_commit: expected_chunks must be a non-negative integer")
				}
				expectedChunks = parsed
			}
			expectedHash, _ := in["sha256"].(string)
			result, err := manager.commit(writeID, expectedChunks, expectedHash)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("write_commit: write_id=%s path=%s bytes=%d chars=%d chunks=%d sha256=%s", writeID, result.rel, result.bytes, result.chars, result.chunks, result.hash), nil
		},
	}
}

func writeAbortTool(manager *chunkWriteManager) Tool {
	return Tool{
		Name:        "write_abort",
		Description: "Abort and discard an unfinished chunked write session.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"write_id": map[string]any{"type": "string"},
			},
			"required": []string{"write_id"},
		},
		Handler: func(ctx context.Context, in map[string]any) (string, error) {
			writeID, _ := in["write_id"].(string)
			chunks, err := manager.abort(writeID)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("write_abort: write_id=%s aborted chunks=%d", writeID, chunks), nil
		},
	}
}

func (m *chunkWriteManager) begin(path, mode string) (*chunkWriteSession, error) {
	if m == nil {
		return nil, fmt.Errorf("write_begin: manager unavailable")
	}
	mode = strings.TrimSpace(mode)
	if mode == "" {
		mode = chunkWriteDefaultMode
	}
	if mode != chunkWriteOverwriteMode && mode != chunkWriteCreateMode {
		return nil, fmt.Errorf("write_begin: unsupported mode %q (expected overwrite or create)", mode)
	}
	rel, abs, err := resolveChunkWritePath(m.workDir, path)
	if err != nil {
		return nil, fmt.Errorf("write_begin: %w", err)
	}
	fileMode := os.FileMode(0o644)
	if info, statErr := os.Stat(abs); statErr == nil {
		if info.IsDir() {
			return nil, fmt.Errorf("write_begin: target %s is a directory", rel)
		}
		if mode == chunkWriteCreateMode {
			return nil, fmt.Errorf("write_begin: target %s already exists", rel)
		}
		fileMode = info.Mode().Perm()
	} else if !os.IsNotExist(statErr) {
		return nil, statErr
	}
	id, err := randomWriteID()
	if err != nil {
		return nil, err
	}
	now := m.now()
	session := &chunkWriteSession{
		id:        id,
		rel:       rel,
		abs:       abs,
		mode:      mode,
		fileMode:  fileMode,
		createdAt: now,
		updatedAt: now,
		chunks:    map[int]chunkWriteChunk{},
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupExpiredLocked(now)
	for _, active := range m.sessions {
		if active.abs == abs {
			return nil, fmt.Errorf("write_begin: a write session is already active for %s", rel)
		}
	}
	m.sessions[id] = session
	return session, nil
}

func (m *chunkWriteManager) chunk(writeID string, index int, content, expectedHash string) (chunkWriteChunk, bool, int, error) {
	if writeID == "" {
		return chunkWriteChunk{}, false, 0, fmt.Errorf("write_chunk: missing write_id")
	}
	if index < 0 {
		return chunkWriteChunk{}, false, 0, fmt.Errorf("write_chunk: index must be non-negative")
	}
	contentBytes := len(content)
	contentChars := utf8.RuneCountInString(content)
	if contentBytes > chunkWriteMaxChunkBytes || contentChars > chunkWriteMaxChunkChars {
		return chunkWriteChunk{}, false, 0, fmt.Errorf("write_chunk: content exceeds max chunk limits")
	}
	hash := sha256Hex([]byte(content))
	if expectedHash != "" && !strings.EqualFold(expectedHash, hash) {
		return chunkWriteChunk{}, false, 0, fmt.Errorf("write_chunk: chunk checksum mismatch for index %d", index)
	}
	chunk := chunkWriteChunk{
		content: content,
		hash:    hash,
		bytes:   contentBytes,
		chars:   contentChars,
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	session, err := m.sessionLocked(writeID, "write_chunk")
	if err != nil {
		return chunkWriteChunk{}, false, 0, err
	}
	if existing, ok := session.chunks[index]; ok {
		if existing.hash == chunk.hash && existing.content == chunk.content {
			return existing, true, len(session.chunks), nil
		}
		return chunkWriteChunk{}, false, len(session.chunks), fmt.Errorf("write_chunk: conflicting duplicate chunk %d", index)
	}
	session.chunks[index] = chunk
	session.updatedAt = m.now()
	return chunk, false, len(session.chunks), nil
}

type chunkWriteCommitResult struct {
	rel    string
	bytes  int
	chars  int
	chunks int
	hash   string
}

func (m *chunkWriteManager) commit(writeID string, expectedChunks int, expectedHash string) (chunkWriteCommitResult, error) {
	if writeID == "" {
		return chunkWriteCommitResult{}, fmt.Errorf("write_commit: missing write_id")
	}
	m.mu.Lock()
	session, err := m.sessionLocked(writeID, "write_commit")
	if err != nil {
		m.mu.Unlock()
		return chunkWriteCommitResult{}, err
	}
	content, result, err := assembleChunkWriteContent(session, expectedChunks, expectedHash)
	if err != nil {
		m.mu.Unlock()
		return chunkWriteCommitResult{}, err
	}
	delete(m.sessions, writeID)
	m.mu.Unlock()
	if err := commitChunkWriteFile(session.abs, content, session.fileMode); err != nil {
		m.mu.Lock()
		m.sessions[writeID] = session
		m.mu.Unlock()
		return chunkWriteCommitResult{}, fmt.Errorf("write_commit: %w", err)
	}
	return result, nil
}

func (m *chunkWriteManager) abort(writeID string) (int, error) {
	if writeID == "" {
		return 0, fmt.Errorf("write_abort: missing write_id")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	session, err := m.sessionLocked(writeID, "write_abort")
	if err != nil {
		return 0, err
	}
	delete(m.sessions, writeID)
	return len(session.chunks), nil
}

func (m *chunkWriteManager) sessionLocked(writeID, prefix string) (*chunkWriteSession, error) {
	now := m.now()
	session, ok := m.sessions[writeID]
	if !ok {
		m.cleanupExpiredLocked(now)
		return nil, fmt.Errorf("%s: unknown write_id %q", prefix, writeID)
	}
	if now.Sub(session.updatedAt) > chunkWriteSessionTTL {
		delete(m.sessions, writeID)
		return nil, fmt.Errorf("%s: stale write_id %q", prefix, writeID)
	}
	m.cleanupExpiredLocked(now)
	return session, nil
}

func (m *chunkWriteManager) cleanupExpiredLocked(now time.Time) {
	for id, session := range m.sessions {
		if now.Sub(session.updatedAt) > chunkWriteSessionTTL {
			delete(m.sessions, id)
		}
	}
}

func assembleChunkWriteContent(session *chunkWriteSession, expectedChunks int, expectedHash string) (string, chunkWriteCommitResult, error) {
	chunkCount := len(session.chunks)
	if chunkCount == 0 {
		return "", chunkWriteCommitResult{}, fmt.Errorf("write_commit: no chunks recorded")
	}
	if expectedChunks >= 0 && expectedChunks != chunkCount {
		return "", chunkWriteCommitResult{}, fmt.Errorf("write_commit: expected_chunks=%d but recorded chunks=%d", expectedChunks, chunkCount)
	}
	totalBytes := 0
	for i := 0; i < chunkCount; i++ {
		chunk, ok := session.chunks[i]
		if !ok {
			return "", chunkWriteCommitResult{}, fmt.Errorf("write_commit: missing chunk %d", i)
		}
		totalBytes += chunk.bytes
	}
	var b strings.Builder
	b.Grow(totalBytes)
	for i := 0; i < chunkCount; i++ {
		chunk := session.chunks[i]
		b.WriteString(chunk.content)
	}
	content := b.String()
	hash := sha256Hex([]byte(content))
	if expectedHash != "" && !strings.EqualFold(expectedHash, hash) {
		return "", chunkWriteCommitResult{}, fmt.Errorf("write_commit: full checksum mismatch")
	}
	return content, chunkWriteCommitResult{
		rel:    session.rel,
		bytes:  len(content),
		chars:  utf8.RuneCountInString(content),
		chunks: chunkCount,
		hash:   hash,
	}, nil
}

func commitChunkWriteFile(path string, content string, mode os.FileMode) error {
	if mode == 0 {
		mode = 0o644
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".juex-write-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func resolveChunkWritePath(workDir, path string) (string, string, error) {
	if strings.TrimSpace(path) == "" {
		return "", "", fmt.Errorf("unsafe path %q", path)
	}
	if strings.Contains(path, ":") || strings.HasPrefix(path, `\\`) || strings.HasPrefix(path, "//") {
		return "", "", fmt.Errorf("unsafe path %q: colons and UNC paths are not allowed", path)
	}
	path = filepath.FromSlash(path)
	if filepath.IsAbs(path) {
		return "", "", fmt.Errorf("unsafe path %q: absolute paths are not allowed", path)
	}
	rel := filepath.Clean(path)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("unsafe path %q: path escapes workspace", path)
	}
	root := workDir
	if root == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", "", err
		}
		root = cwd
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", "", err
	}
	evalRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return "", "", err
	}
	abs := filepath.Join(absRoot, rel)
	if !pathWithin(absRoot, abs) {
		return "", "", fmt.Errorf("unsafe path %q: path escapes workspace", path)
	}
	if err := checkChunkWriteSymlinkBoundary(evalRoot, abs, rel); err != nil {
		return "", "", err
	}
	return filepath.ToSlash(rel), abs, nil
}

func checkChunkWriteSymlinkBoundary(evalRoot, abs, rel string) error {
	checkPath := abs
	if _, err := os.Lstat(checkPath); err != nil {
		for {
			parent := filepath.Dir(checkPath)
			if parent == checkPath {
				return nil
			}
			if _, statErr := os.Lstat(parent); statErr == nil {
				checkPath = parent
				break
			}
			checkPath = parent
		}
	}
	evaluated, err := filepath.EvalSymlinks(checkPath)
	if err != nil {
		return err
	}
	if !pathWithin(evalRoot, evaluated) {
		return fmt.Errorf("unsafe path %q: symlink escapes workspace", filepath.ToSlash(rel))
	}
	return nil
}

func randomWriteID() (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return "w_" + hex.EncodeToString(buf[:]), nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
