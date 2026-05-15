package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
)

// idTimeLayout is the timestamp prefix encoded into every session id.
// See newID() in session.go.
const idTimeLayout = "20060102T150405"

const previewMaxRunes = 80

// Info is a lightweight, read-only summary of a session on disk. It is
// produced by List and LoadInfo and is safe to expose through the CLI
// (no live file handles, no event subscription).
type Info struct {
	ID           string            `json:"id"`
	Alias        string            `json:"alias,omitempty"`
	Dir          string            `json:"dir"`
	StartedAt    time.Time         `json:"started_at"`
	LastActiveAt time.Time         `json:"last_active_at"`
	Turns        int               `json:"turns"`
	Preview      string            `json:"preview"`
	TokenUsage   llm.Usage         `json:"token_usage"`
	ContextUsage *llm.ContextUsage `json:"context_usage,omitempty"`
}

// List enumerates every well-formed session directory under root and
// returns one Info per session, sorted by LastActiveAt descending then
// StartedAt descending. A missing root is treated as "no sessions" and
// returns nil + nil error so callers can render an empty list cleanly.
func List(root string) ([]Info, error) {
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
		info, _, err := loadInfo(dir)
		if err != nil {
			continue // skip unreadable sessions
		}
		out = append(out, info)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].LastActiveAt.Equal(out[j].LastActiveAt) {
			return out[i].LastActiveAt.After(out[j].LastActiveAt)
		}
		return out[i].StartedAt.After(out[j].StartedAt)
	})
	return out, nil
}

// LoadInfo returns both the Info summary and the full message slice for
// dir. Used by `juex sessions show <id>`.
func LoadInfo(dir string) (Info, []llm.Message, error) {
	return loadInfo(dir)
}

// loadInfo is the workhorse for List and LoadInfo. Returns an error for
// any caller that cannot proceed; List filters those errors out itself.
func loadInfo(dir string) (Info, []llm.Message, error) {
	convPath := filepath.Join(dir, conversationFile)
	st, err := os.Stat(convPath)
	if err != nil {
		return Info{}, nil, err
	}
	id := filepath.Base(dir)
	alias, err := LoadAlias(dir)
	if err != nil {
		return Info{}, nil, err
	}
	info := Info{
		ID:           id,
		Alias:        alias,
		Dir:          dir,
		LastActiveAt: st.ModTime(),
		StartedAt:    parseStartedAt(id, st.ModTime()),
	}
	data, err := os.ReadFile(convPath)
	if err != nil {
		return Info{}, nil, err
	}
	var msgs []llm.Message
	for _, line := range splitLines(data) {
		if len(line) == 0 {
			continue
		}
		var m llm.Message
		if err := json.Unmarshal(line, &m); err != nil {
			return Info{}, nil, err
		}
		m = normalizeMessage(m)
		msgs = append(msgs, m)
		if m.Role == llm.RoleUser && m.Kind != llm.MessageKindCompact {
			info.Turns++
			if info.Preview == "" {
				info.Preview = truncateRunes(strings.TrimSpace(m.FirstText()), previewMaxRunes)
			}
		}
	}
	info.TokenUsage, info.ContextUsage, _ = loadLatestSessionUsage(dir)
	return info, msgs, nil
}

func loadLatestSessionUsage(dir string) (llm.Usage, *llm.ContextUsage, error) {
	data, err := os.ReadFile(filepath.Join(dir, eventsFile))
	if err != nil {
		return llm.Usage{}, nil, err
	}
	var tokenUsage llm.Usage
	var latest *llm.ContextUsage
	for _, line := range splitLines(data) {
		if len(line) == 0 {
			continue
		}
		var e events.Event
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		if e.Type != "llm.responded" {
			continue
		}
		if usage, ok := tokenUsageFromPayload(e.Payload); ok {
			tokenUsage = usage
		}
		if usage, ok := contextUsageFromPayload(e.Payload); ok {
			latest = &usage
		}
	}
	return tokenUsage, latest, nil
}

func tokenUsageFromPayload(payload any) (llm.Usage, bool) {
	p, ok := payload.(map[string]any)
	if !ok {
		return llm.Usage{}, false
	}
	raw, ok := p["token_usage"]
	if !ok {
		return llm.Usage{}, false
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return llm.Usage{}, false
	}
	var usage llm.Usage
	if err := json.Unmarshal(data, &usage); err != nil {
		return llm.Usage{}, false
	}
	return usage, true
}

func contextUsageFromPayload(payload any) (llm.ContextUsage, bool) {
	p, ok := payload.(map[string]any)
	if !ok {
		return llm.ContextUsage{}, false
	}
	raw, ok := p["context_usage"]
	if !ok {
		return llm.ContextUsage{}, false
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return llm.ContextUsage{}, false
	}
	var usage llm.ContextUsage
	if err := json.Unmarshal(data, &usage); err != nil {
		return llm.ContextUsage{}, false
	}
	return usage, true
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
