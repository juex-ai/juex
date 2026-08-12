package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/session"
)

func ensureTestWorkspaceAgent(t *testing.T, work string) config.Config {
	t.Helper()
	testHome := filepath.Join(filepath.Dir(work), "juex-home")
	t.Setenv("JUEX_HOME", testHome)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(testHome, "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	cfg, err := config.LoadWithOptions(config.LoadOptions{
		WorkDir:    work,
		AgentState: config.AgentStateMint,
	})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// seedSession writes a session under the workspace's durable agent state.
func seedSession(t *testing.T, work, id string, jsonlBody string) string {
	t.Helper()
	cfg := ensureTestWorkspaceAgent(t, work)
	dir := filepath.Join(cfg.SessionsDir(), id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSessionMetadata(t, dir, id, session.KindPrimary)
	var normalized strings.Builder
	for i, line := range strings.Split(jsonlBody, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var msg llm.Message
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			t.Fatal(err)
		}
		if msg.ID == "" {
			msg.ID = fmt.Sprintf("m%d", i+1)
		}
		data, err := json.Marshal(msg)
		if err != nil {
			t.Fatal(err)
		}
		normalized.Write(data)
		normalized.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "conversation.jsonl"), []byte(normalized.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeSessionMetadata(t *testing.T, dir, id, kind string) {
	t.Helper()
	startedAt := time.Now().UTC()
	if len(id) >= len("20060102T150405") {
		if parsed, err := time.Parse("20060102T150405", id[:len("20060102T150405")]); err == nil {
			startedAt = parsed
		}
	}
	startedAtMS := startedAt.UnixMilli()
	data, err := json.Marshal(map[string]any{
		"kind":              kind,
		"started_at_ms":     startedAtMS,
		"last_active_at_ms": startedAtMS,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSessionsList_JSONShape(t *testing.T) {
	work := t.TempDir()
	body := `{"role":"user","blocks":[{"type":"text","text":"hi"}]}` + "\n" +
		`{"role":"assistant","blocks":[{"type":"text","text":"hello"}]}` + "\n"
	seedSession(t, work, "20260506T103500-aaaa1111", body)

	root := newRootCmd()
	var out bytes.Buffer
	var stderr bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&stderr)
	root.SetArgs([]string{"-C", work, "sessions", "list"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Sessions []struct {
			ID      string `json:"id"`
			Turns   int    `json:"turns"`
			Preview string `json:"preview"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatalf("not JSON: %v\nstdout:\n%s\nstderr:\n%s", err, out.String(), stderr.String())
	}
	if len(parsed.Sessions) != 1 {
		t.Fatalf("len = %d", len(parsed.Sessions))
	}
	if parsed.Sessions[0].ID != "20260506T103500-aaaa1111" {
		t.Errorf("id = %s", parsed.Sessions[0].ID)
	}
	if parsed.Sessions[0].Turns != 1 {
		t.Errorf("turns = %d", parsed.Sessions[0].Turns)
	}
	if parsed.Sessions[0].Preview != "hi" {
		t.Errorf("preview = %q", parsed.Sessions[0].Preview)
	}
}

func TestSessionsList_JSONTimestampsAreUTCForCosmeticIDs(t *testing.T) {
	work := t.TempDir()
	body := `{"role":"user","blocks":[{"type":"text","text":"hi"}]}` + "\n"
	seedSession(t, work, "20260506T103500-aaaa1111", body)
	seedSession(t, work, "20261318T065604-8f0582f4", body)

	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"-C", work, "sessions", "list"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Sessions []struct {
			ID           string `json:"id"`
			StartedAt    string `json:"started_at"`
			LastActiveAt string `json:"last_active_at"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Sessions) != 2 {
		t.Fatalf("sessions = %+v", parsed.Sessions)
	}
	for _, info := range parsed.Sessions {
		if !strings.HasSuffix(info.StartedAt, "Z") || !strings.HasSuffix(info.LastActiveAt, "Z") {
			t.Fatalf("session %s timestamps are not UTC: started=%q last=%q", info.ID, info.StartedAt, info.LastActiveAt)
		}
	}
}

func TestSessionsListRejectsChangedTranscriptDespiteMatchingSizeAndMtime(t *testing.T) {
	work := t.TempDir()
	id := "20260729T120000-cached01"
	valid := `{"id":"m1","role":"user","blocks":[]}` + "\n"
	dir := seedSession(t, work, id, valid)
	mtime := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	convPath := filepath.Join(dir, "conversation.jsonl")
	if err := os.Chtimes(convPath, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	info, _, err := session.LoadInfo(dir)
	if err != nil {
		t.Fatal(err)
	}
	info.Turns = 42
	info.Preview = "cached preview"
	historyPath := ensureTestWorkspaceAgent(t, work).HistoryPath()
	if err := session.RecordSession(historyPath, info); err != nil {
		t.Fatal(err)
	}
	malformed := bytes.Repeat([]byte{'x'}, len(valid))
	malformed[len(malformed)-1] = '\n'
	if err := os.WriteFile(convPath, malformed, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(convPath, mtime, mtime); err != nil {
		t.Fatal(err)
	}

	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"-C", work, "sessions", "list"})
	err = root.Execute()
	if err == nil || !strings.Contains(err.Error(), "parse") {
		t.Fatalf("sessions list error = %v, want canonical transcript parse failure", err)
	}
}

func TestSessionsList_MarksActiveAndKind(t *testing.T) {
	work := t.TempDir()
	body := `{"role":"user","blocks":[{"type":"text","text":"hi"}]}` + "\n"
	primaryDir := seedSession(t, work, "20260506T103500-primary1", body)
	sideDir := seedSession(t, work, "20260506T113500-side0001", body)
	if err := session.SetKind(sideDir, session.KindSide); err != nil {
		t.Fatal(err)
	}
	primary, _, err := session.LoadInfo(primaryDir)
	if err != nil {
		t.Fatal(err)
	}
	side, _, err := session.LoadInfo(sideDir)
	if err != nil {
		t.Fatal(err)
	}
	historyPath := ensureTestWorkspaceAgent(t, work).HistoryPath()
	if err := session.SetActive(historyPath, primary); err != nil {
		t.Fatal(err)
	}
	if err := session.RecordSession(historyPath, side); err != nil {
		t.Fatal(err)
	}

	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"-C", work, "sessions", "list"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	bodyOut := out.String()
	for _, want := range []string{
		`"kind": "primary"`,
		`"kind": "side"`,
		`"active": true`,
	} {
		if !strings.Contains(bodyOut, want) {
			t.Fatalf("list output missing %q in:\n%s", want, bodyOut)
		}
	}
}

func TestSessionsList_EmptyReturnsEmptyArray(t *testing.T) {
	work := t.TempDir()
	ensureTestWorkspaceAgent(t, work)
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"-C", work, "sessions", "list"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"sessions":`) {
		t.Errorf("missing sessions key: %s", out.String())
	}
}

func TestSessionsActivate_PrimaryOnly(t *testing.T) {
	work := t.TempDir()
	body := `{"role":"user","blocks":[{"type":"text","text":"hi"}]}` + "\n"
	firstDir := seedSession(t, work, "20260506T103500-first001", body)
	secondDir := seedSession(t, work, "20260506T113500-second01", body)
	sideDir := seedSession(t, work, "20260506T123500-side0001", body)
	if err := session.SetKind(sideDir, session.KindSide); err != nil {
		t.Fatal(err)
	}
	first, _, err := session.LoadInfo(firstDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.SetActive(ensureTestWorkspaceAgent(t, work).HistoryPath(), first); err != nil {
		t.Fatal(err)
	}

	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"-C", work, "sessions", "activate", filepath.Base(secondDir)})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"active": true`) || !strings.Contains(out.String(), filepath.Base(secondDir)) {
		t.Fatalf("activate output = %s", out.String())
	}

	root2 := newRootCmd()
	var out2 bytes.Buffer
	root2.SetOut(&out2)
	root2.SetErr(&out2)
	root2.SetArgs([]string{"-C", work, "sessions", "activate", filepath.Base(sideDir)})
	err = root2.Execute()
	if err == nil {
		t.Fatal("expected side activation error")
	}
	if _, ok := err.(*usageError); !ok {
		t.Fatalf("got %T: %v", err, err)
	}
}

func TestSessionsContinue_ResumesSideWithoutChangingActive(t *testing.T) {
	work := t.TempDir()
	ensureTestWorkspaceAgent(t, work)
	configPath := filepath.Join(work, ".juex", "juex.yaml")
	if err := writeJuexConfigFile(configPath, "openai", "https://example.invalid", "k", "m"); err != nil {
		t.Fatal(err)
	}

	runStatus := func(t *testing.T, args ...string) runResult {
		t.Helper()
		root := newRootCmd()
		var out bytes.Buffer
		var stderr bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&stderr)
		root.SetArgs(append([]string{"-C", work}, args...))
		if err := root.Execute(); err != nil {
			t.Fatalf("juex %s: %v\nstderr:\n%s", strings.Join(args, " "), err, stderr.String())
		}
		var result runResult
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatalf("decode output %q: %v", out.String(), err)
		}
		return result
	}

	primary := runStatus(t, "run", "--json", "/status")
	side := runStatus(t, "run", "--json", "--side", "/status")
	if side.SessionKind != session.KindSide || side.Active {
		t.Fatalf("created side = %+v, want inactive side", side)
	}

	continued := runStatus(t, "sessions", "continue", side.SessionID, "--json", "/status")
	if continued.SessionID != side.SessionID {
		t.Fatalf("continued session = %s, want %s", continued.SessionID, side.SessionID)
	}
	if continued.SessionKind != session.KindSide || continued.Active {
		t.Fatalf("continued side = %+v, want inactive side", continued)
	}

	active := runStatus(t, "run", "--json", "/status")
	if active.SessionID != primary.SessionID || !active.Active {
		t.Fatalf("default continuation = %+v, want active primary %s", active, primary.SessionID)
	}
}

func TestSessionsContinue_MissingSessionReturnsNotFound(t *testing.T) {
	work := t.TempDir()
	ensureTestWorkspaceAgent(t, work)
	configPath := filepath.Join(work, ".juex", "juex.yaml")
	if err := writeJuexConfigFile(configPath, "openai", "https://example.invalid", "k", "m"); err != nil {
		t.Fatal(err)
	}

	root := newRootCmd()
	root.SetArgs([]string{"-C", work, "sessions", "continue", "missing", "/status"})
	err := root.Execute()
	var notFound *notFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("err = %T %v, want notFoundError", err, err)
	}
}

func TestSessionsContinue_RejectsNewSlashAndPreservesRequestedSession(t *testing.T) {
	work := t.TempDir()
	ensureTestWorkspaceAgent(t, work)
	configPath := filepath.Join(work, ".juex", "juex.yaml")
	if err := writeJuexConfigFile(configPath, "openai", "https://example.invalid", "k", "m"); err != nil {
		t.Fatal(err)
	}

	runStatus := func(t *testing.T) runResult {
		t.Helper()
		root := newRootCmd()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetArgs([]string{"-C", work, "run", "--json", "/status"})
		if err := root.Execute(); err != nil {
			t.Fatal(err)
		}
		var result runResult
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatalf("decode output %q: %v", out.String(), err)
		}
		return result
	}

	before := runStatus(t)
	root := newRootCmd()
	root.SetArgs([]string{"-C", work, "sessions", "continue", before.SessionID, "/new"})
	err := root.Execute()
	var usageErr *usageError
	if !errors.As(err, &usageErr) {
		t.Fatalf("err = %T %v, want usageError", err, err)
	}
	after := runStatus(t)
	if after.SessionID != before.SessionID {
		t.Fatalf("active session changed from %s to %s", before.SessionID, after.SessionID)
	}
}

func TestSessionsList_TableFormat(t *testing.T) {
	work := t.TempDir()
	body := `{"role":"user","blocks":[{"type":"text","text":"hi"}]}` + "\n"
	seedSession(t, work, "20260506T103500-aaaa1111", body)

	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"-C", work, "sessions", "list", "--format", "table"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	body2 := out.String()
	for _, want := range []string{"ID", "TURNS", "PREVIEW", "20260506T103500-aaaa1111"} {
		if !strings.Contains(body2, want) {
			t.Errorf("missing %q in table:\n%s", want, body2)
		}
	}
}

func TestSessionsList_LimitTruncates(t *testing.T) {
	work := t.TempDir()
	body := `{"role":"user","blocks":[{"type":"text","text":"hi"}]}` + "\n"
	for _, id := range []string{
		"20260506T103500-aaaa1111",
		"20260505T103500-bbbb2222",
		"20260504T103500-cccc3333",
	} {
		seedSession(t, work, id, body)
	}
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"-C", work, "sessions", "list", "--limit", "2"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Sessions []map[string]any `json:"sessions"`
	}
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Sessions) != 2 {
		t.Errorf("limit ignored: %d sessions", len(parsed.Sessions))
	}
}

