package config

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestConfigImportsPreserveExistingMergeSemantics(t *testing.T) {
	dir := t.TempDir()
	firstPath := filepath.Join(dir, "first.yaml")
	secondPath := filepath.Join(dir, "second.yaml")
	mainPath := filepath.Join(dir, "juex.yaml")
	writeTextFile(t, firstPath, `providers:
  - id: local
    protocol: openai/chat
    base_url: https://first.example
    headers: {X-Layer: first}
    models:
      - id: test
        headers: {X-Model: first}
hooks:
  commands:
    - name: first
      events: [UserPromptSubmit]
      command: [echo, first]
sandbox:
  file_system:
    blocked_paths: [first]
skills:
  include: [first]
`)
	writeTextFile(t, secondPath, `providers:
  - id: local
    headers: {X-Second: second}
    models:
      - id: test
        query: {layer: second}
hooks:
  commands:
    - name: second
      events: [UserPromptSubmit]
      command: [echo, second]
sandbox:
  file_system:
    blocked_paths: [second]
skills:
  include: [second]
`)
	writeTextFile(t, mainPath, `imports:
  - source: first.yaml
  - source: second.yaml
providers:
  - id: local
    base_url: https://main.example
    headers: {X-Layer: main}
hooks:
  commands:
    - name: main
      events: [UserPromptSubmit]
      command: [echo, main]
sandbox:
  file_system:
    blocked_paths: [main]
skills:
  include: []
`)

	cfg := Config{HomeJuexDir: t.TempDir()}
	source := yamlConfigSource{Path: mainPath, Scope: configScopeInstanceHome}
	if err := applyYAMLFile(&cfg, source); err != nil {
		t.Fatal(err)
	}
	provider := cfg.providerConfigs["local"]
	if provider.BaseURL != "https://main.example" || provider.Headers["X-Layer"] != "main" || provider.Headers["X-Second"] != "second" {
		t.Fatalf("provider merge = %+v", provider)
	}
	if len(provider.Models) != 1 || provider.Models[0].Headers["X-Model"] != "first" || provider.Models[0].Query["layer"] != "second" {
		t.Fatalf("provider model merge = %+v", provider.Models)
	}
	if got := []string{cfg.Hooks.Commands[0].Name, cfg.Hooks.Commands[1].Name, cfg.Hooks.Commands[2].Name}; !reflect.DeepEqual(got, []string{"first", "second", "main"}) {
		t.Fatalf("hooks = %v, want append order", got)
	}
	if cfg.Skills.Include == nil || len(cfg.Skills.Include) != 0 {
		t.Fatalf("skills include = %#v, want explicit empty replacement", cfg.Skills.Include)
	}
	if got := cfg.Sandbox.FileSystem.BlockedPaths; !reflect.DeepEqual(got, []string{"first", "second", "main"}) {
		t.Fatalf("blocked paths = %v, want append order", got)
	}
}

func TestConfigImportsApplyInDeclarationOrderBeforeMainFile(t *testing.T) {
	dir := t.TempDir()
	firstPath := filepath.Join(dir, "first.yaml")
	secondPath := filepath.Join(dir, "nested", "second.yaml")
	mainPath := filepath.Join(dir, "juex.yaml")
	writeTextFile(t, firstPath, `models: [local:first]
runtime:
  tool_timeout: 11s
sandbox:
  file_system:
    blocked_paths: [first-secret]
skills:
  include: [first-skill]
`)
	writeTextFile(t, secondPath, `models: [local:second]
runtime:
  tool_timeout: 22s
sandbox:
  file_system:
    blocked_paths: [second-secret]
skills:
  include: [second-skill]
`)
	writeTextFile(t, mainPath, fmt.Sprintf(`imports:
  - source: %s
  - source: ./nested/second.yaml
models: [local:main]
runtime:
  tool_timeout: 33s
sandbox:
  file_system:
    blocked_paths: [main-secret]
skills:
  include: [main-skill]
`, firstPath))

	cfg := Config{HomeJuexDir: t.TempDir()}
	if err := applyYAMLFile(&cfg, explicitYAMLSource(mainPath)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.Models, []string{"local:main"}) {
		t.Fatalf("models = %v, want main replacement", cfg.Models)
	}
	if cfg.ToolTimeout != 33*time.Second {
		t.Fatalf("tool timeout = %s, want 33s", cfg.ToolTimeout)
	}
	if !reflect.DeepEqual(cfg.Skills.Include, []string{"main-skill"}) {
		t.Fatalf("skills include = %v, want main replacement", cfg.Skills.Include)
	}
	if got, want := cfg.Sandbox.FileSystem.BlockedPaths, []string{"first-secret", "second-secret", "main-secret"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("blocked paths = %v, want %v", got, want)
	}
	statuses := cfg.ImportStatuses()
	if len(statuses) != 2 || statuses[0].Source != firstPath || statuses[1].Source != secondPath {
		t.Fatalf("import statuses = %+v, want resolved sources in declaration order", statuses)
	}
}

