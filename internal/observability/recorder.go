// Package observability derives human-readable session logs from runtime
// events without changing the canonical conversation and event journals.
package observability

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/toolevents"
)

const (
	logsDir  = "logs"
	juexLog  = "juex.log"
	debugLog = "debug.log"
)

const (
	previewLimit = 512
	maxDepth     = 4
	maxItems     = 12
)

type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

func ParseLevel(raw string) (Level, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "info":
		return LevelInfo, nil
	case "debug":
		return LevelDebug, nil
	case "warn", "warning":
		return LevelWarn, nil
	case "error":
		return LevelError, nil
	default:
		return LevelInfo, fmt.Errorf("observability: invalid log level %q", raw)
	}
}

func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "debug"
	case LevelInfo:
		return "info"
	case LevelWarn:
		return "warn"
	case LevelError:
		return "error"
	default:
		return "info"
	}
}

type Options struct {
	SessionDir string
	Debug      bool
	LogLevel   string
}

type Recorder struct {
	sessionDir string
	level      Level
	debug      bool

	mu    sync.Mutex
	files map[string]*os.File

	closed       bool
	filesEnsured bool
}

func NewRecorder(opts Options) (*Recorder, error) {
	level, err := ParseLevel(opts.LogLevel)
	if err != nil {
		return nil, err
	}
	if opts.Debug && strings.TrimSpace(opts.LogLevel) == "" {
		level = LevelDebug
	}
	return &Recorder{
		sessionDir: opts.SessionDir,
		level:      level,
		debug:      opts.Debug || level == LevelDebug,
		files:      map[string]*os.File{},
	}, nil
}

func (r *Recorder) Record(ev events.Event) error {
	if r == nil || r.sessionDir == "" {
		return nil
	}
	if ev.Transient {
		return nil
	}
	if ev.Type == toolevents.OutputDeltaType && !r.shouldRecord(LevelDebug) {
		return nil
	}
	meta := classify(ev)
	if !r.shouldRecord(meta.Level) {
		return nil
	}
	ts := ev.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}
	ts = ts.UTC()

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	if !r.filesEnsured {
		if err := r.ensureStableFilesLocked(); err != nil {
			return err
		}
		r.filesEnsured = true
	}

	if err := r.writeLogLocked(filepath.Join(logsDir, juexLog), ts, meta.Level, ev.Type, ev.TurnID, meta.Status, meta.Summary); err != nil {
		return err
	}
	if r.debug {
		if err := r.writeLogLocked(filepath.Join(logsDir, debugLog), ts, LevelDebug, ev.Type, ev.TurnID, meta.Status, meta.Summary); err != nil {
			return err
		}
	} else if _, err := r.fileLocked(filepath.Join(logsDir, debugLog)); err != nil {
		return err
	}
	return nil
}

func (r *Recorder) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	var first error
	for _, f := range r.files {
		if err := f.Close(); err != nil && first == nil {
			first = err
		}
	}
	r.files = nil
	return first
}

func (r *Recorder) shouldRecord(level Level) bool {
	return level >= r.level
}

func (r *Recorder) ensureStableFilesLocked() error {
	for _, name := range []string{filepath.Join(logsDir, juexLog), filepath.Join(logsDir, debugLog)} {
		if _, err := r.fileLocked(name); err != nil {
			return err
		}
	}
	return nil
}

func (r *Recorder) writeLogLocked(name string, ts time.Time, level Level, event, turnID, status string, summary map[string]any) error {
	f, err := r.fileLocked(name)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(summary)
	_, err = fmt.Fprintf(f, "%s %-5s event=%s status=%s turn_id=%s summary=%s\n", ts.Format(time.RFC3339Nano), level.String(), event, status, turnID, string(body))
	return err
}

