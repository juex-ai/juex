package migration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/migration/legacy"
)

func configFile(path, data string) legacy.SourceFile {
	h := sha256.Sum256([]byte(data))
	return legacy.SourceFile{Path: path, Data: []byte(data), Size: int64(len(data)), SHA256: hex.EncodeToString(h[:]), Mode: 0600}
}

func configFixture() (legacy.Fleet, ConfigEvidence) {
	home := "/source/home"
	f := legacy.Fleet{SourceHome: home, DefaultHome: legacy.HomeConfig{Directory: home},
		Agents:     []legacy.Agent{{Definition: legacy.AgentDefinition{ID: "abc234", Name: "source", Workspace: "/source/work"}}},
		Workspaces: []legacy.Workspace{{AgentID: "abc234", Path: "/source/work", ResolvedPath: "/source/work", AbsentFiles: []string{".juex/juex.yaml"}}}}
	base := configFile("juex.yaml", `models: [local:qwen3.8:27b, local:backup]
providers:
  - id: local
    protocol: openai/chat
    base_url: https://model.example
    api_key: private-model-key
    headers: {X-Account: private-account, X-Keep: retained}
    models:
      - id: qwen3.8:27b
        context_window: 8192
        capabilities: {vision: true}
      - id: backup
`)
	f.DefaultHome.Files = []legacy.SourceFile{base}
	f.Files = []legacy.SourceFile{base}
	f.Agents[0].AbsentFiles = []string{"juex.yaml"}
	e := ConfigEvidence{Contexts: map[string]ConfigContext{"abc234": {WorkingDirectory: "/source/work"}}, Identities: map[string]string{}}
	for _, p := range []string{home + "/juex.yaml", "/source/work", "/source/work/.juex/juex.yaml", home + "/agents/abc234/juex.yaml"} {
		e.Identities[p] = p
	}
	return f, e
}

func TestResolveConfigPreservesLayeredModelsAndSparseModules(t *testing.T) {
	f, e := configFixture()
	f.Agents[0].AbsentFiles = nil
	f.Agents[0].Files = []legacy.SourceFile{configFile("juex.yaml", `preset: minimal
modules:
  memory: {enabled: true, profile: supervisor}
  worker-threads: {max_depth: 2}
providers:
  - id: local
    api_key: ""
    headers: {X-Account: final-private-account}
    models:
      - id: qwen3.8:27b
        context_window: 0
        capabilities: {vision: false}
        query: {secret: private-query}
enable_user_agents_resources: false
extensions: {allow: []}
`)}
	got, err := ResolveConfig(f, e)
	if err != nil {
		t.Fatal(err)
	}
	a := got[0]
	if len(a.Models) != 2 || a.Models[0].Ref != "local:qwen3.8:27b" || a.Models[1].Ref != "local:backup" {
		t.Fatal("model chain changed")
	}
	p := a.Models[0]
	if p.Configuration.Model != "qwen3.8:27b" || p.ContextWindow != 8192 || p.Configuration.APIKey != "private-model-key" || p.Configuration.Headers["X-Keep"] != "retained" || p.Configuration.Headers["X-Account"] != "final-private-account" || p.Configuration.Capabilities.Vision == nil || *p.Configuration.Capabilities.Vision {
		t.Fatal("provider/model merge changed")
	}
	if a.Modules["worker-threads"] || !a.Modules["memory"] || !a.Modules["shell"] || a.Modules["agents-md"] || a.MemoryProfile != "supervisor" || a.WorkerDepth != 2 || a.UserResources || !a.ExtensionPolicyConfigured || len(a.ExtensionAllow) != 0 {
		t.Fatal("sparse module/resource policy changed")
	}
	if len(a.Sources) != 2 {
		t.Fatalf("same Home applied twice: %d", len(a.Sources))
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private-model-key", "private-account", "private-query", "model.example"} {
		if strings.Contains(string(b), secret) {
			t.Fatal("configuration secrets entered report")
		}
	}
	second, err := ResolveConfig(f, e)
	if err != nil || !reflect.DeepEqual(got, second) {
		t.Fatal("conversion mutated inputs or is not deterministic")
	}
}

