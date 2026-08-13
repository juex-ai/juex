package observability

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/toolevents"
)

func TestParseLevel(t *testing.T) {
	cases := map[string]Level{
		"":        LevelInfo,
		"debug":   LevelDebug,
		"info":    LevelInfo,
		"warn":    LevelWarn,
		"warning": LevelWarn,
		"error":   LevelError,
	}
	for raw, want := range cases {
		got, err := ParseLevel(raw)
		if err != nil {
			t.Fatalf("ParseLevel(%q): %v", raw, err)
		}
		if got != want {
			t.Fatalf("ParseLevel(%q) = %v, want %v", raw, got, want)
		}
	}
	if _, err := ParseLevel("chatty"); err == nil {
		t.Fatal("expected invalid level error")
	}
}

func TestRecorderCreatesOnlyTextLogsAndFiltersDebug(t *testing.T) {
	dir := t.TempDir()
	rec, err := NewRecorder(Options{SessionDir: dir, LogLevel: "info"})
	if err != nil {
		t.Fatal(err)
	}
	if err := rec.Record(event(toolevents.OutputDeltaType, "t1", map[string]any{"name": "exec_command", "text": "chunk"})); err != nil {
		t.Fatal(err)
	}
	if err := rec.Record(event("turn.started", "t1", map[string]any{"input": "hello"})); err != nil {
		t.Fatal(err)
	}
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}

	assertNoJSONL(t, dir)
	juexData := readLog(t, dir, juexLog)
	if !strings.Contains(juexData, "event=turn.started") || strings.Contains(juexData, toolevents.OutputDeltaType) {
		t.Fatalf("juex log should contain only the info event:\n%s", juexData)
	}
	if debugData := readLog(t, dir, debugLog); debugData != "" {
		t.Fatalf("debug log should be empty when debug is disabled:\n%s", debugData)
	}
}

func TestRecorderSkipsTransientEventsEvenInDebug(t *testing.T) {
	dir := t.TempDir()
	rec, err := NewRecorder(Options{SessionDir: dir, Debug: true})
	if err != nil {
		t.Fatal(err)
	}
	delta := event(toolevents.OutputDeltaType, "t1", map[string]any{"name": "exec_command", "text": "chunk"})
	delta.Transient = true
	if err := rec.Record(delta); err != nil {
		t.Fatal(err)
	}
	if err := rec.Record(event("turn.started", "t1", map[string]any{"input": "hello"})); err != nil {
		t.Fatal(err)
	}
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}

	debugData := readLog(t, dir, debugLog)
	if strings.Contains(debugData, toolevents.OutputDeltaType) || strings.Contains(debugData, "chunk") {
		t.Fatalf("debug log persisted transient event:\n%s", debugData)
	}
}

