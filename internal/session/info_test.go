package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
)

// makeSession creates a session dir under root with the given id and
// pre-populates conversation.jsonl with one message per element of msgs.
// mtime sets the file's modification time so list ordering tests are stable.
func makeSession(t *testing.T, root, id string, msgs []llm.Message, mtime time.Time) string {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	convPath := filepath.Join(dir, "conversation.jsonl")
	f, err := os.Create(convPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		buf, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(buf); err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte{'\n'}); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if !mtime.IsZero() {
		if err := os.Chtimes(convPath, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func writeEvents(t *testing.T, dir string, evs []events.Event) {
	t.Helper()
	f, err := os.Create(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		buf, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(append(buf, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestInfoDirPrefersID(t *testing.T) {
	got := InfoDir("/sessions", Info{ID: "abc", Dir: "/legacy"})
	if got != filepath.Join("/sessions", "abc") {
		t.Fatalf("InfoDir = %q, want canonical ID path", got)
	}
}

func TestInfoDirFallsBackToDir(t *testing.T) {
	got := InfoDir("/sessions", Info{Dir: "/legacy"})
	if got != "/legacy" {
		t.Fatalf("InfoDir = %q, want recorded dir", got)
	}
}

func TestHasConversation(t *testing.T) {
	if HasConversation("") {
		t.Fatal("HasConversation(\"\") = true, want false")
	}
	dir := t.TempDir()
	if HasConversation(dir) {
		t.Fatal("HasConversation(empty dir) = true, want false")
	}
	if err := os.WriteFile(filepath.Join(dir, conversationFile), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !HasConversation(dir) {
		t.Fatal("HasConversation(dir) = false, want true")
	}
}

func TestLoadInfo_DefaultsKindToPrimary(t *testing.T) {
	root := t.TempDir()
	dir := makeSession(t, root, "20260506T103500-primary1",
		[]llm.Message{llm.TextMessage(llm.RoleUser, "primary")},
		time.Date(2026, 5, 6, 10, 35, 0, 0, time.UTC))

	info, _, err := LoadInfo(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Kind != KindPrimary {
		t.Fatalf("kind = %q, want primary", info.Kind)
	}
}

func TestSetKindAndLoadInfo(t *testing.T) {
	root := t.TempDir()
	dir := makeSession(t, root, "20260506T103500-side0001",
		[]llm.Message{llm.TextMessage(llm.RoleUser, "side")},
		time.Date(2026, 5, 6, 10, 35, 0, 0, time.UTC))

	if err := SetKind(dir, KindSide); err != nil {
		t.Fatal(err)
	}
	info, _, err := LoadInfo(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Kind != KindSide {
		t.Fatalf("kind = %q, want side", info.Kind)
	}
}

func TestList_SortsByLastActiveDesc(t *testing.T) {
	root := t.TempDir()
	older := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC)
	makeSession(t, root, "20260501T100000-aaaa1111",
		[]llm.Message{llm.TextMessage(llm.RoleUser, "older")}, older)
	makeSession(t, root, "20260502T100000-bbbb2222",
		[]llm.Message{llm.TextMessage(llm.RoleUser, "newer")}, newer)

	got, err := List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].ID != "20260502T100000-bbbb2222" {
		t.Errorf("got[0].ID = %s, want newer first", got[0].ID)
	}
	if got[1].ID != "20260501T100000-aaaa1111" {
		t.Errorf("got[1].ID = %s, want older second", got[1].ID)
	}
}

func TestList_ExtractsTurnsAndPreview(t *testing.T) {
	root := t.TempDir()
	dir := makeSession(t, root, "20260506T103500-abcd1234",
		[]llm.Message{
			llm.TextMessage(llm.RoleUser, "summarise README.md"),
			llm.TextMessage(llm.RoleAssistant, "the readme says hello world"),
			compactTestMessage("old context summary"),
			llm.TextMessage(llm.RoleUser, "follow up"),
			llm.TextMessage(llm.RoleAssistant, "done"),
		}, time.Date(2026, 5, 6, 10, 35, 0, 0, time.UTC))
	writeEvents(t, dir, []events.Event{{
		Type: "llm.responded",
		Payload: map[string]any{
			"token_usage":   llm.Usage{InputTokens: 17, OutputTokens: 7},
			"context_usage": llm.ContextUsage{Model: "mock", InputTokens: 7, OutputTokens: 3, TotalTokens: 10},
		},
	}})

	got, err := List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d", len(got))
	}
	if got[0].Turns != 2 {
		t.Errorf("turns = %d, want 2 (user messages)", got[0].Turns)
	}
	if got[0].Preview != "summarise README.md" {
		t.Errorf("preview = %q", got[0].Preview)
	}
	if got[0].Dir != dir {
		t.Errorf("dir = %s, want %s", got[0].Dir, dir)
	}
	if got[0].TokenUsage != (llm.Usage{InputTokens: 17, OutputTokens: 7}) {
		t.Errorf("token_usage = %+v", got[0].TokenUsage)
	}
	if got[0].ContextUsage == nil || got[0].ContextUsage.TotalTokens != 10 {
		t.Fatalf("context_usage = %+v, want latest total 10", got[0].ContextUsage)
	}
	want := time.Date(2026, 5, 6, 10, 35, 0, 0, time.Local)
	if !got[0].StartedAt.Equal(want) {
		t.Errorf("started_at = %v, want %v", got[0].StartedAt, want)
	}
}

func compactTestMessage(text string) llm.Message {
	msg := llm.TextMessage(llm.RoleUser, text)
	msg.Kind = llm.MessageKindCompact
	return msg
}

func TestList_TruncatesPreviewToRunes(t *testing.T) {
	root := t.TempDir()
	long := ""
	for i := 0; i < 100; i++ {
		long += "中"
	}
	makeSession(t, root, "20260506T103500-aa000001",
		[]llm.Message{llm.TextMessage(llm.RoleUser, long)},
		time.Now())

	got, _ := List(root)
	if r := []rune(got[0].Preview); len(r) != 80 {
		t.Fatalf("preview rune count = %d, want 80; got %q", len(r), got[0].Preview)
	}
}

func TestList_SkipsDirsWithoutConversationJSONL(t *testing.T) {
	root := t.TempDir()
	makeSession(t, root, "20260506T103500-good00001",
		[]llm.Message{llm.TextMessage(llm.RoleUser, "ok")}, time.Now())
	if err := os.MkdirAll(filepath.Join(root, "20260506T100000-empty0001"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "stray.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1; got %+v", len(got), got)
	}
}

func TestList_ReturnsEmptyWhenRootMissing(t *testing.T) {
	got, err := List(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestLoadInfo_ReturnsFullMessages(t *testing.T) {
	root := t.TempDir()
	dir := makeSession(t, root, "20260506T103500-load0001",
		[]llm.Message{
			llm.TextMessage(llm.RoleUser, "u1"),
			llm.TextMessage(llm.RoleAssistant, "a1"),
		}, time.Now())

	info, msgs, err := LoadInfo(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.ID != "20260506T103500-load0001" {
		t.Errorf("id = %s", info.ID)
	}
	if len(msgs) != 2 {
		t.Fatalf("messages len = %d", len(msgs))
	}
	if msgs[0].FirstText() != "u1" || msgs[1].FirstText() != "a1" {
		t.Errorf("messages mismatch: %+v", msgs)
	}
}

func TestLoadInfo_NormalizesNullBlocks(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "20260509T074114-a20bf346")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "conversation.jsonl"), []byte(`{"role":"assistant","blocks":null}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, msgs, err := LoadInfo(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("messages len = %d, want 1", len(msgs))
	}
	if msgs[0].Blocks == nil {
		t.Fatal("blocks is nil, want empty slice")
	}
}

func TestLoadInfo_NormalizesLegacyIDsAndPreservesCompactionMetadata(t *testing.T) {
	root := t.TempDir()
	compact := compactTestMessage("old context summary")
	compact.Compaction = &llm.CompactionMetadata{
		Auto:               true,
		Reason:             "auto",
		TailStartMessageID: "m2",
		TokensBefore:       100,
		TokensAfter:        40,
		SummaryChars:       12,
		SummaryModel:       "mock",
	}
	dir := makeSession(t, root, "20260515T010203-meta0001",
		[]llm.Message{llm.TextMessage(llm.RoleUser, "legacy"), compact},
		time.Now())

	_, msgs, err := LoadInfo(dir)
	if err != nil {
		t.Fatal(err)
	}
	if msgs[0].ID != "legacy-000001" || msgs[1].ID != "legacy-000002" {
		t.Fatalf("legacy IDs = %q, %q", msgs[0].ID, msgs[1].ID)
	}
	if msgs[1].Compaction == nil || msgs[1].Compaction.TokensBefore != 100 || msgs[1].Compaction.TailStartMessageID != "m2" {
		t.Fatalf("compaction metadata = %+v", msgs[1].Compaction)
	}
}

func TestLoadInfo_NotFound(t *testing.T) {
	_, _, err := LoadInfo(filepath.Join(t.TempDir(), "missing"))
	if err == nil {
		t.Fatal("expected error")
	}
}
