package config

import (
	"bytes"
	"errors"
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

	"github.com/juex-ai/juex/internal/homestore"
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

func TestExplicitLoadedHomeConfigReplaysImportsWithoutDuplicatingAppendOnlyValues(t *testing.T) {
	for _, tc := range []struct {
		name       string
		setupHome  func(t *testing.T, userHome string) (string, string)
		hookSource string
	}{
		{
			name: "default home",
			setupHome: func(_ *testing.T, userHome string) (string, string) {
				homeDir := filepath.Join(userHome, ".juex")
				return homeDir, filepath.Join(homeDir, "juex.yaml")
			},
			hookSource: "home:default",
		},
		{
			name: "instance home",
			setupHome: func(t *testing.T, _ string) (string, string) {
				homeDir := t.TempDir()
				t.Setenv("JUEX_HOME", homeDir)
				return homeDir, filepath.Join(homeDir, "juex.yaml")
			},
			hookSource: "home:instance",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			userHome := prepareConfigTest(t)
			homeDir, homePath := tc.setupHome(t, userHome)
			importPath := filepath.Join(homeDir, "imported.yaml")
			workDir := t.TempDir()

			writeTextFile(t, importPath, `models: [local:imported]
providers:
  - id: local
    protocol: openai/chat
    base_url: https://imported.example
    api_key: test-key
    headers: {X-Layer: imported}
    models:
      - id: imported
      - id: workspace
runtime:
  tool_timeout: 17s
skills:
  include: [imported]
hooks:
  commands:
    - name: imported
      events: [UserPromptSubmit]
      command: [echo, imported]
sandbox:
  file_system:
    blocked_paths: [imported-secret]
extensions:
  allow: [imported]
`)
			writeTextFile(t, homePath, `imports:
  - source: imported.yaml
hooks:
  commands:
    - name: home
      events: [UserPromptSubmit]
      command: [echo, home]
sandbox:
  file_system:
    blocked_paths: [home-secret]
extensions:
  allow: [home]
`)
			writeTextFile(t, filepath.Join(workDir, ".juex", "juex.yaml"), `models: [local:workspace]
providers:
  - id: local
    headers: {X-Layer: workspace}
runtime:
  tool_timeout: 33s
skills:
  include: [workspace]
hooks:
  trusted: true
  commands:
    - name: workspace
      events: [UserPromptSubmit]
      command: [echo, workspace]
sandbox:
  file_system:
    blocked_paths: [workspace-secret]
extensions:
  allow: [workspace]
`)

			cfg, err := LoadWithOptions(LoadOptions{
				WorkDir:    workDir,
				ConfigPath: homePath,
				AgentState: AgentStateNone,
			})
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Model != "imported" || !reflect.DeepEqual(cfg.Models, []string{"local:imported"}) {
				t.Fatalf("models = %v (selected %q), want imported Home replacement", cfg.Models, cfg.Model)
			}
			if cfg.ProviderHeaders["X-Layer"] != "imported" {
				t.Fatalf("provider headers = %v, want imported Home map value", cfg.ProviderHeaders)
			}
			if cfg.ToolTimeout != 17*time.Second {
				t.Fatalf("tool timeout = %s, want imported Home scalar", cfg.ToolTimeout)
			}
			if !reflect.DeepEqual(cfg.Skills.Include, []string{"imported"}) {
				t.Fatalf("skills include = %v, want imported Home replacement", cfg.Skills.Include)
			}
			if got, want := cfg.ExtensionPolicy().Allow, []string{"workspace"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("extension allow = %v, want workspace policy %v", got, want)
			}
			if got, want := cfg.Sandbox.FileSystem.BlockedPaths, []string{"imported-secret", "home-secret", "workspace-secret"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("blocked paths = %v, want single application %v", got, want)
			}
			if len(cfg.Hooks.Commands) != 3 {
				t.Fatalf("hooks = %+v, want three single-application commands", cfg.Hooks.Commands)
			}
			if got, want := []string{cfg.Hooks.Commands[0].Name, cfg.Hooks.Commands[1].Name, cfg.Hooks.Commands[2].Name}, []string{"imported", "home", "workspace"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("hooks = %v, want single application %v", got, want)
			}
			if cfg.Hooks.Commands[0].Source != tc.hookSource || cfg.Hooks.Commands[1].Source != tc.hookSource {
				t.Fatalf("Home hook sources = (%q, %q), want %q", cfg.Hooks.Commands[0].Source, cfg.Hooks.Commands[1].Source, tc.hookSource)
			}
			statuses := cfg.ImportStatuses()
			if len(statuses) != 1 {
				t.Fatalf("import statuses = %+v, want one original Home import", statuses)
			}
			sameImport, err := sameConfigPath(statuses[0].Source, importPath)
			if err != nil {
				t.Fatal(err)
			}
			if !sameImport {
				t.Fatalf("import status source = %q, want %q", statuses[0].Source, importPath)
			}
		})
	}
}

func TestExplicitLoadedHomeConfigReusesStaleRemoteImportDuringReplay(t *testing.T) {
	userHome := prepareConfigTest(t)
	homePath := filepath.Join(userHome, ".juex", "juex.yaml")
	workDir := t.TempDir()
	var fallbackPhase atomic.Bool
	var fallbackRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if fallbackPhase.Load() {
			if fallbackRequests.Add(1) == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`models: [local:imported]
providers:
  - id: local
    protocol: openai/chat
    base_url: https://imported.example
    api_key: test-key
    models:
      - id: imported
      - id: workspace
`))
	}))
	defer server.Close()

	writeTextFile(t, homePath, "imports:\n  - source: "+server.URL+"/config.yaml\n")
	writeTextFile(t, filepath.Join(workDir, ".juex", "juex.yaml"), "models: [local:workspace]\n")
	load := func() (Config, error) {
		return LoadWithOptions(LoadOptions{
			WorkDir:    workDir,
			ConfigPath: homePath,
			AgentState: AgentStateNone,
		})
	}

	seeded, err := load()
	if err != nil {
		t.Fatal(err)
	}
	if seeded.Model != "imported" {
		t.Fatalf("seeded model = %q, want imported", seeded.Model)
	}

	fallbackPhase.Store(true)
	replayed, err := load()
	if err != nil {
		t.Fatal(err)
	}
	if got := fallbackRequests.Load(); got != 1 {
		t.Fatalf("fallback requests = %d, want one remote resolution for Home load and replay", got)
	}
	if replayed.Model != "imported" {
		t.Fatalf("replayed model = %q, want stale imported Home replacement", replayed.Model)
	}
	statuses := replayed.ImportStatuses()
	if len(statuses) != 1 || statuses[0].State != "stale" {
		t.Fatalf("import statuses = %+v, want one stale Home import", statuses)
	}
}

