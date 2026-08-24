package config

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/environment"
	"github.com/juex-ai/juex/internal/homestore"
	"github.com/juex-ai/juex/internal/hooks"
	"gopkg.in/yaml.v3"
)

const (
	configImportTimeout         = 5 * time.Second
	configImportMaxBytes        = 1 << 20
	configImportMaxRedirects    = 3
	configImportMaxCacheAge     = 7 * 24 * time.Hour
	configImportCacheVersion    = 3
	configImportJournalVersion  = 1
	configImportJournalMaxBytes = 64 << 20
	configImportJournalName     = ".publication-journal.json"
)

const (
	configImportJournalPrepared  = "prepared"
	configImportJournalCommitted = "committed"
)

type importConfig struct {
	Source string `yaml:"source"`
}

// ConfigImportStatus is value-free provenance for one directly imported
// configuration source.
type ConfigImportStatus struct {
	Source    string    `json:"source"`
	State     string    `json:"state"`
	Digest    string    `json:"digest"`
	FetchedAt time.Time `json:"fetched_at,omitempty"`
}

// ImportStatuses returns import provenance without configuration contents or
// URL credentials and query values.
func (c Config) ImportStatuses() []ConfigImportStatus {
	return append([]ConfigImportStatus(nil), c.importStatuses...)
}

type configImportLoader struct {
	homeDir       string
	contextDigest string
	client        *http.Client
	now           func() time.Time
	timeout       time.Duration
	maxBytes      int64
	maxCacheAge   time.Duration
	maxRedirects  int
	remoteMemo    map[string]configImportDocument
	cacheLock     *homestore.Lock
}

type configImportDocument struct {
	data       []byte
	source     yamlConfigSource
	status     ConfigImportStatus
	cacheWrite *configImportCacheRecord
}

type configImportCacheRecord struct {
	Version         int       `json:"version"`
	Source          string    `json:"source"`
	SourceSHA256    string    `json:"source_sha256"`
	DeclaringSHA256 string    `json:"declaring_sha256"`
	ContextSHA256   string    `json:"context_sha256"`
	ETag            string    `json:"etag,omitempty"`
	LastModified    string    `json:"last_modified,omitempty"`
	FetchedAt       time.Time `json:"fetched_at"`
	ContentSHA256   string    `json:"content_sha256"`
	Content         string    `json:"content"`
	cachePath       string
}

func newConfigImportLoader(homeDir string) *configImportLoader {
	return &configImportLoader{
		homeDir:      homeDir,
		client:       &http.Client{},
		now:          time.Now,
		timeout:      configImportTimeout,
		maxBytes:     configImportMaxBytes,
		maxCacheAge:  configImportMaxCacheAge,
		maxRedirects: configImportMaxRedirects,
		remoteMemo:   make(map[string]configImportDocument),
	}
}

func applyYAMLFileWithImportLoader(cfg *Config, source yamlConfigSource, loader *configImportLoader) error {
	return applyYAMLFileWithImportLoaderAndOptions(cfg, source, loader, applyYAMLDataOptions{})
}

func applyYAMLFileWithImportLoaderAndOptions(cfg *Config, source yamlConfigSource, loader *configImportLoader, opts applyYAMLDataOptions) error {
	if cfg.importLoader == nil {
		cfg.importLoader = loader
	}
	if source.Path == "" {
		return nil
	}
	if err := loader.recoverConfigImportPublicationIfPresent(); err != nil {
		return err
	}
	data, err := os.ReadFile(source.Path)
	if err != nil {
		if source.MissingOK && os.IsNotExist(err) {
			return nil
		}
		return err
	}
	mainConfig, err := decodeFileConfig(data, source.Path)
	if err != nil {
		return err
	}
	if configImportsRemoteSource(mainConfig.Imports) && loader.cacheLock == nil && strings.TrimSpace(loader.homeDir) != "" {
		if err := loader.ensureConfigImportCacheLock(); err != nil {
			return err
		}
		// A workspace writer may have committed or rolled back after the first
		// read but before this reader acquired the cache-generation lock.
		// Re-read under the lock so the declaring YAML and its LKG generation
		// always come from the same publication.
		data, err = os.ReadFile(source.Path)
		if err != nil {
			if source.MissingOK && os.IsNotExist(err) {
				return nil
			}
			return err
		}
	}
	return applyYAMLContentWithImportLoader(cfg, data, source, loader, opts)
}

func configImportsRemoteSource(imports []importConfig) bool {
	for _, item := range imports {
		if _, ok := remoteConfigImportIdentity(item.Source); ok {
			return true
		}
	}
	return false
}

func remoteConfigImportIdentity(rawSource string) (string, bool) {
	rawSource = strings.TrimSpace(rawSource)
	if filepath.IsAbs(rawSource) || !strings.Contains(rawSource, "://") {
		return "", false
	}
	parsed, err := url.Parse(rawSource)
	if err != nil || (!strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https")) {
		return "", false
	}
	return parsed.String(), true
}

