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

	"github.com/juex-ai/juex/internal/chunkedwrite"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/sandbox"
)

const (
	chunkWriteDefaultMode   = "overwrite"
	chunkWriteCreateMode    = "create"
	chunkWriteOverwriteMode = "overwrite"
	// Tool storage can accept larger chunks, but provider-side tool argument
	// JSON is often bounded by the model's visible output budget.
	chunkWriteRecommendedChunkBytes = 4000
	chunkWriteRecommendedChunkChars = 2000
	chunkWriteMaxChunkBytes         = chunkWriteRecommendedChunkBytes
	chunkWriteMaxChunkChars         = chunkWriteRecommendedChunkChars
	chunkWriteSessionTTL            = 2 * time.Hour
)

type ChunkedWriteManager = chunkWriteManager

func NewChunkedWriteManager(workDir string, guards ...sandbox.PathGuard) *ChunkedWriteManager {
	return newChunkWriteManager(workDir, guards...)
}

func (m *chunkWriteManager) RestoreActiveFromHistory(history []llm.Message) {
	if m == nil {
		return
	}
	toolUses := map[string]llm.Block{}
	now := m.now()
	restored := map[string]*chunkWriteSession{}
	invalid := map[string]bool{}
	var events []chunkedwrite.Event
	for _, msg := range history {
		for _, block := range msg.Blocks {
			if block.Type == llm.BlockToolUse && block.ToolUseID != "" {
				toolUses[block.ToolUseID] = block
				continue
			}
			if block.Type != llm.BlockToolResult || block.ChunkedWrite == nil {
				continue
			}
			event := *block.ChunkedWrite
			if event.WriteID == "" || invalid[event.WriteID] {
				continue
			}
			events = append(events, event)
			switch event.Kind {
			case chunkedwrite.EventBegin:
				session, ok := m.restoreSessionFromBeginEvent(event, now)
				if !ok {
					invalid[event.WriteID] = true
					delete(restored, event.WriteID)
					continue
				}
				restored[event.WriteID] = session
			case chunkedwrite.EventChunk:
				session := restored[event.WriteID]
				if session == nil || event.Index < 0 {
					continue
				}
				content, ok := chunkContentFromToolUse(toolUses[block.ToolUseID])
				if !ok {
					invalid[event.WriteID] = true
					delete(restored, event.WriteID)
					continue
				}
				hash := sha256Hex([]byte(content))
				if event.SHA256 != "" && !strings.EqualFold(event.SHA256, hash) {
					invalid[event.WriteID] = true
					delete(restored, event.WriteID)
					continue
				}
				session.chunks[event.Index] = chunkWriteChunk{
					content: content,
					hash:    hash,
					bytes:   len(content),
					chars:   utf8.RuneCountInString(content),
				}
				session.updatedAt = now
			case chunkedwrite.EventCommit, chunkedwrite.EventAbort:
				delete(restored, event.WriteID)
				delete(invalid, event.WriteID)
			}
		}
	}
	states := chunkedwrite.BuildStates(events)
	for writeID := range restored {
		if states[writeID].Status != chunkedwrite.StatusActive {
			delete(restored, writeID)
		}
	}
	m.mu.Lock()
	m.sessions = restored
	m.mu.Unlock()
}

func (m *chunkWriteManager) restoreSessionFromBeginEvent(event chunkedwrite.Event, now time.Time) (*chunkWriteSession, bool) {
	mode := event.Mode
	if mode == "" {
		mode = chunkWriteDefaultMode
	}
	if mode != chunkWriteOverwriteMode && mode != chunkWriteCreateMode {
		return nil, false
	}
	resolved, err := m.resolvePath(event.Path)
	if err != nil {
		return nil, false
	}
	if err := m.guard.CheckWrite(resolved.Absolute); err != nil {
		return nil, false
	}
	fileMode := os.FileMode(event.FileMode).Perm()
	if fileMode == 0 {
		fileMode = 0o644
	}
	if info, statErr := os.Stat(resolved.Absolute); statErr == nil {
		if info.IsDir() || mode == chunkWriteCreateMode {
			return nil, false
		}
		if event.FileMode == 0 {
			fileMode = info.Mode().Perm()
		}
	} else if !os.IsNotExist(statErr) {
		return nil, false
	}
	return &chunkWriteSession{
		id:        event.WriteID,
		rel:       resolved.Relative,
		abs:       resolved.Absolute,
		identity:  resolved.Identity,
		mode:      mode,
		fileMode:  fileMode,
		createdAt: now,
		updatedAt: now,
		chunks:    map[int]chunkWriteChunk{},
	}, true
}

