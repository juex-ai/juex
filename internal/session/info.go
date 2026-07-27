package session

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/juex-ai/juex/internal/llm"
)

// idTimeLayout is the timestamp prefix encoded into every session id.
// See newID() in session.go.
const idTimeLayout = "20060102T150405"

const previewMaxRunes = 80

// Session lists only need recent cumulative usage. Bounding the event tail
// prevents legacy journals without context_usage from turning list requests
// into full-file scans.
const maxSessionUsageScanBytes = int64(maxEventLineBytes)

// Info is a lightweight, read-only summary of a session on disk. It is
// produced by List and LoadInfo and is safe to expose through the CLI
// (no live file handles, no event subscription).
type Info struct {
	ID           string            `json:"id"`
	Alias        string            `json:"alias,omitempty"`
	Dir          string            `json:"dir"`
	Kind         string            `json:"kind"`
	Active       bool              `json:"active"`
	StartedAt    time.Time         `json:"started_at"`
	LastActiveAt time.Time         `json:"last_active_at"`
	Turns        int               `json:"turns"`
	Preview      string            `json:"preview"`
	TokenUsage   llm.Usage         `json:"token_usage"`
	ContextUsage *llm.ContextUsage `json:"context_usage,omitempty"`
}

// InfoDir returns the canonical on-disk directory for info under sessionsRoot.
// Recorded Info values without an ID keep their stored Dir.
func InfoDir(sessionsRoot string, info Info) string {
	if info.ID != "" {
		return filepath.Join(sessionsRoot, info.ID)
	}
	return info.Dir
}

// HasConversation reports whether dir contains a persisted conversation file.
func HasConversation(dir string) bool {
	if dir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(dir, conversationFile))
	return err == nil
}

// List enumerates every well-formed session directory under root and
// returns one Info per session, sorted by LastActiveAt descending then
// StartedAt descending. A missing root is treated as "no sessions" and
// returns nil + nil error so callers can render an empty list cleanly.
func List(root string) ([]Info, error) {
	return list(root, nil)
}

// ListWithHistory enumerates canonical session directories like List while
// reusing transcript summaries recorded in historyPath when their transcript
// modification time still matches. Current metadata and event usage are always
// read from their canonical session files.
func ListWithHistory(root, historyPath string) ([]Info, error) {
	history, err := LoadHistory(historyPath)
	if err != nil {
		return nil, err
	}
	recorded := make(map[string]Info, len(history.Sessions))
	for _, info := range history.Sessions {
		if info.ID != "" {
			recorded[info.ID] = info
		}
	}
	return list(root, recorded)
}

func list(root string, recorded map[string]Info) ([]Info, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []Info
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		info, err := cachedOrScannedInfo(dir, e.Name(), recorded)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		out = append(out, info)
	}
	sortInfos(out)
	return out, nil
}

func cachedOrScannedInfo(dir, id string, recorded map[string]Info) (Info, error) {
	convPath := filepath.Join(dir, conversationFile)
	st, err := os.Stat(convPath)
	if err != nil {
		return Info{}, err
	}
	cached, ok := recorded[id]
	if !ok || !cached.LastActiveAt.Equal(st.ModTime()) {
		info, _, err := loadInfoSummary(dir)
		return info, err
	}
	meta, err := loadMetadata(dir)
	if err != nil {
		return Info{}, err
	}
	info := Info{
		ID:           id,
		Alias:        meta.Alias,
		Dir:          dir,
		Kind:         meta.Kind,
		StartedAt:    parseStartedAt(id, st.ModTime()),
		LastActiveAt: st.ModTime(),
		Turns:        cached.Turns,
		Preview:      cached.Preview,
	}
	info.TokenUsage, info.ContextUsage, _ = loadLatestSessionUsageWithin(dir, maxSessionUsageScanBytes)
	return info, nil
}

