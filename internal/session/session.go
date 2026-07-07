// Package session owns the in-memory conversation history for a single
// runtime session and persists every message + emitted event to jsonl files.
//
// File layout under <root>/<session_id>/:
//
//	conversation.jsonl   one llm.Message per line
//	events.jsonl         one events.Event per line
//
// The CLI and web server use Load to resume existing sessions.
package session

import (
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
)

const (
	conversationFile = "conversation.jsonl"
	eventsFile       = "events.jsonl"
)

type Session struct {
	ID           string
	Dir          string
	Alias        string
	Kind         string
	Active       bool
	History      []llm.Message
	TokenUsage   llm.Usage
	ContextUsage *llm.ContextUsage

	mu           sync.Mutex
	convFD       *os.File
	eventFD      *os.File
	historyPath  string
	recordActive bool
}

type Options struct {
	Alias            string
	Kind             string
	Active           bool
	RecordActive     bool
	NoRecordActive   bool
	HistoryPath      string
	Lazy             bool
	RepairTranscript bool
}

// New creates a new session under rootDir. rootDir is created if missing.
func New(rootDir string) (*Session, error) {
	return NewWithOptions(rootDir, Options{})
}

func NewWithOptions(rootDir string, opts Options) (*Session, error) {
	id := newID()
	dir := filepath.Join(rootDir, id)
	kind := NormalizeKind(opts.Kind)
	recordActive := shouldRecordActive(opts, kind)
	if opts.Lazy {
		return &Session{
			ID:           id,
			Dir:          dir,
			Alias:        opts.Alias,
			Kind:         kind,
			Active:       opts.Active,
			historyPath:  opts.HistoryPath,
			recordActive: recordActive,
		}, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if err := saveMetadata(dir, metadata{Alias: opts.Alias, Kind: kind}); err != nil {
		return nil, err
	}
	convFD, err := os.OpenFile(filepath.Join(dir, conversationFile), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	eventFD, err := os.OpenFile(filepath.Join(dir, eventsFile), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		convFD.Close()
		return nil, err
	}
	return &Session{
		ID:           id,
		Dir:          dir,
		Alias:        opts.Alias,
		Kind:         kind,
		Active:       opts.Active,
		convFD:       convFD,
		eventFD:      eventFD,
		historyPath:  opts.HistoryPath,
		recordActive: recordActive,
	}, nil
}

// Append adds m to the in-memory history and writes it to conversation.jsonl.
func (s *Session) Append(m llm.Message) error {
	s.mu.Lock()
	m = prepareNewMessage(m)
	if err := s.ensureFilesLocked(); err != nil {
		s.mu.Unlock()
		return err
	}
	s.History = append(s.History, m)
	if err := writeJSONL(s.convFD, m); err != nil {
		s.mu.Unlock()
		return err
	}
	info, ok := s.historyInfoLocked()
	historyPath := s.historyPath
	s.mu.Unlock()
	if !ok {
		return nil
	}
	if s.recordActive && info.Kind == KindPrimary {
		return SetActive(historyPath, info)
	}
	return RecordSession(historyPath, info)
}

// AppendEvent persists e to events.jsonl. Unlike Append, the event itself
// is not retained in memory.
func (s *Session) AppendEvent(e events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureFilesLocked(); err != nil {
		return err
	}
	e = events.Normalize(e)
	return writeJSONL(s.eventFD, e)
}

func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var firstErr error
	if s.convFD != nil {
		if err := s.convFD.Close(); err != nil {
			firstErr = err
		}
		s.convFD = nil
	}
	if s.eventFD != nil {
		if err := s.eventFD.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		s.eventFD = nil
	}
	return firstErr
}

// Load reads conversation.jsonl from dir and returns the assembled session.
// The new session shares the same id (= directory basename) and appends to
// the existing files.
func Load(dir string) (*Session, error) {
	return LoadWithOptions(dir, Options{})
}

func LoadWithOptions(dir string, opts Options) (*Session, error) {
	id := filepath.Base(dir)
	meta, err := loadMetadata(dir)
	if err != nil {
		return nil, err
	}
	alias := meta.Alias
	kind := meta.Kind
	if opts.Alias != "" {
		alias = opts.Alias
	}
	if opts.Kind != "" {
		kind = NormalizeKind(opts.Kind)
	}
	recordActive := shouldRecordActive(opts, kind)
	if opts.Alias != "" || opts.Kind != "" {
		if err := saveMetadata(dir, metadata{Alias: alias, Kind: kind}); err != nil {
			return nil, err
		}
	}
	convPath := filepath.Join(dir, conversationFile)
	data, err := os.ReadFile(convPath)
	if err != nil {
		return nil, err
	}
	var history []llm.Message
	for i, line := range splitLines(data) {
		if len(line) == 0 {
			continue
		}
		var m llm.Message
		if err := json.Unmarshal(line, &m); err != nil {
			return nil, fmt.Errorf("session: parse %s: %w", convPath, err)
		}
		m = normalizeLoadedMessage(m, i)
		history = append(history, m)
	}
	var repairs []TranscriptRepair
	if opts.RepairTranscript {
		history, repairs = repairTranscriptMessages(history, "load")
		if len(repairs) > 0 {
			if err := writeConversationMessages(convPath, history); err != nil {
				return nil, err
			}
		}
	}
	convFD, err := os.OpenFile(convPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	eventFD, err := os.OpenFile(filepath.Join(dir, eventsFile), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		convFD.Close()
		return nil, err
	}
	if len(repairs) > 0 {
		_ = writeJSONL(eventFD, events.Normalize(events.Event{
			Type:    "transcript.repaired",
			Payload: TranscriptRepairedPayload{Reason: "load", Repairs: repairs},
		}))
	}
	tokenUsage, contextUsage, _ := loadLatestSessionUsage(dir)
	return &Session{
		ID:           id,
		Dir:          dir,
		Alias:        alias,
		Kind:         kind,
		Active:       opts.Active,
		History:      history,
		TokenUsage:   tokenUsage,
		ContextUsage: contextUsage,
		convFD:       convFD,
		eventFD:      eventFD,
		historyPath:  opts.HistoryPath,
		recordActive: recordActive,
	}, nil
}

func shouldRecordActive(opts Options, kind string) bool {
	if opts.RecordActive {
		return true
	}
	if opts.NoRecordActive {
		return false
	}
	return opts.HistoryPath != "" && NormalizeKind(kind) == KindPrimary
}

// SubscribeBus wires every event emitted on bus through to AppendEvent so the
// runtime doesn't have to remember to do it manually. The returned function
// removes the subscription.
func (s *Session) SubscribeBus(bus *events.Bus) func() {
	if bus == nil {
		return func() {}
	}
	return bus.Subscribe("*", func(e events.Event) {
		_ = s.AppendEvent(e)
	})
}

// Info returns a summary of the in-memory session. For lazy sessions that have
// not yet been persisted, now is used as the LastActiveAt fallback.
func (s *Session) Info(now time.Time) Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.infoLocked(now)
}

func (s *Session) RecordResponseUsage(usage llm.Usage, contextUsage *llm.ContextUsage) llm.Usage {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !usage.IsZero() {
		s.TokenUsage.Add(usage)
	}
	if contextUsage != nil {
		s.ContextUsage = contextUsage
	}
	return s.TokenUsage
}

func (s *Session) TokenUsageSnapshot() llm.Usage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.TokenUsage
}