func TestConfigImportsRejectNestedImportsIncludingEmptyList(t *testing.T) {
	for _, importedBody := range []string{"imports: []\nmodels: [local:nested]\n", "imports:\n  - source: other.yaml\n"} {
		t.Run(strings.ReplaceAll(strings.TrimSpace(importedBody), "\n", "_"), func(t *testing.T) {
			dir := t.TempDir()
			importedPath := filepath.Join(dir, "imported.yaml")
			mainPath := filepath.Join(dir, "juex.yaml")
			writeTextFile(t, importedPath, importedBody)
			writeTextFile(t, mainPath, "imports:\n  - source: ./imported.yaml\n")

			cfg := Config{HomeJuexDir: t.TempDir()}
			err := applyYAMLFile(&cfg, explicitYAMLSource(mainPath))
			if err == nil {
				t.Fatal("applyYAMLFile() error = nil, want nested imports rejection")
			}
			for _, want := range []string{mainPath, "imports[0]", importedPath, "nested imports"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error = %q, want %q", err, want)
				}
			}
		})
	}
}

func TestConfigImportsAreAtomicAcrossReadParseAndApplyFailures(t *testing.T) {
	dir := t.TempDir()
	firstPath := filepath.Join(dir, "first.yaml")
	secondPath := filepath.Join(dir, "second.yaml")
	mainPath := filepath.Join(dir, "juex.yaml")
	writeTextFile(t, firstPath, `models: [local:changed]
runtime:
  tool_timeout: 99s
environment:
  variables:
    IMPORTED: value
`)
	writeTextFile(t, secondPath, "unknown_field: true\n")
	writeTextFile(t, mainPath, "imports:\n  - source: ./first.yaml\n  - source: ./second.yaml\n")

	cfg := Config{
		Models:          []string{"local:original"},
		ToolTimeout:     7 * time.Second,
		ProviderHeaders: map[string]string{"X-Original": "yes"},
		HomeJuexDir:     t.TempDir(),
	}
	want := cloneConfigForImport(&cfg)
	err := applyYAMLFile(&cfg, explicitYAMLSource(mainPath))
	if err == nil || !strings.Contains(err.Error(), "field unknown_field not found") {
		t.Fatalf("applyYAMLFile() error = %v, want strict imported YAML failure", err)
	}
	for _, text := range []string{mainPath, "imports[1]", secondPath} {
		if !strings.Contains(err.Error(), text) {
			t.Fatalf("error = %q, want actionable context %q", err, text)
		}
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("config changed after failed import chain:\n got: %#v\nwant: %#v", cfg, want)
	}
}

