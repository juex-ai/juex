package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/chunkedwrite"
	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/hooks"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/mcp"
	"github.com/juex-ai/juex/internal/observable"
	"github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/session"
	"github.com/juex-ai/juex/internal/tools"
)

type stubProvider struct {
	replies   []llm.Response
	calls     int
	systems   []string
	histories [][]llm.Message
}

func TestDurationSecondsCeilAndCap(t *testing.T) {
	if got := durationSeconds(1500 * time.Millisecond); got != 2 {
		t.Fatalf("durationSeconds(1.5s) = %d, want 2", got)
	}
	if got := durationSeconds(10 * time.Minute); got != 300 {
		t.Fatalf("durationSeconds(10m) = %d, want cap 300", got)
	}
}

func (s *stubProvider) Name() string { return "stub" }
func (s *stubProvider) Complete(ctx context.Context, sys string, h []llm.Message, t []llm.ToolSpec) (llm.Response, error) {
	if s.calls >= len(s.replies) {
		return llm.Response{}, errors.New("stub exhausted")
	}
	s.systems = append(s.systems, sys)
	s.histories = append(s.histories, append([]llm.Message(nil), h...))
	r := s.replies[s.calls]
	s.calls++
	return r, nil
}

func newStubApp(t *testing.T, replies ...llm.Response) (*App, *stubProvider) {
	t.Helper()
	dir := t.TempDir()
	prov := &stubProvider{replies: replies}
	a, err := New(Options{
		Config:   config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: dir},
		Provider: prov,
		WorkDir:  dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	return a, prov
}

func TestAppResumeRestoresActiveChunkedWriteLifecycle(t *testing.T) {
	work := t.TempDir()
	cfg := config.Config{
		WorkDir:                   work,
		ProviderProtocol:          "openai/chat",
		EnableUserGlobalResources: false,
	}
	firstProvider := &chunkedWriteResumeProvider{t: t, phase: "start"}
	first, err := New(Options{
		Config:     cfg,
		Provider:   firstProvider,
		WorkDir:    work,
		DisableMCP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := first.Run(context.Background(), "start a chunked write")
	if err != nil {
		t.Fatal(err)
	}
	if out != "paused" {
		t.Fatalf("first output = %q", out)
	}
	sessionDir := first.Session.Dir
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	secondProvider := &chunkedWriteResumeProvider{t: t, phase: "resume"}
	second, err := New(Options{
		Config:     cfg,
		Provider:   secondProvider,
		WorkDir:    work,
		ResumeDir:  sessionDir,
		DisableMCP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	out, err = second.Run(context.Background(), "finish the chunked write")
	if err != nil {
		t.Fatal(err)
	}
	if out != "done" {
		t.Fatalf("second output = %q", out)
	}
	body, err := os.ReadFile(filepath.Join(work, "resume.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "one\ntwo\n" {
		t.Fatalf("resume.md = %q", body)
	}
}

type chunkedWriteResumeProvider struct {
	t      *testing.T
	phase  string
	called int
}

func (p *chunkedWriteResumeProvider) Name() string { return "chunked-write-resume" }

func (p *chunkedWriteResumeProvider) Complete(ctx context.Context, sys string, hist []llm.Message, tools []llm.ToolSpec) (llm.Response, error) {
	idx := p.called
	p.called++
	switch p.phase {
	case "start":
		switch idx {
		case 0:
			return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{
				Type:      llm.BlockToolUse,
				ToolUseID: "begin_resume",
				ToolName:  "write_begin",
				Input:     map[string]any{"path": "resume.md", "mode": "create"},
			}}}, StopReason: llm.StopToolUse}, nil
		case 1:
			return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{
				Type:      llm.BlockToolUse,
				ToolUseID: "chunk_resume_0",
				ToolName:  "write_chunk",
				Input:     map[string]any{"write_id": chunkedWriteIDFromLifecycle(p.t, hist), "index": 0, "content": "one\n"},
			}}}, StopReason: llm.StopToolUse}, nil
		case 2:
			return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "paused"), StopReason: llm.StopEndTurn}, nil
		}
	case "resume":
		switch idx {
		case 0:
			return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{
				Type:      llm.BlockToolUse,
				ToolUseID: "chunk_resume_1",
				ToolName:  "write_chunk",
				Input:     map[string]any{"write_id": chunkedWriteIDFromLifecycle(p.t, hist), "index": 1, "content": "two\n"},
			}}}, StopReason: llm.StopToolUse}, nil
		case 1:
			return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{
				Type:      llm.BlockToolUse,
				ToolUseID: "commit_resume",
				ToolName:  "write_commit",
				Input:     map[string]any{"write_id": chunkedWriteIDFromLifecycle(p.t, hist), "expected_chunks": 2},
			}}}, StopReason: llm.StopToolUse}, nil
		case 2:
			return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn}, nil
		}
	}
	return llm.Response{}, fmt.Errorf("unexpected %s provider call %d", p.phase, idx)
}

func chunkedWriteIDFromLifecycle(t *testing.T, history []llm.Message) string {
	t.Helper()
	for _, msg := range history {
		for _, block := range msg.Blocks {
			if block.Type == llm.BlockToolResult && block.ChunkedWrite != nil && block.ChunkedWrite.Kind == chunkedwrite.EventBegin {
				return block.ChunkedWrite.WriteID
			}
		}
	}
	t.Fatalf("history missing chunked write begin lifecycle fact: %+v", history)
	return ""
}