func TestSessionsShow_JSONIncludesMessages(t *testing.T) {
	work := t.TempDir()
	body := `{"role":"user","blocks":[{"type":"text","text":"hi"}]}` + "\n" +
		`{"role":"assistant","blocks":[{"type":"text","text":"hello"}]}` + "\n"
	seedSession(t, work, "20260506T103500-show0001", body)

	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"-C", work, "sessions", "show", "20260506T103500-show0001"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		ID       string `json:"id"`
		Turns    int    `json:"turns"`
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out.String())
	}
	if parsed.ID != "20260506T103500-show0001" {
		t.Errorf("id = %s", parsed.ID)
	}
	if len(parsed.Messages) != 2 {
		t.Errorf("messages len = %d", len(parsed.Messages))
	}
	if parsed.Messages[0].Role != "user" || parsed.Messages[1].Role != "assistant" {
		t.Errorf("roles wrong: %+v", parsed.Messages)
	}
}

func TestSessionsShow_TextRendersTranscript(t *testing.T) {
	work := t.TempDir()
	body := `{"role":"user","blocks":[{"type":"text","text":"hi"}]}` + "\n" +
		`{"role":"assistant","blocks":[{"type":"text","text":"hello"}]}` + "\n"
	seedSession(t, work, "20260506T103500-show0002", body)

	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"-C", work, "sessions", "show", "20260506T103500-show0002", "--format", "text"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	body2 := out.String()
	for _, want := range []string{"20260506T103500-show0002", "user>", "hi", "assistant>", "hello"} {
		if !strings.Contains(body2, want) {
			t.Errorf("missing %q in:\n%s", want, body2)
		}
	}
}

