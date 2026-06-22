package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
)

const (
	repeatedFailureThreshold = 2
	failurePreviewLimit      = 500
)

type toolFailureObservation struct {
	ToolName  string
	ToolUseID string
	Input     map[string]any
	Content   string
	Error     string
	TimedOut  bool
	ExitCode  *int
}

type toolFailureClassificationResult struct {
	Classification ToolFailureClassification
	Blocking       bool
}

type toolFailureRecord struct {
	Fingerprint     string
	ToolName        string
	ToolUseID       string
	Classification  ToolFailureClassification
	Status          ToolFailureStatus
	Blocking        bool
	Occurrences     int
	Error           string
	ExitCode        *int
	OutputLen       int
	OutputPreview   string
	RelatedPaths    []string
	LatestModUnixMS int64
}

type toolFailureLedger struct {
	records map[string]*toolFailureRecord
	order   []string
	workDir string
}

func newToolFailureLedger(sessionDir string) *toolFailureLedger {
	return &toolFailureLedger{
		records: map[string]*toolFailureRecord{},
		workDir: workDirFromSessionDir(sessionDir),
	}
}

func (e *Engine) recordToolFailureBatch(turnID string, calls []llm.Block, results []llm.Block) {
	if e == nil || e.toolFailures == nil {
		return
	}
	for i, result := range results {
		var call llm.Block
		if i < len(calls) {
			call = calls[i]
		}
		toolName := firstNonEmptyString(result.ToolName, call.ToolName)
		toolUseID := firstNonEmptyString(result.ToolUseID, call.ToolUseID)
		errText := extractToolError(result.Content)
		if errText == "" && strings.HasPrefix(strings.ToLower(strings.TrimSpace(result.Content)), "hooks:") {
			errText = strings.TrimSpace(result.Content)
		}
		obs := toolFailureObservation{
			ToolName:  toolName,
			ToolUseID: toolUseID,
			Input:     call.Input,
			Content:   result.Content,
			Error:     errText,
			TimedOut:  strings.Contains(strings.ToLower(result.Content), "timed out"),
			ExitCode:  firstExitCode(nil, result.Content),
		}
		if result.IsError {
			payload := e.toolFailures.recordFailure(obs)
			e.emit(events.Event{Type: "tool.failure.recorded", TurnID: turnID, Payload: payload})
			continue
		}
		resolved, stale := e.toolFailures.recordSuccess(obs)
		for _, payload := range resolved {
			e.emit(events.Event{Type: "tool.failure.resolved", TurnID: turnID, Payload: payload})
		}
		for _, payload := range stale {
			e.emit(events.Event{Type: "tool.failure.stale", TurnID: turnID, Payload: payload})
		}
	}
}

func classifyToolFailure(obs toolFailureObservation) toolFailureClassificationResult {
	text := strings.ToLower(obs.Error + "\n" + obs.Content)
	toolName := strings.ToLower(obs.ToolName)
	switch {
	case strings.Contains(text, "unknown tool"):
		return toolFailureClassificationResult{Classification: ToolFailureRuntimeFatal, Blocking: true}
	case strings.Contains(strings.ToLower(obs.Error), "hooks:"):
		return toolFailureClassificationResult{Classification: ToolFailureRuntimeFatal, Blocking: true}
	case toolName == "grep" && strings.Contains(text, "no matches"):
		return toolFailureClassificationResult{Classification: ToolFailureNonblockingExploratory, Blocking: false}
	case (toolName == "read" || toolName == "grep") && containsAny(text, "no such file", "no such directory", "cannot find the file specified"):
		return toolFailureClassificationResult{Classification: ToolFailureNonblockingExploratory, Blocking: false}
	case obs.TimedOut || containsAny(text,
		"timed out",
		"context deadline exceeded",
		"permission denied",
		"unauthorized",
		"authentication",
		"connection refused",
		"no such host",
		"network is unreachable",
		"rate limit",
	):
		return toolFailureClassificationResult{Classification: ToolFailureExternalBlocked, Blocking: true}
	default:
		return toolFailureClassificationResult{Classification: ToolFailureRecoverable, Blocking: true}
	}
}