func TestAppRegistersSkillSearchAndLoadTools(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, ".agents", "skills", "visual")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: visual\ndescription: inspect screenshots\n---\n# Visual Skill\nUse vision carefully.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := New(Options{
		Config:   config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: dir, Skills: config.DefaultSkillsConfig()},
		Provider: &stubProvider{},
		WorkDir:  dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	out, err := a.Engine.Tools.Call(context.Background(), "skill_search", map[string]any{"query": "screen"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"name": "visual"`) {
		t.Fatalf("skill_search output = %s", out)
	}
	loaded, err := a.Engine.Tools.Call(context.Background(), "skill_load", map[string]any{"name": "visual"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(loaded, "Path: "+filepath.Join(skillDir, "SKILL.md")) {
		t.Fatalf("skill_load = %q, want SKILL.md path", loaded)
	}
	if !strings.Contains(loaded, "Directory: "+skillDir) {
		t.Fatalf("skill_load = %q, want skill directory", loaded)
	}
	if !strings.Contains(loaded, body) {
		t.Fatalf("skill_load = %q, want full body", loaded)
	}
	if _, err := a.Engine.Tools.Call(context.Background(), "skill_load", map[string]any{"name": nil}); err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Fatalf("skill_load nil name error = %v, want name required", err)
	}
}

func TestAppSkillLoadRespectsSandboxBlockedPaths(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, ".agents", "skills", "secret")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: secret\ndescription: blocked\n---\nSECRET BODY\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := New(Options{
		Config: config.Config{
			ProviderID: "openai",
			APIKey:     "x",
			Model:      "m",
			WorkDir:    dir,
			Skills:     config.DefaultSkillsConfig(),
			Sandbox: config.SandboxPolicy{
				Enabled: true,
				FileSystem: config.FileSystemSandboxPolicy{
					BlockedPaths: []string{skillDir},
				},
			},
		},
		Provider: &stubProvider{},
		WorkDir:  dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	if _, err := a.Engine.Tools.Call(context.Background(), "skill_load", map[string]any{"name": "secret"}); err == nil || !strings.Contains(err.Error(), "blocked path") {
		t.Fatalf("skill_load blocked path error = %v, want sandbox blocked path", err)
	}
}

func TestAppShellHelperProcess(t *testing.T) {
	if os.Getenv("JUEX_APP_FAKE_SHELL") != "1" {
		return
	}
	fmt.Fprintln(os.Stdout, "app shell start")
	switch os.Getenv("JUEX_APP_FAKE_SHELL_MODE") {
	case "delayed":
		time.Sleep(2 * time.Second)
	default:
		time.Sleep(10 * time.Second)
	}
	fmt.Fprintln(os.Stdout, "app shell done")
	os.Exit(0)
}

func TestApp_DefaultAttachesActivePrimary(t *testing.T) {
	dir := t.TempDir()
	first, err := New(Options{
		Config: config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: dir},
		Provider: &stubProvider{replies: []llm.Response{{
			Message:    llm.TextMessage(llm.RoleAssistant, "first"),
			StopReason: llm.StopEndTurn,
		}}},
		WorkDir: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Run(context.Background(), "remember me"); err != nil {
		t.Fatal(err)
	}
	firstID := first.Session.ID
	if first.Session.Kind != session.KindPrimary {
		t.Fatalf("kind = %q, want primary", first.Session.Kind)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := New(Options{
		Config:   config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: dir},
		Provider: &stubProvider{replies: []llm.Response{}},
		WorkDir:  dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if second.Session.ID != firstID {
		t.Fatalf("session id = %s, want active %s", second.Session.ID, firstID)
	}
	if len(second.Session.History) != 2 {
		t.Fatalf("history len = %d, want resumed user+assistant", len(second.Session.History))
	}
}

func TestApp_DefaultCreatesActivePrimaryWhenHistoryIsEmpty(t *testing.T) {
	dir := t.TempDir()
	a, err := New(Options{
		Config:   config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: dir},
		Provider: &stubProvider{replies: []llm.Response{}},
		WorkDir:  dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	if a.Session.Kind != session.KindPrimary || !a.Session.Active {
		t.Fatalf("session kind/active = %q/%v, want active primary", a.Session.Kind, a.Session.Active)
	}
	h, err := session.LoadHistory(filepath.Join(dir, ".juex", "history.json"))
	if err != nil {
		t.Fatal(err)
	}
	if h.Active == nil || h.Active.ID != a.Session.ID {
		t.Fatalf("history active = %+v, want %s", h.Active, a.Session.ID)
	}
}

func TestApp_NewAppliesRuntimePolicyValues(t *testing.T) {
	dir := t.TempDir()
	compaction := config.DefaultCompactionConfig()
	compaction.ReserveTokens = 123
	a, err := New(Options{
		Config: config.Config{
			ProviderID:       "openai",
			APIKey:           "x",
			Model:            "m",
			WorkDir:          dir,
			ContextWindow:    2048,
			MaxOutputTokens:  8192,
			Compaction:       compaction,
			PendingInputTTL:  30 * time.Minute,
			ExternalEventTTL: 48 * time.Hour,
		},
		Provider: &stubProvider{replies: []llm.Response{}},
		WorkDir:  dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if a.Engine.ContextWindow != 2048 {
		t.Fatalf("Engine.ContextWindow = %d, want 2048", a.Engine.ContextWindow)
	}
	if a.Engine.MaxOutputTokens != 8192 {
		t.Fatalf("Engine.MaxOutputTokens = %d, want 8192", a.Engine.MaxOutputTokens)
	}
	if a.Engine.Compaction.ReserveTokens != 123 {
		t.Fatalf("Engine.Compaction.ReserveTokens = %d, want 123", a.Engine.Compaction.ReserveTokens)
	}
	if a.Engine.PendingInputTTL != 30*time.Minute || a.Engine.ExternalEventTTL != 48*time.Hour {
		t.Fatalf("Engine pending TTLs = %s/%s", a.Engine.PendingInputTTL, a.Engine.ExternalEventTTL)
	}
}

func TestApp_NewInjectedProviderDoesNotConstructSummaryProvider(t *testing.T) {
	dir := t.TempDir()
	compaction := config.DefaultCompactionConfig()
	compaction.SummaryModel = "missing:gpt-4-mini"
	a, err := New(Options{
		Config: config.Config{
			ProviderID: "openai",
			APIKey:     "x",
			Model:      "m",
			WorkDir:    dir,
			Compaction: compaction,
		},
		Provider: &stubProvider{},
		WorkDir:  dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if a.Engine.SummaryProvider != nil {
		t.Fatalf("SummaryProvider = %T, want nil for injected provider without explicit summary provider", a.Engine.SummaryProvider)
	}
}

func TestApp_HandleObservationStartsTurnWhenNoActiveTurn(t *testing.T) {
	reply := llm.TextMessage(llm.RoleAssistant, "ack")
	a, prov := newStubApp(t, llm.Response{Message: reply, StopReason: llm.StopEndTurn})
	record := testObservationRecord("obs-delivered")
	if err := a.HandleObservation(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if prov.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", prov.calls)
	}
	got := prov.histories[0][0]
	if got.Kind != llm.MessageKindObservation {
		t.Fatalf("message kind = %q, want observation", got.Kind)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(got.FirstText()), &body); err != nil {
		t.Fatalf("observation body is not JSON: %v\n%s", err, got.FirstText())
	}
	if body["observation_id"] != record.ID || body["observable_id"] != record.ObservableID {
		t.Fatalf("body = %+v", body)
	}
	if body["window_start"] != float64(record.WindowStart.UnixMilli()) ||
		body["window_end"] != float64(record.WindowEnd.UnixMilli()) {
		t.Fatalf("body window timestamps = %+v", body)
	}
}

func TestObservationMessageZeroWindowTimestampsAreZero(t *testing.T) {
	record := testObservationRecord("obs-zero-window")
	record.WindowStart = time.Time{}
	record.WindowEnd = time.Time{}
	msg, err := observationMessage(record)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(msg.FirstText()), &body); err != nil {
		t.Fatal(err)
	}
	if body["window_start"] != float64(0) || body["window_end"] != float64(0) {
		t.Fatalf("body window timestamps = %+v, want zeros", body)
	}
}

func TestApp_HandleObservationQueuesDuringActiveTurn(t *testing.T) {
	a, prov := newStubApp(t)
	if err := a.Engine.ReserveTurnID("turn-active"); err != nil {
		t.Fatal(err)
	}
	record := testObservationRecord("obs-queued")
	if err := a.HandleObservation(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if prov.calls != 0 {
		t.Fatalf("provider calls = %d, want no direct turn", prov.calls)
	}
	records, err := a.Engine.PendingInputQueue.Records()
	if err != nil {
		t.Fatal(err)
	}
	pending := records["observation-"+record.ID]
	if pending.ID == "" {
		t.Fatalf("pending records = %+v, want observation id", records)
	}
	if pending.Message.Kind != llm.MessageKindObservation || pending.State != runtime.PendingInputStatePending {
		t.Fatalf("pending = %+v", pending)
	}
}

func TestApp_RegistersObservableTools(t *testing.T) {
	a, _ := newStubApp(t)
	if _, ok := a.Engine.Tools.Get("observable_list"); !ok {
		t.Fatal("observable_list tool missing")
	}
}

func testObservationRecord(id string) observable.ObservationRecord {
	now := time.Date(2026, 7, 6, 10, 0, 0, 0, time.UTC)
	return observable.ObservationRecord{
		ID:           id,
		ObservableID: "lark-events",
		Kind:         "lark_notification",
		Severity:     "info",
		WindowStart:  now,
		WindowEnd:    now.Add(10 * time.Second),
		Content:      "hello",
		State:        observable.ObservationStateRecorded,
		CreatedAt:    now,
	}
}

func TestApp_CloseClosesShellSessionManager(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("JUEX_APP_FAKE_SHELL", "1")
	a, err := New(Options{
		Config: config.Config{
			ProviderID: "openai",
			APIKey:     "x",
			Model:      "m",
			WorkDir:    dir,
			Shell: config.ShellProfile{
				Profile:   "fake",
				Family:    "posix",
				Binary:    os.Args[0],
				Args:      []string{"-test.run=TestAppShellHelperProcess", "--"},
				PathStyle: "posix",
			},
		},
		Provider: &stubProvider{replies: []llm.Response{}},
		WorkDir:  dir,
	})
	if err != nil {
		t.Fatal(err)
	}

	out, info, err := a.Engine.Tools.CallWithInfo(context.Background(), "exec_command", map[string]any{
		"cmd":           "slow",
		"yield_time_ms": 250,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, ok := info.StructuredResult.(tools.ShellResult)
	if !ok {
		t.Fatalf("structured result = %#v, want ShellResult", info.StructuredResult)
	}
	if !result.Running || result.SessionID <= 0 {
		t.Fatalf("exec output/result = %q / %+v, want running shell session", out, result)
	}

	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	_, _, err = a.Engine.Tools.CallWithInfo(context.Background(), "write_stdin", map[string]any{
		"session_id": result.SessionID,
	})
	if err == nil {
		t.Fatal("write_stdin after App.Close succeeded, want closed session manager error")
	}
	if !strings.Contains(err.Error(), "session manager closed") {
		t.Fatalf("write_stdin after App.Close err = %v, want session manager closed", err)
	}
}

func TestApp_ActiveShellSessionsAppearInPromptThroughCompaction(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("JUEX_APP_FAKE_SHELL", "1")
	t.Setenv("JUEX_APP_FAKE_SHELL_MODE", "delayed")
	prov := &stubProvider{replies: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "active seen"), StopReason: llm.StopEndTurn},
		{Message: llm.TextMessage(llm.RoleAssistant, "compact summary"), StopReason: llm.StopEndTurn},
		{Message: llm.TextMessage(llm.RoleAssistant, "after compact"), StopReason: llm.StopEndTurn},
		{Message: llm.TextMessage(llm.RoleAssistant, "after complete"), StopReason: llm.StopEndTurn},
	}}
	a, err := New(Options{
		Config: config.Config{
			ProviderID: "openai",
			APIKey:     "x",
			Model:      "m",
			WorkDir:    dir,
			Compaction: config.DefaultCompactionConfig(),
			Shell: config.ShellProfile{
				Profile:   "fake",
				Family:    "posix",
				Binary:    os.Args[0],
				Args:      []string{"-test.run=TestAppShellHelperProcess", "--"},
				PathStyle: "posix",
			},
		},
		Provider: prov,
		WorkDir:  dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })

	_, info, err := a.Engine.Tools.CallWithInfo(context.Background(), "exec_command", map[string]any{
		"cmd":           "delayed active",
		"yield_time_ms": 250,
	})
	if err != nil {
		t.Fatal(err)
	}
	started := shellResultFromAppInfo(t, info)
	if !started.Running || started.SessionID <= 0 {
		t.Fatalf("exec result = %+v, want running session", started)
	}

	if out, err := a.Run(context.Background(), "inspect active shell"); err != nil || out != "active seen" {
		t.Fatalf("first run = %q, %v", out, err)
	}
	requireActiveShellPrompt(t, prov.systems[0], started.SessionID)

	compact, err := a.CompactWithInstructions(context.Background(), "manual", false, "keep shell session ids visible")
	if err != nil {
		t.Fatal(err)
	}
	if compact.MessageID == "" {
		t.Fatalf("compact result = %+v, want appended compact message", compact)
	}
	requireActiveShellPrompt(t, prov.systems[1], started.SessionID)

	if out, err := a.Run(context.Background(), "continue after compact"); err != nil || out != "after compact" {
		t.Fatalf("post-compact run = %q, %v", out, err)
	}
	requireActiveShellPrompt(t, prov.systems[2], started.SessionID)

	_, _, err = a.Engine.Tools.CallWithInfo(context.Background(), "write_stdin", map[string]any{
		"session_id":    started.SessionID,
		"yield_time_ms": 5000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out, err := a.Run(context.Background(), "after shell complete"); err != nil || out != "after complete" {
		t.Fatalf("final run = %q, %v", out, err)
	}
	if len(prov.systems) != 4 {
		t.Fatalf("provider systems = %d, want 4", len(prov.systems))
	}
	if strings.Contains(prov.systems[3], "## Active Shell Sessions") || strings.Contains(prov.systems[3], fmt.Sprintf("session_id=%d", started.SessionID)) {
		t.Fatalf("stale active shell session leaked into final prompt:\n%s", prov.systems[3])
	}
}

func requireActiveShellPrompt(t *testing.T, systemPrompt string, sessionID int) {
	t.Helper()
	for _, want := range []string{
		"## Active Shell Sessions",
		fmt.Sprintf("session_id=%d", sessionID),
		"running=true",
		"tty=false",
		"delayed active",
		"write_stdin",
		"list_shell_sessions",
	} {
		if !strings.Contains(systemPrompt, want) {
			t.Fatalf("system prompt missing %q:\n%s", want, systemPrompt)
		}
	}
}

func shellResultFromAppInfo(t *testing.T, info tools.CallInfo) tools.ShellResult {
	t.Helper()
	result, ok := info.StructuredResult.(tools.ShellResult)
	if !ok {
		t.Fatalf("structured result = %#v, want ShellResult", info.StructuredResult)
	}
	return result
}

func TestApp_NewRunsSessionStartHooks(t *testing.T) {
	dir := t.TempDir()
	a, err := New(Options{
		Config: config.Config{
			ProviderID: "openai",
			APIKey:     "x",
			Model:      "m",
			WorkDir:    dir,
			Hooks: hooks.Config{Commands: []hooks.CommandHook{{
				Name:    "startup",
				Events:  []hooks.EventName{hooks.EventSessionStart},
				Command: appHookCommand("allow"),
			}}},
		},
		Provider: &stubProvider{replies: []llm.Response{}},
		WorkDir:  dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	data, err := os.ReadFile(filepath.Join(a.Session.Dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !strings.Contains(body, `"type":"hook.completed"`) || !strings.Contains(body, `"event_name":"SessionStart"`) {
		t.Fatalf("events missing SessionStart hook:\n%s", body)
	}
}

func TestApp_NewRunsExtensionSessionStartHook(t *testing.T) {
	dir := t.TempDir()
	command, err := json.Marshal(appHookCommand("allow"))
	if err != nil {
		t.Fatal(err)
	}
	mustWriteAppTestFile(t, filepath.Join(dir, ".juex", "extensions", "demo", "hooks.yaml"), `trusted: true
commands:
  - name: ext-startup
    events: [SessionStart]
    command: `+string(command)+`
`)
	a, err := New(Options{
		Config:   config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: dir},
		Provider: &stubProvider{replies: []llm.Response{}},
		WorkDir:  dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	data, err := os.ReadFile(filepath.Join(a.Session.Dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !strings.Contains(body, `"name":"ext-startup"`) || !strings.Contains(body, `"source":"ext:demo"`) {
		t.Fatalf("events missing extension SessionStart hook source:\n%s", body)
	}
}

func TestApp_DebugObservabilityWritesSessionArtifacts(t *testing.T) {
	dir := t.TempDir()
	a, err := New(Options{
		Config: config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: dir},
		Provider: &stubProvider{replies: []llm.Response{{
			Message:    llm.TextMessage(llm.RoleAssistant, "answer"),
			StopReason: llm.StopEndTurn,
		}}},
		WorkDir: dir,
		Debug:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := a.Run(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if out != "answer" {
		t.Fatalf("out = %q", out)
	}
	sessionDir := a.Session.Dir
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}

	for _, rel := range []string{"logs/juex.log", "logs/debug.log", "trace.jsonl", "spans.jsonl", "tools.jsonl"} {
		if _, err := os.Stat(filepath.Join(sessionDir, rel)); err != nil {
			t.Fatalf("%s missing: %v", rel, err)
		}
	}
	trace, err := os.ReadFile(filepath.Join(sessionDir, "trace.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"event":"turn.started"`, `"event":"llm.requested"`, `"event":"llm.responded"`, `"event":"finish.attempted"`, `"session_id":"` + filepath.Base(sessionDir) + `"`} {
		if !strings.Contains(string(trace), want) {
			t.Fatalf("trace missing %s:\n%s", want, trace)
		}
	}
	debugData, err := os.ReadFile(filepath.Join(sessionDir, "logs", "debug.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(debugData), "finish.attempted") {
		t.Fatalf("debug log missing finish event:\n%s", debugData)
	}
}

func mustWriteAppTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestApp_NewSessionStartHookDenyFailsStartup(t *testing.T) {
	dir := t.TempDir()
	_, err := New(Options{
		Config: config.Config{
			ProviderID: "openai",
			APIKey:     "x",
			Model:      "m",
			WorkDir:    dir,
			Hooks: hooks.Config{Commands: []hooks.CommandHook{{
				Name:    "startup",
				Events:  []hooks.EventName{hooks.EventSessionStart},
				Command: appHookCommand("deny"),
			}}},
		},
		Provider: &stubProvider{replies: []llm.Response{}},
		WorkDir:  dir,
	})
	if err == nil || !strings.Contains(err.Error(), "SessionStart denied: startup blocked") {
		t.Fatalf("err = %v", err)
	}
}

func TestApp_NewInvalidHookConfigClosesDurableSink(t *testing.T) {
	baseline := goruntime.NumGoroutine()
	for i := 0; i < 20; i++ {
		dir := t.TempDir()
		_, err := New(Options{
			Config: config.Config{
				ProviderID: "openai",
				APIKey:     "x",
				Model:      "m",
				WorkDir:    dir,
				Hooks: hooks.Config{Commands: []hooks.CommandHook{{
					Name:   "invalid",
					Events: []hooks.EventName{hooks.EventSessionStart},
				}}},
			},
			Provider: &stubProvider{replies: []llm.Response{}},
			WorkDir:  dir,
		})
		if err == nil || !strings.Contains(err.Error(), "hooks: invalid: command is required") {
			t.Fatalf("err = %v, want invalid hook command error", err)
		}
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if got := goruntime.NumGoroutine(); got <= baseline+5 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("goroutines after failed app.New = %d, want near baseline %d", goruntime.NumGoroutine(), baseline)
}

func TestApp_NewSideDoesNotChangeActive(t *testing.T) {
	dir := t.TempDir()
	primary, err := New(Options{
		Config:      config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: dir},
		Provider:    &stubProvider{replies: []llm.Response{}},
		WorkDir:     dir,
		SessionMode: SessionModeNewPrimary,
	})
	if err != nil {
		t.Fatal(err)
	}
	primaryID := primary.Session.ID
	if err := primary.Close(); err != nil {
		t.Fatal(err)
	}

	side, err := New(Options{
		Config:      config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: dir},
		Provider:    &stubProvider{replies: []llm.Response{}},
		WorkDir:     dir,
		SessionMode: SessionModeNewSide,
	})
	if err != nil {
		t.Fatal(err)
	}
	sideID := side.Session.ID
	if side.Session.Kind != session.KindSide {
		t.Fatalf("side kind = %q, want side", side.Session.Kind)
	}
	if err := side.Close(); err != nil {
		t.Fatal(err)
	}

	h, err := session.LoadHistory(filepath.Join(dir, ".juex", "history.json"))
	if err != nil {
		t.Fatal(err)
	}
	if h.Active == nil || h.Active.ID != primaryID {
		t.Fatalf("active = %+v, want primary %s", h.Active, primaryID)
	}
	if h.Active.ID == sideID {
		t.Fatalf("side session became active: %+v", h.Active)
	}
}