func TestExplicitLoadedHomeConfigResolvesRelativeImportsBesideCanonicalHomePath(t *testing.T) {
	userHome := prepareConfigTest(t)
	homeDir := filepath.Join(userHome, ".juex")
	homePath := filepath.Join(homeDir, "juex.yaml")
	writeTextFile(t, filepath.Join(homeDir, "imported.yaml"), `models: [local:imported]
providers:
  - id: local
    protocol: openai/chat
    base_url: https://imported.example
    api_key: test-key
    models:
      - id: imported
      - id: workspace
`)
	writeTextFile(t, homePath, "imports:\n  - source: imported.yaml\n")

	aliasPath := filepath.Join(t.TempDir(), "home-alias.yaml")
	if err := os.Link(homePath, aliasPath); err != nil {
		t.Skipf("create hard-link alias: %v", err)
	}
	workDir := t.TempDir()
	writeTextFile(t, filepath.Join(workDir, ".juex", "juex.yaml"), "models: [local:workspace]\n")

	cfg, err := LoadWithOptions(LoadOptions{
		WorkDir:    workDir,
		ConfigPath: aliasPath,
		AgentState: AgentStateNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "imported" {
		t.Fatalf("model = %q, want import resolved beside canonical Home config", cfg.Model)
	}
}

func TestConfigImportsTreatColonContainingRelativeFilenameAsLocal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows filenames cannot contain colons")
	}
	dir := t.TempDir()
	importedName := "shared:providers.yaml"
	writeTextFile(t, filepath.Join(dir, importedName), "models: [local:colon]\n")
	mainPath := filepath.Join(dir, "juex.yaml")
	writeTextFile(t, mainPath, "imports:\n  - source: "+importedName+"\n")

	cfg := Config{HomeJuexDir: t.TempDir()}
	if err := applyYAMLFile(&cfg, explicitYAMLSource(mainPath)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.Models, []string{"local:colon"}) {
		t.Fatalf("models = %v, want colon-named local import", cfg.Models)
	}
}

func TestDeclaringConfigIdentityIsStableWhenMissingParentsAppearThroughSymlink(t *testing.T) {
	realRoot := t.TempDir()
	aliasRoot := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(realRoot, aliasRoot); err != nil {
		t.Skipf("create directory symlink: %v", err)
	}
	path := filepath.Join(aliasRoot, "workspace", ".juex", "juex.yaml")
	before := declaringConfigIdentity(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	after := declaringConfigIdentity(path)
	if before != after {
		t.Fatalf("declaring identity changed after parents appeared: before=%q after=%q", before, after)
	}
	canonicalRoot, err := filepath.EvalSymlinks(realRoot)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(canonicalRoot, "workspace", ".juex", "juex.yaml")
	if before != want {
		t.Fatalf("declaring identity = %q, want canonical %q", before, want)
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
	loader := newConfigImportLoaderForTest(t, home)
	loader.client = server.Client()
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	loader.now = func() time.Time { return now }

	first := Config{HomeJuexDir: home}
	if err := applyYAMLFileWithImportLoader(&first, explicitYAMLSource(mainPath), loader); err != nil {
		t.Fatal(err)
	}
	commitImportCacheForTest(t, &first)
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
	resetImportLoaderMemoForTest(loader)
	second := Config{HomeJuexDir: home}
	if err := applyYAMLFileWithImportLoader(&second, explicitYAMLSource(mainPath), loader); err != nil {
		t.Fatal(err)
	}
	commitImportCacheForTest(t, &second)
	if second.ToolTimeout != 41*time.Second {
		t.Fatalf("tool timeout after 304 = %s, want cached 41s", second.ToolTimeout)
	}
	statuses = second.ImportStatuses()
	if len(statuses) != 1 || statuses[0].State != "fresh" || !statuses[0].FetchedAt.Equal(now) {
		t.Fatalf("second import statuses = %+v, want fresh 304 at %s", statuses, now)
	}
}

func TestConfigRemoteImportCachesAreScopedToDeclaringConfig(t *testing.T) {
	var body atomic.Value
	body.Store("runtime:\n  tool_timeout: 41s\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body.Load().(string)))
	}))

	home := t.TempDir()
	firstPath := filepath.Join(t.TempDir(), "juex.yaml")
	secondPath := filepath.Join(t.TempDir(), "juex.yaml")
	declaration := "imports:\n  - source: " + server.URL + "/config.yaml\n"
	writeTextFile(t, firstPath, declaration)
	writeTextFile(t, secondPath, declaration)

	first := Config{HomeJuexDir: home}
	if err := applyYAMLFileWithImportLoader(&first, explicitYAMLSource(firstPath), newConfigImportLoaderForTest(t, home)); err != nil {
		t.Fatal(err)
	}
	commitImportCacheForTest(t, &first)
	body.Store("runtime:\n  tool_timeout: 42s\n")
	second := Config{HomeJuexDir: home}
	if err := applyYAMLFileWithImportLoader(&second, explicitYAMLSource(secondPath), newConfigImportLoaderForTest(t, home)); err != nil {
		t.Fatal(err)
	}
	commitImportCacheForTest(t, &second)
	server.Close()

	firstOffline := Config{HomeJuexDir: home}
	firstOfflineLoader := newConfigImportLoaderForTest(t, home)
	if err := applyYAMLFileWithImportLoader(&firstOffline, explicitYAMLSource(firstPath), firstOfflineLoader); err != nil {
		t.Fatal(err)
	}
	if firstOffline.ToolTimeout != 41*time.Second {
		t.Fatalf("first offline timeout = %s, want its validated 41s LKG", firstOffline.ToolTimeout)
	}
	if err := firstOfflineLoader.closeConfigImportCacheLock(); err != nil {
		t.Fatal(err)
	}
	secondOffline := Config{HomeJuexDir: home}
	secondOfflineLoader := newConfigImportLoaderForTest(t, home)
	defer func() {
		if err := secondOfflineLoader.closeConfigImportCacheLock(); err != nil {
			t.Error(err)
		}
	}()
	if err := applyYAMLFileWithImportLoader(&secondOffline, explicitYAMLSource(secondPath), secondOfflineLoader); err != nil {
		t.Fatal(err)
	}
	if secondOffline.ToolTimeout != 42*time.Second {
		t.Fatalf("second offline timeout = %s, want its validated 42s LKG", secondOffline.ToolTimeout)
	}
	if err := secondOfflineLoader.closeConfigImportCacheLock(); err != nil {
		t.Fatal(err)
	}

	sharedOfflineLoader := newConfigImportLoaderForTest(t, home)
	sharedFirst := Config{HomeJuexDir: home}
	if err := applyYAMLFileWithImportLoader(&sharedFirst, explicitYAMLSource(firstPath), sharedOfflineLoader); err != nil {
		t.Fatal(err)
	}
	sharedSecond := Config{HomeJuexDir: home}
	if err := applyYAMLFileWithImportLoader(&sharedSecond, explicitYAMLSource(secondPath), sharedOfflineLoader); err != nil {
		t.Fatal(err)
	}
	if sharedFirst.ToolTimeout != 41*time.Second || sharedSecond.ToolTimeout != 42*time.Second {
		t.Fatalf(
			"shared-loader offline timeouts = (%s, %s), want each declaring config's (41s, 42s) LKG",
			sharedFirst.ToolTimeout,
			sharedSecond.ToolTimeout,
		)
	}
}