func applyYAMLContentWithImportLoader(cfg *Config, data []byte, source yamlConfigSource, loader *configImportLoader, opts applyYAMLDataOptions) error {
	if cfg.importLoader == nil {
		cfg.importLoader = loader
	}
	mainConfig, err := decodeFileConfig(data, source.Path)
	if err != nil {
		return err
	}
	for i, item := range mainConfig.Imports {
		if strings.TrimSpace(item.Source) == "" {
			return fmt.Errorf("config: %s imports[%d]: source is required", source.Path, i)
		}
	}

	documents := make([]configImportDocument, 0, len(mainConfig.Imports))
	for i, item := range mainConfig.Imports {
		document, loadErr := loader.load(source, item.Source)
		if loadErr != nil {
			return fmt.Errorf("config: %s imports[%d] %s: %w", source.Path, i, safeImportSource(item.Source), loadErr)
		}
		nested, parseErr := topLevelYAMLKeyPresent(document.data, "imports")
		if parseErr != nil {
			return fmt.Errorf("config: %s imports[%d] %s: config: parse %s: %w", source.Path, i, document.source.Path, document.source.Path, parseErr)
		}
		if nested {
			return fmt.Errorf("config: %s imports[%d] %s: nested imports are not supported", source.Path, i, document.source.Path)
		}
		if _, parseErr := decodeFileConfig(document.data, document.source.Path); parseErr != nil {
			return fmt.Errorf("config: %s imports[%d] %s: %w", source.Path, i, document.source.Path, parseErr)
		}
		documents = append(documents, document)
	}

	staged := cloneConfigForImport(cfg)
	for i, document := range documents {
		if err := applyYAMLDataWithOptions(&staged, document.data, document.source, opts); err != nil {
			return fmt.Errorf("config: %s imports[%d] %s: %w", source.Path, i, document.source.Path, err)
		}
		if !opts.SkipImportBookkeeping {
			staged.importStatuses = append(staged.importStatuses, document.status)
		}
	}
	if err := applyYAMLDataWithOptions(&staged, data, source, opts); err != nil {
		return err
	}
	if !opts.SkipImportBookkeeping {
		for _, document := range documents {
			if document.cacheWrite != nil {
				staged.pendingImportCache = append(staged.pendingImportCache, *document.cacheWrite)
			}
		}
	}
	*cfg = staged
	return nil
}

func decodeFileConfig(data []byte, path string) (fileConfig, error) {
	var fc fileConfig
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&fc); err != nil {
		return fileConfig{}, fmt.Errorf("config: parse %s: %w", path, err)
	}
	return fc, nil
}

func (l *configImportLoader) load(declaring yamlConfigSource, rawSource string) (configImportDocument, error) {
	rawSource = strings.TrimSpace(rawSource)
	if filepath.IsAbs(rawSource) {
		return l.loadLocal(declaring, rawSource)
	}
	if !strings.Contains(rawSource, "://") {
		return l.loadLocal(declaring, rawSource)
	}
	parsed, err := url.Parse(rawSource)
	if err != nil {
		return configImportDocument{}, errors.New("invalid source syntax")
	}
	if !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return configImportDocument{}, fmt.Errorf("unsupported URL scheme %q; only http and https are allowed", parsed.Scheme)
	}
	identity := parsed.String()
	if document, ok := l.remoteMemo[identity]; ok {
		document.source.Scope = declaring.Scope
		if document.cacheWrite != nil {
			record := *document.cacheWrite
			record.DeclaringSHA256 = declaringConfigDigest(declaring.Path)
			record.ContextSHA256 = l.cacheContextDigest()
			record.cachePath = l.cachePath(identity, declaring.Path)
			document.cacheWrite = &record
		}
		return document, nil
	}
	document, err := l.loadRemote(declaring, parsed)
	if err != nil {
		return configImportDocument{}, err
	}
	if document.status.State == "fresh" {
		l.remoteMemo[identity] = document
	}
	return document, nil
}

func (l *configImportLoader) loadLocal(declaring yamlConfigSource, path string) (configImportDocument, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(filepath.Dir(declaring.Path), path)
	}
	absPath, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return configImportDocument{}, fmt.Errorf("resolve local source: %w", err)
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return configImportDocument{}, fmt.Errorf("read local source: %w", err)
	}
	return configImportDocument{
		data:   data,
		source: yamlConfigSource{Path: absPath, Scope: declaring.Scope},
		status: ConfigImportStatus{Source: absPath, State: "fresh", Digest: contentDigest(data)},
	}, nil
}