func (l *toolFailureLedger) recordFailure(obs toolFailureObservation) ToolFailureRecordedPayload {
	if l == nil {
		return ToolFailureRecordedPayload{}
	}
	classified := classifyToolFailure(obs)
	paths := relatedPathsFromInput(l.workDir, obs.Input)
	fingerprint := failureFingerprint(obs, classified.Classification)
	rec := l.records[fingerprint]
	if rec == nil {
		rec = &toolFailureRecord{
			Fingerprint:     fingerprint,
			ToolName:        obs.ToolName,
			ToolUseID:       obs.ToolUseID,
			Classification:  classified.Classification,
			Status:          ToolFailureStatusUnresolved,
			Blocking:        classified.Blocking,
			RelatedPaths:    paths,
			LatestModUnixMS: latestModUnixMS(l.workDir, paths),
		}
		l.records[fingerprint] = rec
		l.order = append(l.order, fingerprint)
	} else if rec.Status != ToolFailureStatusUnresolved {
		rec.Status = ToolFailureStatusUnresolved
		rec.Occurrences = 0
	}
	rec.Classification = classified.Classification
	rec.Blocking = classified.Blocking
	rec.ToolUseID = obs.ToolUseID
	rec.Occurrences++
	rec.Error = firstNonEmptyString(obs.Error, extractToolError(obs.Content))
	rec.ExitCode = cloneIntPtr(firstExitCode(obs.ExitCode, obs.Content))
	rec.OutputLen = len(obs.Content)
	rec.OutputPreview = truncate(obs.Content, failurePreviewLimit)
	if len(paths) > 0 {
		rec.RelatedPaths = paths
	}
	if latest := latestModUnixMS(l.workDir, rec.RelatedPaths); latest > 0 {
		rec.LatestModUnixMS = latest
	}
	if rec.Blocking && rec.Occurrences >= repeatedFailureThreshold {
		rec.Classification = ToolFailureRepeatedStuck
	}
	return rec.recordedPayload()
}

func (l *toolFailureLedger) recordSuccess(obs toolFailureObservation) (resolved []ToolFailureResolvedPayload, stale []ToolFailureStalePayload) {
	if l == nil {
		return nil, nil
	}
	paths := relatedPathsFromInput(l.workDir, obs.Input)
	for _, fp := range l.order {
		rec := l.records[fp]
		if rec == nil || rec.Status != ToolFailureStatusUnresolved {
			continue
		}
		if mutatesRelatedPath(obs.ToolName, paths, rec.RelatedPaths) {
			rec.Status = ToolFailureStatusStale
			if latest := latestModUnixMS(l.workDir, rec.RelatedPaths); latest > 0 {
				rec.LatestModUnixMS = latest
			}
			stale = append(stale, ToolFailureStalePayload{
				Fingerprint:     rec.Fingerprint,
				Name:            rec.ToolName,
				ToolUseID:       rec.ToolUseID,
				Status:          rec.Status,
				Reason:          "related file changed after failure",
				ResolverName:    obs.ToolName,
				ResolverUseID:   obs.ToolUseID,
				RelatedPaths:    append([]string(nil), rec.RelatedPaths...),
				LatestModUnixMS: rec.LatestModUnixMS,
			})
			continue
		}
		if successResolvesFailure(obs.ToolName, paths, rec) {
			rec.Status = ToolFailureStatusResolved
			resolved = append(resolved, ToolFailureResolvedPayload{
				Fingerprint:   rec.Fingerprint,
				Name:          rec.ToolName,
				ToolUseID:     rec.ToolUseID,
				Status:        rec.Status,
				Reason:        "later successful check",
				ResolverName:  obs.ToolName,
				ResolverUseID: obs.ToolUseID,
			})
		}
	}
	return resolved, stale
}

func (r *toolFailureRecord) recordedPayload() ToolFailureRecordedPayload {
	return ToolFailureRecordedPayload{
		Fingerprint:     r.Fingerprint,
		Name:            r.ToolName,
		ToolUseID:       r.ToolUseID,
		Classification:  r.Classification,
		Status:          r.Status,
		Blocking:        r.Blocking,
		Occurrences:     r.Occurrences,
		Error:           r.Error,
		ExitCode:        cloneIntPtr(r.ExitCode),
		OutputLen:       r.OutputLen,
		OutputPreview:   r.OutputPreview,
		RelatedPaths:    append([]string(nil), r.RelatedPaths...),
		LatestModUnixMS: r.LatestModUnixMS,
	}
}