func appHookCommand(mode string) []string {
	return []string{os.Args[0], "-test.run=TestAppHookHelperProcess", "--", mode}
}

func TestAppHookHelperProcess(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "--" {
		return
	}
	switch os.Args[len(os.Args)-1] {
	case "allow":
		_, _ = os.Stdout.WriteString(`{"decision":"allow"}`)
	case "deny":
		_, _ = os.Stdout.WriteString(`{"decision":"deny","additional_context":"startup blocked"}`)
	default:
		_, _ = os.Stdout.WriteString(`{}`)
	}
	os.Exit(0)
}

func TestApp_RunSingleTurn(t *testing.T) {
	a, _ := newStubApp(t, llm.Response{
		Message:    llm.TextMessage(llm.RoleAssistant, "hello back"),
		StopReason: llm.StopEndTurn,
		Usage:      llm.Usage{InputTokens: 8, OutputTokens: 3},
	})
	out, err := a.Run(context.Background(), "hi")
	if err != nil {
		t.Fatal(err)
	}
	if out != "hello back" {
		t.Fatalf("out = %q", out)
	}
	if got := a.TokenUsage(); got != (llm.Usage{InputTokens: 8, OutputTokens: 3}) {
		t.Fatalf("usage = %+v", got)
	}
}

func TestApp_RunUsesWorkDirForPromptAndFileTools(t *testing.T) {
	processDir := t.TempDir()
	t.Chdir(processDir)
	workDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workDir, "music"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "music", "README.md"), []byte("spelling rules"), 0o644); err != nil {
		t.Fatal(err)
	}
	prov := &stubProvider{replies: []llm.Response{
		{
			Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{
				Type:      llm.BlockToolUse,
				ToolUseID: "read-1",
				ToolName:  "read",
				Input:     map[string]any{"path": "music/README.md"},
			}}},
			StopReason: llm.StopToolUse,
		},
		{
			Message:    llm.TextMessage(llm.RoleAssistant, "done"),
			StopReason: llm.StopEndTurn,
		},
	}}
	a, err := New(Options{
		Config:   config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: workDir},
		Provider: prov,
		WorkDir:  workDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })

	out, err := a.Run(context.Background(), "read relative file")
	if err != nil {
		t.Fatal(err)
	}
	if out != "done" {
		t.Fatalf("out = %q, want done", out)
	}
	if len(prov.systems) == 0 {
		t.Fatal("provider was not called")
	}
	if !strings.Contains(prov.systems[0], "- cwd: "+workDir) {
		t.Fatalf("system prompt did not use workdir:\n%s", prov.systems[0])
	}
	if strings.Contains(prov.systems[0], "- cwd: "+processDir) {
		t.Fatalf("system prompt used process cwd:\n%s", prov.systems[0])
	}
	if len(a.Session.History) < 3 {
		t.Fatalf("history len = %d, want tool result", len(a.Session.History))
	}
	result := a.Session.History[2].Blocks[0]
	if result.IsError || result.Content != "spelling rules" {
		t.Fatalf("tool result = %+v, want workdir file contents", result)
	}
}