func (l *configImportLoader) loadRemote(declaring yamlConfigSource, parsed *url.URL) (configImportDocument, error) {
	if strings.TrimSpace(l.homeDir) == "" {
		return configImportDocument{}, errors.New("remote imports require a configured JUEX_HOME")
	}
	if parsed.Host == "" {
		return configImportDocument{}, errors.New("remote source host is required")
	}
	if parsed.User != nil {
		return configImportDocument{}, errors.New("remote source must not contain URL user information")
	}
	if parsed.Fragment != "" {
		return configImportDocument{}, errors.New("remote source must not contain a fragment")
	}
	identity := parsed.String()
	safeSource := sanitizedRemoteSource(parsed)
	cache, cacheErr := l.readCache(identity, declaring.Path)

	ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, identity, nil)
	if err != nil {
		return configImportDocument{}, errors.New("create request from remote source")
	}
	if cacheErr == nil {
		if cache.ETag != "" {
			req.Header.Set("If-None-Match", cache.ETag)
		}
		if cache.LastModified != "" {
			req.Header.Set("If-Modified-Since", cache.LastModified)
		}
	}
	client := *l.client
	priorRedirect := client.CheckRedirect
	redirected := false
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > l.maxRedirects {
			return fmt.Errorf("too many redirects (maximum %d)", l.maxRedirects)
		}
		if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
			return fmt.Errorf("redirect uses unsupported scheme %q", req.URL.Scheme)
		}
		if len(via) > 0 && strings.EqualFold(via[len(via)-1].URL.Scheme, "https") && strings.EqualFold(req.URL.Scheme, "http") {
			return errors.New("redirect from https to http is not allowed")
		}
		if req.URL.User != nil || req.URL.Fragment != "" {
			return errors.New("redirect URL contains forbidden user information or fragment")
		}
		// net/http derives Referer from the prior request, including its query.
		// Imports do not need redirect referrers, so remove it before a redirect
		// can disclose a signed URL or query token to another origin.
		req.Header.Del("Referer")
		req.Header.Del("If-None-Match")
		req.Header.Del("If-Modified-Since")
		if priorRedirect != nil {
			if err := priorRedirect(req, via); err != nil {
				return err
			}
		}
		// A custom callback must not reattach validators for the original
		// resource to the redirect target.
		req.Header.Del("If-None-Match")
		req.Header.Del("If-Modified-Since")
		redirected = true
		return nil
	}
	resp, requestErr := client.Do(req)
	if requestErr != nil {
		return l.staleOrError(declaring, identity, safeSource, cache, cacheErr, fmt.Errorf("request failed: %w", safeHTTPError(requestErr)))
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		if redirected {
			return configImportDocument{}, errors.New("server returned 304 after a redirect without a matching validator")
		}
		if cacheErr != nil {
			return configImportDocument{}, errors.New("server returned 304 without a valid cache entry")
		}
		cache.FetchedAt = l.now().UTC()
		if value := resp.Header.Get("ETag"); value != "" {
			cache.ETag = boundedCacheValidator(value)
		}
		if value := resp.Header.Get("Last-Modified"); value != "" {
			cache.LastModified = boundedCacheValidator(value)
		}
		return l.remoteDocument(declaring, safeSource, cache, "fresh", true), nil
	}
	if resp.StatusCode != http.StatusOK {
		statusErr := fmt.Errorf("server returned HTTP %d", resp.StatusCode)
		if resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			return l.staleOrError(declaring, identity, safeSource, cache, cacheErr, statusErr)
		}
		return configImportDocument{}, statusErr
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, l.maxBytes+1))
	if err != nil {
		return l.staleOrError(declaring, identity, safeSource, cache, cacheErr, fmt.Errorf("read response: %w", err))
	}
	if int64(len(data)) > l.maxBytes {
		return configImportDocument{}, fmt.Errorf("response exceeds %d byte limit", l.maxBytes)
	}
	record := configImportCacheRecord{
		Version:         configImportCacheVersion,
		Source:          safeSource,
		SourceSHA256:    sourceDigest(identity),
		DeclaringSHA256: declaringConfigDigest(declaring.Path),
		ContextSHA256:   l.cacheContextDigest(),
		ETag:            boundedCacheValidator(resp.Header.Get("ETag")),
		LastModified:    boundedCacheValidator(resp.Header.Get("Last-Modified")),
		FetchedAt:       l.now().UTC(),
		ContentSHA256:   contentDigest(data),
		Content:         string(data),
		cachePath:       l.cachePath(identity, declaring.Path),
	}
	return l.remoteDocument(declaring, safeSource, record, "fresh", true), nil
}

func (l *configImportLoader) staleOrError(declaring yamlConfigSource, identity, safeSource string, cache configImportCacheRecord, cacheErr, cause error) (configImportDocument, error) {
	if cacheErr == nil && l.cacheUsable(cache) {
		return l.remoteDocument(declaring, safeSource, cache, "stale", false), nil
	}
	if cacheErr != nil && !errors.Is(cacheErr, os.ErrNotExist) {
		return configImportDocument{}, fmt.Errorf("%v; cached Last-Known-Good is invalid: %w", cause, cacheErr)
	}
	if cacheErr == nil {
		return configImportDocument{}, fmt.Errorf("%v; cached Last-Known-Good for %s is expired", cause, safeImportSource(identity))
	}
	return configImportDocument{}, fmt.Errorf("%v; no valid Last-Known-Good cache", cause)
}

func (l *configImportLoader) remoteDocument(declaring yamlConfigSource, safeSource string, record configImportCacheRecord, state string, write bool) configImportDocument {
	document := configImportDocument{
		data:   []byte(record.Content),
		source: yamlConfigSource{Path: safeSource, Scope: declaring.Scope},
		status: ConfigImportStatus{Source: safeSource, State: state, Digest: record.ContentSHA256, FetchedAt: record.FetchedAt},
	}
	if write {
		document.cacheWrite = &record
	}
	return document
}

func (l *configImportLoader) cacheUsable(record configImportCacheRecord) bool {
	now := l.now().UTC()
	return !record.FetchedAt.IsZero() && !record.FetchedAt.After(now) && now.Sub(record.FetchedAt) <= l.maxCacheAge
}

func (l *configImportLoader) readCache(identity, declaringPath string) (configImportCacheRecord, error) {
	if err := l.ensureConfigImportCacheLock(); err != nil {
		return configImportCacheRecord{}, err
	}
	return l.readCachePath(l.cachePath(identity, declaringPath), identity, declaringPath, l.cacheContextDigest())
}

type configImportCacheReference struct {
	identity      string
	declaringPath string
}