func TestConfigHomeImportCachesAreScopedToDownstreamWorkspace(t *testing.T) {
	userHome := prepareConfigTest(t)
	var body atomic.Value
	body.Store("models: [workspace-a:model]\n")
	var offline atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if offline.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(body.Load().(string)))
	}))
	defer server.Close()

	writeTextFile(t, filepath.Join(userHome, ".juex", "juex.yaml"), "imports:\n  - source: "+server.URL+"/config.yaml\n")
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	writeTextFile(t, filepath.Join(workspaceA, ".juex", "juex.yaml"), `providers:
  - id: workspace-a
    protocol: openai/chat
    models: [{id: model}]
`)
	writeTextFile(t, filepath.Join(workspaceB, ".juex", "juex.yaml"), `providers:
  - id: workspace-b
    protocol: openai/chat
    models: [{id: model}]
`)

	if _, err := LoadForWorkDirForValidation(workspaceA); err != nil {
		t.Fatal(err)
	}
	body.Store("models: [workspace-b:model]\n")
	if _, err := LoadForWorkDirForValidation(workspaceB); err != nil {
		t.Fatal(err)
	}
	offline.Store(true)

	first, err := LoadForWorkDirForValidation(workspaceA)
	if err != nil {
		t.Fatalf("workspace A offline load: %v", err)
	}
	second, err := LoadForWorkDirForValidation(workspaceB)
	if err != nil {
		t.Fatalf("workspace B offline load: %v", err)
	}
	if !reflect.DeepEqual(first.Models, []string{"workspace-a:model"}) || !reflect.DeepEqual(second.Models, []string{"workspace-b:model"}) {
		t.Fatalf("offline Home import models = (%v, %v), want workspace-scoped LKGs", first.Models, second.Models)
	}
}

func TestConfigHomeImportCachesAreScopedToDownstreamExplicitConfig(t *testing.T) {
	userHome := prepareConfigTest(t)
	var body atomic.Value
	body.Store("models: [explicit-a:model]\n")
	var offline atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if offline.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(body.Load().(string)))
	}))
	defer server.Close()

	writeTextFile(t, filepath.Join(userHome, ".juex", "juex.yaml"), "imports:\n  - source: "+server.URL+"/config.yaml\n")
	workDir := t.TempDir()
	explicitA := filepath.Join(t.TempDir(), "explicit-a.yaml")
	explicitB := filepath.Join(t.TempDir(), "explicit-b.yaml")
	writeTextFile(t, explicitA, "providers:\n  - id: explicit-a\n    protocol: openai/chat\n    models: [{id: model}]\n")
	writeTextFile(t, explicitB, "providers:\n  - id: explicit-b\n    protocol: openai/chat\n    models: [{id: model}]\n")

	if _, err := LoadFromFileForWorkDirForValidation(explicitA, workDir); err != nil {
		t.Fatal(err)
	}
	body.Store("models: [explicit-b:model]\n")
	if _, err := LoadFromFileForWorkDirForValidation(explicitB, workDir); err != nil {
		t.Fatal(err)
	}
	offline.Store(true)

	first, err := LoadFromFileForWorkDirForValidation(explicitA, workDir)
	if err != nil {
		t.Fatalf("explicit A offline load: %v", err)
	}
	second, err := LoadFromFileForWorkDirForValidation(explicitB, workDir)
	if err != nil {
		t.Fatalf("explicit B offline load: %v", err)
	}
	if !reflect.DeepEqual(first.Models, []string{"explicit-a:model"}) || !reflect.DeepEqual(second.Models, []string{"explicit-b:model"}) {
		t.Fatalf("offline Home import models = (%v, %v), want explicit-config-scoped LKGs", first.Models, second.Models)
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
	loader := newConfigImportLoaderForTest(t, home)
	now := time.Date(2026, 8, 22, 11, 0, 0, 0, time.UTC)
	loader.now = func() time.Time { return now }

	first := Config{HomeJuexDir: home}
	if err := applyYAMLFileWithImportLoader(&first, explicitYAMLSource(mainPath), loader); err != nil {
		t.Fatal(err)
	}
	commitImportCacheForTest(t, &first)
	status.Store(http.StatusInternalServerError)
	now = now.Add(time.Hour)
	resetImportLoaderMemoForTest(loader)
	stale := Config{HomeJuexDir: home}
	if err := applyYAMLFileWithImportLoader(&stale, explicitYAMLSource(mainPath), loader); err != nil {
		t.Fatal(err)
	}
	if stale.ToolTimeout != 52*time.Second || len(stale.ImportStatuses()) != 1 || stale.ImportStatuses()[0].State != "stale" {
		t.Fatalf("stale fallback = timeout:%s statuses:%+v", stale.ToolTimeout, stale.ImportStatuses())
	}

	status.Store(http.StatusNotFound)
	resetImportLoaderMemoForTest(loader)
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
	resetImportLoaderMemoForTest(loader)
	expired := Config{HomeJuexDir: home}
	err = applyYAMLFileWithImportLoader(&expired, explicitYAMLSource(mainPath), loader)
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired cache error = %v", err)
	}
}

