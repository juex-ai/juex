// Package bundle creates portable debug archives for persisted JueX sessions.
package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/version"
)

const archiveRoot = "juex-debug-bundle"

var (
	ErrSessionNotFound     = errors.New("bundle: session not found")
	ErrRequiredFileMissing = errors.New("bundle: required file missing")
	ErrOutputExists        = errors.New("bundle: output exists")
)

type Options struct {
	WorkDir                string
	SessionID              string
	OutPath                string
	Redact                 bool
	Force                  bool
	IncludeWorktreeSummary bool
	IncludeArtifacts       bool
	Now                    func() time.Time
	Env                    []string
	Config                 config.Config
	ExtraFiles             []ExtraFile
}

type ExtraFile struct {
	ArchivePath string
	Bytes       []byte
	Redact      bool
}

type Result struct {
	Path      string `json:"path"`
	SessionID string `json:"session_id"`
	Files     int    `json:"files"`
	Bytes     int64  `json:"bytes"`
	Redacted  bool   `json:"redacted"`
}

type Manifest struct {
	SchemaVersion int             `json:"schema_version"`
	GeneratedAt   time.Time       `json:"generated_at"`
	WorkDir       string          `json:"work_dir"`
	SessionID     string          `json:"session_id"`
	Redacted      bool            `json:"redacted"`
	Version       version.Info    `json:"version"`
	Entries       []ManifestEntry `json:"entries"`
}

type ManifestEntry struct {
	Path       string `json:"path"`
	SourcePath string `json:"source_path,omitempty"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256"`
	Redacted   bool   `json:"redacted"`
	Required   bool   `json:"required"`
}

type RuntimeSnapshot struct {
	WorkDir    string              `json:"work_dir"`
	SessionID  string              `json:"session_id"`
	SessionDir string              `json:"session_dir"`
	Provider   RuntimeProvider     `json:"provider"`
	Version    version.Info        `json:"version"`
	OS         string              `json:"os"`
	Arch       string              `json:"arch"`
	Paths      config.RuntimePaths `json:"paths"`
	Env        map[string]string   `json:"env,omitempty"`
}