func (l *configImportLoader) selectNewestCompleteCacheContext(references []configImportCacheReference) (string, error) {
	if len(references) == 0 {
		return "", nil
	}
	if err := l.ensureConfigImportCacheLock(); err != nil {
		return "", err
	}
	type candidate struct {
		oldestFetch time.Time
	}
	candidates := make(map[string]candidate)
	seen := make(map[configImportCacheReference]struct{}, len(references))
	first := true
	for _, reference := range references {
		if _, duplicate := seen[reference]; duplicate {
			continue
		}
		seen[reference] = struct{}{}
		records := l.usableCacheRecordsByContext(reference.identity, reference.declaringPath)
		if first {
			for contextDigest, record := range records {
				candidates[contextDigest] = candidate{oldestFetch: record.FetchedAt}
			}
			first = false
			continue
		}
		for contextDigest, current := range candidates {
			record, ok := records[contextDigest]
			if !ok {
				delete(candidates, contextDigest)
				continue
			}
			if record.FetchedAt.Before(current.oldestFetch) {
				current.oldestFetch = record.FetchedAt
				candidates[contextDigest] = current
			}
		}
	}
	var selected string
	var selectedOldest time.Time
	for contextDigest, current := range candidates {
		if selected == "" || current.oldestFetch.After(selectedOldest) ||
			(current.oldestFetch.Equal(selectedOldest) && contextDigest < selected) {
			selected = contextDigest
			selectedOldest = current.oldestFetch
		}
	}
	return selected, nil
}

func (l *configImportLoader) usableCacheRecordsByContext(identity, declaringPath string) map[string]configImportCacheRecord {
	dir := filepath.Join(l.homeDir, "cache", "config-imports")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	prefix := sourceDigest(identity) + "-" + declaringConfigDigest(declaringPath) + "-"
	records := make(map[string]configImportCacheRecord)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".json") {
			continue
		}
		contextDigest := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".json")
		if len(contextDigest) != sha256.Size*2 {
			continue
		}
		if decoded, decodeErr := hex.DecodeString(contextDigest); decodeErr != nil || len(decoded) != sha256.Size {
			continue
		}
		record, readErr := l.readCachePath(filepath.Join(dir, name), identity, declaringPath, contextDigest)
		if readErr != nil || !l.cacheUsable(record) {
			continue
		}
		records[contextDigest] = record
	}
	return records
}

func (l *configImportLoader) readCachePath(path, identity, declaringPath, contextDigest string) (configImportCacheRecord, error) {
	info, err := os.Stat(path)
	if err != nil {
		return configImportCacheRecord{}, err
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		return configImportCacheRecord{}, fmt.Errorf("cache file permissions are %o, want 0600", info.Mode().Perm())
	}
	file, err := os.Open(path)
	if err != nil {
		return configImportCacheRecord{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 8*l.maxBytes+4097))
	if err != nil {
		return configImportCacheRecord{}, err
	}
	if int64(len(data)) > 8*l.maxBytes+4096 {
		return configImportCacheRecord{}, errors.New("cache record is too large")
	}
	var record configImportCacheRecord
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&record); err != nil {
		return configImportCacheRecord{}, fmt.Errorf("decode cache record: %w", err)
	}
	if err := requireJSONEOF(dec); err != nil {
		return configImportCacheRecord{}, err
	}
	if record.Version != configImportCacheVersion ||
		record.SourceSHA256 != sourceDigest(identity) ||
		record.DeclaringSHA256 != declaringConfigDigest(declaringPath) ||
		record.ContextSHA256 != contextDigest {
		return configImportCacheRecord{}, errors.New("cache identity does not match source")
	}
	parsed, err := url.Parse(identity)
	if err != nil || record.Source != sanitizedRemoteSource(parsed) {
		return configImportCacheRecord{}, errors.New("cache source metadata does not match source")
	}
	if int64(len(record.Content)) > l.maxBytes {
		return configImportCacheRecord{}, errors.New("cache content exceeds response size limit")
	}
	if record.ETag != boundedCacheValidator(record.ETag) || record.LastModified != boundedCacheValidator(record.LastModified) {
		return configImportCacheRecord{}, errors.New("cache validator metadata is invalid")
	}
	if record.ContentSHA256 == "" || record.ContentSHA256 != contentDigest([]byte(record.Content)) {
		return configImportCacheRecord{}, errors.New("cache content digest mismatch")
	}
	record.cachePath = path
	return record, nil
}

func requireJSONEOF(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return fmt.Errorf("decode cache record trailer: %w", err)
	}
	return errors.New("cache record contains trailing JSON data")
}

func marshalConfigImportCache(record configImportCacheRecord) ([]byte, error) {
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	return data, nil
}

func commitConfigImportCaches(cfg *Config) error {
	var lock *homestore.Lock
	if cfg.importLoader != nil {
		lock = cfg.importLoader.takeConfigImportCacheLock()
	}
	return commitConfigImportCachesWithWriterAndLock(cfg, func(path string, data []byte) error {
		return homestore.WriteFileAtomic(path, data, 0o600, 0o700)
	}, lock)
}

type configImportCacheCommit struct {
	record       configImportCacheRecord
	data         []byte
	previous     []byte
	previousMode os.FileMode
	existed      bool
}

type configImportCacheJournal struct {
	Version   int                                `json:"version"`
	State     string                             `json:"state"`
	Entries   []configImportCacheJournalEntry    `json:"entries"`
	Workspace *configImportWorkspaceJournalEntry `json:"workspace,omitempty"`
}