func TestResolveConfigModelsNullAndEmptyAreDifferent(t *testing.T) {
	for _, tc := range []struct {
		value string
		count int
	}{{"null", 2}, {"[]", 0}, {"[local:backup]", 1}} {
		t.Run(tc.value, func(t *testing.T) {
			f, e := configFixture()
			f.Agents[0].AbsentFiles = nil
			f.Agents[0].Files = []legacy.SourceFile{configFile("juex.yaml", "models: "+tc.value+"\n")}
			got, err := ResolveConfig(f, e)
			if err != nil {
				t.Fatal(err)
			}
			if len(got[0].Models) != tc.count {
				t.Fatal("models replacement semantics changed")
			}
		})
	}
}

func TestResolveConfigEmptyStartupModelSelectionKeepsDiskChain(t *testing.T) {
	for _, refs := range [][]string{nil, {}, {"local:backup"}} {
		f, e := configFixture()
		ctx := e.Contexts["abc234"]
		ctx.ModelRefs = refs
		e.Contexts["abc234"] = ctx
		got, err := ResolveConfig(f, e)
		if err != nil {
			t.Fatal(err)
		}
		want := 2
		if len(refs) > 0 {
			want = 1
		}
		if len(got[0].Models) != want {
			t.Fatal("an empty startup selection must not clear the disk model chain")
		}
	}
}

func TestResolveConfigEmptyOptionsDeleteOverridesAndRestoreProviderValues(t *testing.T) {
	f, e := configFixture()
	base := configFile("juex.yaml", `models: [local:model]
providers:
  - id: local
    protocol: openai/chat
    headers: {X-Account: provider-value, X-Remove: delete-me}
    query: {account: provider-value, remove: delete-me}
    models:
      - id: model
        headers: {X-Account: model-value}
        query: {account: model-value}
`)
	f.DefaultHome.Files, f.Files = []legacy.SourceFile{base}, []legacy.SourceFile{base}
	f.Agents[0].AbsentFiles = nil
	f.Agents[0].Files = []legacy.SourceFile{configFile("juex.yaml", `providers:
  - id: local
    headers: {X-Remove: ""}
    query: {remove: ""}
    models:
      - id: model
        headers: {X-Account: ""}
        query: {account: ""}
`)}
	got, err := ResolveConfig(f, e)
	if err != nil {
		t.Fatal(err)
	}
	cfg := got[0].Models[0].Configuration
	if !reflect.DeepEqual(cfg.Headers, map[string]string{"X-Account": "provider-value"}) || !reflect.DeepEqual(cfg.Query, map[string]string{"account": "provider-value"}) {
		t.Fatal("empty model option must remove override and reveal provider value; empty provider option must remove key")
	}
}

func addConfigCache(t *testing.T, f *legacy.Fleet, e ConfigEvidence, content string) string {
	t.Helper()
	digest := func(value string) string { return configFile("", value).SHA256 }
	source, declaring := "https://config.example/models?token=secret-query", f.SourceHome+"/juex.yaml"
	context := digest("work_dir=" + e.Identities[e.Contexts["abc234"].WorkingDirectory])
	name := "cache/config-imports/" + digest(source) + "-" + digest(e.Identities[declaring]) + "-" + context + ".json"
	record := map[string]any{"version": 3, "source": "https://config.example/models", "source_sha256": digest(source), "declaring_sha256": digest(e.Identities[declaring]), "context_sha256": context, "content_sha256": "sha256:" + digest(content), "content": content, "fetched_at": time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}
	b, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	f.Files = append(f.Files, configFile(name, string(b)))
	return name
}

func TestResolveConfigUsesExactCacheIdentityAndImportOrder(t *testing.T) {
	f, e := configFixture()
	imported := string(f.DefaultHome.Files[0].Data)
	base := configFile("juex.yaml", "imports:\n  - source: https://config.example/models?token=secret-query\nmodels: [local:backup]\n")
	f.DefaultHome.Files, f.Files = []legacy.SourceFile{base}, []legacy.SourceFile{base}
	name := addConfigCache(t, &f, e, imported)
	// An unrelated newer cache must never replace the exact source context.
	f.Files = append(f.Files, configFile("cache/config-imports/unrelated.json", "{}"))
	got, err := ResolveConfig(f, e)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Models[0].Ref != "local:backup" || len(got[0].Sources) != 2 || got[0].Sources[0].Kind != "remote-import" {
		t.Fatal("imports must precede declaring values")
	}
	for _, field := range []string{"version", "source_sha256", "declaring_sha256", "context_sha256", "content_sha256", "source", "fetched_at"} {
		t.Run(field, func(t *testing.T) {
			copy := f
			copy.Files = append([]legacy.SourceFile(nil), f.Files...)
			var record map[string]any
			if err := json.Unmarshal(copy.Files[1].Data, &record); err != nil {
				t.Fatal(err)
			}
			delete(record, field)
			b, _ := json.Marshal(record)
			copy.Files[1] = configFile(name, string(b))
			if _, err := ResolveConfig(copy, e); err == nil {
				t.Fatal("invalid cache accepted")
			}
		})
	}
	e.Contexts["abc234"] = ConfigContext{WorkingDirectory: "/source/another"}
	e.Identities["/source/another"] = "/source/another"
	if _, err := ResolveConfig(f, e); err == nil {
		t.Fatal("wrong context selected")
	}
}