func sortInfos(infos []Info) {
	sort.SliceStable(infos, func(i, j int) bool {
		if !infos[i].LastActiveAt.Equal(infos[j].LastActiveAt) {
			return infos[i].LastActiveAt.After(infos[j].LastActiveAt)
		}
		return infos[i].StartedAt.After(infos[j].StartedAt)
	})
}

// LoadInfo returns both the Info summary and the full message slice for
// dir. Used by `juex sessions show <id>`.
func LoadInfo(dir string) (Info, []llm.Message, error) {
	return loadInfo(dir)
}

// loadInfo is the workhorse for List and LoadInfo.
func loadInfo(dir string) (Info, []llm.Message, error) {
	info, idx, err := loadInfoSummary(dir)
	if err != nil {
		return Info{}, nil, err
	}
	msgs, err := readTranscriptMessages(filepath.Join(dir, conversationFile), idx.entries)
	if err != nil {
		return Info{}, nil, err
	}
	return info, msgs, nil
}

func loadInfoSummary(dir string) (Info, transcriptIndex, error) {
	convPath := filepath.Join(dir, conversationFile)
	st, err := os.Stat(convPath)
	if err != nil {
		return Info{}, transcriptIndex{}, err
	}
	id := filepath.Base(dir)
	alias, err := LoadAlias(dir)
	if err != nil {
		return Info{}, transcriptIndex{}, err
	}
	kind, err := LoadKind(dir)
	if err != nil {
		return Info{}, transcriptIndex{}, err
	}
	info := Info{
		ID:           id,
		Alias:        alias,
		Dir:          dir,
		Kind:         kind,
		LastActiveAt: st.ModTime(),
		StartedAt:    parseStartedAt(id, st.ModTime()),
	}
	idx, err := scanTranscriptIndex(convPath)
	if err != nil {
		return Info{}, transcriptIndex{}, err
	}
	info.Turns = idx.turns
	info.Preview = idx.preview
	info.TokenUsage, info.ContextUsage, _ = loadLatestSessionUsage(dir)
	return info, idx, nil
}

func loadLatestSessionUsage(dir string) (llm.Usage, *llm.ContextUsage, error) {
	return loadLatestSessionUsageWithin(dir, 0)
}

func loadLatestSessionUsageWithin(dir string, maxBytes int64) (llm.Usage, *llm.ContextUsage, error) {
	file, err := os.Open(filepath.Join(dir, eventsFile))
	if err != nil {
		return llm.Usage{}, nil, err
	}
	defer file.Close()
	reader, err := newBoundedReverseLineReader(file, maxBytes)
	if err != nil {
		return llm.Usage{}, nil, err
	}
	var tokenUsage llm.Usage
	var contextUsage *llm.ContextUsage
	tokenFound := false
	contextFound := false
	for !tokenFound || !contextFound {
		line, err := reader.next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return llm.Usage{}, nil, err
		}
		var envelope struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(line, &envelope); err != nil || envelope.Type != "llm.responded" {
			continue
		}
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
			continue
		}
		if !tokenFound {
			if raw, ok := payload["token_usage"]; ok {
				var usage llm.Usage
				if err := json.Unmarshal(raw, &usage); err == nil {
					tokenUsage = usage
					tokenFound = true
				}
			}
		}
		if !contextFound {
			if raw, ok := payload["context_usage"]; ok {
				var usage llm.ContextUsage
				if err := json.Unmarshal(raw, &usage); err == nil {
					contextUsage = &usage
					contextFound = true
				}
			}
		}
	}
	return tokenUsage, contextUsage, nil
}

// parseStartedAt extracts the timestamp prefix from a session id
// (YYYYMMDDTHHMMSS-...). Falls back to fallback if the id is malformed.
func parseStartedAt(id string, fallback time.Time) time.Time {
	if len(id) < len(idTimeLayout) {
		return fallback
	}
	t, err := time.ParseInLocation(idTimeLayout, id[:len(idTimeLayout)], time.UTC)
	if err != nil {
		return fallback
	}
	return t
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