// Snapshot returns the current summary and a copy of the in-memory history.
func (s *Session) Snapshot(now time.Time) (Info, []llm.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	msgs := append([]llm.Message(nil), s.History...)
	return s.infoLocked(now), msgs
}

func (s *Session) ensureFilesLocked() error {
	if s.convFD != nil && s.eventFD != nil {
		return nil
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	if err := saveMetadata(s.Dir, metadata{Alias: s.Alias, Kind: s.Kind}); err != nil {
		return err
	}
	if s.convFD == nil {
		convFD, err := os.OpenFile(filepath.Join(s.Dir, conversationFile), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		s.convFD = convFD
	}
	if s.eventFD == nil {
		eventFD, err := os.OpenFile(filepath.Join(s.Dir, eventsFile), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		s.eventFD = eventFD
	}
	return nil
}

func writeJSONL(w *os.File, v any) error {
	if w == nil {
		return fmt.Errorf("session: file closed")
	}
	buf, err := json.Marshal(v)
	if err != nil {
		return err
	}
	buf = append(buf, '\n')
	_, err = w.Write(buf)
	return err
}

func prepareNewMessage(m llm.Message) llm.Message {
	if m.ID == "" {
		m.ID = newMessageID()
	}
	if m.Blocks == nil {
		m.Blocks = []llm.Block{}
	}
	return m
}

func normalizeLoadedMessage(m llm.Message, index int) llm.Message {
	if m.ID == "" {
		m.ID = fmt.Sprintf("legacy-%06d", index+1)
	}
	if m.Blocks == nil {
		m.Blocks = []llm.Block{}
	}
	return m
}

func splitLines(data []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			out = append(out, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		out = append(out, data[start:])
	}
	return out
}

func (s *Session) historyInfoLocked() (Info, bool) {
	if s.historyPath == "" {
		return Info{}, false
	}
	return s.infoLocked(time.Now().UTC()), true
}

func (s *Session) infoLocked(now time.Time) Info {
	info := Info{
		ID:        s.ID,
		Alias:     s.Alias,
		Dir:       s.Dir,
		Kind:      s.Kind,
		Active:    s.Active,
		StartedAt: parseStartedAt(s.ID, now),
	}
	if st, err := os.Stat(filepath.Join(s.Dir, conversationFile)); err == nil {
		info.LastActiveAt = st.ModTime()
	} else {
		info.LastActiveAt = now
	}
	for _, m := range s.History {
		if m.Role == llm.RoleUser && m.Kind != llm.MessageKindCompact {
			info.Turns++
			if info.Preview == "" {
				info.Preview = truncateRunes(strings.TrimSpace(m.FirstText()), previewMaxRunes)
			}
		}
	}
	info.TokenUsage = s.TokenUsage
	info.ContextUsage = s.ContextUsage
	return info
}

func newID() string {
	var b [4]byte
	if _, err := cryptorand.Read(b[:]); err != nil {
		panic(fmt.Errorf("session: random id bytes: %w", err))
	}
	return time.Now().Local().Format(idTimeLayout) + "-" + hex.EncodeToString(b[:])
}

func newMessageID() string {
	return "msg-" + newID()
}