func TestConfigImportsParseMainBeforeFetchingRemoteSources(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()

	dir := t.TempDir()
	mainPath := filepath.Join(dir, "juex.yaml")
	writeTextFile(t, mainPath, "imports:\n  - source: "+server.URL+"/config.yaml\nunknown_field: true\n")
	cfg := Config{HomeJuexDir: t.TempDir()}
	err := applyYAMLFile(&cfg, explicitYAMLSource(mainPath))
	if err == nil || !strings.Contains(err.Error(), "field unknown_field not found") {
		t.Fatalf("applyYAMLFile() error = %v, want main parse failure", err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("remote requests = %d, want 0 before valid main document", got)
	}
}

func TestConfigHTTPSImportCachesValidatedContentAndRevalidatesWithETag(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch requests.Add(1) {
		case 1:
			w.Header().Set("ETag", `"config-v1"`)
			_, _ = w.Write([]byte("runtime:\n  tool_timeout: 41s\n"))
		case 2:
			if got := r.Header.Get("If-None-Match"); got != `"config-v1"` {
				t.Errorf("If-None-Match = %q, want cached ETag", got)
			}
			w.WriteHeader(http.StatusNotModified)
		default:
			t.Errorf("unexpected request %d", requests.Load())
		}
	}))
	defer server.Close()

	home := t.TempDir()
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "juex.yaml")
	writeTextFile(t, mainPath, "imports:\n  - source: "+server.URL+"/config.yaml?token=secret\n")
	loader := newConfigImportLoader(home)
	loader.client = server.Client()
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	loader.now = func() time.Time { return now }

	first := Config{HomeJuexDir: home}
	if err := applyYAMLFileWithImportLoader(&first, explicitYAMLSource(mainPath), loader); err != nil {
		t.Fatal(err)
	}
	if first.ToolTimeout != 41*time.Second {
		t.Fatalf("tool timeout = %s, want imported 41s", first.ToolTimeout)
	}
	statuses := first.ImportStatuses()
	if len(statuses) != 1 || statuses[0].State != "fresh" || statuses[0].Digest == "" {
		t.Fatalf("first import statuses = %+v", statuses)
	}
	if strings.Contains(statuses[0].Source, "secret") {
		t.Fatalf("status source leaked URL query: %q", statuses[0].Source)
	}

	entries, err := os.ReadDir(filepath.Join(home, "cache", "config-imports"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("cache entries = %d, want 1", len(entries))
	}
	cacheInfo, err := entries[0].Info()
	if err != nil {
		t.Fatal(err)
	}
	if got := cacheInfo.Mode().Perm(); runtime.GOOS != "windows" && got != 0o600 {
		t.Fatalf("cache mode = %o, want 600", got)
	}

	now = now.Add(time.Hour)
	second := Config{HomeJuexDir: home}
	if err := applyYAMLFileWithImportLoader(&second, explicitYAMLSource(mainPath), loader); err != nil {
		t.Fatal(err)
	}
	if second.ToolTimeout != 41*time.Second {
		t.Fatalf("tool timeout after 304 = %s, want cached 41s", second.ToolTimeout)
	}
	statuses = second.ImportStatuses()
	if len(statuses) != 1 || statuses[0].State != "fresh" || !statuses[0].FetchedAt.Equal(now) {
		t.Fatalf("second import statuses = %+v, want fresh 304 at %s", statuses, now)
	}
}

func TestConfigRemoteImportUsesCacheOnlyForRetryableFailures(t *testing.T) {
	var status atomic.Int32
	status.Store(http.StatusOK)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(int(status.Load()))
		if status.Load() == http.StatusOK {
			_, _ = w.Write([]byte("runtime:\n  tool_timeout: 52s\n"))
		} else {
			_, _ = w.Write([]byte("top-secret-response-body"))
		}
	}))
	defer server.Close()

	home := t.TempDir()
	mainPath := filepath.Join(t.TempDir(), "juex.yaml")
	writeTextFile(t, mainPath, "imports:\n  - source: "+server.URL+"/config.yaml?token=top-secret-query\n")
	loader := newConfigImportLoader(home)
	now := time.Date(2026, 8, 22, 11, 0, 0, 0, time.UTC)
	loader.now = func() time.Time { return now }

	first := Config{HomeJuexDir: home}
	if err := applyYAMLFileWithImportLoader(&first, explicitYAMLSource(mainPath), loader); err != nil {
		t.Fatal(err)
	}
	status.Store(http.StatusInternalServerError)
	now = now.Add(time.Hour)
	stale := Config{HomeJuexDir: home}
	if err := applyYAMLFileWithImportLoader(&stale, explicitYAMLSource(mainPath), loader); err != nil {
		t.Fatal(err)
	}
	if stale.ToolTimeout != 52*time.Second || len(stale.ImportStatuses()) != 1 || stale.ImportStatuses()[0].State != "stale" {
		t.Fatalf("stale fallback = timeout:%s statuses:%+v", stale.ToolTimeout, stale.ImportStatuses())
	}

	status.Store(http.StatusNotFound)
	nonRetryable := Config{HomeJuexDir: home}
	err := applyYAMLFileWithImportLoader(&nonRetryable, explicitYAMLSource(mainPath), loader)
	if err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("404 error = %v", err)
	}
	if strings.Contains(err.Error(), "top-secret-response-body") || strings.Contains(err.Error(), "top-secret-query") {
		t.Fatalf("404 error leaked secret material: %q", err)
	}

	status.Store(http.StatusInternalServerError)
	now = now.Add(8 * 24 * time.Hour)
	expired := Config{HomeJuexDir: home}
	err = applyYAMLFileWithImportLoader(&expired, explicitYAMLSource(mainPath), loader)
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired cache error = %v", err)
	}
}