func TestConfigRemoteImportRejectsNonRepresentationSuccessStatuses(t *testing.T) {
	var status atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(int(status.Load()))
		_, _ = w.Write([]byte("runtime:\n  tool_timeout: 53s\n"))
	}))
	defer server.Close()

	mainPath := filepath.Join(t.TempDir(), "juex.yaml")
	writeTextFile(t, mainPath, "imports:\n  - source: "+server.URL+"/config.yaml\n")
	for _, code := range []int{http.StatusNoContent, http.StatusPartialContent} {
		status.Store(int32(code))
		err := applyYAMLFile(
			&Config{HomeJuexDir: t.TempDir()},
			explicitYAMLSource(mainPath),
		)
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("HTTP %d", code)) {
			t.Fatalf("HTTP %d error = %v", code, err)
		}
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
	loader := newConfigImportLoaderForTest(t, home)
	loader.now = func() time.Time { return time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC) }
	first := Config{HomeJuexDir: home}
	if err := applyYAMLFileWithImportLoader(&first, explicitYAMLSource(mainPath), loader); err != nil {
		t.Fatal(err)
	}
	commitImportCacheForTest(t, &first)

	body.Store("unknown_field: invalid\n")
	resetImportLoaderMemoForTest(loader)
	invalid := Config{HomeJuexDir: home}
	err := applyYAMLFileWithImportLoader(&invalid, explicitYAMLSource(mainPath), loader)
	if err == nil || !strings.Contains(err.Error(), "unknown_field") {
		t.Fatalf("invalid update error = %v", err)
	}
	body.Store("retry")
	resetImportLoaderMemoForTest(loader)
	stale := Config{HomeJuexDir: home}
	if err := applyYAMLFileWithImportLoader(&stale, explicitYAMLSource(mainPath), loader); err != nil {
		t.Fatal(err)
	}
	if stale.ToolTimeout != 61*time.Second || stale.ImportStatuses()[0].State != "stale" {
		t.Fatalf("fallback after invalid update = timeout:%s statuses:%+v", stale.ToolTimeout, stale.ImportStatuses())
	}
}

func TestConfigRemoteImportDoesNotReplaceLKGWhenFinalValidationFails(t *testing.T) {
	prepareConfigTest(t)
	var body atomic.Value
	body.Store(`models: [remote:ok]
providers:
  - id: remote
    protocol: openai/chat
    base_url: https://example.invalid
    api_key: test-key
    models: [{id: ok}]
`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		value := body.Load().(string)
		if value == "retry" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(value))
	}))
	defer server.Close()

	mainPath := filepath.Join(t.TempDir(), "juex.yaml")
	writeTextFile(t, mainPath, fmt.Sprintf("imports:\n  - source: %s/config.yaml\n", server.URL))
	workDir := t.TempDir()
	first, err := LoadFromFileForWorkDirForValidation(mainPath, workDir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.Models, []string{"remote:ok"}) {
		t.Fatalf("first models = %v, want valid remote selection", first.Models)
	}

	body.Store("models: [missing:model]\n")
	if _, err := LoadFromFileForWorkDirForValidation(mainPath, workDir); err == nil {
		t.Fatal("semantically invalid remote update error = nil")
	}

	body.Store("retry")
	stale, err := LoadFromFileForWorkDirForValidation(mainPath, workDir)
	if err != nil {
		t.Fatalf("stale load after rejected update: %v", err)
	}
	if !reflect.DeepEqual(stale.Models, []string{"remote:ok"}) || len(stale.ImportStatuses()) != 1 || stale.ImportStatuses()[0].State != "stale" {
		t.Fatalf("stale fallback = models:%v statuses:%+v", stale.Models, stale.ImportStatuses())
	}
}

func TestConfigRepeatedRemoteIdentityResolvesOnceAcrossLayers(t *testing.T) {
	userHome := prepareConfigTest(t)
	var requests atomic.Int32
	var offline atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if offline.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		switch requests.Add(1) {
		case 1:
			_, _ = w.Write([]byte(`models: [remote:first]
providers:
  - id: remote
    protocol: openai/chat
    base_url: https://example.invalid
    api_key: test-key
    models: [{id: first}]
`))
		default:
			_, _ = w.Write([]byte("models: [remote:first]\n"))
		}
	}))
	defer server.Close()

	importLine := "imports:\n  - source: " + server.URL + "/config.yaml\n"
	writeTextFile(t, filepath.Join(userHome, ".juex", "juex.yaml"), importLine)
	explicitPath := filepath.Join(t.TempDir(), "juex.yaml")
	writeTextFile(t, explicitPath, importLine)
	workDir := t.TempDir()
	first, err := LoadFromFileForWorkDirForValidation(explicitPath, workDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("remote requests = %d, want one resolution for repeated identity", got)
	}
	if !reflect.DeepEqual(first.Models, []string{"remote:first"}) {
		t.Fatalf("first models = %v", first.Models)
	}

	offline.Store(true)
	stale, err := LoadFromFileForWorkDirForValidation(explicitPath, workDir)
	if err != nil {
		t.Fatalf("offline repeated-identity load: %v", err)
	}
	if !reflect.DeepEqual(stale.Models, []string{"remote:first"}) || len(stale.ImportStatuses()) != 2 || stale.ImportStatuses()[0].State != "stale" || stale.ImportStatuses()[1].State != "stale" {
		t.Fatalf("stale repeated identity = models:%v statuses:%+v", stale.Models, stale.ImportStatuses())
	}
}