func failureFingerprint(obs toolFailureObservation, class ToolFailureClassification) string {
	body := map[string]any{
		"classification": class,
		"tool":           obs.ToolName,
		"input":          obs.Input,
		"exit_code":      obs.ExitCode,
		"error":          normalizeFailureText(firstNonEmptyString(obs.Error, extractToolError(obs.Content), obs.Content)),
	}
	data, _ := json.Marshal(body)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

func normalizeFailureText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	fields := strings.Fields(text)
	text = strings.Join(fields, " ")
	if len(text) > 300 {
		text = text[:300]
	}
	return text
}

func extractToolError(content string) string {
	const marker = "[tool error]"
	idx := strings.LastIndex(content, marker)
	if idx < 0 {
		return ""
	}
	return strings.TrimSpace(content[idx+len(marker):])
}

var shellExitCodePattern = regexp.MustCompile(`Process exited with code (-?\d+)`)

func firstExitCode(explicit *int, content string) *int {
	if explicit != nil {
		return explicit
	}
	matches := shellExitCodePattern.FindStringSubmatch(content)
	if len(matches) != 2 {
		return nil
	}
	code, err := strconv.Atoi(matches[1])
	if err != nil {
		return nil
	}
	return &code
}

func relatedPathsFromInput(workDir string, input map[string]any) []string {
	seen := map[string]bool{}
	var out []string
	var visit func(any, string)
	visit = func(v any, key string) {
		switch typed := v.(type) {
		case string:
			if isPathKey(key) && typed != "" {
				addRelatedPath(workDir, typed, seen, &out)
			}
		case []any:
			for _, item := range typed {
				visit(item, key)
			}
		case []string:
			for _, item := range typed {
				visit(item, key)
			}
		case map[string]any:
			for childKey, child := range typed {
				visit(child, childKey)
			}
		}
	}
	for key, value := range input {
		visit(value, key)
	}
	sort.Strings(out)
	return out
}

func addRelatedPath(workDir string, path string, seen map[string]bool, out *[]string) {
	cleaned := resolveRelatedPath(workDir, path)
	if cleaned == "." || seen[cleaned] {
		return
	}
	seen[cleaned] = true
	*out = append(*out, cleaned)
}

func isPathKey(key string) bool {
	switch strings.ToLower(key) {
	case "path", "file", "filename", "workdir":
		return true
	default:
		return false
	}
}

func latestModUnixMS(workDir string, paths []string) int64 {
	var latest int64
	for _, path := range paths {
		info, err := os.Stat(resolveRelatedPath(workDir, path))
		if err != nil {
			continue
		}
		if ts := info.ModTime().UnixMilli(); ts > latest {
			latest = ts
		}
	}
	return latest
}

func resolveRelatedPath(workDir string, path string) string {
	cleaned := filepath.Clean(path)
	if !filepath.IsAbs(cleaned) && workDir != "" {
		cleaned = filepath.Join(workDir, cleaned)
	}
	return filepath.Clean(cleaned)
}

func workDirFromSessionDir(sessionDir string) string {
	if sessionDir == "" {
		return ""
	}
	cleaned := filepath.Clean(sessionDir)
	sessionsDir := filepath.Dir(cleaned)
	juexDir := filepath.Dir(sessionsDir)
	if filepath.Base(sessionsDir) != "sessions" || filepath.Base(juexDir) != ".juex" {
		return ""
	}
	return filepath.Dir(juexDir)
}

func mutatesRelatedPath(toolName string, successPaths []string, failurePaths []string) bool {
	switch toolName {
	case "write", "edit":
	default:
		return false
	}
	return pathsIntersect(successPaths, failurePaths)
}

func successResolvesFailure(toolName string, successPaths []string, rec *toolFailureRecord) bool {
	if rec == nil || rec.ToolName != toolName {
		return false
	}
	if len(rec.RelatedPaths) == 0 && len(successPaths) == 0 {
		return true
	}
	return pathsIntersect(successPaths, rec.RelatedPaths)
}

func pathsIntersect(a []string, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	seen := make(map[string]bool, len(a))
	for _, path := range a {
		seen[filepath.Clean(path)] = true
	}
	for _, path := range b {
		if seen[filepath.Clean(path)] {
			return true
		}
	}
	return false
}

func containsAny(text string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func cloneIntPtr(v *int) *int {
	if v == nil {
		return nil
	}
	copied := *v
	return &copied
}

func intPtr(v int) *int {
	return &v
}
