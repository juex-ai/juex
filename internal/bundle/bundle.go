// Package bundle creates portable debug archives for persisted JueX Threads.
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
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/artifact"
	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/environment"
	"github.com/juex-ai/juex/internal/thread"
	"github.com/juex-ai/juex/internal/version"
)

const archiveRoot = "juex-debug-bundle"

var (
	ErrThreadNotFound      = errors.New("bundle: Thread not found")
	ErrRequiredFileMissing = errors.New("bundle: required file missing")
	ErrOutputExists        = errors.New("bundle: output exists")
)

type Options struct {
	WorkDir                string
	ThreadID               string
	OutPath                string
	Redact                 bool
	Force                  bool
	IncludeWorktreeSummary bool
	IncludeMedia           bool
	Now                    func() time.Time
	Config                 config.Config
	Environment            environment.Snapshot
	ExtraFiles             []ExtraFile
}

type ExtraFile struct {
	ArchivePath string
	Bytes       []byte
	Redact      bool
}

type Result struct {
	Path     string `json:"path"`
	ThreadID string `json:"thread_id"`
	Files    int    `json:"files"`
	Bytes    int64  `json:"bytes"`
	Redacted bool   `json:"redacted"`
}

type Manifest struct {
	SchemaVersion int             `json:"schema_version"`
	GeneratedAt   time.Time       `json:"generated_at"`
	WorkDir       string          `json:"work_dir"`
	ThreadID      string          `json:"thread_id"`
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
	WorkDir     string                 `json:"work_dir"`
	ThreadID    string                 `json:"thread_id"`
	ThreadDir   string                 `json:"thread_dir"`
	Provider    RuntimeProvider        `json:"provider"`
	Version     version.Info           `json:"version"`
	OS          string                 `json:"os"`
	Arch        string                 `json:"arch"`
	Paths       config.RuntimePaths    `json:"paths"`
	Environment []environment.Metadata `json:"environment,omitempty"`
}