func (r *Recorder) fileLocked(name string) (*os.File, error) {
	if f := r.files[name]; f != nil {
		return f, nil
	}
	path := filepath.Join(r.sessionDir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	r.files[name] = f
	return f, nil
}

type eventMeta struct {
	Level   Level
	Status  string
	Summary map[string]any
}

func classify(ev events.Event) eventMeta {
	payload := payloadMap(ev.Payload)
	meta := eventMeta{
		Level:   LevelInfo,
		Status:  "ok",
		Summary: summaryFor(ev.Type, payload),
	}
	if strings.Contains(ev.Type, "errored") || ev.Type == "turn.errored" {
		meta.Level = LevelError
		meta.Status = "error"
	}
	if ev.Type == "tool.failure.recorded" {
		meta.Level = LevelWarn
		meta.Status = "unresolved"
	}
	if ev.Type == "tool.failure.continued" {
		meta.Level = LevelWarn
		meta.Status = "continued"
	}
	if ev.Type == "tool.failure.resolved" || ev.Type == "tool.failure.stale" {
		meta.Status = stringValue(payload["status"])
	}
	if ev.Type == "context.compact.errored" {
		meta.Level = LevelWarn
	}
	if ev.Type == toolevents.OutputDeltaType {
		meta.Level = LevelDebug
	}
	if ev.Type == "llm.retry" {
		meta.Level = LevelWarn
		if boolValue(payload["exhausted"]) {
			meta.Status = "exhausted"
		} else if boolValue(payload["will_retry"]) {
			meta.Status = "retrying"
		}
	}
	return meta
}

func payloadMap(payload any) map[string]any {
	if payload == nil {
		return nil
	}
	if m, ok := payload.(map[string]any); ok {
		return m
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return map[string]any{"value": fmt.Sprint(payload)}
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return map[string]any{"value": truncate(fmt.Sprint(payload), previewLimit)}
	}
	return out
}

func summaryFor(event string, p map[string]any) map[string]any {
	out := map[string]any{}
	add := func(key string) {
		if value, ok := p[key]; ok {
			out[key] = sanitize(key, value, 0)
		}
	}
	switch event {
	case "turn.started":
		add("input")
		add("kind")
	case "turn.completed":
		add("output_len")
		add("duration_ms")
		add("token_usage")
	case "turn.errored":
		add("error")
		add("error_kind")
		add("timed_out")
		add("raw_cause")
		add("signal")
		add("signal_number")
		add("interrupted")
	case "llm.requested":
		add("iter")
		add("history_len")
		add("tool_count")
	case "llm.responded":
		add("stop_reason")
		add("model")
		add("usage")
		add("token_usage")
		add("text")
	case "llm.retry":
		add("purpose")
		add("iter")
		add("provider")
		add("model")
		add("protocol")
		add("transport")
		add("operation")
		add("attempt")
		add("max_attempts")
		add("delay_ms")
		add("retry_reason")
		add("raw_error")
		add("will_retry")
		add("exhausted")
	case toolevents.RequestedType:
		add("name")
		add("tool_use_id")
		add("timeout_seconds")
		if input, ok := p["input"]; ok {
			out["input"] = sanitize("input", input, 0)
		}
	case toolevents.CompletedType:
		add("name")
		add("tool_use_id")
		add("len")
		add("preview")
		add("result")
	case toolevents.ErroredType:
		add("name")
		add("tool_use_id")
		add("error")
		add("error_kind")
		add("raw_cause")
		add("timeout_seconds")
		add("timed_out")
		add("preview")
		add("exit_code")
		add("result")
	case toolevents.OutputDeltaType:
		add("name")
		add("tool_use_id")
		add("session_id")
		add("chunk_id")
		add("stream")
		add("text")
	case "tool.failure.recorded":
		add("name")
		add("tool_use_id")
		add("fingerprint")
		add("classification")
		add("status")
		add("blocking")
		add("occurrences")
		add("exit_code")
		add("error")
		add("output_preview")
		add("related_paths")
	case "tool.failure.resolved", "tool.failure.stale":
		add("name")
		add("tool_use_id")
		add("fingerprint")
		add("status")
		add("reason")
		add("resolver_name")
		add("resolver_tool_use_id")
		add("related_paths")
	case "tool.failure.continued":
		add("failure_count")
		add("fingerprints")
		add("repeated")
		add("continuation_prompt_len")
	case "context.compact.started", "context.compact.completed", "context.compact.errored", "context.compact.skipped":
		add("reason")
		add("auto")
		add("error")
		add("tokens_before")
		add("tokens_after")
	case "hook.started", "hook.completed", "hook.errored":
		add("name")
		add("source")
		add("event_name")
		add("tool_name")
		add("duration_ms")
		add("error")
		add("stdout_preview")
		add("stderr_preview")
	case "finish.attempted":
		add("stop_reason")
		add("output_len")
	default:
		for key, value := range p {
			out[key] = sanitize(key, value, 0)
		}
	}
	return out
}

func sanitize(key string, value any, depth int) any {
	if isSecretKey(key) {
		return "[REDACTED]"
	}
	if depth >= maxDepth {
		return "[TRUNCATED]"
	}
	switch v := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := map[string]any{}
		for i, k := range keys {
			if i >= maxItems {
				out["_truncated"] = len(keys) - maxItems
				break
			}
			out[k] = sanitize(k, v[k], depth+1)
		}
		return out
	case []any:
		limit := len(v)
		if limit > maxItems {
			limit = maxItems
		}
		out := make([]any, 0, limit)
		for i := 0; i < limit; i++ {
			out = append(out, sanitize("", v[i], depth+1))
		}
		if len(v) > limit {
			out = append(out, map[string]any{"_truncated": len(v) - limit})
		}
		return out
	case string:
		return truncate(v, previewLimit)
	default:
		return v
	}
}

func isSecretKey(key string) bool {
	key = strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	if strings.Contains(key, "token_usage") || strings.HasSuffix(key, "_tokens") || strings.Contains(key, "tokens_") {
		return false
	}
	for _, marker := range []string{"api_key", "secret", "password", "authorization", "cookie"} {
		if strings.Contains(key, marker) {
			return true
		}
	}
	return key == "token" || strings.HasSuffix(key, "_token") || strings.HasPrefix(key, "token_") || strings.Contains(key, "_token_")
}

func boolValue(v any) bool {
	switch b := v.(type) {
	case bool:
		return b
	default:
		return false
	}
}

func stringValue(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case fmt.Stringer:
		return s.String()
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
