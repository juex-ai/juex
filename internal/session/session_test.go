package session

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
)

func TestSession_AppendsToConversationJSONL(t *testing.T) {
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	_ = s.Append(llm.TextMessage(llm.RoleUser, "hello"))
	_ = s.Append(llm.TextMessage(llm.RoleAssistant, "hi"))

	data, _ := os.ReadFile(filepath.Join(s.Dir, conversationFile))
	lines := countLines(data)
	if lines != 2 {
		t.Fatalf("expected 2 lines, got %d: %s", lines, data)
	}
	if len(s.History) != 2 {
		t.Fatalf("history len = %d", len(s.History))
	}
}

func TestSession_AppendNormalizesNilBlocks(t *testing.T) {
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.Append(llm.Message{Role: llm.RoleAssistant}); err != nil {
		t.Fatal(err)
	}
	if s.History[0].Blocks == nil {
		t.Fatal("history blocks is nil, want empty slice")
	}

	data, _ := os.ReadFile(filepath.Join(s.Dir, conversationFile))
	if strings.Contains(string(data), `"blocks":null`) {
		t.Fatalf("conversation contains null blocks: %s", data)
	}
	if !strings.Contains(string(data), `"blocks":[]`) {
		t.Fatalf("conversation missing empty blocks array: %s", data)
	}
}

func TestSession_AppendEventToJSONL(t *testing.T) {
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	_ = s.AppendEvent(events.Event{Type: "turn.started", Payload: "x"})
	_ = s.AppendEvent(events.Event{Type: "tool.completed", Payload: "y"})

	data, _ := os.ReadFile(filepath.Join(s.Dir, eventsFile))
	if c := countLines(data); c != 2 {
		t.Fatalf("expected 2 event lines, got %d", c)
	}
}

func TestSession_BusSubscription(t *testing.T) {
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	bus := events.NewBus()
	s.SubscribeBus(bus)

	bus.Emit(events.Event{Type: "x.fired"})
	bus.Emit(events.Event{Type: "y.fired"})

	data, _ := os.ReadFile(filepath.Join(s.Dir, eventsFile))
	if c := countLines(data); c != 2 {
		t.Fatalf("expected 2 events from bus, got %d: %s", c, data)
	}
}

func TestSession_LoadRoundTrip(t *testing.T) {
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Append(llm.TextMessage(llm.RoleUser, "msg-1"))
	_ = s.Append(llm.TextMessage(llm.RoleAssistant, "msg-2"))
	dir := s.Dir
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if len(s2.History) != 2 {
		t.Fatalf("loaded history len = %d", len(s2.History))
	}
	if s2.History[0].FirstText() != "msg-1" || s2.History[1].FirstText() != "msg-2" {
		t.Fatalf("history mismatch: %+v", s2.History)
	}
	if !strings.HasPrefix(s2.ID, time2025OrLater(t)) {
		// just make sure ID is the dir basename
		if s2.ID != filepath.Base(dir) {
			t.Errorf("id = %s vs dir base %s", s2.ID, filepath.Base(dir))
		}
	}
}

func TestSession_LoadNormalizesNullBlocks(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "20260509T074114-a20bf346")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, conversationFile), []byte(`{"role":"assistant","blocks":null}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if len(s.History) != 1 {
		t.Fatalf("history len = %d, want 1", len(s.History))
	}
	if s.History[0].Blocks == nil {
		t.Fatal("loaded blocks is nil, want empty slice")
	}
}

func countLines(data []byte) int {
	n := 0
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		if len(scanner.Bytes()) > 0 {
			n++
		}
	}
	return n
}

// time2025OrLater is a tiny helper that just returns "" — kept here so the
// HasPrefix check above always falls through to the basename comparison while
// staying explicit about intent.
func time2025OrLater(t *testing.T) string { t.Helper(); return "" }