type configImportCacheJournalEntry struct {
	CacheFile    string `json:"cache_file"`
	Previous     []byte `json:"previous,omitempty"`
	PreviousMode uint32 `json:"previous_mode,omitempty"`
	Existed      bool   `json:"existed"`
}

type configImportWorkspaceJournalEntry struct {
	Path         string `json:"path"`
	Previous     []byte `json:"previous,omitempty"`
	PreviousMode uint32 `json:"previous_mode,omitempty"`
	Existed      bool   `json:"existed"`
}

func commitConfigImportCachesWithWriter(cfg *Config, publish func(string, []byte) error) (returnErr error) {
	return commitConfigImportCachesWithWriterAndLock(cfg, publish, nil)
}

func commitConfigImportCachesWithWriterAndLock(
	cfg *Config,
	publish func(string, []byte) error,
	lock *homestore.Lock,
) (returnErr error) {
	lockHome := strings.TrimSpace(cfg.HomeJuexDir)
	if lockHome == "" && len(cfg.pendingImportCache) > 0 {
		lockHome = filepath.Dir(filepath.Dir(filepath.Dir(cfg.pendingImportCache[0].cachePath)))
	}
	if lock == nil && len(cfg.pendingImportCache) > 0 {
		var err error
		lock, err = homestore.AcquireLock(configImportCacheLockPath(lockHome), homestore.LockWait)
		if err != nil {
			return fmt.Errorf("config: lock import cache publication: %w", err)
		}
	}
	if lock != nil {
		defer func() {
			if err := lock.Close(); err != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("config: unlock import cache publication: %w", err))
			}
		}()
	}
	return commitConfigImportCachesWhileLocked(cfg, publish)
}

func commitConfigImportCachesWhileLocked(cfg *Config, publish func(string, []byte) error) error {
	writes := uniqueConfigImportCacheWrites(cfg.pendingImportCache)
	lockHome := strings.TrimSpace(cfg.HomeJuexDir)
	if lockHome == "" && len(writes) > 0 {
		lockHome = filepath.Dir(filepath.Dir(filepath.Dir(writes[0].cachePath)))
	}
	if len(writes) > 0 {
		if err := recoverConfigImportCachePublication(lockHome); err != nil {
			return fmt.Errorf("config: recover import cache publication: %w", err)
		}
	}
	cfg.pendingImportCache = nil
	if len(writes) == 0 {
		return nil
	}
	commits, err := prepareConfigImportCacheCommits(writes)
	if err != nil {
		return err
	}
	journalPath, err := beginConfigImportCachePublication(commits)
	if err != nil {
		return err
	}
	if err := publishConfigImportCacheCommits(commits, publish); err != nil {
		rollbackErr := recoverConfigImportCachePublicationAt(journalPath)
		return errors.Join(err, rollbackErr)
	}
	if err := markConfigImportCachePublicationCommitted(journalPath); err != nil {
		if journal, readErr := readConfigImportCacheJournal(journalPath); readErr == nil && journal.State == configImportJournalCommitted {
			_ = clearConfigImportCacheJournal(journalPath)
			return nil
		}
		rollbackErr := recoverConfigImportCachePublicationAt(journalPath)
		return errors.Join(fmt.Errorf("config: commit import cache publication: %w", err), rollbackErr)
	}
	// Once the committed marker is durable, either this process or the next
	// reader may remove it without changing the selected cache generation.
	_ = clearConfigImportCacheJournal(journalPath)
	return nil
}

func publishPendingConfigImportCachesWhileLocked(cfg *Config, publish func(string, []byte) error) error {
	writes := uniqueConfigImportCacheWrites(cfg.pendingImportCache)
	cfg.pendingImportCache = nil
	commits, err := prepareConfigImportCacheCommits(writes)
	if err != nil {
		return err
	}
	return publishConfigImportCacheCommits(commits, publish)
}

func publishConfigImportCacheCommits(commits []configImportCacheCommit, publish func(string, []byte) error) error {
	for _, commit := range commits {
		if err := publish(commit.record.cachePath, commit.data); err != nil {
			return fmt.Errorf("config: cache import %s: %w", commit.record.Source, err)
		}
	}
	return nil
}

func (l *configImportLoader) ensureConfigImportCacheLock() error {
	if l.cacheLock != nil {
		return nil
	}
	lock, err := homestore.AcquireLock(configImportCacheLockPath(l.homeDir), homestore.LockWait)
	if err != nil {
		return fmt.Errorf("lock import cache read: %w", err)
	}
	if err := recoverConfigImportCachePublication(l.homeDir); err != nil {
		return errors.Join(fmt.Errorf("recover interrupted import cache publication: %w", err), lock.Close())
	}
	l.cacheLock = lock
	return nil
}

func (l *configImportLoader) recoverConfigImportPublicationIfPresent() error {
	if l.cacheLock != nil || strings.TrimSpace(l.homeDir) == "" {
		return nil
	}
	if _, err := os.Stat(configImportCacheJournalPath(l.homeDir)); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect interrupted import cache publication: %w", err)
	}
	return l.ensureConfigImportCacheLock()
}

func (l *configImportLoader) takeConfigImportCacheLock() *homestore.Lock {
	lock := l.cacheLock
	l.cacheLock = nil
	return lock
}