func TestResolveConfigRejectsMissingAmbiguousAndUnsupportedEvidence(t *testing.T) {
	cases := map[string]func(*legacy.Fleet, *ConfigEvidence){
		"missing-context":  func(_ *legacy.Fleet, e *ConfigEvidence) { delete(e.Contexts, "abc234") },
		"missing-identity": func(_ *legacy.Fleet, e *ConfigEvidence) { delete(e.Identities, "/source/home/juex.yaml") },
		"missing-presence": func(f *legacy.Fleet, _ *ConfigEvidence) { f.Agents[0].AbsentFiles = nil },
		"duplicate-source": func(f *legacy.Fleet, _ *ConfigEvidence) {
			f.DefaultHome.Files = append(f.DefaultHome.Files, f.DefaultHome.Files[0])
		},
		"corrupt-source":         func(f *legacy.Fleet, _ *ConfigEvidence) { f.DefaultHome.Files[0].Data = []byte("{}") },
		"contradictory-presence": func(f *legacy.Fleet, _ *ConfigEvidence) { f.DefaultHome.AbsentFiles = []string{"juex.yaml"} },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f, e := configFixture()
			change(&f, &e)
			if _, err := ResolveConfig(f, e); err == nil {
				t.Fatal("invalid evidence accepted")
			}
		})
	}
	for _, data := range []string{"unexpected: private-secret", "hooks: private-secret", "models: [missing:model]", "models: [local:backup, local:backup]", "modules: {typo: {enabled: true}}", "preset: unknown", "fleet: {addr: private-secret}", "a: &a {imports: []}\n<<: *a", "models: []\n---\nmodels: [local:backup]"} {
		f, e := configFixture()
		f.Agents[0].AbsentFiles = nil
		f.Agents[0].Files = []legacy.SourceFile{configFile("juex.yaml", data)}
		if _, err := ResolveConfig(f, e); err == nil || strings.Contains(err.Error(), "private-secret") {
			t.Fatal("unsupported config accepted or secret leaked")
		}
	}
}

func TestResolveConfigLocalImportsRequireCapturedBytes(t *testing.T) {
	f, e := configFixture()
	base := configFile("juex.yaml", "imports: [{source: models.yaml}]\n")
	imported := configFile(filepath.Join(f.SourceHome, "models.yaml"), string(f.DefaultHome.Files[0].Data))
	f.DefaultHome.Files, f.Files = []legacy.SourceFile{base}, []legacy.SourceFile{base}
	e.Identities[imported.Path] = imported.Path
	if _, err := ResolveConfig(f, e); err == nil {
		t.Fatal("uncaptured import accepted")
	}
	e.LocalImports = []legacy.SourceFile{imported}
	if _, err := ResolveConfig(f, e); err != nil {
		t.Fatal(err)
	}
	e.LocalImports[0] = configFile(imported.Path, "imports: []\n")
	if _, err := ResolveConfig(f, e); err == nil {
		t.Fatal("nested import accepted")
	}
}

