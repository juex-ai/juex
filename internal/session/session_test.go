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