func (l *configImportLoader) closeConfigImportCacheLock() error {
	lock := l.takeConfigImportCacheLock()
	if lock == nil {
		return nil
	}
	if err := lock.Close(); err != nil {
		return fmt.Errorf("unlock import cache read: %w", err)
	}
	return nil
}

func configImportCacheLockPath(homeDir string) string {
	return filepath.Join(homeDir, ".locks", "config-imports-cache.lock")
}

func configImportCacheJournalPath(homeDir string) string {
	return filepath.Join(homeDir, "cache", "config-imports", configImportJournalName)
}

func beginConfigImportCachePublication(commits []configImportCacheCommit) (string, error) {
	if len(commits) == 0 {
		return "", errors.New("config: import cache publication is empty")
	}
	homeDir := filepath.Dir(filepath.Dir(filepath.Dir(commits[0].record.cachePath)))
	return beginConfigImportCachePublicationWithWorkspace(homeDir, commits, nil)
}

func beginConfigImportCachePublicationWithWorkspace(
	homeDir string,
	commits []configImportCacheCommit,
	workspace *workspaceConfigSnapshot,
) (string, error) {
	if len(commits) == 0 && workspace == nil {
		return "", errors.New("config: import cache publication is empty")
	}
	homeDir = strings.TrimSpace(homeDir)
	if homeDir == "" {
		return "", errors.New("config: import cache publication requires a configured JUEX_HOME")
	}
	dir := filepath.Dir(configImportCacheJournalPath(homeDir))
	journal := configImportCacheJournal{
		Version: configImportJournalVersion,
		State:   configImportJournalPrepared,
		Entries: make([]configImportCacheJournalEntry, 0, len(commits)),
	}
	if workspace != nil {
		journal.Workspace = &configImportWorkspaceJournalEntry{
			Path:         workspace.path,
			Previous:     workspace.data,
			PreviousMode: uint32(workspace.mode.Perm()),
			Existed:      workspace.existed,
		}
	}
	for _, commit := range commits {
		if filepath.Dir(commit.record.cachePath) != dir {
			return "", errors.New("config: import cache publication spans multiple cache directories")
		}
		journal.Entries = append(journal.Entries, configImportCacheJournalEntry{
			CacheFile:    filepath.Base(commit.record.cachePath),
			Previous:     commit.previous,
			PreviousMode: uint32(commit.previousMode.Perm()),
			Existed:      commit.existed,
		})
	}
	path := filepath.Join(dir, configImportJournalName)
	if err := writeConfigImportCacheJournal(path, journal); err != nil {
		return "", fmt.Errorf("config: prepare import cache publication journal: %w", err)
	}
	return path, nil
}

func markConfigImportCachePublicationCommitted(path string) error {
	journal, err := readConfigImportCacheJournal(path)
	if err != nil {
		return err
	}
	if journal.State != configImportJournalPrepared {
		return fmt.Errorf("publication journal state is %q, want %q", journal.State, configImportJournalPrepared)
	}
	journal.State = configImportJournalCommitted
	return writeConfigImportCacheJournal(path, journal)
}

func writeConfigImportCacheJournal(path string, journal configImportCacheJournal) error {
	data, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if len(data) > configImportJournalMaxBytes {
		return fmt.Errorf("publication journal exceeds %d byte limit", configImportJournalMaxBytes)
	}
	return homestore.WriteFileAtomic(path, data, 0o600, 0o700)
}

func recoverConfigImportCachePublication(homeDir string) error {
	return recoverConfigImportCachePublicationAt(configImportCacheJournalPath(homeDir))
}