func TestApp_MCPNotificationRunsAgentTurn(t *testing.T) {
	a, prov := newStubApp(t, llm.Response{
		Message:    llm.TextMessage(llm.RoleAssistant, "handled event"),
		StopReason: llm.StopEndTurn,
	})

	err := a.HandleMCPNotification(context.Background(), mcp.Notification{
		ServerName: "local",
		Method:     "notifications/claude/channel",
		EventType:  "message",
		Content:    "[realtime] hello alice",
		Params: map[string]any{
			"content": "[realtime] hello alice",
			"meta":    map[string]any{"event_type": "message", "topic": "ops"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if prov.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", prov.calls)
	}
	if len(a.Session.History) != 2 {
		t.Fatalf("history len = %d, want user and assistant", len(a.Session.History))
	}
	user := a.Session.History[0]
	if user.Kind != llm.MessageKindMCPEvent {
		t.Fatalf("user kind = %q", user.Kind)
	}
	if got, want := user.FirstText(), "local:message:{\n  \"content\": \"[realtime] hello alice\",\n  \"meta\": {\n    \"event_type\": \"message\",\n    \"topic\": \"ops\"\n  }\n}"; got != want {
		t.Fatalf("user text = %q", got)
	}
}

type blockingAppProvider struct {
	started chan struct{}
	release chan struct{}

	mu        sync.Mutex
	calls     int
	histories [][]llm.Message
}

func newBlockingAppProvider() *blockingAppProvider {
	return &blockingAppProvider{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (p *blockingAppProvider) Name() string { return "blocking" }

func (p *blockingAppProvider) Complete(ctx context.Context, sys string, h []llm.Message, t []llm.ToolSpec) (llm.Response, error) {
	p.mu.Lock()
	idx := p.calls
	p.calls++
	p.histories = append(p.histories, append([]llm.Message(nil), h...))
	p.mu.Unlock()
	if idx == 0 {
		close(p.started)
		select {
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		case <-p.release:
		}
		return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "first"), StopReason: llm.StopEndTurn}, nil
	}
	return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "handled queued event"), StopReason: llm.StopEndTurn}, nil
}

func TestApp_MCPNotificationQueuesDuringActiveTurn(t *testing.T) {
	dir := t.TempDir()
	prov := newBlockingAppProvider()
	a, err := New(Options{
		Config:   config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: dir},
		Provider: prov,
		WorkDir:  dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })

	done := make(chan error, 1)
	go func() {
		out, err := a.Run(context.Background(), "active")
		if err == nil && out != "handled queued event" {
			err = errors.New("unexpected output: " + out)
		}
		done <- err
	}()
	select {
	case <-prov.started:
	case <-time.After(2 * time.Second):
		t.Fatal("provider did not start")
	}
	if err := a.HandleMCPNotification(context.Background(), mcp.Notification{
		ServerName: "local",
		EventType:  "message",
		Content:    "queued",
		Params: map[string]any{
			"content": "queued",
			"meta":    map[string]any{"event_type": "message", "topic": "ops"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.HandleMCPNotification(context.Background(), mcp.Notification{
		ServerName: "local",
		EventType:  "message",
		Content:    "queued",
		Params: map[string]any{
			"content": "queued",
			"meta":    map[string]any{"event_type": "message", "topic": "ops"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	close(prov.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(a.Session.History) != 4 {
		t.Fatalf("history len = %d, want active user, first assistant, queued event, final assistant", len(a.Session.History))
	}
	if a.Session.History[2].Kind != llm.MessageKindMCPEvent {
		t.Fatalf("queued message kind = %q", a.Session.History[2].Kind)
	}
	if got, want := a.Session.History[2].FirstText(), "local:message:{\n  \"content\": \"queued\",\n  \"meta\": {\n    \"event_type\": \"message\",\n    \"topic\": \"ops\"\n  }\n}"; got != want {
		t.Fatalf("queued text = %q", got)
	}
}

func TestApp_MCPNotificationUsesLifecycleContext(t *testing.T) {
	a, prov := newStubApp(t, llm.Response{
		Message:    llm.TextMessage(llm.RoleAssistant, "handled event"),
		StopReason: llm.StopEndTurn,
	})
	a.cancel()

	err := a.HandleMCPNotification(a.ctx, mcp.Notification{
		ServerName: "local",
		Method:     "notifications/claude/channel",
		EventType:  "message",
		Content:    "ignored",
	})
	if err == nil {
		t.Fatal("expected cancelled context error")
	}
	if prov.calls != 0 {
		t.Fatalf("provider calls = %d, want 0", prov.calls)
	}
	if len(a.Session.History) != 0 {
		t.Fatalf("history len = %d, want 0", len(a.Session.History))
	}
}

func TestFormatTokenUsage(t *testing.T) {
	got := FormatTokenUsage(llm.Usage{InputTokens: 12, OutputTokens: 5})
	want := "tokens: 17 total (input 12, output 5)"
	if got != want {
		t.Fatalf("FormatTokenUsage() = %q, want %q", got, want)
	}
	got = FormatTokenUsage(llm.Usage{InputTokens: 1_250, OutputTokens: 2_000_000})
	want = "tokens: 2m total (input 1.3k, output 2m)"
	if got != want {
		t.Fatalf("FormatTokenUsage() = %q, want %q", got, want)
	}
}

func TestApp_REPLProcessesMultipleLines(t *testing.T) {
	a, prov := newStubApp(t,
		llm.Response{
			Message:    llm.TextMessage(llm.RoleAssistant, "one"),
			StopReason: llm.StopEndTurn,
			Usage:      llm.Usage{InputTokens: 1, OutputTokens: 2},
		},
		llm.Response{
			Message:    llm.TextMessage(llm.RoleAssistant, "two"),
			StopReason: llm.StopEndTurn,
			Usage:      llm.Usage{InputTokens: 3, OutputTokens: 4},
		},
	)

	in := strings.NewReader("first\n\nsecond\n") // blank line is ignored
	var out bytes.Buffer
	if err := a.REPL(context.Background(), in, &out); err != nil {
		t.Fatal(err)
	}
	body := out.String()
	if !strings.Contains(body, "one") || !strings.Contains(body, "two") {
		t.Fatalf("repl output = %q", body)
	}
	for _, want := range []string{
		"tokens: 3 total (input 1, output 2)",
		"tokens: 10 total (input 4, output 6)",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("repl output missing %q in:\n%s", want, body)
		}
	}
	if prov.calls != 2 {
		t.Fatalf("expected 2 LLM calls, got %d", prov.calls)
	}
}

func TestApp_REPLContinuesAfterTurnError(t *testing.T) {
	// First call errors (stub exhausted on call 0 if we provide nothing) ->
	// REPL should print "error:" and keep reading. Use a single-reply stub
	// and feed two lines.
	a, _ := newStubApp(t,
		llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn},
	)
	in := strings.NewReader("first\nsecond\n")
	var out bytes.Buffer
	if err := a.REPL(context.Background(), in, &out); err != nil {
		t.Fatal(err)
	}
	body := out.String()
	if !strings.Contains(body, "ok") {
		t.Fatalf("first turn missing: %q", body)
	}
	if !strings.Contains(body, "error:") {
		t.Fatalf("second turn should have errored: %q", body)
	}
}

func TestApp_VerboseEmitsToStderr(t *testing.T) {
	dir := t.TempDir()
	prov := &stubProvider{replies: []llm.Response{
		{
			Message:    llm.TextMessage(llm.RoleAssistant, "ok"),
			StopReason: llm.StopEndTurn,
			Usage:      llm.Usage{InputTokens: 5, OutputTokens: 2},
		},
	}}
	var stderr bytes.Buffer
	a, err := New(Options{
		Config:   config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: dir},
		Provider: prov,
		WorkDir:  dir,
		Verbose:  true,
		Stderr:   &stderr,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	if _, err := a.Run(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	body := stderr.String()
	for _, want := range []string{"› user: x", "[turn 1]", "assistant: ok", "tokens: 7 total", "✓ done in"} {
		if !strings.Contains(body, want) {
			t.Errorf("verbose stderr missing %q in:\n%s", want, body)
		}
	}
}

func TestApp_SessionWritesIntoWorkDirJuex(t *testing.T) {
	dir := t.TempDir()
	prov := &stubProvider{replies: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn},
	}}
	a, err := New(Options{
		Config:   config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: dir},
		Provider: prov,
		WorkDir:  dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	if _, err := a.Run(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	// Session must live under <WorkDir>/.juex/sessions/<id>/.
	sessRoot := filepath.Join(dir, ".juex", "sessions")
	if !strings.HasPrefix(a.Session.Dir, sessRoot) {
		t.Fatalf("session dir %s not under %s", a.Session.Dir, sessRoot)
	}
}

func TestAppPromptLoadsGlobalAgentsBeforeWorkspaceAgents(t *testing.T) {
	work := t.TempDir()
	homeAgents := t.TempDir()
	projectAgents := filepath.Join(work, ".agents")
	if err := os.MkdirAll(projectAgents, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(homeAgents, "AGENTS.md"), []byte("global agent rule"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectAgents, "AGENTS.md"), []byte("workspace agent rule"), 0o644); err != nil {
		t.Fatal(err)
	}

	a, err := New(Options{
		Config: config.Config{
			ProviderID:                "openai",
			APIKey:                    "x",
			Model:                     "m",
			HomeAgentsDir:             homeAgents,
			WorkDir:                   work,
			EnableUserGlobalResources: true,
		},
		Provider: &stubProvider{},
		WorkDir:  work,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	got := a.Engine.Prompt.Build()
	globalPos := strings.Index(got, "global agent rule")
	workspacePos := strings.Index(got, "workspace agent rule")
	if globalPos < 0 || workspacePos < 0 {
		t.Fatalf("prompt missing AGENTS.md content:\n%s", got)
	}
	if globalPos > workspacePos {
		t.Fatalf("global AGENTS.md should load before workspace AGENTS.md:\n%s", got)
	}
}

func TestAppPromptSkipsGlobalAgentsWhenUserGlobalResourcesDisabled(t *testing.T) {
	work := t.TempDir()
	homeAgents := t.TempDir()
	projectAgents := filepath.Join(work, ".agents")
	if err := os.MkdirAll(projectAgents, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(homeAgents, "AGENTS.md"), []byte("global agent rule"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectAgents, "AGENTS.md"), []byte("workspace agent rule"), 0o644); err != nil {
		t.Fatal(err)
	}

	a, err := New(Options{
		Config: config.Config{
			ProviderID:                "openai",
			APIKey:                    "x",
			Model:                     "m",
			HomeAgentsDir:             homeAgents,
			WorkDir:                   work,
			EnableUserGlobalResources: false,
		},
		Provider: &stubProvider{},
		WorkDir:  work,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	got := a.Engine.Prompt.Build()
	if strings.Contains(got, "global agent rule") {
		t.Fatalf("prompt should skip global AGENTS.md when user-global resources are disabled:\n%s", got)
	}
	if !strings.Contains(got, "workspace agent rule") {
		t.Fatalf("prompt should keep workspace AGENTS.md when user-global resources are disabled:\n%s", got)
	}
}

func TestApp_WritesSessionHistoryWithAlias(t *testing.T) {
	dir := t.TempDir()
	prov := &stubProvider{replies: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn},
	}}
	a, err := New(Options{
		Config:   config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: dir},
		Provider: prov,
		WorkDir:  dir,
		Alias:    "daily",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	if _, err := a.Run(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	h, err := session.LoadHistory(filepath.Join(dir, ".juex", "history.json"))
	if err != nil {
		t.Fatal(err)
	}
	if h.Active == nil {
		t.Fatal("history active is nil")
	}
	if h.Active.ID != a.Session.ID || h.Active.Alias != "daily" {
		t.Fatalf("active = %+v, want id %s alias daily", h.Active, a.Session.ID)
	}
	if len(h.Sessions) != 1 || h.Sessions[0].ID != a.Session.ID {
		t.Fatalf("sessions = %+v", h.Sessions)
	}
}

func TestApp_NewWithoutKeyFails(t *testing.T) {
	_, err := New(Options{
		Config:  config.Config{ProviderID: "openai" /* no key */, Model: "m", WorkDir: t.TempDir()},
		WorkDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected error for missing API key")
	}
}

func TestApp_NewSoftFailsOptionalMCPStartup(t *testing.T) {
	dir := t.TempDir()
	mcpPath := filepath.Join(dir, ".agents", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(mcpPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mcpPath, []byte(`{"mcpServers":{"alpha":{"command":""}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	a, err := New(Options{
		Config:   config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: dir},
		Provider: &stubProvider{},
		WorkDir:  dir,
		Stderr:   &stderr,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	status := a.MCPStatus()
	if status.Configured != 1 || status.Connected != 0 || status.Errors != 1 {
		t.Fatalf("mcp status = %+v", status)
	}
	if len(status.Servers) != 1 || status.Servers[0].Status != "error" || !strings.Contains(status.Servers[0].Error, "missing command") {
		t.Fatalf("mcp servers = %+v", status.Servers)
	}
	if !strings.Contains(stderr.String(), `optional MCP server "alpha" is unavailable`) {
		t.Fatalf("stderr missing warning:\n%s", stderr.String())
	}
}

func TestNew_ResumeDirReusesExistingSession(t *testing.T) {
	work := t.TempDir()
	sessionsRoot := filepath.Join(work, ".juex", "sessions")
	id := "20260506T103500-resume001"
	dir := filepath.Join(sessionsRoot, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"role":"user","blocks":[{"type":"text","text":"hi"}]}` + "\n" +
		`{"role":"assistant","blocks":[{"type":"text","text":"hello"}]}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "conversation.jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	a, err := New(Options{
		Config:    config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: work},
		Provider:  &stubProvider{},
		WorkDir:   work,
		ResumeDir: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	if a.Session.ID != id {
		t.Errorf("session id = %s, want %s", a.Session.ID, id)
	}
	if a.Session.Dir != dir {
		t.Errorf("session dir = %s, want %s", a.Session.Dir, dir)
	}
	if len(a.Session.History) != 2 {
		t.Errorf("history len = %d, want 2", len(a.Session.History))
	}
}

func TestApp_NewDefaultsWorkDirToCwd(t *testing.T) {
	// Switch into a fresh tempdir for this test so the default-cwd path
	// does not leak files into the package directory.
	prev, _ := os.Getwd()
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	prov := &stubProvider{replies: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn},
	}}
	a, err := New(Options{
		Config:   config.Config{ProviderID: "openai", APIKey: "x", Model: "m"},
		Provider: prov,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if a.Session == nil {
		t.Fatal("session not built")
	}
	// Resolve both sides to canonical paths before comparing:
	// - macOS rewrites /var/... -> /private/var/...
	// - Windows can return 8.3 short names (RUNNER~1) where the long form
	//   is "runneradmin"; EvalSymlinks normalises to the long form.
	resolveSessionParent := func(p string) string {
		r, err := filepath.EvalSymlinks(p)
		if err != nil {
			return p
		}
		return r
	}
	wantParent := resolveSessionParent(filepath.Join(dir, ".juex", "sessions"))
	gotParent := resolveSessionParent(filepath.Dir(a.Session.Dir))
	if !strings.HasPrefix(gotParent, wantParent) {
		t.Fatalf("session dir %q (resolved parent %q) not under %q",
			a.Session.Dir, gotParent, wantParent)
	}
}