func TestSessionsShow_TextRendersReasoning(t *testing.T) {
	work := t.TempDir()
	body := `{"role":"assistant","blocks":[{"type":"reasoning","text":"step one"},{"type":"text","text":"answer"}]}` + "\n" +
		`{"role":"assistant","blocks":[{"type":"reasoning","content":"x","redacted":true}]}` + "\n"
	seedSession(t, work, "20260506T103500-show0003", body)

	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"-C", work, "sessions", "show", "20260506T103500-show0003", "--format", "text"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	body2 := out.String()
	for _, want := range []string{"thinking> step one", "thinking> [redacted]", "assistant> answer"} {
		if !strings.Contains(body2, want) {
			t.Errorf("missing %q in:\n%s", want, body2)
		}
	}
}

func TestSessionsShow_TextRendersImages(t *testing.T) {
	work := t.TempDir()
	body := `{"role":"assistant","blocks":[{"type":"image","media":{"artifact_path":"sessions/s/media/chart.png","media_type":"image/png","original_bytes":2048,"width":640,"height":480}}]}` + "\n" +
		`{"role":"user","blocks":[{"type":"tool_result","tool_use_id":"tool-1","content":"chart rendered","media":{"artifact_path":"sessions/s/media/tool.png","media_type":"image/png","original_bytes":512,"width":20,"height":10}}]}` + "\n"
	seedSession(t, work, "20260506T103500-show0004", body)

	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"-C", work, "sessions", "show", "20260506T103500-show0004", "--format", "text"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	body2 := out.String()
	for _, want := range []string{
		"assistant> [图片: chart.png (640x480, 2.0 KB)]",
		"tool< chart rendered",
		"tool< [图片: tool.png (20x10, 512 B)]",
	} {
		if !strings.Contains(body2, want) {
			t.Errorf("missing %q in:\n%s", want, body2)
		}
	}
}