func recoverConfigImportCachePublicationAt(path string) error {
	journal, err := readConfigImportCacheJournal(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if journal.State == configImportJournalPrepared {
		dir := filepath.Dir(path)
		var rollbackErr error
		for index := len(journal.Entries) - 1; index >= 0; index-- {
			entry := journal.Entries[index]
			cachePath := filepath.Join(dir, entry.CacheFile)
			var restoreErr error
			if entry.Existed {
				restoreErr = homestore.WriteFileAtomic(cachePath, entry.Previous, os.FileMode(entry.PreviousMode), 0o700)
			} else {
				restoreErr = os.Remove(cachePath)
				if errors.Is(restoreErr, os.ErrNotExist) {
					restoreErr = nil
				}
				if restoreErr == nil {
					restoreErr = homestore.SyncDir(dir)
				}
			}
			if restoreErr != nil {
				rollbackErr = errors.Join(rollbackErr, fmt.Errorf("restore %s: %w", entry.CacheFile, restoreErr))
			}
		}
		if journal.Workspace != nil {
			workspace := workspaceConfigSnapshot{
				path:    journal.Workspace.Path,
				data:    journal.Workspace.Previous,
				mode:    os.FileMode(journal.Workspace.PreviousMode),
				existed: journal.Workspace.Existed,
			}
			rollbackErr = errors.Join(rollbackErr, rollbackWorkspaceConfig(workspace))
		}
		if rollbackErr != nil {
			return rollbackErr
		}
	}
	return clearConfigImportCacheJournal(path)
}

func readConfigImportCacheJournal(path string) (configImportCacheJournal, error) {
	info, err := os.Stat(path)
	if err != nil {
		return configImportCacheJournal{}, err
	}
	if !info.Mode().IsRegular() {
		return configImportCacheJournal{}, errors.New("publication journal is not a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		return configImportCacheJournal{}, fmt.Errorf("publication journal permissions are %o, want 0600", info.Mode().Perm())
	}
	file, err := os.Open(path)
	if err != nil {
		return configImportCacheJournal{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, configImportJournalMaxBytes+1))
	if err != nil {
		return configImportCacheJournal{}, err
	}
	if len(data) > configImportJournalMaxBytes {
		return configImportCacheJournal{}, fmt.Errorf("publication journal exceeds %d byte limit", configImportJournalMaxBytes)
	}
	var journal configImportCacheJournal
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&journal); err != nil {
		return configImportCacheJournal{}, fmt.Errorf("decode publication journal: %w", err)
	}
	if err := requireJSONEOF(dec); err != nil {
		return configImportCacheJournal{}, err
	}
	if journal.Version != configImportJournalVersion ||
		(journal.State != configImportJournalPrepared && journal.State != configImportJournalCommitted) ||
		(len(journal.Entries) == 0 && journal.Workspace == nil) {
		return configImportCacheJournal{}, errors.New("publication journal metadata is invalid")
	}
	seen := make(map[string]struct{}, len(journal.Entries))
	for _, entry := range journal.Entries {
		if !validConfigImportCacheFilename(entry.CacheFile) {
			return configImportCacheJournal{}, errors.New("publication journal cache filename is invalid")
		}
		if _, duplicate := seen[entry.CacheFile]; duplicate {
			return configImportCacheJournal{}, errors.New("publication journal contains a duplicate cache file")
		}
		seen[entry.CacheFile] = struct{}{}
		if entry.PreviousMode > 0o777 || (!entry.Existed && (len(entry.Previous) != 0 || entry.PreviousMode != 0)) {
			return configImportCacheJournal{}, errors.New("publication journal prior state is invalid")
		}
	}
	if workspace := journal.Workspace; workspace != nil {
		cleanPath := filepath.Clean(workspace.Path)
		workspaceDir := filepath.Dir(cleanPath)
		if !filepath.IsAbs(workspace.Path) || cleanPath != workspace.Path ||
			filepath.Base(cleanPath) != "juex.yaml" || filepath.Base(workspaceDir) != ".juex" ||
			workspace.PreviousMode > 0o777 ||
			(!workspace.Existed && (len(workspace.Previous) != 0 || workspace.PreviousMode != 0)) {
			return configImportCacheJournal{}, errors.New("publication journal workspace prior state is invalid")
		}
	}
	return journal, nil
}

func validConfigImportCacheFilename(name string) bool {
	if filepath.Base(name) != name || !strings.HasSuffix(name, ".json") {
		return false
	}
	parts := strings.Split(strings.TrimSuffix(name, ".json"), "-")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		decoded, err := hex.DecodeString(part)
		if err != nil || len(decoded) != sha256.Size {
			return false
		}
	}
	return true
}

func clearConfigImportCacheJournal(path string) error {
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return homestore.SyncDir(filepath.Dir(path))
}

func uniqueConfigImportCacheWrites(writes []configImportCacheRecord) []configImportCacheRecord {
	unique := make([]configImportCacheRecord, 0, len(writes))
	indices := make(map[string]int, len(writes))
	for _, record := range writes {
		if index, ok := indices[record.cachePath]; ok {
			unique[index] = record
			continue
		}
		indices[record.cachePath] = len(unique)
		unique = append(unique, record)
	}
	return unique
}

func prepareConfigImportCacheCommits(writes []configImportCacheRecord) ([]configImportCacheCommit, error) {
	commits := make([]configImportCacheCommit, 0, len(writes))
	for _, record := range writes {
		if strings.TrimSpace(record.cachePath) == "" {
			return nil, fmt.Errorf("config: cache import %s: cache path is empty", record.Source)
		}
		data, err := marshalConfigImportCache(record)
		if err != nil {
			return nil, fmt.Errorf("config: cache import %s: %w", record.Source, err)
		}
		commit := configImportCacheCommit{record: record, data: data}
		info, err := os.Stat(record.cachePath)
		switch {
		case err == nil:
			if !info.Mode().IsRegular() {
				return nil, fmt.Errorf("config: cache import %s: existing cache is not a regular file", record.Source)
			}
			commit.previous, err = os.ReadFile(record.cachePath)
			if err != nil {
				return nil, fmt.Errorf("config: cache import %s: read existing cache: %w", record.Source, err)
			}
			commit.previousMode = info.Mode().Perm()
			commit.existed = true
		case errors.Is(err, os.ErrNotExist):
		case err != nil:
			return nil, fmt.Errorf("config: cache import %s: inspect existing cache: %w", record.Source, err)
		}
		commits = append(commits, commit)
	}
	return commits, nil
}

func (l *configImportLoader) cachePath(identity, declaringPath string) string {
	filename := sourceDigest(identity) + "-" + declaringConfigDigest(declaringPath) + "-" + l.cacheContextDigest() + ".json"
	return filepath.Join(l.homeDir, "cache", "config-imports", filename)
}

func (l *configImportLoader) cacheContextDigest() string {
	if strings.TrimSpace(l.contextDigest) != "" {
		return l.contextDigest
	}
	return sourceDigest("standalone")
}

func configImportContextDigest(workDir string, explicitPaths ...string) string {
	identities := []string{"work_dir=" + declaringConfigIdentity(workDir)}
	for _, path := range explicitPaths {
		if strings.TrimSpace(path) != "" {
			identities = append(identities, "explicit="+declaringConfigIdentity(path))
		}
	}
	return sourceDigest(strings.Join(identities, "\n"))
}

