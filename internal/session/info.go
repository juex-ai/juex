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

// idTimeLayout is the cosmetic timestamp prefix encoded into every session id.
// It keeps directory names readable and sortable but is not session metadata.
const idTimeLayout = "20060102T150405"

const previewMaxRunes = 80

// Session lists need only recent cumulative usage. Bounding the event tail
// keeps list request work independent of the full event journal size.
const maxSessionUsageScanBytes = int64(maxEventLineBytes)

type transcriptFingerprint struct {
	Size     int64  `json:"size"`
	MtimeNS  int64  `json:"mtime_ns"`
	ChangeID string `json:"change_id,omitempty"`
}

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

	transcript transcriptFingerprint
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
// fingerprint still matches. Current metadata and event usage are always
// read from their canonical session files.
func ListWithHistory(root, historyPath string) ([]Info, error) {
	return listWithHistoryLoader(root, historyPath, loadInfoSummary)
}

func listWithHistoryLoader(root, historyPath string, loadSummary summaryLoader) ([]Info, error) {
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
	infos, refreshed, err := listWithSummaryLoader(root, recorded, loadSummary)
	if err != nil {
		return nil, err
	}
	if err := repairHistorySummaries(root, historyPath, refreshed); err != nil {
		return nil, err
	}
	return infos, nil
}

func list(root string, recorded map[string]Info) ([]Info, error) {
	infos, _, err := listWithSummaryLoader(root, recorded, loadInfoSummary)
	return infos, err
}

type summaryLoader func(string) (Info, transcriptIndex, error)

func listWithSummaryLoader(root string, recorded map[string]Info, loadSummary summaryLoader) ([]Info, []Info, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	var out []Info
	var refreshed []Info
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		info, scanned, err := cachedOrScannedInfo(dir, e.Name(), recorded, loadSummary)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, ErrSessionTimeUnavailable) {
				continue
			}
			return nil, nil, err
		}
		out = append(out, info)
		if scanned {
			refreshed = append(refreshed, info)
		}
	}
	sortInfos(out)
	return out, refreshed, nil
}

func cachedOrScannedInfo(
	dir, id string,
	recorded map[string]Info,
	loadSummary summaryLoader,
) (Info, bool, error) {
	convPath := filepath.Join(dir, conversationFile)
	fingerprint, err := fingerprintFromPath(convPath)
	if err != nil {
		return Info{}, false, err
	}
	cached, ok := recorded[id]
	if !ok || !fingerprint.strong() || cached.transcript != fingerprint {
		info, _, err := loadSummary(dir)
		if err != nil {
			return Info{}, false, err
		}
		return info, true, nil
	}
	meta, err := loadMetadata(dir)
	if err != nil {
		return Info{}, false, err
	}
	info := Info{
		ID:           id,
		Alias:        meta.Alias,
		Dir:          dir,
		Kind:         meta.Kind,
		StartedAt:    time.UnixMilli(meta.StartedAtMS).UTC(),
		LastActiveAt: time.UnixMilli(meta.LastActiveAtMS).UTC(),
		Turns:        cached.Turns,
		Preview:      cached.Preview,
		transcript:   fingerprint,
	}
	info.TokenUsage, info.ContextUsage, _ = loadLatestSessionUsageWithin(dir, maxSessionUsageScanBytes)
	return info, false, nil
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
	info, _, err := loadInfoSummary(dir)
	if err != nil {
		return Info{}, nil, err
	}
	idx, err := scanTranscriptIndex(filepath.Join(dir, conversationFile))
	if err != nil {
		return Info{}, nil, err
	}
	info.Turns = idx.turns
	info.Preview = idx.preview
	msgs, err := readTranscriptMessagesForFingerprint(
		filepath.Join(dir, conversationFile), idx.entries, idx.fingerprint,
	)
	if err != nil {
		return Info{}, nil, err
	}
	return info, msgs, nil
}

func loadInfoSummary(dir string) (Info, transcriptIndex, error) {
	convPath := filepath.Join(dir, conversationFile)
	fingerprint, err := fingerprintFromPath(convPath)
	if err != nil {
		return Info{}, transcriptIndex{}, err
	}
	id := filepath.Base(dir)
	meta, err := loadMetadata(dir)
	if err != nil {
		return Info{}, transcriptIndex{}, err
	}
	info := Info{
		ID:           id,
		Alias:        meta.Alias,
		Dir:          dir,
		Kind:         meta.Kind,
		LastActiveAt: time.UnixMilli(meta.LastActiveAtMS).UTC(),
		StartedAt:    time.UnixMilli(meta.StartedAtMS).UTC(),
		transcript:   fingerprint,
	}
	var idx transcriptIndex
	if transcriptCheckpointValid(meta.Transcript, info.transcript) {
		idx.turns = meta.Transcript.Turns
		idx.preview = meta.Transcript.Preview
		idx.fingerprint = info.transcript
	} else {
		idx, err = scanTranscriptIndex(convPath)
		if err != nil {
			return Info{}, transcriptIndex{}, err
		}
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

func fingerprintFromFileInfo(info os.FileInfo) transcriptFingerprint {
	return transcriptFingerprint{
		Size:    info.Size(),
		MtimeNS: info.ModTime().UnixNano(),
	}
}

func fingerprintFromPath(path string) (transcriptFingerprint, error) {
	file, err := os.Open(path)
	if err != nil {
		return transcriptFingerprint{}, err
	}
	defer file.Close()
	return fingerprintFromOpenFile(file)
}

func fingerprintFromOpenFile(file *os.File) (transcriptFingerprint, error) {
	info, err := file.Stat()
	if err != nil {
		return transcriptFingerprint{}, err
	}
	fingerprint := fingerprintFromFileInfo(info)
	fingerprint.ChangeID = transcriptFileChangeID(file, info)
	return fingerprint, nil
}

func (f transcriptFingerprint) strong() bool {
	return f.ChangeID != ""
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
