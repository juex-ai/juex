// Package observability derives session-local logs, traces, spans, and tool
// summaries from runtime events without changing the compatibility transcript.
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
)

const (
	traceFile = "trace.jsonl"
	spansFile = "spans.jsonl"
	toolsFile = "tools.jsonl"

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
	SessionID  string
	SessionDir string
	Debug      bool
	LogLevel   string
}

type Recorder struct {
	sessionID  string
	sessionDir string
	level      Level
	debug      bool

	mu         sync.Mutex
	files      map[string]*os.File
	spanStarts map[string]time.Time
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
		sessionID:  opts.SessionID,
		sessionDir: opts.SessionDir,
		level:      level,
		debug:      opts.Debug || level == LevelDebug,
		files:      map[string]*os.File{},
		spanStarts: map[string]time.Time{},
	}, nil
}

func (r *Recorder) Record(ev events.Event) error {
	if r == nil || r.sessionDir == "" {
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
	if err := r.ensureStableFilesLocked(); err != nil {
		return err
	}

	trace := TraceRecord{
		TS:           ts,
		SessionID:    r.sessionID,
		TurnID:       ev.TurnID,
		Event:        ev.Type,
		Level:        meta.Level.String(),
		Status:       meta.Status,
		SpanID:       meta.SpanID,
		ParentID:     meta.ParentID,
		DurationMS:   meta.DurationMS,
		ErrorKind:    meta.ErrorKind,
		Summary:      meta.Summary,
		ArtifactPath: meta.ArtifactPath,
	}
	if err := r.writeJSONLocked(traceFile, trace); err != nil {
		return err
	}
	if span, ok := r.spanRecordLocked(ts, ev, meta); ok {
		if err := r.writeJSONLocked(spansFile, span); err != nil {
			return err
		}
	}
	if meta.Tool != nil {
		meta.Tool.SessionID = r.sessionID
		if err := r.writeJSONLocked(toolsFile, *meta.Tool); err != nil {
			return err
		}
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
	var first error
	for _, name := range sortedFileNames(r.files) {
		if err := r.files[name].Close(); err != nil && first == nil {
			first = err
		}
	}
	r.files = map[string]*os.File{}
	return first
}

func (r *Recorder) shouldRecord(level Level) bool {
	return level >= r.level
}

func (r *Recorder) ensureStableFilesLocked() error {
	for _, name := range []string{traceFile, spansFile, toolsFile, filepath.Join(logsDir, juexLog), filepath.Join(logsDir, debugLog)} {
		if _, err := r.fileLocked(name); err != nil {
			return err
		}
	}
	return nil
}

func (r *Recorder) writeJSONLocked(name string, v any) error {
	f, err := r.fileLocked(name)
	if err != nil {
		return err
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = f.Write(data)
	return err
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

type TraceRecord struct {
	TS           time.Time      `json:"ts"`
	SessionID    string         `json:"session_id"`
	TurnID       string         `json:"turn_id,omitempty"`
	Event        string         `json:"event"`
	Level        string         `json:"level"`
	Status       string         `json:"status"`
	SpanID       string         `json:"span_id,omitempty"`
	ParentID     string         `json:"parent_id,omitempty"`
	DurationMS   int64          `json:"duration_ms,omitempty"`
	ErrorKind    string         `json:"error_kind,omitempty"`
	Summary      map[string]any `json:"summary,omitempty"`
	ArtifactPath string         `json:"artifact_path,omitempty"`
}

type SpanRecord struct {
	TS         time.Time      `json:"ts"`
	SessionID  string         `json:"session_id"`
	TurnID     string         `json:"turn_id,omitempty"`
	SpanID     string         `json:"span_id"`
	ParentID   string         `json:"parent_id,omitempty"`
	Name       string         `json:"name"`
	Event      string         `json:"event"`
	Status     string         `json:"status"`
	StartTS    time.Time      `json:"start_ts"`
	EndTS      time.Time      `json:"end_ts,omitempty"`
	DurationMS int64          `json:"duration_ms"`
	ErrorKind  string         `json:"error_kind,omitempty"`
	Summary    map[string]any `json:"summary,omitempty"`
}

type ToolRecord struct {
	TS         time.Time      `json:"ts"`
	SessionID  string         `json:"session_id"`
	TurnID     string         `json:"turn_id,omitempty"`
	Event      string         `json:"event"`
	Status     string         `json:"status"`
	ToolName   string         `json:"tool_name,omitempty"`
	ToolUseID  string         `json:"tool_use_id,omitempty"`
	SessionRef string         `json:"session_ref,omitempty"`
	ChunkID    int            `json:"chunk_id,omitempty"`
	Stream     string         `json:"stream,omitempty"`
	Input      any            `json:"input,omitempty"`
	Preview    string         `json:"preview,omitempty"`
	Error      string         `json:"error,omitempty"`
	ErrorKind  string         `json:"error_kind,omitempty"`
	Summary    map[string]any `json:"summary,omitempty"`
}

type eventMeta struct {
	Level        Level
	Status       string
	SpanID       string
	ParentID     string
	SpanName     string
	SpanEvent    string
	DurationMS   int64
	ErrorKind    string
	Summary      map[string]any
	ArtifactPath string
	Tool         *ToolRecord
}

func classify(ev events.Event) eventMeta {
	payload := payloadMap(ev.Payload)
	meta := eventMeta{
		Level:     LevelInfo,
		Status:    "ok",
		SpanID:    spanID(ev.Type, ev.TurnID, payload),
		ParentID:  parentID(ev.Type, ev.TurnID),
		SpanName:  spanName(ev.Type),
		SpanEvent: spanEvent(ev.Type),
		Summary:   summaryFor(ev.Type, payload),
	}
	meta.DurationMS = int64Value(payload["duration_ms"])
	if strings.Contains(ev.Type, "errored") || ev.Type == "turn.errored" {
		meta.Level = LevelError
		meta.Status = "error"
		meta.ErrorKind = classifyErrorKind(stringValue(payload["error"]))
	}
	if ev.Type == "context.compact.errored" {
		meta.Level = LevelWarn
	}
	if ev.Type == "tool.output_delta" {
		meta.Level = LevelDebug
	}
	if strings.HasPrefix(ev.Type, "tool.") {
		meta.ArtifactPath = toolsFile
		meta.Tool = toolRecord(ev, payload, meta)
	}
	return meta
}

func (r *Recorder) spanRecordLocked(ts time.Time, ev events.Event, meta eventMeta) (SpanRecord, bool) {
	if meta.SpanID == "" || meta.SpanEvent == "" {
		return SpanRecord{}, false
	}
	start := ts
	if meta.SpanEvent == "start" {
		r.spanStarts[meta.SpanID] = ts
	} else if existing, ok := r.spanStarts[meta.SpanID]; ok {
		start = existing
		delete(r.spanStarts, meta.SpanID)
	}
	end := time.Time{}
	if meta.SpanEvent != "start" {
		end = ts
	}
	duration := meta.DurationMS
	if duration == 0 && !end.IsZero() {
		duration = end.Sub(start).Milliseconds()
	}
	return SpanRecord{
		TS:         ts,
		SessionID:  r.sessionID,
		TurnID:     ev.TurnID,
		SpanID:     meta.SpanID,
		ParentID:   meta.ParentID,
		Name:       meta.SpanName,
		Event:      meta.SpanEvent,
		Status:     meta.Status,
		StartTS:    start,
		EndTS:      end,
		DurationMS: duration,
		ErrorKind:  meta.ErrorKind,
		Summary:    meta.Summary,
	}, true
}

func payloadMap(payload any) map[string]any {
	if payload == nil {
		return nil
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
	case "tool.requested":
		add("name")
		add("tool_use_id")
		add("timeout_seconds")
		if input, ok := p["input"]; ok {
			out["input"] = sanitize("input", input, 0)
		}
	case "tool.completed":
		add("name")
		add("tool_use_id")
		add("len")
		add("preview")
	case "tool.errored":
		add("name")
		add("tool_use_id")
		add("error")
		add("timed_out")
		add("preview")
	case "tool.output_delta":
		add("name")
		add("tool_use_id")
		add("session_id")
		add("chunk_id")
		add("stream")
		add("text")
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

func toolRecord(ev events.Event, p map[string]any, meta eventMeta) *ToolRecord {
	record := &ToolRecord{
		Event:     ev.Type,
		Status:    meta.Status,
		ToolName:  stringValue(p["name"]),
		ToolUseID: stringValue(p["tool_use_id"]),
		Error:     truncate(stringValue(p["error"]), previewLimit),
		ErrorKind: meta.ErrorKind,
		Summary:   meta.Summary,
	}
	if ev.Timestamp.IsZero() {
		record.TS = time.Now().UTC()
	} else {
		record.TS = ev.Timestamp.UTC()
	}
	record.TurnID = ev.TurnID
	if input, ok := p["input"]; ok {
		record.Input = sanitize("input", input, 0)
	}
	if preview := stringValue(p["preview"]); preview != "" {
		record.Preview = truncate(preview, previewLimit)
	}
	if text := stringValue(p["text"]); text != "" {
		record.Preview = truncate(text, previewLimit)
	}
	record.SessionRef = stringValue(p["session_id"])
	record.ChunkID = intValue(p["chunk_id"])
	record.Stream = stringValue(p["stream"])
	return record
}

func spanID(event, turnID string, p map[string]any) string {
	if turnID == "" {
		turnID = "session"
	}
	switch event {
	case "turn.started", "turn.completed", "turn.errored":
		return "turn:" + turnID
	case "llm.requested", "llm.responded":
		iter := stringValue(p["iter"])
		if iter == "" {
			iter = "0"
		}
		return "llm:" + turnID + ":" + iter
	case "tool.requested", "tool.completed", "tool.errored":
		return "tool:" + turnID + ":" + firstNonEmpty(stringValue(p["tool_use_id"]), stringValue(p["name"]))
	case "context.compact.started", "context.compact.completed", "context.compact.errored":
		return "compact:" + turnID + ":" + firstNonEmpty(stringValue(p["reason"]), "context")
	case "hook.started", "hook.completed", "hook.errored":
		return "hook:" + turnID + ":" + firstNonEmpty(stringValue(p["name"]), "hook") + ":" + firstNonEmpty(stringValue(p["event_name"]), "event")
	case "finish.attempted":
		return "finish:" + turnID
	default:
		return ""
	}
}

func parentID(event, turnID string) string {
	if turnID == "" {
		return ""
	}
	switch event {
	case "turn.started", "turn.completed", "turn.errored":
		return ""
	case "llm.requested", "llm.responded", "tool.requested", "tool.completed", "tool.errored", "context.compact.started", "context.compact.completed", "context.compact.errored", "hook.started", "hook.completed", "hook.errored", "finish.attempted":
		return "turn:" + turnID
	default:
		return ""
	}
}

func spanEvent(event string) string {
	switch event {
	case "turn.started", "llm.requested", "tool.requested", "context.compact.started", "hook.started":
		return "start"
	case "turn.completed", "llm.responded", "tool.completed", "context.compact.completed", "hook.completed":
		return "end"
	case "turn.errored", "tool.errored", "context.compact.errored", "hook.errored":
		return "error"
	case "finish.attempted":
		return "instant"
	default:
		return ""
	}
}

func spanName(event string) string {
	switch {
	case strings.HasPrefix(event, "turn."):
		return "turn"
	case strings.HasPrefix(event, "llm."):
		return "provider"
	case strings.HasPrefix(event, "tool."):
		return "tool"
	case strings.HasPrefix(event, "context.compact."):
		return "compaction"
	case strings.HasPrefix(event, "hook."):
		return "hook"
	case event == "finish.attempted":
		return "finish"
	default:
		return event
	}
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

func classifyErrorKind(raw string) string {
	raw = strings.ToLower(raw)
	switch {
	case strings.Contains(raw, "timeout") || strings.Contains(raw, "timed out"):
		return "timeout"
	case strings.Contains(raw, "permission") || strings.Contains(raw, "denied"):
		return "permission"
	case strings.Contains(raw, "auth") || strings.Contains(raw, "unauthorized"):
		return "auth"
	case strings.Contains(raw, "cancel"):
		return "cancelled"
	case raw == "":
		return "error"
	default:
		return "error"
	}
}

func int64Value(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	default:
		return 0
	}
}

func intValue(v any) int {
	return int(int64Value(v))
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func sortedFileNames(files map[string]*os.File) []string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