func TestSessionsShow_NotFound(t *testing.T) {
	work := t.TempDir()
	ensureTestWorkspaceAgent(t, work)
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"-C", work, "sessions", "show", "missing-id"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected error")
	}
	if _, ok := err.(*notFoundError); !ok {
		t.Fatalf("expected *notFoundError, got %T: %v", err, err)
	}
}

func TestSessionsContextJSON(t *testing.T) {
	work := t.TempDir()
	body := `{"id":"m1","role":"user","blocks":[{"type":"text","text":"hi"}]}` + "\n"
	seedSession(t, work, "20260515T010203-context", body)

	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"-C", work, "sessions", "context", "20260515T010203-context", "--format", "json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"messages"`) || !strings.Contains(out.String(), `"hi"`) {
		t.Fatalf("output = %s", out.String())
	}
}

func TestSessionsCompact(t *testing.T) {
	work := t.TempDir()
	id := "20260515T010203-compact"
	body := `{"id":"m1","role":"user","blocks":[{"type":"text","text":"` + strings.Repeat("old ", 80) + `"}]}` + "\n"
	sessionDir := seedSession(t, work, id, body)
	cfg := config.Config{
		WorkDir:       work,
		AgentStateDir: filepath.Dir(filepath.Dir(sessionDir)),
		ContextWindow: config.DefaultContextWindow,
		Compaction:    config.DefaultCompactionConfig(),
	}
	prov := &sessionsCompactProvider{response: llm.Response{
		Message:    llm.TextMessage(llm.RoleAssistant, "summary"),
		StopReason: llm.StopEndTurn,
	}}

	result, err := compactSession(context.Background(), cfg, id, "manual", "focus on API changes", prov, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.MessageID == "" || result.Reason != "manual" || result.SummaryChars != len("summary") {
		t.Fatalf("result = %+v", result)
	}
	data, err := os.ReadFile(filepath.Join(sessionDir, "conversation.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"kind":"compact"`) || !strings.Contains(string(data), `summary`) || !strings.Contains(string(data), `"compaction"`) {
		t.Fatalf("conversation not compacted:\n%s", data)
	}
	if len(prov.systems) != 1 || !strings.Contains(prov.systems[0], "Compact Instructions:\nfocus on API changes") {
		t.Fatalf("summary prompt missing compact instructions: %q", prov.systems)
	}
}

type sessionsCompactProvider struct {
	response llm.Response
	systems  []string
}

func (p *sessionsCompactProvider) Name() string { return "mock" }

func (p *sessionsCompactProvider) Complete(ctx context.Context, sys string, h []llm.Message, t []llm.ToolSpec) (llm.Response, error) {
	p.systems = append(p.systems, sys)
	return p.response, nil
}

func TestSessionsDelete_RemovesSessionAndHistory(t *testing.T) {
	work := t.TempDir()
	cfg := ensureTestWorkspaceAgent(t, work)
	id := "20260506T103500-delete01"
	body := `{"role":"user","blocks":[{"type":"text","text":"bye"}]}` + "\n"
	dir := filepath.Join(cfg.SessionsDir(), id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "conversation.jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	historyPath := cfg.HistoryPath()
	if err := os.MkdirAll(filepath.Dir(historyPath), 0o755); err != nil {
		t.Fatal(err)
	}
	entry := map[string]any{
		"id":             id,
		"dir":            dir,
		"started_at":     "2026-05-06T10:35:00Z",
		"last_active_at": "2026-05-06T10:35:00Z",
		"turns":          1,
		"preview":        "bye",
	}
	historyData, err := json.MarshalIndent(map[string]any{
		"sessions": []map[string]any{entry},
		"last":     entry,
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	historyData = append(historyData, '\n')
	if err := os.WriteFile(historyPath, historyData, 0o644); err != nil {
		t.Fatal(err)
	}

	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"-C", work, "sessions", "delete", id})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"deleted": true`) || !strings.Contains(out.String(), id) {
		t.Fatalf("delete output = %s", out.String())
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("deleted dir stat err = %v, want not exist", err)
	}
	data, err := os.ReadFile(historyPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), id) {
		t.Fatalf("history still contains deleted id:\n%s", data)
	}
}

func TestSessionsDelete_NotFound(t *testing.T) {
	work := t.TempDir()
	ensureTestWorkspaceAgent(t, work)
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"-C", work, "sessions", "delete", "missing-id"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected error")
	}
	if _, ok := err.(*notFoundError); !ok {
		t.Fatalf("expected *notFoundError, got %T: %v", err, err)
	}
}