func chunkContentFromToolUse(block llm.Block) (string, bool) {
	if block.Type != llm.BlockToolUse {
		return "", false
	}
	content, ok := block.Input["content"].(string)
	return content, ok
}

type chunkWriteManager struct {
	mu          sync.Mutex
	paths       workspacePathResolver
	resolverErr error
	guard       sandbox.PathGuard
	sessions    map[string]*chunkWriteSession
	now         func() time.Time
}

type chunkWriteSession struct {
	id        string
	rel       string
	abs       string
	identity  string
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

func newChunkWriteManager(workDir string, guards ...sandbox.PathGuard) *chunkWriteManager {
	paths, resolverErr := newWorkspacePathResolver(workDir)
	var guard sandbox.PathGuard
	if len(guards) > 0 {
		guard = guards[0]
	}
	return &chunkWriteManager{
		paths:       paths,
		resolverErr: resolverErr,
		guard:       guard,
		sessions:    map[string]*chunkWriteSession{},
		now:         func() time.Time { return time.Now().UTC() },
	}
}

func (m *chunkWriteManager) resolvePath(path string) (workspacePath, error) {
	if m.resolverErr != nil {
		return workspacePath{}, m.resolverErr
	}
	return m.paths.Resolve(path)
}

func writeBeginTool(manager *chunkWriteManager) Tool {
	return writeBeginToolDefinition().BindResult(func(ctx context.Context, in map[string]any) (Result, error) {
		path, _ := in["path"].(string)
		mode, _ := in["mode"].(string)
		session, err := manager.begin(path, mode)
		if err != nil {
			return Result{}, err
		}
		text := fmt.Sprintf("write_begin: write_id=%s path=%s mode=%s max_chunk_bytes=%d max_chunk_chars=%d recommended_chunk_bytes=%d recommended_chunk_chars=%d", session.id, session.rel, session.mode, chunkWriteMaxChunkBytes, chunkWriteMaxChunkChars, chunkWriteRecommendedChunkBytes, chunkWriteRecommendedChunkChars)
		return Result{Text: text, Structured: chunkedwrite.Event{
			Kind:     chunkedwrite.EventBegin,
			WriteID:  session.id,
			Path:     session.rel,
			Mode:     session.mode,
			FileMode: uint32(session.fileMode.Perm()),
		}}, nil
	})
}

func writeChunkTool(manager *chunkWriteManager) Tool {
	return writeChunkToolDefinition().BindResult(func(ctx context.Context, in map[string]any) (Result, error) {
		writeID, _ := in["write_id"].(string)
		index, ok := toInt(in["index"])
		if !ok {
			return Result{}, fmt.Errorf("write_chunk: index must be an integer")
		}
		content, contentOK := in["content"].(string)
		if !contentOK {
			if projectedWriteChunkMetadata(in) {
				return Result{}, fmt.Errorf("write_chunk: missing content; content_omitted/content_bytes/content_chars/content_sha256 are summary metadata, not valid input. Send the actual content string, preferably no more than %d chars or %d bytes per chunk", chunkWriteRecommendedChunkChars, chunkWriteRecommendedChunkBytes)
			}
			return Result{}, fmt.Errorf("write_chunk: missing content")
		}
		expectedHash, _ := in["sha256"].(string)
		chunk, duplicate, count, err := manager.chunk(writeID, index, content, expectedHash)
		if err != nil {
			return Result{}, err
		}
		text := fmt.Sprintf("write_chunk: write_id=%s index=%d bytes=%d chars=%d sha256=%s chunks=%d duplicate=%t", writeID, index, chunk.bytes, chunk.chars, chunk.hash, count, duplicate)
		return Result{Text: text, Structured: chunkedwrite.Event{
			Kind:      chunkedwrite.EventChunk,
			WriteID:   writeID,
			Index:     index,
			Bytes:     chunk.bytes,
			Chars:     chunk.chars,
			SHA256:    chunk.hash,
			Chunks:    count,
			Duplicate: duplicate,
		}}, nil
	})
}

func projectedWriteChunkMetadata(in map[string]any) bool {
	for _, key := range []string{"content_omitted", "content_bytes", "content_chars", "content_sha256"} {
		if _, ok := in[key]; ok {
			return true
		}
	}
	return false
}

func writeCommitTool(manager *chunkWriteManager) Tool {
	return writeCommitToolDefinition().BindResult(func(ctx context.Context, in map[string]any) (Result, error) {
		writeID, _ := in["write_id"].(string)
		expectedChunks := -1
		if value, ok := in["expected_chunks"]; ok && value != nil {
			parsed, parsedOK := toInt(value)
			if !parsedOK || parsed < 0 {
				return Result{}, fmt.Errorf("write_commit: expected_chunks must be a non-negative integer")
			}
			expectedChunks = parsed
		}
		expectedHash, _ := in["sha256"].(string)
		result, err := manager.commit(writeID, expectedChunks, expectedHash)
		if err != nil {
			return Result{}, err
		}
		text := fmt.Sprintf("write_commit: write_id=%s path=%s bytes=%d chars=%d chunks=%d sha256=%s", writeID, result.rel, result.bytes, result.chars, result.chunks, result.hash)
		return Result{Text: text, Structured: chunkedwrite.Event{
			Kind:    chunkedwrite.EventCommit,
			WriteID: writeID,
			Path:    result.rel,
			Bytes:   result.bytes,
			Chars:   result.chars,
			Chunks:  result.chunks,
			SHA256:  result.hash,
		}}, nil
	})
}

func writeAbortTool(manager *chunkWriteManager) Tool {
	return writeAbortToolDefinition().BindResult(func(ctx context.Context, in map[string]any) (Result, error) {
		writeID, _ := in["write_id"].(string)
		chunks, err := manager.abort(writeID)
		if err != nil {
			return Result{}, err
		}
		text := fmt.Sprintf("write_abort: write_id=%s aborted chunks=%d", writeID, chunks)
		return Result{Text: text, Structured: chunkedwrite.Event{
			Kind:    chunkedwrite.EventAbort,
			WriteID: writeID,
			Chunks:  chunks,
		}}, nil
	})
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
	resolved, err := m.resolvePath(path)
	if err != nil {
		return nil, fmt.Errorf("write_begin: %w", err)
	}
	rel, abs := resolved.Relative, resolved.Absolute
	if err := m.guard.CheckWrite(abs); err != nil {
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
		identity:  resolved.Identity,
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
		if active.identity == resolved.Identity {
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
		return chunkWriteChunk{}, false, 0, fmt.Errorf("write_chunk: content exceeds max chunk limits (%d chars/%d bytes); split into smaller chunks, preferably no more than %d chars or %d bytes per chunk", chunkWriteMaxChunkChars, chunkWriteMaxChunkBytes, chunkWriteRecommendedChunkChars, chunkWriteRecommendedChunkBytes)
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
	resolved, err := m.resolvePath(session.rel)
	if err != nil {
		m.mu.Lock()
		m.sessions[writeID] = session
		m.mu.Unlock()
		return chunkWriteCommitResult{}, fmt.Errorf("write_commit: %w", err)
	}
	if resolved.Identity != session.identity || !m.paths.sameAbsolute(resolved.Absolute, session.abs) {
		m.mu.Lock()
		m.sessions[writeID] = session
		m.mu.Unlock()
		return chunkWriteCommitResult{}, fmt.Errorf("write_commit: unsafe path %q: path identity changed before commit", session.rel)
	}
	if err := m.guard.CheckWrite(resolved.Absolute); err != nil {
		m.mu.Lock()
		m.sessions[writeID] = session
		m.mu.Unlock()
		return chunkWriteCommitResult{}, fmt.Errorf("write_commit: %w", err)
	}
	if err := commitChunkWriteFile(resolved.Absolute, content, session.fileMode); err != nil {
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
