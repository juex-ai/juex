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
	configImportTimeout      = 5 * time.Second
	configImportMaxBytes     = 1 << 20
	configImportMaxRedirects = 3
	configImportMaxCacheAge  = 7 * 24 * time.Hour
	configImportCacheVersion = 2
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
	homeDir      string
	client       *http.Client
	now          func() time.Time
	timeout      time.Duration
	maxBytes     int64
	maxCacheAge  time.Duration
	maxRedirects int
	remoteMemo   map[string]configImportDocument
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
	if source.Path == "" {
		return nil
	}
	data, err := os.ReadFile(source.Path)
	if err != nil {
		if source.MissingOK && os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return applyYAMLContentWithImportLoader(cfg, data, source, loader, opts)
}

func applyYAMLContentWithImportLoader(cfg *Config, data []byte, source yamlConfigSource, loader *configImportLoader, opts applyYAMLDataOptions) error {
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
		staged.importStatuses = append(staged.importStatuses, document.status)
	}
	if err := applyYAMLDataWithOptions(&staged, data, source, opts); err != nil {
		return err
	}
	for _, document := range documents {
		if document.cacheWrite != nil {
			staged.pendingImportCache = append(staged.pendingImportCache, *document.cacheWrite)
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
			record.cachePath = l.cachePath(identity, declaring.Path)
			document.cacheWrite = &record
		}
		return document, nil
	}
	document, err := l.loadRemote(declaring, parsed)
	if err != nil {
		return configImportDocument{}, err
	}
	l.remoteMemo[identity] = document
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
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > l.maxRedirects {
			return fmt.Errorf("too many redirects (maximum %d)", l.maxRedirects)
		}
		if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
			return fmt.Errorf("redirect uses unsupported scheme %q", req.URL.Scheme)
		}
		if strings.EqualFold(parsed.Scheme, "https") && strings.EqualFold(req.URL.Scheme, "http") {
			return errors.New("redirect from https to http is not allowed")
		}
		if req.URL.User != nil || req.URL.Fragment != "" {
			return errors.New("redirect URL contains forbidden user information or fragment")
		}
		if priorRedirect != nil {
			return priorRedirect(req, via)
		}
		return nil
	}
	resp, requestErr := client.Do(req)
	if requestErr != nil {
		return l.staleOrError(declaring, identity, safeSource, cache, cacheErr, fmt.Errorf("request failed: %w", safeHTTPError(requestErr)))
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
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
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
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
	path := l.cachePath(identity, declaringPath)
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
		record.DeclaringSHA256 != declaringConfigDigest(declaringPath) {
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

func writeConfigImportCache(record configImportCacheRecord) error {
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return homestore.WriteFileAtomic(record.cachePath, data, 0o600, 0o700)
}

func commitConfigImportCaches(cfg *Config) error {
	writes := cfg.pendingImportCache
	cfg.pendingImportCache = nil
	for _, record := range writes {
		if err := writeConfigImportCache(record); err != nil {
			return fmt.Errorf("config: cache import %s: %w", record.Source, err)
		}
	}
	return nil
}

func (l *configImportLoader) cachePath(identity, declaringPath string) string {
	filename := sourceDigest(identity) + "-" + declaringConfigDigest(declaringPath) + ".json"
	return filepath.Join(l.homeDir, "cache", "config-imports", filename)
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
	if resolved, resolveErr := filepath.EvalSymlinks(parent); resolveErr == nil {
		return filepath.Join(resolved, base)
	}
	return abs
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
