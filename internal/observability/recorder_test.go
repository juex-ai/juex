package observability

import (
	"encoding/json"
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

func TestRecorderCreatesStableFilesAndFiltersDebug(t *testing.T) {
	dir := t.TempDir()
	rec, err := NewRecorder(Options{SessionID: "s1", SessionDir: dir, LogLevel: "info"})
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

	for _, rel := range []string{"trace.jsonl", "spans.jsonl", "tools.jsonl", "logs/juex.log", "logs/debug.log"} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Fatalf("%s missing: %v", rel, err)
		}
	}
	trace := readJSONLines[TraceRecord](t, filepath.Join(dir, "trace.jsonl"))
	if len(trace) != 1 || trace[0].Event != "turn.started" {
		t.Fatalf("trace = %+v, want only info-level event", trace)
	}
	debugData, err := os.ReadFile(filepath.Join(dir, "logs", "debug.log"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(debugData), "tool.output_delta") {
		t.Fatalf("debug log should be empty when debug disabled: %s", debugData)
	}
}

func TestRecorderDebugRecordsDebugEvents(t *testing.T) {
	dir := t.TempDir()
	rec, err := NewRecorder(Options{SessionID: "s1", SessionDir: dir, Debug: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := rec.Record(event(toolevents.OutputDeltaType, "t1", map[string]any{"name": "exec_command", "text": "chunk"})); err != nil {
		t.Fatal(err)
	}
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}
	trace := readJSONLines[TraceRecord](t, filepath.Join(dir, "trace.jsonl"))
	if len(trace) != 1 || trace[0].Event != toolevents.OutputDeltaType || trace[0].Level != "debug" {
		t.Fatalf("trace = %+v", trace)
	}
	debugData, err := os.ReadFile(filepath.Join(dir, "logs", "debug.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(debugData), "tool.output_delta") {
		t.Fatalf("debug log missing event: %s", debugData)
	}
}

func TestRecorderRecordsLLMRetryDiagnostics(t *testing.T) {
	dir := t.TempDir()
	rec, err := NewRecorder(Options{SessionID: "s1", SessionDir: dir, Debug: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := rec.Record(event("llm.retry", "t1", llm.ProviderRetryDiagnostic{
		Provider:    "openai-codex",
		Model:       "gpt-5.5",
		Protocol:    llm.ProtocolOpenAICodexResponses,
		Transport:   llm.CodexTransportSSE,
		Operation:   "responses.sse",
		Attempt:     1,
		MaxAttempts: 11,
		DelayMS:     100,
		RetryReason: "codex_sse_read",
		RawError:    "codex SSE read: stream error",
		WillRetry:   true,
	})); err != nil {
		t.Fatal(err)
	}
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}
	trace := readJSONLines[TraceRecord](t, filepath.Join(dir, "trace.jsonl"))
	if len(trace) != 1 || trace[0].Event != "llm.retry" || trace[0].Level != "warn" || trace[0].Status != "retrying" {
		t.Fatalf("trace = %+v", trace)
	}
	if trace[0].Summary["provider"] != "openai-codex" || trace[0].Summary["attempt"] != float64(1) || trace[0].Summary["raw_error"] == "" {
		t.Fatalf("retry summary = %+v", trace[0].Summary)
	}
	debugData, err := os.ReadFile(filepath.Join(dir, "logs", "debug.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(debugData), "llm.retry") || !strings.Contains(string(debugData), "codex_sse_read") {
		t.Fatalf("debug log missing retry diagnostic: %s", debugData)
	}
}

func TestRecorderCloseIsIdempotentAndPreventsReopen(t *testing.T) {
	dir := t.TempDir()
	rec, err := NewRecorder(Options{SessionID: "s1", SessionDir: dir, Debug: true})
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

	trace := readJSONLines[TraceRecord](t, filepath.Join(dir, "trace.jsonl"))
	if len(trace) != 1 || trace[0].Event != "turn.started" {
		t.Fatalf("trace after close = %+v, want no write-after-close", trace)
	}
}

func TestRecorderWritesSpanSchemaAndParents(t *testing.T) {
	dir := t.TempDir()
	rec, err := NewRecorder(Options{SessionID: "s1", SessionDir: dir, Debug: true})
	if err != nil {
		t.Fatal(err)
	}
	events := []events.Event{
		event("turn.started", "turn-a", map[string]any{"input": "hi"}),
		event("llm.requested", "turn-a", map[string]any{"iter": 0, "history_len": 1, "tool_count": 1}),
		event("llm.responded", "turn-a", map[string]any{"stop_reason": "tool_use", "duration_ms": 7}),
		event(toolevents.RequestedType, "turn-a", map[string]any{"name": "read", "tool_use_id": "tu1", "input": map[string]any{"path": "README.md"}}),
		event(toolevents.CompletedType, "turn-a", map[string]any{"name": "read", "tool_use_id": "tu1", "len": 42, "preview": "ok"}),
		event("context.compact.started", "turn-a", map[string]any{"reason": "manual", "auto": false}),
		event("context.compact.completed", "turn-a", map[string]any{"reason": "manual", "auto": false, "tokens_before": 100, "tokens_after": 20}),
		event("hook.started", "turn-a", map[string]any{"name": "stop-check", "event_name": "Stop"}),
		event("hook.completed", "turn-a", map[string]any{"name": "stop-check", "event_name": "Stop", "duration_ms": 3}),
		event("finish.attempted", "turn-a", map[string]any{"stop_reason": "end_turn", "output_len": 2}),
	}
	for _, ev := range events {
		if err := rec.Record(ev); err != nil {
			t.Fatal(err)
		}
	}
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}

	trace := readJSONLines[TraceRecord](t, filepath.Join(dir, "trace.jsonl"))
	if trace[0].SessionID != "s1" || trace[0].TurnID != "turn-a" || trace[0].SpanID != "turn:turn-a" {
		t.Fatalf("trace schema = %+v", trace[0])
	}
	spans := readJSONLines[SpanRecord](t, filepath.Join(dir, "spans.jsonl"))
	seen := map[string]bool{}
	for _, span := range spans {
		seen[span.Name+":"+span.Event] = true
		if span.Name == "tool" && span.Event == "end" {
			if span.ParentID != "turn:turn-a" || span.Status != "ok" || span.DurationMS < 0 {
				t.Fatalf("tool span = %+v", span)
			}
		}
	}
	for _, want := range []string{"tool:end", "compaction:end", "hook:end", "finish:instant"} {
		if !seen[want] {
			t.Fatalf("spans missing %s: %+v", want, spans)
		}
	}
}

func TestRecorderRedactsSecretsAndClassifiesErrors(t *testing.T) {
	dir := t.TempDir()
	rec, err := NewRecorder(Options{SessionID: "s1", SessionDir: dir, Debug: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := rec.Record(event(toolevents.RequestedType, "t1", map[string]any{
		"name":        "exec_command",
		"tool_use_id": "tu1",
		"input": map[string]any{
			"cmd":        "echo",
			"api_key":    "sk-secret",
			"auth_token": "credential-token",
			"nested":     map[string]any{"password": "secret-password"},
			"long_arg":   strings.Repeat("x", previewLimit+50),
		},
	})); err != nil {
		t.Fatal(err)
	}
	if err := rec.Record(event(toolevents.ErroredType, "t1", map[string]any{
		"name":        "exec_command",
		"tool_use_id": "tu1",
		"error":       "permission denied",
		"exit_code":   126,
	})); err != nil {
		t.Fatal(err)
	}
	if err := rec.Record(event("turn.completed", "t1", map[string]any{
		"duration_ms": 12,
		"output_len":  2,
		"token_usage": map[string]any{"input_tokens": 3, "output_tokens": 1},
	})); err != nil {
		t.Fatal(err)
	}
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}

	tools := readJSONLines[ToolRecord](t, filepath.Join(dir, "tools.jsonl"))
	body := mustMarshal(t, tools)
	if strings.Contains(body, "sk-secret") || strings.Contains(body, "secret-password") || strings.Contains(body, "credential-token") {
		t.Fatalf("secrets were not redacted: %s", body)
	}
	if !strings.Contains(body, "[REDACTED]") {
		t.Fatalf("redaction marker missing: %s", body)
	}
	if tools[len(tools)-1].ErrorKind != "permission" {
		t.Fatalf("error kind = %q", tools[len(tools)-1].ErrorKind)
	}
	if tools[len(tools)-1].Summary["exit_code"] != float64(126) {
		t.Fatalf("tool summary exit_code = %#v", tools[len(tools)-1].Summary)
	}
	traceBody := mustMarshal(t, readJSONLines[TraceRecord](t, filepath.Join(dir, "trace.jsonl")))
	if strings.Contains(traceBody, `"token_usage":"[REDACTED]"`) || !strings.Contains(traceBody, "input_tokens") {
		t.Fatalf("token usage counters should not be redacted: %s", traceBody)
	}
}

func TestRecorderPreservesTimeoutRawCause(t *testing.T) {
	dir := t.TempDir()
	rec, err := NewRecorder(Options{SessionID: "s1", SessionDir: dir, Debug: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := rec.Record(event("turn.errored", "t1", map[string]any{
		"error":      "openai codex responses: codex SSE read timed out",
		"error_kind": "timeout",
		"timed_out":  true,
		"raw_cause":  "openai codex responses: codex SSE read: context deadline exceeded",
	})); err != nil {
		t.Fatal(err)
	}
	if err := rec.Record(event(toolevents.ErroredType, "t1", map[string]any{
		"name":            "exec_command",
		"tool_use_id":     "tu1",
		"error":           "tools: exec_command timed out after 1s",
		"error_kind":      "timeout",
		"timed_out":       true,
		"timeout_seconds": 1,
		"raw_cause":       "context deadline exceeded",
	})); err != nil {
		t.Fatal(err)
	}
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}

	trace := readJSONLines[TraceRecord](t, filepath.Join(dir, "trace.jsonl"))
	if len(trace) != 2 {
		t.Fatalf("trace len = %d, want 2", len(trace))
	}
	if trace[0].ErrorKind != "timeout" || trace[0].Summary["raw_cause"] == "" {
		t.Fatalf("turn trace = %+v, want timeout raw cause", trace[0])
	}
	tools := readJSONLines[ToolRecord](t, filepath.Join(dir, "tools.jsonl"))
	if len(tools) != 1 {
		t.Fatalf("tools len = %d, want 1", len(tools))
	}
	if tools[0].ErrorKind != "timeout" || tools[0].Summary["raw_cause"] != "context deadline exceeded" {
		t.Fatalf("tool record = %+v, want timeout raw cause", tools[0])
	}
	if tools[0].Summary["timeout_seconds"] != float64(1) {
		t.Fatalf("tool summary = %+v, want timeout_seconds", tools[0].Summary)
	}
}

func TestRecorderPreservesSignalMetadata(t *testing.T) {
	dir := t.TempDir()
	rec, err := NewRecorder(Options{SessionID: "s1", SessionDir: dir, Debug: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := rec.Record(event("turn.errored", "t1", map[string]any{
		"error":         "run terminated by signal SIGTERM (15)",
		"error_kind":    "terminated",
		"signal":        "SIGTERM",
		"signal_number": 15,
		"interrupted":   true,
	})); err != nil {
		t.Fatal(err)
	}
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}

	trace := readJSONLines[TraceRecord](t, filepath.Join(dir, "trace.jsonl"))
	if len(trace) != 1 {
		t.Fatalf("trace len = %d, want 1", len(trace))
	}
	if trace[0].ErrorKind != "terminated" {
		t.Fatalf("error kind = %q, want terminated", trace[0].ErrorKind)
	}
	if trace[0].Summary["signal"] != "SIGTERM" || trace[0].Summary["signal_number"] != float64(15) || trace[0].Summary["interrupted"] != true {
		t.Fatalf("trace summary = %+v, want signal metadata", trace[0].Summary)
	}
}

func TestRecorderCapturesToolFailureLedgerEvents(t *testing.T) {
	dir := t.TempDir()
	rec, err := NewRecorder(Options{SessionID: "s1", SessionDir: dir, Debug: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := rec.Record(event("tool.failure.recorded", "t1", map[string]any{
		"name":           "exec_command",
		"tool_use_id":    "tu1",
		"fingerprint":    "abc123",
		"classification": "recoverable",
		"status":         "unresolved",
		"blocking":       true,
		"occurrences":    1,
		"exit_code":      1,
		"error":          "process exited with code 1",
		"output_preview": "FAIL",
		"related_paths":  []string{"go.mod"},
	})); err != nil {
		t.Fatal(err)
	}
	if err := rec.Record(event("tool.failure.continued", "t1", map[string]any{
		"failure_count":           1,
		"fingerprints":            []string{"abc123"},
		"continuation_prompt_len": 120,
	})); err != nil {
		t.Fatal(err)
	}
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}

	trace := readJSONLines[TraceRecord](t, filepath.Join(dir, "trace.jsonl"))
	if len(trace) != 2 || trace[0].Event != "tool.failure.recorded" || trace[0].Level != "warn" || trace[0].Status != "unresolved" {
		t.Fatalf("trace = %+v", trace)
	}
	tools := readJSONLines[ToolRecord](t, filepath.Join(dir, "tools.jsonl"))
	body := mustMarshal(t, tools)
	for _, want := range []string{"tool.failure.recorded", "abc123", "recoverable", "continuation_prompt_len"} {
		if !strings.Contains(body, want) {
			t.Fatalf("tools record missing %q:\n%s", want, body)
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

func readJSONLines[T any](t *testing.T, path string) []T {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var out []T
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var v T
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Fatalf("parse %s: %v\n%s", path, err, line)
		}
		out = append(out, v)
	}
	return out
}

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