func TestConfigRemoteImportDoesNotReplaceLKGWithInvalidContent(t *testing.T) {
	var body atomic.Value
	body.Store("runtime:\n  tool_timeout: 61s\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		value := body.Load().(string)
		if value == "retry" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(value))
	}))
	defer server.Close()

	home := t.TempDir()
	mainPath := filepath.Join(t.TempDir(), "juex.yaml")
	writeTextFile(t, mainPath, fmt.Sprintf("imports:\n  - source: %s/config.yaml\n", server.URL))
	loader := newConfigImportLoader(home)
	loader.now = func() time.Time { return time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC) }
	first := Config{HomeJuexDir: home}
	if err := applyYAMLFileWithImportLoader(&first, explicitYAMLSource(mainPath), loader); err != nil {
		t.Fatal(err)
	}

	body.Store("unknown_field: invalid\n")
	invalid := Config{HomeJuexDir: home}
	err := applyYAMLFileWithImportLoader(&invalid, explicitYAMLSource(mainPath), loader)
	if err == nil || !strings.Contains(err.Error(), "unknown_field") {
		t.Fatalf("invalid update error = %v", err)
	}
	body.Store("retry")
	stale := Config{HomeJuexDir: home}
	if err := applyYAMLFileWithImportLoader(&stale, explicitYAMLSource(mainPath), loader); err != nil {
		t.Fatal(err)
	}
	if stale.ToolTimeout != 61*time.Second || stale.ImportStatuses()[0].State != "stale" {
		t.Fatalf("fallback after invalid update = timeout:%s statuses:%+v", stale.ToolTimeout, stale.ImportStatuses())
	}
}

func TestConfigRemoteImportBoundsTimeoutAndResponseSize(t *testing.T) {
	t.Run("response size", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(strings.Repeat("x", 65)))
		}))
		defer server.Close()
		mainPath := filepath.Join(t.TempDir(), "juex.yaml")
		writeTextFile(t, mainPath, fmt.Sprintf("imports:\n  - source: %s/config.yaml\n", server.URL))
		loader := newConfigImportLoader(t.TempDir())
		loader.maxBytes = 64
		err := applyYAMLFileWithImportLoader(&Config{HomeJuexDir: loader.homeDir}, explicitYAMLSource(mainPath), loader)
		if err == nil || !strings.Contains(err.Error(), "64 byte limit") {
			t.Fatalf("oversize error = %v", err)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(100 * time.Millisecond)
			_, _ = w.Write([]byte("runtime:\n  tool_timeout: 1m\n"))
		}))
		defer server.Close()
		mainPath := filepath.Join(t.TempDir(), "juex.yaml")
		writeTextFile(t, mainPath, fmt.Sprintf("imports:\n  - source: %s/config.yaml\n", server.URL))
		loader := newConfigImportLoader(t.TempDir())
		loader.timeout = 10 * time.Millisecond
		err := applyYAMLFileWithImportLoader(&Config{HomeJuexDir: loader.homeDir}, explicitYAMLSource(mainPath), loader)
		if err == nil || !strings.Contains(err.Error(), "no valid Last-Known-Good cache") {
			t.Fatalf("timeout error = %v", err)
		}
	})
}