type RuntimeProvider struct {
	ID       string `json:"id,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	Model    string `json:"model,omitempty"`
	BaseURL  string `json:"base_url,omitempty"`
	APIKey   string `json:"api_key,omitempty"`
}

type archiveEntry struct {
	ManifestEntry
	Data []byte
}

func Create(opts Options) (Result, error) {
	now := func() time.Time { return time.Now().UTC() }
	if opts.Now != nil {
		now = func() time.Time { return opts.Now().UTC() }
	}
	workDir, err := filepath.Abs(strings.TrimSpace(opts.WorkDir))
	if err != nil {
		return Result{}, err
	}
	sessionID := strings.TrimSpace(opts.SessionID)
	if sessionID == "" || sessionID == "." || sessionID == ".." || strings.ContainsAny(sessionID, `/\`) || filepath.Clean(sessionID) != sessionID {
		return Result{}, fmt.Errorf("%w: invalid session id format", ErrSessionNotFound)
	}
	opts.SessionID = sessionID
	trimmedOut := strings.TrimSpace(opts.OutPath)
	if trimmedOut == "" {
		return Result{}, fmt.Errorf("bundle: output path required")
	}
	outPath, err := filepath.Abs(trimmedOut)
	if err != nil {
		return Result{}, err
	}
	if st, err := os.Stat(outPath); err == nil {
		if st.IsDir() {
			return Result{}, fmt.Errorf("bundle: output path is a directory: %s", outPath)
		}
		if !opts.Force {
			return Result{}, fmt.Errorf("%w: %s", ErrOutputExists, outPath)
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Result{}, err
	}

	sessionsDir := opts.Config.SessionsDir()
	if sessionsDir == "" {
		sessionsDir = filepath.Join(workDir, ".juex", "sessions")
	}
	sessionDir := filepath.Join(sessionsDir, sessionID)
	if st, err := os.Stat(sessionDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Result{}, fmt.Errorf("%w: %s", ErrSessionNotFound, sessionID)
		}
		return Result{}, err
	} else if !st.IsDir() {
		return Result{}, fmt.Errorf("%w: %s", ErrSessionNotFound, sessionID)
	}

	entries, err := collectEntries(opts, workDir, sessionDir, now())
	if err != nil {
		return Result{}, err
	}
	manifest := Manifest{
		SchemaVersion: 1,
		GeneratedAt:   now(),
		WorkDir:       workDir,
		SessionID:     opts.SessionID,
		Redacted:      opts.Redact,
		Version:       version.Build(),
		Entries:       manifestEntries(entries),
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return Result{}, err
	}
	manifestBytes = append(manifestBytes, '\n')
	entries = append([]archiveEntry{newEntry(pathInBundle("manifest.json"), "", manifestBytes, false, true)}, entries...)

	if err := writeArchive(outPath, entries, now(), opts.Force); err != nil {
		return Result{}, err
	}
	st, err := os.Stat(outPath)
	if err != nil {
		return Result{}, err
	}
	return Result{Path: outPath, SessionID: sessionID, Files: len(entries), Bytes: st.Size(), Redacted: opts.Redact}, nil
}

func collectEntries(opts Options, workDir, sessionDir string, now time.Time) ([]archiveEntry, error) {
	var entries []archiveEntry
	runtimeBytes, err := json.MarshalIndent(runtimeSnapshot(opts, workDir, sessionDir), "", "  ")
	if err != nil {
		return nil, err
	}
	runtimeBytes = append(runtimeBytes, '\n')
	if opts.Redact {
		runtimeBytes = redactBytes(runtimeBytes)
	}
	entries = append(entries, newEntry(pathInBundle("runtime.json"), "", runtimeBytes, opts.Redact, true))

	for _, item := range sessionBundleFiles() {
		source := filepath.Join(sessionDir, item.name)
		data, err := os.ReadFile(source)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) && !item.required {
				continue
			}
			if errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("%w: %s", ErrRequiredFileMissing, source)
			}
			return nil, err
		}
		redacted := opts.Redact && isRedactableArchivePath(item.name)
		if redacted {
			data = redactBytes(data)
		}
		entries = append(entries, newEntry(pathInBundle(filepath.Join("session", filepath.ToSlash(item.name))), source, data, redacted, item.required))
	}
	if opts.IncludeWorktreeSummary {
		data, err := json.MarshalIndent(map[string]any{
			"work_dir":     workDir,
			"generated_at": now,
			"note":         "summary only; no worktree file contents included",
		}, "", "  ")
		if err != nil {
			return nil, err
		}
		entries = append(entries, newEntry(pathInBundle("worktree/summary.json"), "", append(data, '\n'), false, false))
	}
	if opts.IncludeArtifacts {
		artifactEntries, err := collectArtifacts(workDir, opts.Redact)
		if err != nil {
			return nil, err
		}
		entries = append(entries, artifactEntries...)
	}
	for _, extra := range opts.ExtraFiles {
		path, err := safeExtraArchivePath(extra.ArchivePath)
		if err != nil {
			return nil, err
		}
		data := append([]byte(nil), extra.Bytes...)
		redacted := opts.Redact && extra.Redact
		if redacted {
			data = redactBytes(data)
		}
		entries = append(entries, newEntry(pathInBundle(path), "", data, redacted, false))
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

func runtimeSnapshot(opts Options, workDir, sessionDir string) RuntimeSnapshot {
	cfg := opts.Config
	if cfg.WorkDir == "" {
		cfg.WorkDir = workDir
	}
	return RuntimeSnapshot{
		WorkDir:    workDir,
		SessionID:  opts.SessionID,
		SessionDir: sessionDir,
		Provider: RuntimeProvider{
			ID:       cfg.ProviderID,
			Protocol: cfg.ProviderProtocol,
			Model:    cfg.Model,
			BaseURL:  cfg.BaseURL,
			APIKey:   cfg.APIKey,
		},
		Version: version.Build(),
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
		Paths:   cfg.RuntimePaths(),
		Env:     envMap(opts.Env),
	}
}

type sessionBundleFile struct {
	name     string
	required bool
}

func sessionBundleFiles() []sessionBundleFile {
	return []sessionBundleFile{
		{name: "session.json", required: true},
		{name: "conversation.jsonl", required: true},
		{name: "events.jsonl", required: true},
		{name: "pending_input.jsonl"},
		{name: "notes.md"},
		{name: "goal_state.json"},
		{name: "trace.jsonl"},
		{name: "spans.jsonl"},
		{name: "tools.jsonl"},
		{name: filepath.Join("logs", "juex.log")},
		{name: filepath.Join("logs", "debug.log")},
	}
}

func collectArtifacts(workDir string, redact bool) ([]archiveEntry, error) {
	root := filepath.Join(workDir, ".juex", "artifacts")
	if _, err := os.Stat(root); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var entries []archiveEntry
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		redacted := redact && isLikelyText(data)
		if redacted {
			data = redactBytes(data)
		}
		entries = append(entries, newEntry(pathInBundle(filepath.Join("artifacts", filepath.ToSlash(rel))), path, data, redacted, false))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}

func writeArchive(outPath string, entries []archiveEntry, now time.Time, force bool) error {
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(outPath), filepath.Base(outPath)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	gz := gzip.NewWriter(tmp)
	tw := tar.NewWriter(gz)
	cleanup := true
	defer func() {
		if cleanup {
			_ = tw.Close()
			_ = gz.Close()
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
		}
	}()
	for _, entry := range entries {
		header := &tar.Header{
			Name:    entry.Path,
			Mode:    0o644,
			Size:    int64(len(entry.Data)),
			ModTime: now,
		}
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if _, err := tw.Write(entry.Data); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if force {
		_ = os.Remove(outPath)
	}
	if err := os.Rename(tmpPath, outPath); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func newEntry(path, source string, data []byte, redacted, required bool) archiveEntry {
	sum := sha256.Sum256(data)
	return archiveEntry{
		ManifestEntry: ManifestEntry{
			Path:       filepath.ToSlash(path),
			SourcePath: source,
			Size:       int64(len(data)),
			SHA256:     hex.EncodeToString(sum[:]),
			Redacted:   redacted,
			Required:   required,
		},
		Data: data,
	}
}

func manifestEntries(entries []archiveEntry) []ManifestEntry {
	out := make([]ManifestEntry, len(entries))
	for i, entry := range entries {
		out[i] = entry.ManifestEntry
	}
	return out
}

func pathInBundle(path string) string {
	return filepath.ToSlash(filepath.Join(archiveRoot, filepath.Clean(path)))
}

func safeExtraArchivePath(archivePath string) (string, error) {
	trimmed := strings.TrimSpace(archivePath)
	if trimmed == "" || filepath.IsAbs(trimmed) || isWindowsAbsolutePath(trimmed) {
		return "", fmt.Errorf("bundle: invalid extra archive path %q", archivePath)
	}
	clean := path.Clean(strings.ReplaceAll(trimmed, `\`, "/"))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
		return "", fmt.Errorf("bundle: invalid extra archive path %q", archivePath)
	}
	return clean, nil
}

func isWindowsAbsolutePath(path string) bool {
	if strings.HasPrefix(path, `\\`) || strings.HasPrefix(path, "//") {
		return true
	}
	if len(path) < 2 || path[1] != ':' {
		return false
	}
	drive := path[0]
	return (drive >= 'A' && drive <= 'Z') || (drive >= 'a' && drive <= 'z')
}

func envMap(env []string) map[string]string {
	if len(env) == 0 {
		env = os.Environ()
	}
	out := map[string]string{}
	for _, item := range env {
		key, value, ok := strings.Cut(item, "=")
		if !ok || strings.TrimSpace(key) == "" {
			continue
		}
		out[key] = value
	}
	return out
}

func isRedactableArchivePath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json", ".jsonl", ".log", ".txt", ".yaml", ".yml", ".md":
		return true
	default:
		return filepath.Ext(path) == ""
	}
}

func isLikelyText(data []byte) bool {
	if len(data) == 0 {
		return true
	}
	return bytes.IndexByte(data, 0) < 0
}

func redactBytes(data []byte) []byte {
	if len(bytes.TrimSpace(data)) == 0 {
		return append([]byte(nil), data...)
	}
	if redacted, ok := redactJSON(data); ok {
		return redacted
	}
	if redacted, ok := redactJSONLines(data); ok {
		return redacted
	}
	return []byte(redactString(string(data)))
}

func redactJSON(data []byte) ([]byte, bool) {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, false
	}
	v = redactValue("", v)
	out, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}
	return append(out, '\n'), true
}

func redactJSONLines(data []byte) ([]byte, bool) {
	hasTrailingNewline := bytes.HasSuffix(data, []byte("\n"))
	trimmed := bytes.TrimSuffix(data, []byte("\n"))
	lines := bytes.Split(trimmed, []byte("\n"))
	out := make([][]byte, 0, len(lines))
	parsed := false
	for _, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			out = append(out, line)
			continue
		}
		redacted, ok := redactJSON(line)
		if !ok {
			return nil, false
		}
		parsed = true
		out = append(out, bytes.TrimSuffix(redacted, []byte("\n")))
	}
	if !parsed {
		return nil, false
	}
	joined := bytes.Join(out, []byte("\n"))
	if hasTrailingNewline {
		joined = append(joined, '\n')
	}
	return joined, true
}

func redactValue(key string, v any) any {
	if isSecretKey(key) {
		return "[REDACTED]"
	}
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, value := range x {
			out[k] = redactValue(k, value)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, value := range x {
			out[i] = redactValue("", value)
		}
		return out
	case string:
		return redactString(x)
	default:
		return v
	}
}

func isSecretKey(key string) bool {
	key = strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	if key == "" || strings.Contains(key, "token_usage") || strings.HasSuffix(key, "_tokens") || strings.Contains(key, "tokens_") {
		return false
	}
	for _, marker := range []string{"api_key", "secret", "password", "authorization", "cookie"} {
		if strings.Contains(key, marker) {
			return true
		}
	}
	return key == "token" || strings.HasSuffix(key, "_token") || strings.HasPrefix(key, "token_") || strings.Contains(key, "_token_")
}

var (
	secretAssignmentPattern = regexp.MustCompile(`(?i)(api[_-]?key|secret|password|authorization|cookie|token)[A-Za-z0-9_-]*\s*[:=]\s*("[^"\n\r]*"|'[^'\n\r]*'|[^ \n\r\t]+)`)
	bearerPattern           = regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._-]+`)
	openAIKeyPattern        = regexp.MustCompile(`sk-[A-Za-z0-9_-]{6,}`)
)

func redactString(text string) string {
	text = secretAssignmentPattern.ReplaceAllString(text, "[REDACTED]")
	text = bearerPattern.ReplaceAllString(text, "Bearer [REDACTED]")
	text = openAIKeyPattern.ReplaceAllString(text, "[REDACTED]")
	return text
}