type RuntimeProvider struct {
	ID       string `json:"id,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	Model    string `json:"model,omitempty"`
	BaseURL  string `json:"base_url,omitempty"`
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
	threadID := strings.TrimSpace(opts.ThreadID)
	if !thread.ValidID(threadID) {
		return Result{}, fmt.Errorf("%w: invalid Thread id format", ErrThreadNotFound)
	}
	opts.ThreadID = threadID
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

	store := thread.NewStore(opts.Config.RuntimePaths().StateDir)
	threadDir := filepath.Join(store.ThreadsDir(), threadID)
	if st, statErr := os.Stat(threadDir); errors.Is(statErr, os.ErrNotExist) {
		threadDir = filepath.Join(store.ArchiveDir(), threadID)
	} else if statErr != nil {
		return Result{}, statErr
	} else if !st.IsDir() {
		return Result{}, fmt.Errorf("%w: %s", ErrThreadNotFound, threadID)
	}
	if st, statErr := os.Stat(threadDir); statErr != nil || !st.IsDir() {
		return Result{}, fmt.Errorf("%w: %s", ErrThreadNotFound, threadID)
	}

	entries, err := collectEntries(opts, workDir, threadDir, now())
	if err != nil {
		return Result{}, err
	}
	snapshot := bundleEnvironment(opts)
	usedArchivePaths := map[string]struct{}{}
	manifestPath, manifestPathRedacted := uniqueRedactedArchivePath(
		snapshot,
		pathInBundle("manifest.json"),
		usedArchivePaths,
	)
	pathsRedacted := redactConfiguredArchivePaths(snapshot, entries, usedArchivePaths)
	manifest := Manifest{
		SchemaVersion: 1,
		GeneratedAt:   now(),
		WorkDir:       workDir,
		ThreadID:      opts.ThreadID,
		Redacted:      opts.Redact || manifestPathRedacted || pathsRedacted || entriesContainRedaction(entries),
		Version:       version.Build(),
		Entries:       manifestEntries(entries),
	}
	if redactManifestMetadata(snapshot, &manifest) {
		manifest.Redacted = true
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return Result{}, err
	}
	manifestBytes = append(manifestBytes, '\n')
	entries = append([]archiveEntry{newEntry(manifestPath, "", manifestBytes, manifestPathRedacted, true)}, entries...)

	if err := writeArchive(outPath, entries, now(), opts.Force); err != nil {
		return Result{}, err
	}
	st, err := os.Stat(outPath)
	if err != nil {
		return Result{}, err
	}
	return Result{Path: outPath, ThreadID: threadID, Files: len(entries), Bytes: st.Size(), Redacted: manifest.Redacted}, nil
}

func redactManifestMetadata(snapshot environment.Snapshot, manifest *Manifest) bool {
	if manifest == nil {
		return false
	}
	changed := redactManifestString(snapshot, &manifest.WorkDir)
	if redactManifestString(snapshot, &manifest.ThreadID) {
		changed = true
	}
	for index := range manifest.Entries {
		if redactManifestString(snapshot, &manifest.Entries[index].SourcePath) {
			changed = true
		}
	}
	return changed
}

func redactManifestString(snapshot environment.Snapshot, value *string) bool {
	if value == nil || *value == "" {
		return false
	}
	redacted, changed := snapshot.RedactConfiguredValues([]byte(*value))
	if changed {
		*value = string(redacted)
	}
	return changed
}

func collectEntries(opts Options, workDir, threadDir string, now time.Time) ([]archiveEntry, error) {
	var entries []archiveEntry
	runtimeBytes, err := json.MarshalIndent(runtimeSnapshot(opts, workDir, threadDir), "", "  ")
	if err != nil {
		return nil, err
	}
	runtimeBytes = append(runtimeBytes, '\n')
	if opts.Redact {
		runtimeBytes = redactBytes(runtimeBytes)
	}
	entries = append(entries, newEntry(pathInBundle("runtime.json"), "", runtimeBytes, opts.Redact, true))

	for _, item := range threadBundleFiles() {
		source := filepath.Join(threadDir, item.name)
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
		entries = append(entries, newEntry(pathInBundle(filepath.Join("thread", filepath.ToSlash(item.name))), source, data, redacted, item.required))
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
	if opts.IncludeMedia {
		mediaEntries, err := collectMedia(opts.Config.MediaDir(), opts.Redact)
		if err != nil {
			return nil, err
		}
		entries = append(entries, mediaEntries...)
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
	snapshot := bundleEnvironment(opts)
	for i := range entries {
		data, redacted := redactConfiguredArchiveData(snapshot, entries[i].Path, entries[i].Data)
		if !redacted {
			continue
		}
		entry := entries[i]
		entries[i] = newEntry(entry.Path, entry.SourcePath, data, true, entry.Required)
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

func redactConfiguredArchivePaths(snapshot environment.Snapshot, entries []archiveEntry, used map[string]struct{}) bool {
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	changed := false
	for index := range entries {
		redactedPath, redacted := uniqueRedactedArchivePath(snapshot, entries[index].Path, used)
		if redactedPath == entries[index].Path {
			continue
		}
		entries[index].Path = redactedPath
		entries[index].Redacted = true
		changed = changed || redacted
	}
	return changed
}

func uniqueRedactedArchivePath(snapshot environment.Snapshot, archivePath string, used map[string]struct{}) (string, bool) {
	redacted, configuredValueRedacted := snapshot.RedactConfiguredValues([]byte(archivePath))
	candidate := string(redacted)
	if _, exists := used[candidate]; !exists {
		used[candidate] = struct{}{}
		return candidate, configuredValueRedacted
	}
	dir, base := path.Split(candidate)
	ext := path.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	for suffix := 2; ; suffix++ {
		rawUnique := fmt.Sprintf("%s%s~%d%s", dir, stem, suffix, ext)
		redactedUnique, _ := snapshot.RedactConfiguredValues([]byte(rawUnique))
		unique := string(redactedUnique)
		if _, exists := used[unique]; exists {
			continue
		}
		used[unique] = struct{}{}
		return unique, true
	}
}

func redactConfiguredArchiveData(snapshot environment.Snapshot, archivePath string, data []byte) ([]byte, bool) {
	switch strings.ToLower(filepath.Ext(archivePath)) {
	case ".json":
		if redacted, changed, err := redactConfiguredArchiveJSON(snapshot, data); err == nil {
			return redacted, changed
		}
	case ".jsonl":
		lines := bytes.SplitAfter(data, []byte{'\n'})
		var output bytes.Buffer
		changed := false
		for _, line := range lines {
			if len(line) == 0 {
				continue
			}
			hasNewline := line[len(line)-1] == '\n'
			payload := line
			if hasNewline {
				payload = line[:len(line)-1]
			}
			if len(bytes.TrimSpace(payload)) == 0 {
				output.Write(payload)
			} else if redacted, lineChanged, err := redactConfiguredArchiveJSON(snapshot, payload); err == nil {
				if !lineChanged {
					output.Write(payload)
				} else {
					var compact bytes.Buffer
					if err := json.Compact(&compact, redacted); err != nil {
						return snapshot.RedactConfiguredValues(data)
					}
					output.Write(compact.Bytes())
					changed = true
				}
			} else {
				redacted, lineChanged := snapshot.RedactConfiguredValues(payload)
				output.Write(redacted)
				changed = changed || lineChanged
			}
			if hasNewline {
				output.WriteByte('\n')
			}
		}
		return output.Bytes(), changed
	}
	return snapshot.RedactConfiguredValues(data)
}

// redactConfiguredArchiveJSON is intentionally lossier than the schema-safe
// Snapshot redactor used by APIs. Debug bundles must scrub configured values
// even when an artifact encoded them as object keys or non-string scalars.
func redactConfiguredArchiveJSON(snapshot environment.Snapshot, data []byte) ([]byte, bool, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, false, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, false, fmt.Errorf("bundle: JSON payload contains multiple values")
		}
		return nil, false, err
	}
	changed := redactConfiguredArchiveJSONValue(snapshot, &value)
	if !changed {
		return append([]byte(nil), data...), false, nil
	}
	out, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, false, err
	}
	return append(out, '\n'), true, nil
}

func redactConfiguredArchiveJSONValue(snapshot environment.Snapshot, value *any) bool {
	switch current := (*value).(type) {
	case string:
		redacted, changed := snapshot.RedactConfiguredValues([]byte(current))
		if changed {
			*value = string(redacted)
		}
		return changed
	case []any:
		changed := false
		for index := range current {
			if redactConfiguredArchiveJSONValue(snapshot, &current[index]) {
				changed = true
			}
		}
		return changed
	case map[string]any:
		keys := make([]string, 0, len(current))
		for key := range current {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		redactedMap := make(map[string]any, len(current))
		changed := false
		for _, key := range keys {
			item := current[key]
			if redactConfiguredArchiveJSONValue(snapshot, &item) {
				changed = true
			}
			redactedKey, keyChanged := snapshot.RedactConfiguredValues([]byte(key))
			outputKey := uniqueRedactedJSONKey(redactedMap, string(redactedKey))
			if keyChanged || outputKey != key {
				changed = true
			}
			redactedMap[outputKey] = item
		}
		if changed {
			*value = redactedMap
		}
		return changed
	case json.Number:
		return redactConfiguredArchiveJSONScalar(snapshot, value, current.String())
	case bool:
		return redactConfiguredArchiveJSONScalar(snapshot, value, fmt.Sprint(current))
	case nil:
		return redactConfiguredArchiveJSONScalar(snapshot, value, "null")
	default:
		return false
	}
}

func redactConfiguredArchiveJSONScalar(snapshot environment.Snapshot, value *any, text string) bool {
	redacted, changed := snapshot.RedactConfiguredValues([]byte(text))
	if changed {
		*value = string(redacted)
	}
	return changed
}

func uniqueRedactedJSONKey(values map[string]any, key string) string {
	if _, exists := values[key]; !exists {
		return key
	}
	candidate := key
	for {
		candidate += "~[REDACTED_ENV]"
		if _, exists := values[candidate]; !exists {
			return candidate
		}
	}
}

func entriesContainRedaction(entries []archiveEntry) bool {
	for _, entry := range entries {
		if entry.Redacted {
			return true
		}
	}
	return false
}

func runtimeSnapshot(opts Options, workDir, threadDir string) RuntimeSnapshot {
	cfg := opts.Config
	if cfg.WorkDir == "" {
		cfg.WorkDir = workDir
	}
	return RuntimeSnapshot{
		WorkDir:   workDir,
		ThreadID:  opts.ThreadID,
		ThreadDir: threadDir,
		Provider: RuntimeProvider{
			ID:       cfg.ProviderID,
			Protocol: cfg.ProviderProtocol,
			Model:    cfg.Model,
			BaseURL:  cfg.BaseURL,
		},
		Version:     version.Build(),
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		Paths:       cfg.RuntimePaths(),
		Environment: bundleEnvironment(opts).ConfiguredMetadata(),
	}
}

func bundleEnvironment(opts Options) environment.Snapshot {
	if !opts.Environment.IsZero() {
		return opts.Environment
	}
	return opts.Config.EnvironmentSnapshot()
}

type threadBundleFile struct {
	name     string
	required bool
}

func threadBundleFiles() []threadBundleFile {
	return []threadBundleFile{
		{name: "journal.jsonl", required: true},
		{name: "thread.json"},
	}
}

func collectMedia(mediaDir string, redact bool) ([]archiveEntry, error) {
	if strings.TrimSpace(mediaDir) == "" {
		return nil, nil
	}
	store, err := artifact.NewStore(mediaDir)
	if err != nil {
		return nil, err
	}
	files, err := store.Files()
	if err != nil {
		return nil, err
	}
	entries := make([]archiveEntry, 0, len(files))
	for _, file := range files {
		data := file.Data
		redacted := redact && isLikelyText(data)
		if redacted {
			data = redactBytes(data)
		}
		entries = append(entries, newEntry(pathInBundle(filepath.Join("media", filepath.FromSlash(file.Path))), "", data, redacted, false))
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