func TestConfigRemoteImportControlsRedirects(t *testing.T) {
	t.Run("redirect limit", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/again", http.StatusFound)
		}))
		defer server.Close()
		mainPath := filepath.Join(t.TempDir(), "juex.yaml")
		writeTextFile(t, mainPath, fmt.Sprintf("imports:\n  - source: %s/config.yaml\n", server.URL))
		err := applyYAMLFile(&Config{HomeJuexDir: t.TempDir()}, explicitYAMLSource(mainPath))
		if err == nil || !strings.Contains(err.Error(), "too many redirects") || !strings.Contains(err.Error(), "maximum 3") {
			t.Fatalf("redirect error = %v", err)
		}
	})

	t.Run("https downgrade", func(t *testing.T) {
		plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("runtime:\n  tool_timeout: 1m\n"))
		}))
		defer plain.Close()
		secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, plain.URL+"/config.yaml", http.StatusFound)
		}))
		defer secure.Close()
		mainPath := filepath.Join(t.TempDir(), "juex.yaml")
		writeTextFile(t, mainPath, fmt.Sprintf("imports:\n  - source: %s/config.yaml\n", secure.URL))
		loader := newConfigImportLoader(t.TempDir())
		loader.client = secure.Client()
		err := applyYAMLFileWithImportLoader(&Config{HomeJuexDir: loader.homeDir}, explicitYAMLSource(mainPath), loader)
		if err == nil || !strings.Contains(err.Error(), "redirect from https to http is not allowed") {
			t.Fatalf("downgrade redirect error = %v", err)
		}
	})
}

func TestConfigRemoteImportRejectsTamperedCacheAndRedactsInvalidSource(t *testing.T) {
	t.Run("tampered permissions", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Windows file mode bits do not model Unix secret permissions")
		}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("runtime:\n  tool_timeout: 1m\n"))
		}))
		home := t.TempDir()
		mainPath := filepath.Join(t.TempDir(), "juex.yaml")
		writeTextFile(t, mainPath, fmt.Sprintf("imports:\n  - source: %s/config.yaml\n", server.URL))
		loader := newConfigImportLoader(home)
		if err := applyYAMLFileWithImportLoader(&Config{HomeJuexDir: home}, explicitYAMLSource(mainPath), loader); err != nil {
			t.Fatal(err)
		}
		server.Close()
		entries, err := os.ReadDir(filepath.Join(home, "cache", "config-imports"))
		if err != nil {
			t.Fatal(err)
		}
		cachePath := filepath.Join(home, "cache", "config-imports", entries[0].Name())
		if err := os.Chmod(cachePath, 0o644); err != nil {
			t.Fatal(err)
		}
		err = applyYAMLFileWithImportLoader(&Config{HomeJuexDir: home}, explicitYAMLSource(mainPath), loader)
		if err == nil || !strings.Contains(err.Error(), "permissions") {
			t.Fatalf("tampered cache error = %v", err)
		}
	})

	t.Run("invalid URL", func(t *testing.T) {
		mainPath := filepath.Join(t.TempDir(), "juex.yaml")
		writeTextFile(t, mainPath, "imports:\n  - source: http://example.invalid/%zz?token=url-secret\n")
		err := applyYAMLFile(&Config{HomeJuexDir: t.TempDir()}, explicitYAMLSource(mainPath))
		if err == nil || !strings.Contains(err.Error(), "invalid source syntax") {
			t.Fatalf("invalid URL error = %v", err)
		}
		if strings.Contains(err.Error(), "url-secret") {
			t.Fatalf("invalid URL error leaked query secret: %q", err)
		}
	})
}

func TestConfigImportFailureDoesNotCreateAgentStateOrPublishRemoteCache(t *testing.T) {
	t.Run("agent state", func(t *testing.T) {
		prepareConfigTest(t)
		workDir := t.TempDir()
		writeTextFile(t, filepath.Join(workDir, ".juex", "bad.yaml"), "unknown_field: true\n")
		writeTextFile(t, filepath.Join(workDir, ".juex", "juex.yaml"), "imports:\n  - source: bad.yaml\n")
		if _, err := LoadForWorkDir(workDir); err == nil {
			t.Fatal("LoadForWorkDir() error = nil, want imported config failure")
		}
		if _, err := os.Stat(filepath.Join(workDir, ".juex", "juex.local.json")); !os.IsNotExist(err) {
			t.Fatalf("failed import created agent state: %v", err)
		}
	})

	t.Run("remote cache", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("runtime:\n  tool_timeout: 1m\n"))
		}))
		defer server.Close()
		home := t.TempDir()
		mainPath := filepath.Join(t.TempDir(), "juex.yaml")
		writeTextFile(t, mainPath, fmt.Sprintf("imports:\n  - source: %s/config.yaml\nfleet:\n  addr: 127.0.0.1:5999\n", server.URL))
		err := applyYAMLFile(&Config{HomeJuexDir: home}, explicitYAMLSource(mainPath))
		if err == nil || !strings.Contains(err.Error(), "fleet is only supported") {
			t.Fatalf("main scope error = %v", err)
		}
		if _, err := os.Stat(filepath.Join(home, "cache", "config-imports")); !os.IsNotExist(err) {
			t.Fatalf("failed chain published remote cache: %v", err)
		}
	})
}

