// Package session owns the in-memory conversation history for a single
// runtime session and persists every message + emitted event to jsonl files.
//
// File layout under <root>/<session_id>/:
//
//	conversation.jsonl   one llm.Message per line
//	events.jsonl         one events.Event per line
//
// v0.1 only persists; reload (Load) is provided for future use but is not
// wired into the CLI.
package session

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
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
	ID      string
	Dir     string
	History []llm.Message

	mu      sync.Mutex
	convFD  *os.File
	eventFD *os.File
}

// New creates a new session under rootDir. rootDir is created if missing.
func New(rootDir string) (*Session, error) {
	id := newID()
	dir := filepath.Join(rootDir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	convFD, err := os.OpenFile(filepath.Join(dir, conversationFile), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	eventFD, err := os.OpenFile(filepath.Join(dir, eventsFile), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		_ = convFD.Close()
		return nil, err
	}
	return &Session{
		ID:      id,
		Dir:     dir,
		convFD:  convFD,
		eventFD: eventFD,
	}, nil
}

// Append adds m to the in-memory history and writes it to conversation.jsonl.
func (s *Session) Append(m llm.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.History = append(s.History, m)
	return writeJSONL(s.convFD, m)
}

// AppendEvent persists e to events.jsonl. Unlike Append, the event itself
// is not retained in memory.
func (s *Session) AppendEvent(e events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	id := filepath.Base(dir)
	convPath := filepath.Join(dir, conversationFile)
	data, err := os.ReadFile(convPath)
	if err != nil {
		return nil, err
	}
	var history []llm.Message
	for _, line := range splitLines(data) {
		if len(line) == 0 {
			continue
		}
		var m llm.Message
		if err := json.Unmarshal(line, &m); err != nil {
			return nil, fmt.Errorf("session: parse %s: %w", convPath, err)
		}
		history = append(history, m)
	}
	convFD, err := os.OpenFile(convPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	eventFD, err := os.OpenFile(filepath.Join(dir, eventsFile), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		_ = convFD.Close()
		return nil, err
	}
	return &Session{
		ID:      id,
		Dir:     dir,
		History: history,
		convFD:  convFD,
		eventFD: eventFD,
	}, nil
}

// SubscribeBus wires every event emitted on bus through to AppendEvent so the
// runtime doesn't have to remember to do it manually.
func (s *Session) SubscribeBus(bus *events.Bus) {
	bus.Subscribe("*", func(e events.Event) {
		_ = s.AppendEvent(e)
	})
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

func newID() string {
	var b [4]byte
	rand.New(rand.NewSource(time.Now().UnixNano())).Read(b[:])
	return time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b[:])
}