func TestRecorderRecordsLLMRetryDiagnostics(t *testing.T) {
	dir := t.TempDir()
	rec, err := NewRecorder(Options{SessionDir: dir, Debug: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := rec.Record(event("llm.retry", "t1", map[string]any{
		"purpose":      "turn",
		"iter":         0,
		"provider":     "openai-codex",
		"model":        "gpt-5.5",
		"protocol":     llm.ProtocolOpenAICodexResponses,
		"transport":    llm.CodexTransportSSE,
		"operation":    "responses.sse",
		"attempt":      1,
		"max_attempts": 11,
		"delay_ms":     100,
		"retry_reason": "codex_sse_read",
		"raw_error":    "codex SSE read: stream error",
		"will_retry":   true,
	})); err != nil {
		t.Fatal(err)
	}
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}

	if body := readLog(t, dir, juexLog); !strings.Contains(body, "warn") {
		t.Fatalf("juex log missing warn level:\n%s", body)
	}
	for _, name := range []string{juexLog, debugLog} {
		body := readLog(t, dir, name)
		for _, want := range []string{"event=llm.retry", "status=retrying", "codex_sse_read", "openai-codex"} {
			if !strings.Contains(body, want) {
				t.Fatalf("%s missing %q:\n%s", name, want, body)
			}
		}
	}
}

func TestRecorderCloseIsIdempotentAndPreventsReopen(t *testing.T) {
	dir := t.TempDir()
	rec, err := NewRecorder(Options{SessionDir: dir, Debug: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := rec.Record(event("turn.started", "t1", map[string]any{"input": "hello"})); err != nil {
		t.Fatal(err)
	}
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}
	if err := rec.Record(event("turn.completed", "t1", map[string]any{"output_len": 2})); err != nil {
		t.Fatal(err)
	}

	body := readLog(t, dir, juexLog)
	if strings.Count(body, "event=") != 1 || !strings.Contains(body, "event=turn.started") {
		t.Fatalf("unexpected write after close:\n%s", body)
	}
}

func TestRecorderRedactsSecretsAndKeepsDiagnosticFields(t *testing.T) {
	dir := t.TempDir()
	rec, err := NewRecorder(Options{SessionDir: dir, Debug: true})
	if err != nil {
		t.Fatal(err)
	}
	events := []events.Event{
		event(toolevents.RequestedType, "t1", map[string]any{
			"name":        "exec_command",
			"tool_use_id": "tu1",
			"input": map[string]any{
				"cmd":        "echo",
				"api_key":    "sk-secret",
				"auth_token": "credential-token",
				"nested":     map[string]any{"password": "secret-password"},
			},
		}),
		event(toolevents.ErroredType, "t1", map[string]any{
			"name":            "exec_command",
			"tool_use_id":     "tu1",
			"error":           "tools: exec_command timed out after 1s",
			"error_kind":      "timeout",
			"timed_out":       true,
			"timeout_seconds": 1,
			"raw_cause":       "context deadline exceeded",
		}),
		event("turn.completed", "t1", map[string]any{
			"token_usage": map[string]any{"input_tokens": 3, "output_tokens": 1},
		}),
	}
	for _, ev := range events {
		if err := rec.Record(ev); err != nil {
			t.Fatal(err)
		}
	}
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}

	body := readLog(t, dir, debugLog)
	for _, leaked := range []string{"sk-secret", "credential-token", "secret-password"} {
		if strings.Contains(body, leaked) {
			t.Fatalf("log leaked %q:\n%s", leaked, body)
		}
	}
	for _, want := range []string{"[REDACTED]", `"error_kind":"timeout"`, `"raw_cause":"context deadline exceeded"`, `"timeout_seconds":1`, "input_tokens"} {
		if !strings.Contains(body, want) {
			t.Fatalf("log missing %q:\n%s", want, body)
		}
	}
}

func TestRecorderPreservesSignalAndFailureLedgerMetadata(t *testing.T) {
	dir := t.TempDir()
	rec, err := NewRecorder(Options{SessionDir: dir, Debug: true})
	if err != nil {
		t.Fatal(err)
	}
	events := []events.Event{
		event("turn.errored", "t1", map[string]any{
			"error":         "run terminated by signal SIGTERM (15)",
			"error_kind":    "terminated",
			"signal":        "SIGTERM",
			"signal_number": 15,
			"interrupted":   true,
		}),
		event("tool.failure.recorded", "t1", map[string]any{
			"name":           "exec_command",
			"fingerprint":    "abc123",
			"classification": "recoverable",
			"status":         "unresolved",
			"blocking":       true,
		}),
		event("tool.failure.continued", "t1", map[string]any{
			"failure_count":           1,
			"fingerprints":            []string{"abc123"},
			"continuation_prompt_len": 120,
		}),
	}
	for _, ev := range events {
		if err := rec.Record(ev); err != nil {
			t.Fatal(err)
		}
	}
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}

	body := readLog(t, dir, debugLog)
	for _, want := range []string{"SIGTERM", `"signal_number":15`, `"interrupted":true`, "abc123", "recoverable", "continuation_prompt_len"} {
		if !strings.Contains(body, want) {
			t.Fatalf("log missing %q:\n%s", want, body)
		}
	}
}

func event(typ, turnID string, payload any) events.Event {
	return events.Event{
		Type:      typ,
		TurnID:    turnID,
		Timestamp: time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC),
		Payload:   payload,
	}
}

func readLog(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, logsDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func assertNoJSONL(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jsonl") {
			t.Fatalf("recorder should not create JSONL files; found %s", entry.Name())
		}
	}
}