func TestConfigImportsPreserveHomeWorkspaceExplicitPriorityAndProvenance(t *testing.T) {
	userHome := prepareConfigTest(t)
	workDir := t.TempDir()
	explicitDir := t.TempDir()

	homeImport := filepath.Join(userHome, ".juex", "home-import.yaml")
	workspaceImport := filepath.Join(workDir, ".juex", "workspace-import.yaml")
	explicitImport := filepath.Join(explicitDir, "explicit-import.yaml")
	writeTextFile(t, homeImport, "runtime:\n  tool_timeout: 10s\nenvironment:\n  variables: {HOME_IMPORTED: yes}\n")
	writeTextFile(t, filepath.Join(userHome, ".juex", "juex.yaml"), "imports:\n  - source: home-import.yaml\nruntime:\n  tool_timeout: 20s\n")
	writeTextFile(t, workspaceImport, "runtime:\n  tool_timeout: 30s\nenvironment:\n  variables: {WORK_IMPORTED: yes}\n")
	writeTextFile(t, filepath.Join(workDir, ".juex", "juex.yaml"), "imports:\n  - source: workspace-import.yaml\nruntime:\n  tool_timeout: 40s\n")
	writeTextFile(t, explicitImport, "runtime:\n  tool_timeout: 50s\nenvironment:\n  variables: {EXPLICIT_IMPORTED: yes}\n")
	explicitPath := filepath.Join(explicitDir, "juex.yaml")
	writeTextFile(t, explicitPath, "imports:\n  - source: explicit-import.yaml\nruntime:\n  tool_timeout: 60s\n")

	cfg, err := LoadFromFileForWorkDirForValidation(explicitPath, workDir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ToolTimeout != 60*time.Second {
		t.Fatalf("tool timeout = %s, want explicit main 60s", cfg.ToolTimeout)
	}
	homeImport = filepath.Join(filepath.Dir(cfg.DefaultHomeRuntimeConfigPath()), "home-import.yaml")
	statuses := cfg.ImportStatuses()
	if len(statuses) != 3 || statuses[0].Source != homeImport || statuses[1].Source != workspaceImport || statuses[2].Source != explicitImport {
		t.Fatalf("import statuses = %+v", statuses)
	}
	metadata := map[string]string{}
	for _, item := range cfg.EnvironmentSnapshot().ConfiguredMetadata() {
		metadata[item.Key] = string(item.Source) + ":" + item.Path
	}
	for key, want := range map[string]string{
		"HOME_IMPORTED":     "user_config:" + homeImport,
		"WORK_IMPORTED":     "workspace_config:" + workspaceImport,
		"EXPLICIT_IMPORTED": "explicit_config:" + explicitImport,
	} {
		if metadata[key] != want {
			t.Fatalf("%s metadata = %q, want %q", key, metadata[key], want)
		}
	}
}

func TestConfigImportedDocumentInheritsDeclaringScope(t *testing.T) {
	prepareConfigTest(t)
	workDir := t.TempDir()
	importedPath := filepath.Join(workDir, ".juex", "imported.yaml")
	writeTextFile(t, importedPath, "fleet:\n  addr: 127.0.0.1:5999\n")
	mainPath := filepath.Join(workDir, ".juex", "juex.yaml")
	writeTextFile(t, mainPath, "imports:\n  - source: imported.yaml\n")

	_, err := LoadForWorkDirForValidation(workDir)
	if err == nil || !strings.Contains(err.Error(), importedPath) || !strings.Contains(err.Error(), "fleet is only supported") {
		t.Fatalf("workspace scope error = %v", err)
	}
}