func TestFleetOnlyLoadDoesNotReplaceRuntimeLKG(t *testing.T) {
	userHome := prepareConfigTest(t)
	var body atomic.Value
	body.Store(`models: [remote:ok]
providers:
  - id: remote
    protocol: openai/chat
    base_url: https://example.invalid
    api_key: test-key
    models: [{id: ok}]
fleet:
  addr: 127.0.0.1:5888
`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		value := body.Load().(string)
		if value == "retry" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(value))
	}))
	defer server.Close()
	writeTextFile(t, filepath.Join(userHome, ".juex", "juex.yaml"), "imports:\n  - source: "+server.URL+"/config.yaml\n")
	workDir := t.TempDir()
	if _, err := LoadForWorkDirForValidation(workDir); err != nil {
		t.Fatal(err)
	}

	body.Store("models: [missing:model]\nfleet:\n  addr: 127.0.0.1:5999\n")
	fleet, err := LoadHomeFleetConfig()
	if err != nil {
		t.Fatal(err)
	}
	if fleet.Addr != "127.0.0.1:5999" {
		t.Fatalf("fleet addr = %q, want fresh fleet-only value", fleet.Addr)
	}

	body.Store("retry")
	stale, err := LoadForWorkDirForValidation(workDir)
	if err != nil {
		t.Fatalf("runtime stale load after fleet-only update: %v", err)
	}
	if !reflect.DeepEqual(stale.Models, []string{"remote:ok"}) || len(stale.ImportStatuses()) != 1 || stale.ImportStatuses()[0].State != "stale" {
		t.Fatalf("runtime LKG after fleet-only load = models:%v statuses:%+v", stale.Models, stale.ImportStatuses())
	}
}

func TestFleetOnlyLoadReusesRuntimeLKGFromAnotherWorkspaceContext(t *testing.T) {
	userHome := prepareConfigTest(t)
	var unavailable atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if unavailable.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`models: [remote:ok]
providers:
  - id: remote
    protocol: openai/chat
    base_url: https://example.invalid
    api_key: test-key
    models: [{id: ok}]
fleet:
  addr: 127.0.0.1:5888
`))
	}))
	defer server.Close()
	writeTextFile(t, filepath.Join(userHome, ".juex", "juex.yaml"), "imports:\n  - source: "+server.URL+"/config.yaml\n")

	runtimeWorkDir := t.TempDir()
	if _, err := LoadForWorkDirForValidation(runtimeWorkDir); err != nil {
		t.Fatal(err)
	}
	unavailable.Store(true)
	t.Chdir(t.TempDir())

	fleet, err := LoadHomeFleetConfig()
	if err != nil {
		t.Fatalf("fleet-only load from runtime LKG: %v", err)
	}
	if fleet.Addr != "127.0.0.1:5888" {
		t.Fatalf("fleet addr = %q, want runtime-validated LKG address", fleet.Addr)
	}
}