func declaringConfigDigest(path string) string {
	return sourceDigest(declaringConfigIdentity(path))
}

func declaringConfigIdentity(path string) string {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return filepath.Clean(path)
	}
	parent, base := filepath.Dir(abs), filepath.Base(abs)
	unresolved := []string{base}
	for {
		resolved, resolveErr := filepath.EvalSymlinks(parent)
		if resolveErr == nil {
			for index := len(unresolved) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, unresolved[index])
			}
			return resolved
		}
		if !errors.Is(resolveErr, os.ErrNotExist) {
			return abs
		}
		next := filepath.Dir(parent)
		if next == parent {
			return abs
		}
		unresolved = append(unresolved, filepath.Base(parent))
		parent = next
	}
}

func sourceDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func contentDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func boundedCacheValidator(value string) string {
	if len(value) > 8192 || strings.ContainsAny(value, "\r\n") {
		return ""
	}
	return value
}

func sanitizedRemoteSource(parsed *url.URL) string {
	clean := *parsed
	clean.User = nil
	clean.RawQuery = ""
	clean.ForceQuery = false
	clean.Fragment = ""
	return clean.String()
}

func safeImportSource(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if filepath.IsAbs(trimmed) {
		return raw
	}
	if !strings.Contains(trimmed, "://") {
		return raw
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "<invalid source>"
	}
	return sanitizedRemoteSource(parsed)
}

func safeHTTPError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("request timed out")
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if errors.Is(urlErr.Err, context.DeadlineExceeded) {
			return errors.New("request timed out")
		}
		message := urlErr.Err.Error()
		if strings.Contains(message, "too many redirects") ||
			strings.Contains(message, "unsupported scheme") ||
			strings.Contains(message, "redirect from https") ||
			strings.Contains(message, "forbidden user information") {
			return errors.New(message)
		}
		return errors.New("network request failed")
	}
	return errors.New("network request failed")
}

func cloneConfigForImport(cfg *Config) Config {
	out := *cfg
	out.Models = append([]string(nil), cfg.Models...)
	out.ProviderHeaders = cloneStringMap(cfg.ProviderHeaders)
	out.ProviderQuery = cloneStringMap(cfg.ProviderQuery)
	out.ProviderCompat.ReasoningReplayFields = append([]string(nil), cfg.ProviderCompat.ReasoningReplayFields...)
	out.Hooks = cloneHooksConfig(cfg.Hooks)
	out.Shell.Args = append([]string(nil), cfg.Shell.Args...)
	out.Sandbox.FileSystem.BlockedPaths = append([]string(nil), cfg.Sandbox.FileSystem.BlockedPaths...)
	out.Skills.Include = append([]string(nil), cfg.Skills.Include...)
	out.Skills.Exclude = append([]string(nil), cfg.Skills.Exclude...)
	out.Modules = cloneModulePolicy(cfg.Modules)
	out.Extensions.Allow = append([]string(nil), cfg.Extensions.Allow...)
	out.AgentStateNotices = append([]string(nil), cfg.AgentStateNotices...)
	out.shellConfig.Args = append([]string(nil), cfg.shellConfig.Args...)
	out.providerConfigs = cloneProviderConfigs(cfg.providerConfigs)
	out.environmentLayers = cloneEnvironmentLayers(cfg.environmentLayers)
	out.importStatuses = append([]ConfigImportStatus(nil), cfg.importStatuses...)
	out.pendingImportCache = append([]configImportCacheRecord(nil), cfg.pendingImportCache...)
	return out
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func cloneHooksConfig(value hooks.Config) hooks.Config {
	out := hooks.Config{Commands: append([]hooks.CommandHook(nil), value.Commands...)}
	for i := range out.Commands {
		out.Commands[i].Events = append([]hooks.EventName(nil), value.Commands[i].Events...)
		out.Commands[i].Tools = append([]string(nil), value.Commands[i].Tools...)
		out.Commands[i].Command = append([]string(nil), value.Commands[i].Command...)
	}
	return out
}

func cloneModulePolicy(value ModulePolicy) ModulePolicy {
	if value == nil {
		return nil
	}
	out := make(ModulePolicy, len(value))
	for key, settings := range value {
		out[key] = settings
	}
	return out
}

func cloneProviderConfigs(values map[string]providerConfig) map[string]providerConfig {
	if values == nil {
		return nil
	}
	out := make(map[string]providerConfig, len(values))
	for id, value := range values {
		value.Headers = cloneStringMap(value.Headers)
		value.Query = cloneStringMap(value.Query)
		value.Compat.ReasoningReplayFields = append([]string(nil), value.Compat.ReasoningReplayFields...)
		value.Models = append([]providerModelConfig(nil), value.Models...)
		for i := range value.Models {
			value.Models[i].Headers = cloneStringMap(value.Models[i].Headers)
			value.Models[i].Query = cloneStringMap(value.Models[i].Query)
			value.Models[i].Compat.ReasoningReplayFields = append([]string(nil), value.Models[i].Compat.ReasoningReplayFields...)
		}
		out[id] = value
	}
	return out
}

func cloneEnvironmentLayers(values []environment.Layer) []environment.Layer {
	out := append([]environment.Layer(nil), values...)
	for i := range out {
		out[i].Values = cloneStringMap(values[i].Values)
	}
	return out
}