func TestResolveConfigDistinctHomesWorkspaceAndExplicitReplay(t *testing.T) {
	f, e := configFixture()
	f.DefaultHome.Directory = "/source/default"
	e.Identities["/source/default/juex.yaml"] = "/source/default/juex.yaml"
	f.Files = []legacy.SourceFile{configFile("juex.yaml", "models: [local:backup]\npreset: minimal\nmodules: {memory: {enabled: true}}\nextensions: {allow: [home-ext]}\n")}
	f.Workspaces[0].AbsentFiles = nil
	f.Workspaces[0].Files = []legacy.SourceFile{configFile(".juex/juex.yaml", "models: [local:qwen3.8:27b]\npreset: standard\nmodules: {memory: {enabled: false}}\n")}
	f.Agents[0].AbsentFiles = nil
	f.Agents[0].Files = []legacy.SourceFile{configFile("juex.yaml", "models: [local:backup]\nextensions: {allow: [agent-ext]}\n")}
	got, err := ResolveConfig(f, e)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Models[0].Ref != "local:backup" || got[0].Preset != "standard" || got[0].Modules["memory"] || len(got[0].Sources) != 4 {
		t.Fatal("persistent layer order changed")
	}
	ctx := e.Contexts["abc234"]
	file := f.Files[0]
	file.Path = "/source/home/juex.yaml"
	ctx.ExplicitFiles = []legacy.SourceFile{file}
	e.Contexts["abc234"] = ctx
	got, err = ResolveConfig(f, e)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Preset != "minimal" || !got[0].Modules["memory"] || !reflect.DeepEqual(got[0].ExtensionAllow, []string{"agent-ext"}) || len(got[0].Sources) != 4 {
		t.Fatal("explicit replay must override values but preserve durable extension policy and single provenance")
	}
	ctx.ExplicitFiles = []legacy.SourceFile{configFile("/startup/override.yaml", "models: [local:qwen3.8:27b]\npreset: standard\n")}
	e.Identities["/startup/override.yaml"] = "/startup/override.yaml"
	e.Contexts["abc234"] = ctx
	got, err = ResolveConfig(f, e)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Models[0].Ref != "local:qwen3.8:27b" || got[0].Preset != "standard" || len(got[0].Sources) != 5 {
		t.Fatal("explicit startup configuration was not last")
	}
	ctx.ModelRefs = []string{"local:backup"}
	e.Contexts["abc234"] = ctx
	got, err = ResolveConfig(f, e)
	if err != nil || got[0].Models[0].Ref != "local:backup" {
		t.Fatal("explicit model selection must be last")
	}
}

func TestResolveConfigRemoteCacheUsesExplicitStartupContext(t *testing.T) {
	f, e := configFixture()
	imported := string(f.DefaultHome.Files[0].Data)
	base := configFile("juex.yaml", "imports: [{source: 'https://config.example/models?token=secret-query'}]\n")
	f.DefaultHome.Files, f.Files = []legacy.SourceFile{base}, []legacy.SourceFile{base}
	addConfigCache(t, &f, e, imported)
	ctx := e.Contexts["abc234"]
	ctx.ExplicitFiles = []legacy.SourceFile{configFile("/source/explicit.yaml", "models: [local:backup]\n")}
	e.Identities["/source/explicit.yaml"] = "/canonical/explicit.yaml"
	e.Contexts["abc234"] = ctx
	if _, err := ResolveConfig(f, e); err == nil {
		t.Fatal("cache from a different explicit-file context accepted")
	}
	var record map[string]any
	if err := json.Unmarshal(f.Files[1].Data, &record); err != nil {
		t.Fatal(err)
	}
	context := configFile("", "work_dir=/source/work\nexplicit=/canonical/explicit.yaml").SHA256
	record["context_sha256"] = context
	name := "cache/config-imports/" + record["source_sha256"].(string) + "-" + record["declaring_sha256"].(string) + "-" + context + ".json"
	b, _ := json.Marshal(record)
	f.Files[1] = configFile(name, string(b))
	got, err := ResolveConfig(f, e)
	if err != nil || got[0].Models[0].Ref != "local:backup" {
		t.Fatalf("exact startup context rejected: %v", err)
	}
}

func TestResolveConfigRejectsOtherWorkspaceEvenWithCompleteCache(t *testing.T) {
	f, e := configFixture()
	imported := string(f.DefaultHome.Files[0].Data)
	base := configFile("juex.yaml", "imports: [{source: 'https://config.example/models?token=secret-query'}]\n")
	f.DefaultHome.Files, f.Files = []legacy.SourceFile{base}, []legacy.SourceFile{base}
	e.Contexts["abc234"] = ConfigContext{WorkingDirectory: "/source/other"}
	e.Identities["/source/other"] = "/source/other"
	addConfigCache(t, &f, e, imported)
	if _, err := ResolveConfig(f, e); err == nil {
		t.Fatal("Agent was configured using another Workspace's cache context")
	}
}