func TestFleetOnlyLoadSelectsOneCompleteRuntimeCacheContext(t *testing.T) {
	userHome := prepareConfigTest(t)
	var phase atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := phase.Load()
		switch r.URL.Path {
		case "/addr.yaml":
			if current == 3 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			if current == 1 {
				_, _ = w.Write([]byte("fleet:\n  addr: 127.0.0.1:5999\n"))
				return
			}
			_, _ = w.Write([]byte("fleet:\n  addr: 0.0.0.0:5888\n"))
		case "/unsafe.yaml":
			if current >= 2 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			if current == 1 {
				_, _ = w.Write([]byte("fleet:\n  unsafe_bind_any: true\n"))
				return
			}
			_, _ = w.Write([]byte("fleet:\n  unsafe_bind_any: false\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	writeTextFile(t, filepath.Join(userHome, ".juex", "juex.yaml"), `imports:
  - source: `+server.URL+`/addr.yaml
  - source: `+server.URL+`/unsafe.yaml
models: [local:test]
providers:
  - id: local
    protocol: openai/chat
    api_key: test-key
    models: [{id: test}]
`)

	contextA := t.TempDir()
	if _, err := LoadForWorkDirForValidation(contextA); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	phase.Store(1)
	contextB := t.TempDir()
	if _, err := LoadForWorkDirForValidation(contextB); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	phase.Store(2)
	if _, err := LoadForWorkDirForValidation(contextA); err != nil {
		t.Fatal(err)
	}
	phase.Store(3)

	fleet, err := LoadHomeFleetConfig()
	if err != nil {
		t.Fatal(err)
	}
	if fleet.Addr != "127.0.0.1:5999" || !fleet.UnsafeBindAny {
		t.Fatalf("fleet fallback = %+v, want the complete newer context B", fleet)
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
		loader := newConfigImportLoaderForTest(t, t.TempDir())
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
		loader := newConfigImportLoaderForTest(t, t.TempDir())
		loader.timeout = 10 * time.Millisecond
		err := applyYAMLFileWithImportLoader(&Config{HomeJuexDir: loader.homeDir}, explicitYAMLSource(mainPath), loader)
		if err == nil || !strings.Contains(err.Error(), "no valid Last-Known-Good cache") {
			t.Fatalf("timeout error = %v", err)
		}
	})
}

func TestConfigRemoteImportControlsRedirects(t *testing.T) {
	t.Run("cross-origin referer", func(t *testing.T) {
		var referer atomic.Value
		referer.Store("")
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			referer.Store(r.Header.Get("Referer"))
			_, _ = w.Write([]byte("runtime:\n  tool_timeout: 1m\n"))
		}))
		defer target.Close()
		origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL+"/config.yaml", http.StatusFound)
		}))
		defer origin.Close()
		mainPath := filepath.Join(t.TempDir(), "juex.yaml")
		writeTextFile(t, mainPath, fmt.Sprintf("imports:\n  - source: %s/config.yaml?token=query-secret\n", origin.URL))
		loader := newConfigImportLoaderForTest(t, t.TempDir())
		if err := applyYAMLFileWithImportLoader(&Config{HomeJuexDir: loader.homeDir}, explicitYAMLSource(mainPath), loader); err != nil {
			t.Fatal(err)
		}
		if got := referer.Load().(string); got != "" {
			t.Fatalf("cross-origin Referer = %q, want empty", got)
		}
	})

	t.Run("cached validators are not forwarded", func(t *testing.T) {
		var requests atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/config.yaml":
				if requests.Add(1) == 1 {
					w.Header().Set("ETag", `"config-v1"`)
					w.Header().Set("Last-Modified", "Fri, 22 Aug 2025 00:00:00 GMT")
					_, _ = w.Write([]byte("runtime:\n  tool_timeout: 41s\n"))
					return
				}
				http.Redirect(w, r, "/replacement.yaml", http.StatusFound)
			case "/replacement.yaml":
				if etag, modified := r.Header.Get("If-None-Match"), r.Header.Get("If-Modified-Since"); etag != "" || modified != "" {
					t.Errorf("redirect target received original validators: etag=%q modified=%q", etag, modified)
					w.WriteHeader(http.StatusNotModified)
					return
				}
				_, _ = w.Write([]byte("runtime:\n  tool_timeout: 42s\n"))
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()

		home := t.TempDir()
		mainPath := filepath.Join(t.TempDir(), "juex.yaml")
		writeTextFile(t, mainPath, fmt.Sprintf("imports:\n  - source: %s/config.yaml\n", server.URL))
		loader := newConfigImportLoaderForTest(t, home)
		first := Config{HomeJuexDir: home}
		if err := applyYAMLFileWithImportLoader(&first, explicitYAMLSource(mainPath), loader); err != nil {
			t.Fatal(err)
		}
		commitImportCacheForTest(t, &first)

		resetImportLoaderMemoForTest(loader)
		second := Config{HomeJuexDir: home}
		if err := applyYAMLFileWithImportLoader(&second, explicitYAMLSource(mainPath), loader); err != nil {
			t.Fatal(err)
		}
		if second.ToolTimeout != 42*time.Second {
			t.Fatalf("redirected tool timeout = %s, want replacement 42s", second.ToolTimeout)
		}
	})

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
		loader := newConfigImportLoaderForTest(t, t.TempDir())
		loader.client = secure.Client()
		err := applyYAMLFileWithImportLoader(&Config{HomeJuexDir: loader.homeDir}, explicitYAMLSource(mainPath), loader)
		if err == nil || !strings.Contains(err.Error(), "redirect from https to http is not allowed") {
			t.Fatalf("downgrade redirect error = %v", err)
		}
	})

	t.Run("intermediate https downgrade", func(t *testing.T) {
		plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("runtime:\n  tool_timeout: 1m\n"))
		}))
		defer plain.Close()
		secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, plain.URL+"/config.yaml", http.StatusFound)
		}))
		defer secure.Close()
		origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, secure.URL+"/config.yaml", http.StatusFound)
		}))
		defer origin.Close()
		mainPath := filepath.Join(t.TempDir(), "juex.yaml")
		writeTextFile(t, mainPath, fmt.Sprintf("imports:\n  - source: %s/config.yaml\n", origin.URL))
		loader := newConfigImportLoaderForTest(t, t.TempDir())
		loader.client = secure.Client()
		err := applyYAMLFileWithImportLoader(&Config{HomeJuexDir: loader.homeDir}, explicitYAMLSource(mainPath), loader)
		if err == nil || !strings.Contains(err.Error(), "redirect from https to http is not allowed") {
			t.Fatalf("intermediate downgrade redirect error = %v", err)
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
		loader := newConfigImportLoaderForTest(t, home)
		cfg := Config{HomeJuexDir: home}
		if err := applyYAMLFileWithImportLoader(&cfg, explicitYAMLSource(mainPath), loader); err != nil {
			t.Fatal(err)
		}
		commitImportCacheForTest(t, &cfg)
		server.Close()
		entries, err := os.ReadDir(filepath.Join(home, "cache", "config-imports"))
		if err != nil {
			t.Fatal(err)
		}
		cachePath := filepath.Join(home, "cache", "config-imports", entries[0].Name())
		if err := os.Chmod(cachePath, 0o644); err != nil {
			t.Fatal(err)
		}
		resetImportLoaderMemoForTest(loader)
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

func commitImportCacheForTest(t *testing.T, cfg *Config) {
	t.Helper()
	if err := commitConfigImportCaches(cfg); err != nil {
		t.Fatal(err)
	}
}

func newConfigImportLoaderForTest(t *testing.T, home string) *configImportLoader {
	t.Helper()
	loader := newConfigImportLoader(home)
	t.Cleanup(func() {
		if err := loader.closeConfigImportCacheLock(); err != nil {
			t.Errorf("close config import cache lock: %v", err)
		}
	})
	return loader
}

func TestCommitConfigImportCachesPreservesEarlierRecordsWhenLaterWriteFails(t *testing.T) {
	home := t.TempDir()
	firstSource := "https://config.example/first.yaml"
	declaringDigest := sourceDigest("first-declarer")
	contextDigest := sourceDigest("standalone")
	firstPath := filepath.Join(home, "cache", "config-imports", sourceDigest(firstSource)+"-"+declaringDigest+"-"+contextDigest+".json")
	old := configImportCacheRecord{
		Version:         configImportCacheVersion,
		Source:          firstSource,
		SourceSHA256:    sourceDigest(firstSource),
		DeclaringSHA256: declaringDigest,
		ContextSHA256:   contextDigest,
		FetchedAt:       time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC),
		Content:         "runtime:\n  tool_timeout: 41s\n",
		cachePath:       firstPath,
	}
	old.ContentSHA256 = contentDigest([]byte(old.Content))
	secondSource := "https://config.example/second.yaml"
	secondPath := filepath.Join(home, "cache", "config-imports", sourceDigest(secondSource)+"-"+declaringDigest+"-"+contextDigest+".json")
	secondOld := old
	secondOld.Source = secondSource
	secondOld.SourceSHA256 = sourceDigest(secondOld.Source)
	secondOld.Content = "runtime:\n  tool_timeout: 40s\n"
	secondOld.ContentSHA256 = contentDigest([]byte(secondOld.Content))
	secondOld.cachePath = secondPath
	seed := Config{HomeJuexDir: home, pendingImportCache: []configImportCacheRecord{old, secondOld}}
	if err := commitConfigImportCaches(&seed); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	secondBefore, err := os.ReadFile(secondPath)
	if err != nil {
		t.Fatal(err)
	}

	firstUpdate := old
	firstUpdate.Content = "runtime:\n  tool_timeout: 42s\n"
	firstUpdate.ContentSHA256 = contentDigest([]byte(firstUpdate.Content))
	secondUpdate := secondOld
	secondUpdate.Content = "runtime:\n  tool_timeout: 43s\n"
	secondUpdate.ContentSHA256 = contentDigest([]byte(secondUpdate.Content))

	cfg := Config{HomeJuexDir: home, pendingImportCache: []configImportCacheRecord{firstUpdate, secondUpdate}}
	writes := 0
	err = commitConfigImportCachesWithWriter(&cfg, func(path string, data []byte) error {
		writes++
		if writes == 2 {
			if err := os.WriteFile(path, data, 0o600); err != nil {
				return err
			}
			return &homestore.AtomicWriteError{
				Operation: "sync parent directory",
				Path:      filepath.Dir(path),
				Replaced:  true,
				Err:       fmt.Errorf("injected second write failure"),
			}
		}
		return os.WriteFile(path, data, 0o600)
	})
	if err == nil {
		t.Fatal("commitConfigImportCaches() error = nil")
	}
	after, err := os.ReadFile(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("first cache changed after later write failure:\n%s", after)
	}
	secondAfter, err := os.ReadFile(secondPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(secondAfter, secondBefore) {
		t.Fatalf("second cache changed after its write failure:\n%s", secondAfter)
	}
}

func TestConfigImportCacheRecoversInterruptedPublication(t *testing.T) {
	home := t.TempDir()
	loader := newConfigImportLoaderForTest(t, home)
	declarer := filepath.Join(t.TempDir(), "juex.yaml")
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	newRecord := func(source, content string) configImportCacheRecord {
		record := configImportCacheRecord{
			Version:         configImportCacheVersion,
			Source:          source,
			SourceSHA256:    sourceDigest(source),
			DeclaringSHA256: declaringConfigDigest(declarer),
			ContextSHA256:   loader.cacheContextDigest(),
			FetchedAt:       now,
			Content:         content,
			cachePath:       loader.cachePath(source, declarer),
		}
		record.ContentSHA256 = contentDigest([]byte(record.Content))
		return record
	}

	firstSource := "https://config.example/first.yaml"
	secondSource := "https://config.example/second.yaml"
	firstOld := newRecord(firstSource, "runtime:\n  tool_timeout: 40s\n")
	secondOld := newRecord(secondSource, "runtime:\n  tool_timeout: 41s\n")
	if err := commitConfigImportCaches(&Config{HomeJuexDir: home, pendingImportCache: []configImportCacheRecord{firstOld, secondOld}}); err != nil {
		t.Fatal(err)
	}

	firstNew := newRecord(firstSource, "runtime:\n  tool_timeout: 42s\n")
	secondNew := newRecord(secondSource, "runtime:\n  tool_timeout: 43s\n")
	commits, err := prepareConfigImportCacheCommits([]configImportCacheRecord{firstNew, secondNew})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := beginConfigImportCachePublication(commits); err != nil {
		t.Fatal(err)
	}
	if err := homestore.WriteFileAtomic(firstNew.cachePath, commits[0].data, 0o600, 0o700); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(firstNew.cachePath); err != nil || bytes.Equal(got, commits[0].previous) {
		t.Fatalf("first cache was not replaced before simulated death: changed=%t err=%v", !bytes.Equal(got, commits[0].previous), err)
	}

	reader := newConfigImportLoader(home)
	firstRecovered, err := reader.readCache(firstSource, declarer)
	if err != nil {
		t.Fatal(err)
	}
	secondRecovered, err := reader.readCache(secondSource, declarer)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.closeConfigImportCacheLock(); err != nil {
		t.Fatal(err)
	}
	if firstRecovered.Content != firstOld.Content || secondRecovered.Content != secondOld.Content {
		t.Fatalf("recovered generation = (%q, %q), want old/old", firstRecovered.Content, secondRecovered.Content)
	}
	if _, err := os.Stat(configImportCacheJournalPath(home)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("publication journal remains after recovery: %v", err)
	}

	commits, err = prepareConfigImportCacheCommits([]configImportCacheRecord{firstNew, secondNew})
	if err != nil {
		t.Fatal(err)
	}
	journalPath, err := beginConfigImportCachePublication(commits)
	if err != nil {
		t.Fatal(err)
	}
	for _, commit := range commits {
		if err := homestore.WriteFileAtomic(commit.record.cachePath, commit.data, 0o600, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := markConfigImportCachePublicationCommitted(journalPath); err != nil {
		t.Fatal(err)
	}

	committedReader := newConfigImportLoader(home)
	firstCommitted, err := committedReader.readCache(firstSource, declarer)
	if err != nil {
		t.Fatal(err)
	}
	secondCommitted, err := committedReader.readCache(secondSource, declarer)
	if err != nil {
		t.Fatal(err)
	}
	if err := committedReader.closeConfigImportCacheLock(); err != nil {
		t.Fatal(err)
	}
	if firstCommitted.Content != firstNew.Content || secondCommitted.Content != secondNew.Content {
		t.Fatalf("committed generation = (%q, %q), want new/new", firstCommitted.Content, secondCommitted.Content)
	}
	if _, err := os.Stat(configImportCacheJournalPath(home)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("committed publication journal remains after recovery: %v", err)
	}
}

func TestConfigImportCacheReaderObservesOnePublishedGeneration(t *testing.T) {
	home := t.TempDir()
	loader := newConfigImportLoaderForTest(t, home)
	declarer := filepath.Join(t.TempDir(), "juex.yaml")
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	newRecord := func(source, content string) configImportCacheRecord {
		record := configImportCacheRecord{
			Version:         configImportCacheVersion,
			Source:          source,
			SourceSHA256:    sourceDigest(source),
			DeclaringSHA256: declaringConfigDigest(declarer),
			ContextSHA256:   loader.cacheContextDigest(),
			FetchedAt:       now,
			Content:         content,
			cachePath:       loader.cachePath(source, declarer),
		}
		record.ContentSHA256 = contentDigest([]byte(record.Content))
		return record
	}

	firstSource := "https://config.example/first.yaml"
	secondSource := "https://config.example/second.yaml"
	firstOld := newRecord(firstSource, "runtime:\n  tool_timeout: 40s\n")
	secondOld := newRecord(secondSource, "runtime:\n  tool_timeout: 41s\n")
	seed := Config{HomeJuexDir: home, pendingImportCache: []configImportCacheRecord{firstOld, secondOld}}
	if err := commitConfigImportCaches(&seed); err != nil {
		t.Fatal(err)
	}

	firstNew := newRecord(firstSource, "runtime:\n  tool_timeout: 42s\n")
	secondNew := newRecord(secondSource, "runtime:\n  tool_timeout: 43s\n")
	firstPublished := make(chan struct{})
	continuePublish := make(chan struct{})
	writerDone := make(chan error, 1)
	writerCfg := Config{HomeJuexDir: home, pendingImportCache: []configImportCacheRecord{firstNew, secondNew}}
	go func() {
		writes := 0
		writerDone <- commitConfigImportCachesWithWriter(&writerCfg, func(path string, data []byte) error {
			if err := homestore.WriteFileAtomic(path, data, 0o600, 0o700); err != nil {
				return err
			}
			writes++
			if writes == 1 {
				close(firstPublished)
				<-continuePublish
			}
			return nil
		})
	}()

	select {
	case <-firstPublished:
	case err := <-writerDone:
		t.Fatalf("cache writer stopped before publishing its first record: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the first cache record publication")
	}

	type cacheReadResult struct {
		first  configImportCacheRecord
		second configImportCacheRecord
		err    error
	}
	readerDone := make(chan cacheReadResult, 1)
	go func() {
		reader := newConfigImportLoader(home)
		first, err := reader.readCache(firstSource, declarer)
		if err == nil {
			var second configImportCacheRecord
			second, err = reader.readCache(secondSource, declarer)
			readerDone <- cacheReadResult{first: first, second: second, err: errors.Join(err, reader.closeConfigImportCacheLock())}
			return
		}
		readerDone <- cacheReadResult{err: errors.Join(err, reader.closeConfigImportCacheLock())}
	}()

	select {
	case result := <-readerDone:
		close(continuePublish)
		<-writerDone
		t.Fatalf("cache reader completed between set publications: first=%q second=%q err=%v", result.first.Content, result.second.Content, result.err)
	case <-time.After(150 * time.Millisecond):
		close(continuePublish)
	}
	if err := <-writerDone; err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-readerDone:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.first.Content != firstNew.Content || result.second.Content != secondNew.Content {
			t.Fatalf("cache reader generation = (%q, %q), want (%q, %q)", result.first.Content, result.second.Content, firstNew.Content, secondNew.Content)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cache reader remained blocked after publication completed")
	}
}

func resetImportLoaderMemoForTest(loader *configImportLoader) {
	loader.remoteMemo = make(map[string]configImportDocument)
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

func TestExplicitWorkspaceConfigDoesNotReapplyImports(t *testing.T) {
	prepareConfigTest(t)
	workDir := t.TempDir()
	writeTextFile(t, filepath.Join(workDir, ".juex", "imported.yaml"), `hooks:
  trusted: true
  commands:
    - name: imported
      events: [UserPromptSubmit]
      command: [echo, imported]
`)
	workspacePath := filepath.Join(workDir, ".juex", "juex.yaml")
	writeTextFile(t, workspacePath, `imports:
  - source: imported.yaml
models: [local:test]
providers:
  - id: local
    protocol: openai/chat
    api_key: test-key
    models: [{id: test}]
hooks:
  trusted: true
  commands:
    - name: declaring
      events: [UserPromptSubmit]
      command: [echo, declaring]
`)

	cfg, err := LoadWithOptions(LoadOptions{WorkDir: workDir, ConfigPath: workspacePath, AgentState: AgentStateNone})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Hooks.Commands) != 2 {
		t.Fatalf("hooks = %v, want workspace source chain applied once", cfg.Hooks.Commands)
	}
	if got := []string{cfg.Hooks.Commands[0].Name, cfg.Hooks.Commands[1].Name}; !reflect.DeepEqual(got, []string{"imported", "declaring"}) {
		t.Fatalf("hooks = %v, want workspace source chain applied once", cfg.Hooks.Commands)
	}
	if statuses := cfg.ImportStatuses(); len(statuses) != 1 {
		t.Fatalf("import statuses = %+v, want one workspace import", statuses)
	}
}

func TestExplicitHomeConfigReappliesOnlyDeclaringDocument(t *testing.T) {
	home := prepareConfigTest(t)
	homeConfigDir := filepath.Join(home, ".juex")
	writeTextFile(t, filepath.Join(homeConfigDir, "imported.yaml"), `hooks:
  commands:
    - name: imported
      events: [UserPromptSubmit]
      command: [echo, imported]
`)
	homePath := filepath.Join(homeConfigDir, "juex.yaml")
	writeTextFile(t, homePath, `imports:
  - source: imported.yaml
models: [local:home]
providers:
  - id: local
    protocol: openai/chat
    api_key: test-key
    models: [{id: home}, {id: workspace}]
fleet:
  addr: 127.0.0.1:5998
  unsafe_bind_any: true
hooks:
  commands:
    - name: declaring
      events: [UserPromptSubmit]
      command: [echo, declaring]
sandbox:
  file_system:
    blocked_paths: [home-secret]
`)
	workDir := t.TempDir()
	writeTextFile(t, filepath.Join(workDir, ".juex", "juex.yaml"), "models: [local:workspace]\nsandbox:\n  file_system:\n    blocked_paths: [workspace-secret]\n")

	cfg, err := LoadWithOptions(LoadOptions{WorkDir: workDir, ConfigPath: homePath, AgentState: AgentStateNone})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "home" {
		t.Fatalf("model = %q, want explicit Home declaring document to win", cfg.Model)
	}
	if cfg.Fleet.Addr != "127.0.0.1:5998" || !cfg.Fleet.UnsafeBindAny {
		t.Fatalf("fleet = %+v, want replayed default-Home scope", cfg.Fleet)
	}
	if len(cfg.Hooks.Commands) != 2 {
		t.Fatalf("hooks = %v, want imported and declaring Home hooks applied once", cfg.Hooks.Commands)
	}
	if got := []string{cfg.Hooks.Commands[0].Name, cfg.Hooks.Commands[1].Name}; !reflect.DeepEqual(got, []string{"imported", "declaring"}) {
		t.Fatalf("hooks = %v, want imported and declaring Home hooks applied once", cfg.Hooks.Commands)
	}
	if got := cfg.Sandbox.FileSystem.BlockedPaths; !reflect.DeepEqual(got, []string{"home-secret", "workspace-secret"}) {
		t.Fatalf("blocked paths = %v, want append-only Home values applied once", got)
	}
	if statuses := cfg.ImportStatuses(); len(statuses) != 1 {
		t.Fatalf("import statuses = %+v, want one Home import", statuses)
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
