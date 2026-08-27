package eval

import (
	"bufio"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/homestore"
	"gopkg.in/yaml.v3"
)

func TestCIWorkflowPreparesAndRunsRaceTests(t *testing.T) {
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		On struct {
			WorkflowDispatch struct {
				Inputs map[string]struct {
					Type    string   `yaml:"type"`
					Default string   `yaml:"default"`
					Options []string `yaml:"options"`
				} `yaml:"inputs"`
			} `yaml:"workflow_dispatch"`
		} `yaml:"on"`
		Jobs struct {
			Test struct {
				Name     string `yaml:"name"`
				Strategy struct {
					Matrix struct {
						Include string `yaml:"include"`
					} `yaml:"matrix"`
				} `yaml:"strategy"`
				Steps []struct {
					Name string `yaml:"name"`
					If   string `yaml:"if"`
					Run  string `yaml:"run"`
					Uses string `yaml:"uses"`
				} `yaml:"steps"`
			} `yaml:"test"`
			WindowsRaceGate struct {
				Name  string `yaml:"name"`
				If    string `yaml:"if"`
				Needs string `yaml:"needs"`
				Steps []struct {
					Run string `yaml:"run"`
				} `yaml:"steps"`
			} `yaml:"windows-race-gate"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatalf("parse CI workflow: %v", err)
	}

	benchmarkInput, ok := workflow.On.WorkflowDispatch.Inputs["windows_topology"]
	if !ok {
		t.Fatal("CI workflow is missing the manual Windows topology input")
	}
	if benchmarkInput.Type != "choice" || benchmarkInput.Default != "split" ||
		!reflect.DeepEqual(benchmarkInput.Options, []string{"1", "2", "default", "split", "web-g1", "web-g2"}) {
		t.Fatalf("Windows topology input = %#v", benchmarkInput)
	}
	if strings.Contains(workflow.Jobs.Test.Name, "matrix.suite == 'ordinary'") {
		t.Fatalf("ordinary shard must not own the stable aggregate check name: %s", workflow.Jobs.Test.Name)
	}
	for _, want := range []string{
		`inputs.windows_topology == 'split'`,
		`startsWith(inputs.windows_topology, 'web-g')`,
		`"os":"windows-latest","suite":"ordinary"`,
		`"os":"windows-latest","suite":"web"`,
		`"os":"windows-latest","suite":"e2e"`,
		`"os":"windows-latest","suite":"eval"`,
	} {
		if !strings.Contains(workflow.Jobs.Test.Strategy.Matrix.Include, want) {
			t.Errorf("CI matrix missing %q:\n%s", want, workflow.Jobs.Test.Strategy.Matrix.Include)
		}
	}

	buildAt, windowsRaceAt, installerAt, artifactAt := -1, -1, -1, -1
	for index, step := range workflow.Jobs.Test.Steps {
		switch step.Name {
		case "Build Juex evaluator binary":
			buildAt = index
		case "Run Go race tests":
			if strings.TrimSpace(step.If) != "matrix.os != 'windows-latest'" || strings.TrimSpace(step.Run) != "go test ./... -race -count=1" {
				t.Errorf("non-Windows race step = if %q run %q", step.If, step.Run)
			}
		case "Run Go race tests on Windows":
			windowsRaceAt = index
			if strings.TrimSpace(step.If) != "matrix.os == 'windows-latest'" {
				t.Errorf("Windows race condition = %q", step.If)
			}
			for _, want := range []string{
				`inputs.windows_topology || 'split'`,
				`mapfile -t test_packages`,
				`grep -Ev '(/internal/web|/tests/(e2e|eval))$'`,
				`test_packages=(./internal/web)`,
				`mode" == "web-g1`,
				`export GOMAXPROCS=1`,
				`test_packages=(./tests/e2e)`,
				`test_packages=(./tests/eval)`,
				`export GOMAXPROCS=2`,
				`race_args=("-p=2")`,
				`go test -json "${test_packages[@]}" -race -count=1`,
				`go test "${test_packages[@]}" -race -count=1`,
				`race_args+=("-p=$mode")`,
			} {
				if !strings.Contains(step.Run, want) {
					t.Errorf("Windows race step missing %q:\n%s", want, step.Run)
				}
			}
			if strings.Count(step.Run, `export GOMAXPROCS=2`) != 2 {
				t.Errorf("Windows race step must bound ordinary and web runtime concurrency:\n%s", step.Run)
			}
			if !strings.Contains(step.Run, `if [[ "$mode" != "default" ]]`) {
				t.Errorf("Windows race step cannot select default package parallelism:\n%s", step.Run)
			}
		case "Test PowerShell release installer":
			installerAt = index
			if !strings.Contains(step.If, "matrix.suite == 'ordinary' || matrix.suite == 'all'") {
				t.Errorf("PowerShell installer condition = %q", step.If)
			}
		}
		if step.Uses == "actions/upload-artifact@v4" {
			artifactAt = index
		}
	}
	if buildAt < 0 || windowsRaceAt < 0 || installerAt < 0 || artifactAt < 0 {
		t.Fatalf("CI step indexes: build=%d windows-race=%d installer=%d artifact=%d", buildAt, windowsRaceAt, installerAt, artifactAt)
	}
	if buildAt >= windowsRaceAt || windowsRaceAt >= installerAt || installerAt >= artifactAt {
		t.Fatalf("CI step order: build=%d windows-race=%d installer=%d artifact=%d", buildAt, windowsRaceAt, installerAt, artifactAt)
	}
	gate := workflow.Jobs.WindowsRaceGate
	if gate.Name != "test (windows-latest)" || gate.Needs != "test" ||
		!strings.Contains(gate.If, "always()") || !strings.Contains(gate.If, "github.event_name != 'workflow_dispatch'") ||
		len(gate.Steps) != 1 || !strings.Contains(gate.Steps[0].Run, `TEST_MATRIX_RESULT" != "success`) {
		t.Fatalf("Windows race aggregate gate = %#v", gate)
	}
}

func TestIntegrationWorkflowUsesExplicitProviderConfigFile(t *testing.T) {
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "integration.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs struct {
			Live struct {
				Steps []struct {
					Name string `yaml:"name"`
					Run  string `yaml:"run"`
				} `yaml:"steps"`
			} `yaml:"live"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatalf("parse integration workflow: %v", err)
	}

	for _, step := range workflow.Jobs.Live.Steps {
		if step.Name != "Write live provider config" {
			continue
		}
		for _, want := range []string{
			`export JUEX_PROVIDER_CONFIG="$RUNNER_TEMP/juex-integration-${LIVE_PROVIDER}.yaml"`,
			`path = Path(os.environ["JUEX_PROVIDER_CONFIG"])`,
			`printf 'JUEX_PROVIDER_CONFIG=%s\n' "$JUEX_PROVIDER_CONFIG" >> "$GITHUB_ENV"`,
		} {
			if !strings.Contains(step.Run, want) {
				t.Fatalf("provider-config step missing %q:\n%s", want, step.Run)
			}
		}
		if strings.Contains(step.Run, "JUEX_HOME") {
			t.Fatalf("provider-config step still changes JUEX_HOME:\n%s", step.Run)
		}
		return
	}
	t.Fatal("integration workflow is missing the provider-config step")
}

func TestWriteModelConfigSelectsTopLevelAndExplicitRef(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	isolateWriteModelConfigHomes(t)
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	source := filepath.Join(t.TempDir(), "juex.yaml")
	body := `models: [alpha:model-a]
environment:
  variables:
    PROVIDER_API_ID: must-not-replace-alpha
    PROVIDER_API_MODEL: must-not-replace-model-a
    PROVIDER_API_PROTOCOL: anthropic/messages
    PROVIDER_API_KEY: environment-key
    PROVIDER_CONTEXT_WINDOW: "12345"
    UNRELATED_SECRET: must-not-be-copied
providers:
  - id: alpha
    protocol: openai/chat
    api_key: alpha-key
    models:
      - id: model-a
  - id: beta
    protocol: anthropic/messages
    api_key: beta-key
    models:
      - id: model-b
runtime:
  tool_timeout: 45s
`
	if err := os.WriteFile(source, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		name string
		ref  string
		want string
	}{
		{name: "top level", want: "alpha:model-a"},
		{name: "explicit ref", ref: "beta:model-b", want: "beta:model-b"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "selected.yaml")
			args := []string{
				"python", "-m", "tests.eval.juex_eval", "write-model-config",
				"--juex", "",
				"--source", source,
				"--output", output,
			}
			if tt.ref != "" {
				args = append(args, "--ref", tt.ref)
			}
			runUV(t, root, args...)

			data, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			var selected struct {
				Models      []string `yaml:"models"`
				Environment struct {
					Variables map[string]string `yaml:"variables"`
				} `yaml:"environment"`
				Providers []struct {
					ID     string `yaml:"id"`
					Models []struct {
						ID string `yaml:"id"`
					} `yaml:"models"`
				} `yaml:"providers"`
			}
			if err := yaml.Unmarshal(data, &selected); err != nil {
				t.Fatalf("parse selected config: %v\n%s", err, data)
			}
			if len(selected.Models) != 1 || selected.Models[0] != tt.want {
				t.Fatalf("models = %q, want [%q]", selected.Models, tt.want)
			}
			if len(selected.Providers) != 1 || len(selected.Providers[0].Models) != 1 {
				t.Fatalf("selected provider shape = %#v", selected.Providers)
			}
			wantProvider, wantModel, _ := strings.Cut(tt.want, ":")
			if selected.Providers[0].ID != wantProvider || selected.Providers[0].Models[0].ID != wantModel {
				t.Fatalf(
					"selected provider/model = %s:%s, want %s",
					selected.Providers[0].ID,
					selected.Providers[0].Models[0].ID,
					tt.want,
				)
			}
			if selected.Environment.Variables["PROVIDER_API_KEY"] != "environment-key" ||
				selected.Environment.Variables["PROVIDER_CONTEXT_WINDOW"] != "12345" {
				t.Fatalf("selected provider environment = %#v", selected.Environment.Variables)
			}
			for _, key := range []string{
				"PROVIDER_API_ID",
				"PROVIDER_API_PROTOCOL",
				"PROVIDER_API_MODEL",
				"UNRELATED_SECRET",
			} {
				if _, ok := selected.Environment.Variables[key]; ok {
					t.Fatalf("selected config retained %s in environment.variables", key)
				}
			}
			if strings.Contains(string(data), "tool_timeout") {
				t.Fatalf("selected config retained unrelated runtime settings:\n%s", data)
			}
		})
	}
}

func TestWriteModelConfigResolvesDirectConfigImports(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	isolateWriteModelConfigHomes(t)
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	imported := filepath.Join(dir, "providers.yaml")
	if err := os.WriteFile(imported, []byte(`models: [local:imported]
providers:
  - id: local
    protocol: openai/chat
    base_url: https://base.invalid
    api_key: imported-secret
    headers: {X-Imported: base}
    models:
      - id: imported
`), 0o600); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "juex.yaml")
	if err := os.WriteFile(source, []byte(`imports:
  - source: providers.yaml
providers:
  - id: local
    headers: {X-Main: override}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "selected.yaml")
	runUV(t, root,
		"python", "-m", "tests.eval.juex_eval", "write-model-config",
		"--juex", "",
		"--source", source,
		"--output", output,
	)
	body, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"local:imported", "imported-secret", "X-Imported: base", "X-Main: override"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("selected config missing %q:\n%s", want, body)
		}
	}

	if err := os.WriteFile(imported, []byte("imports: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("uv", "run", "--quiet", "--project", root,
		"python", "-m", "tests.eval.juex_eval", "write-model-config",
		"--juex", "",
		"--source", source,
		"--output", output,
	)
	cmd.Dir = root
	combined, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(combined), "nested imports are not supported") {
		t.Fatalf("nested import command error = %v, output = %s", err, combined)
	}
}

func TestWriteModelConfigRejectsInvalidUnprojectedImportedSchema(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	isolateWriteModelConfigHomes(t)
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	imported := filepath.Join(dir, "providers.yaml")
	if err := os.WriteFile(imported, []byte(`runtime:
  typo: true
models: [local:imported]
providers:
  - id: local
    protocol: openai/chat
    models: [{id: imported}]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "juex.yaml")
	if err := os.WriteFile(source, []byte("imports:\n  - source: providers.yaml\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("uv", "run", "--quiet", "--project", root,
		"python", "-m", "tests.eval.juex_eval", "write-model-config",
		"--juex", "",
		"--source", source,
		"--output", filepath.Join(dir, "selected.yaml"),
	)
	cmd.Dir = root
	combined, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(combined), "provider config is not loadable by Juex") {
		t.Fatalf("invalid imported source error = %v, output = %s", err, combined)
	}
}

func TestWriteModelConfigPreservesTopLevelNullImportSemantics(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	isolateWriteModelConfigHomes(t)
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "base.yaml"), []byte(`models: [local:imported]
providers:
  - id: local
    protocol: openai/chat
    models: [{id: imported}]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "null.yaml"), []byte("models: null\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "juex.yaml")
	if err := os.WriteFile(source, []byte(`imports:
  - source: base.yaml
  - source: null.yaml
`), 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "selected.yaml")
	runUV(t, root,
		"python", "-m", "tests.eval.juex_eval", "write-model-config",
		"--juex", "",
		"--source", source,
		"--output", output,
	)
	body, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "local:imported") {
		t.Fatalf("selected config lost the earlier model chain:\n%s", body)
	}
}

func TestWriteModelConfigResolvesColonNamedLocalImport(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows filenames cannot contain colons")
	}
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	isolateWriteModelConfigHomes(t)
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	importedName := "providers:v2.yaml"
	if err := os.WriteFile(filepath.Join(dir, importedName), []byte(`models: [local:colon]
providers:
  - id: local
    protocol: openai/chat
    api_key: colon-secret
    models: [{id: colon}]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "juex.yaml")
	if err := os.WriteFile(source, []byte("imports:\n  - source: "+importedName+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "selected.yaml")
	runUV(t, root,
		"python", "-m", "tests.eval.juex_eval", "write-model-config",
		"--juex", "",
		"--source", source,
		"--output", output,
	)
	body, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"local:colon", "colon-secret"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("selected config missing %q:\n%s", want, body)
		}
	}
}

func TestWriteModelConfigResolvesHTTPConfigImport(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	isolateWriteModelConfigHomes(t)
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`models: [remote:model]
providers:
  - id: remote
    protocol: openai/chat
    api_key: remote-secret
    models: [{id: model}]
`))
	}))
	defer server.Close()
	bypassProxyForLoopbackServer(t, server.URL)
	dir := t.TempDir()
	source := filepath.Join(dir, "juex.yaml")
	if err := os.WriteFile(source, []byte("imports:\n  - source: "+server.URL+"/config.yaml?token=request-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "selected.yaml")
	runUV(t, root,
		"python", "-m", "tests.eval.juex_eval", "write-model-config",
		"--juex", "",
		"--source", source,
		"--output", output,
	)
	body, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"remote:model", "remote-secret"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("selected config missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(string(body), "request-secret") {
		t.Fatalf("selected config retained import URL query:\n%s", body)
	}
}

func TestWriteModelConfigRejectsIncompleteHTTPConfigImport(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	isolateWriteModelConfigHomes(t)
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte(`models: [remote:model]
providers:
  - id: remote
    protocol: openai/chat
    models: [{id: model}]
`))
	}))
	defer server.Close()
	bypassProxyForLoopbackServer(t, server.URL)
	dir := t.TempDir()
	source := filepath.Join(dir, "juex.yaml")
	if err := os.WriteFile(source, []byte("imports:\n  - source: "+server.URL+"/config.yaml\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("uv", "run", "--quiet", "--project", root,
		"python", "-m", "tests.eval.juex_eval", "write-model-config",
		"--source", source,
		"--output", filepath.Join(dir, "selected.yaml"),
	)
	cmd.Dir = root
	combined, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(combined), "HTTP 206") {
		t.Fatalf("partial response error = %v, output = %s", err, combined)
	}
}

func TestProviderConfigSelectionIsStableAndRedacted(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"from datetime import date",
		"import json",
		"import shlex",
		"from pathlib import Path",
		"from tests.eval.juex_eval import selection",
		"providers = [",
		"    {'id': 'zeta', 'api_key': 'never-report-zeta', 'capabilities': {'tools': True}, 'models': [{'id': 'large', 'context_window': 64000}]},",
		"    {'id': 'alpha', 'api_key': 'never-report-alpha', 'headers': {'Authorization': 'secret-header'}, 'models': [{'id': 'one'}, {'id': 'two'}]},",
		"]",
		"cfg_a = {'environment': {'variables': {'TOKEN': 'never-report-token'}}, 'providers': providers}",
		"cfg_b = {'providers': list(reversed(providers))}",
		"def choose(cfg, seed):",
		"    return selection.select(cfg, kind='provider-smoke', config_path=Path('/tmp/config.yaml'), seed=seed, command_prefix=['juex-eval', 'provider-smoke'])",
		"selected_a, evidence_a = choose(cfg_a, 'fixed-seed')",
		"selected_b, evidence_b = choose(cfg_b, 'fixed-seed')",
		"assert [item.ref for item in selection.enumerate_candidates(cfg_a)] == ['alpha:one', 'alpha:two', 'zeta:large']",
		"assert selected_a[0].ref == selected_b[0].ref",
		"assert evidence_a.redacted_config_hash == evidence_b.redacted_config_hash",
		"cfg_changed = json.loads(json.dumps(cfg_a))",
		"cfg_changed['providers'][0]['protocol'] = 'openai/responses'",
		"cfg_changed['providers'][0]['capabilities']['reasoning_effort'] = True",
		"cfg_changed['providers'][0]['models'][0]['thinking_effort'] = 'high'",
		"assert choose(cfg_changed, 'fixed-seed')[1].redacted_config_hash != evidence_a.redacted_config_hash",
		"cfg_env = json.loads(json.dumps(cfg_a))",
		"cfg_env['environment']['variables'].update({'PROVIDER_CONTEXT_WINDOW': '32000', 'PROVIDER_THINKING_EFFORT': 'high', 'PROVIDER_API_KEY': 'never-hash-key'})",
		"env_hash = choose(cfg_env, 'fixed-seed')[1].redacted_config_hash",
		"assert env_hash != evidence_a.redacted_config_hash",
		"cfg_env['environment']['variables'].update({'TOKEN': 'changed-secret', 'PROVIDER_API_KEY': 'changed-key'})",
		"assert choose(cfg_env, 'fixed-seed')[1].redacted_config_hash == env_hash",
		"cfg_env_whitespace = json.loads(json.dumps(cfg_env))",
		"cfg_env_whitespace['environment']['variables']['PROVIDER_CONTEXT_WINDOW'] = ' 32000 '",
		"assert choose(cfg_env_whitespace, 'fixed-seed')[1].redacted_config_hash != env_hash",
		"cfg_env_base = json.loads(json.dumps(cfg_env))",
		"cfg_env_base['environment']['variables']['PROVIDER_API_BASE'] = 'https://env-gateway.example/v1?token=never-report-env-query'",
		"env_endpoint_evidence = choose(cfg_env_base, 'fixed-seed')[1]",
		"assert env_endpoint_evidence.redacted_config_hash != env_hash",
		"cfg_smoke_endpoint = json.loads(json.dumps(cfg_env_base))",
		"cfg_smoke_endpoint['providers'][0]['base_url'] = 'https://smoke-gateway-a.example/v1'",
		"smoke_endpoint_a = selection.select(cfg_smoke_endpoint, kind='provider-smoke', config_path=Path('/tmp/config.yaml'), seed='fixed-seed', provider_api_base_override='', command_prefix=['juex-eval', 'provider-smoke'])[1]",
		"cfg_smoke_endpoint['providers'][0]['base_url'] = 'https://smoke-gateway-b.example/v1'",
		"smoke_endpoint_b = selection.select(cfg_smoke_endpoint, kind='provider-smoke', config_path=Path('/tmp/config.yaml'), seed='fixed-seed', provider_api_base_override='', command_prefix=['juex-eval', 'provider-smoke'])[1]",
		"assert smoke_endpoint_a.redacted_config_hash != smoke_endpoint_b.redacted_config_hash",
		"cfg_endpoint = json.loads(json.dumps(cfg_a))",
		"cfg_endpoint['providers'][0]['base_url'] = 'https://user:never-report-password@gateway.example/v1?token=never-report-query'",
		"endpoint_evidence = choose(cfg_endpoint, 'fixed-seed')[1]",
		"endpoint_hash = endpoint_evidence.redacted_config_hash",
		"assert endpoint_hash != evidence_a.redacted_config_hash",
		"cfg_endpoint['providers'][0]['base_url'] = ' https://user:never-report-password@gateway.example/v1?token=never-report-query '",
		"assert choose(cfg_endpoint, 'fixed-seed')[1].redacted_config_hash != endpoint_hash",
		"cfg_endpoint['providers'][0]['base_url'] = 'https://other-gateway.example/v1'",
		"assert choose(cfg_endpoint, 'fixed-seed')[1].redacted_config_hash != endpoint_hash",
		"cfg_profile = json.loads(json.dumps(cfg_a))",
		"cfg_profile['providers'][0].update({'capabilities': {'tools': True, 'reasoning_replay': True}, 'compat': {'codex_transport': 'websocket'}})",
		"cfg_profile['providers'][0]['models'][0].update({'headers': {'X-Route': 'never-report-route'}, 'query': {'tenant': 'never-report-tenant'}, 'compat': {'reasoning_replay_fields': ['reasoning']}})",
		"profile_evidence = choose(cfg_profile, 'fixed-seed')[1]",
		"assert profile_evidence.redacted_config_hash != evidence_a.redacted_config_hash",
		"cfg_profile_date = json.loads(json.dumps(cfg_a))",
		"cfg_profile_date['providers'][0]['headers'] = {'X-Date': date(2026, 8, 20)}",
		"cfg_profile_text = json.loads(json.dumps(cfg_a))",
		"cfg_profile_text['providers'][0]['headers'] = {'X-Date': '2026-08-20'}",
		"assert choose(cfg_profile_date, 'fixed-seed')[1].redacted_config_hash == choose(cfg_profile_text, 'fixed-seed')[1].redacted_config_hash",
		"cfg_null_capability = {'providers': [{'id': 'null-capability', 'capabilities': {'tools': False, 'reasoning_effort': True}, 'models': [{'id': 'model', 'capabilities': {'tools': None, 'reasoning_effort': None}}]}]}",
		"null_candidate = selection.enumerate_candidates(cfg_null_capability)[0]",
		"assert null_candidate.tools_capability == 'false' and null_candidate.reasoning_effort_capability == 'true', null_candidate",
		"assert selection.eligible_candidates(cfg_null_capability, 'provider-smoke') == []",
		"assert evidence_a.eligible_refs == ('alpha:one', 'alpha:two', 'zeta:large')",
		"assert len({choose(cfg_a, f'seed-{index}')[0][0].ref for index in range(20)}) > 1",
		"rendered = json.dumps([evidence_a.as_dict(), endpoint_evidence.as_dict(), env_endpoint_evidence.as_dict(), profile_evidence.as_dict()])",
		"for secret in ['never-report-zeta', 'never-report-alpha', 'secret-header', 'never-report-token', 'never-report-password', 'never-report-query', 'never-report-env-query', 'never-report-route', 'never-report-tenant']:",
		"    assert secret not in rendered, rendered",
		"tokens = shlex.split(evidence_a.reproduction_command)",
		"assert tokens[tokens.index('--config') + 1] == str(Path('/tmp/config.yaml').resolve())",
		"assert '--selection-seed fixed-seed' in evidence_a.reproduction_command",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestProviderConfigLoaderLayersHomeAndPreservesStringMapLexemes(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"import os",
		"import tempfile",
		"from pathlib import Path",
		"from tests.eval.juex_eval import helper, selection",
		"with tempfile.TemporaryDirectory() as tmp:",
		"    root = Path(tmp)",
		"    home = root / 'home'",
		"    instance = root / 'instance'",
		"    default_config = home / '.juex' / 'juex.yaml'",
		"    instance_config = instance / 'juex.yaml'",
		"    overlay = root / 'overlay.yaml'",
		"    default_config.parent.mkdir(parents=True)",
		"    instance.mkdir(parents=True)",
		"    default_config.write_text('''runtime:",
		"  tool_timeout: 40s",
		"compaction:",
		"  reserve_tokens: 8000",
		"tool_output:",
		"  inline_max_bytes: 1000",
		"skills:",
		"  include: [base]",
		"environment:",
		"  variables: {PROVIDER_THINKING_EFFORT: low}",
		"providers:",
		"  - id: layered",
		"    protocol: openai/chat",
		"    headers: {X-Mode: yes}",
		"    models: [{id: inherited}]",
		"''', encoding='utf-8')",
		"    instance_config.write_text('''providers:",
		"  - id: layered",
		"    api_key: never-report-layered-key",
		"    models:",
		"      - id: shared",
		"        query: {as_of: 2026-08-20}",
		"''', encoding='utf-8')",
		"    overlay.write_text('''runtime:",
		"  pending_input_ttl: 30s",
		"compaction:",
		"  keep_recent_tokens: 6000",
		"tool_output:",
		"  preview_head_bytes: 200",
		"skills:",
		"  exclude: [tmp]",
		"providers:",
		"  - id: layered",
		"    headers: {X-Overlay: no}",
		"    models: [{id: shared, context_window: 64000}]",
		"''', encoding='utf-8')",
		"    original = {name: os.environ.get(name) for name in ['HOME', 'USERPROFILE', 'JUEX_HOME']}",
		"    os.environ.update({'HOME': str(home), 'USERPROFILE': str(home), 'JUEX_HOME': str(instance)})",
		"    try:",
		"        cfg = helper.load_source_config(overlay)",
		"        provider = selection.merged_providers(cfg)[0]",
		"        assert [model['id'] for model in provider['models']] == ['inherited', 'shared'], provider",
		"        assert provider['protocol'] == 'openai/chat' and provider['api_key'] == 'never-report-layered-key'",
		"        assert provider['headers'] == {'X-Mode': 'yes', 'X-Overlay': 'no'}, provider['headers']",
		"        assert cfg['runtime'] == {'tool_timeout': '40s', 'pending_input_ttl': '30s'}, cfg['runtime']",
		"        assert cfg['compaction'] == {'reserve_tokens': 8000, 'keep_recent_tokens': 6000}, cfg['compaction']",
		"        assert cfg['tool_output'] == {'inline_max_bytes': 1000, 'preview_head_bytes': 200}, cfg['tool_output']",
		"        assert cfg['skills'] == {'include': ['base'], 'exclude': ['tmp']}, cfg['skills']",
		"        shared = provider['models'][1]",
		"        assert shared['query']['as_of'] == '2026-08-20' and shared['context_window'] == 64000, shared",
		"        out = root / 'selected.yaml'",
		"        helper.write_selected_config(cfg, 'layered', 'shared', out)",
		"        selected = helper.load_yaml_file(out)",
		"        selected_provider = selected['providers'][0]",
		"        assert selected_provider['headers'] == {'X-Mode': 'yes', 'X-Overlay': 'no'}",
		"        assert selected_provider['models'][0]['query']['as_of'] == '2026-08-20'",
		"        lexical = root / 'lexical.yaml'",
		"        lexical.write_text('''providers:",
		"  - id: lexical",
		"    headers: &base_headers {X-Mode: yes}",
		"    models:",
		"      - id: model",
		"        headers:",
		"          <<: *base_headers",
		"          X-Date: 2026-08-20",
		"          X-Null: null",
		"''', encoding='utf-8')",
		"        lexical_cfg = helper.load_yaml_file(lexical)",
		"        assert lexical_cfg['providers'][0]['models'][0]['headers'] == {'X-Mode': 'yes', 'X-Date': '2026-08-20', 'X-Null': ''}",
		"        scalar_fields = root / 'scalar-fields.yaml'",
		"        scalar_fields.write_text('''scalar_provider: &scalar_provider",
		"  id: 123",
		"  protocol: openai/chat",
		"  base_url: 456",
		"  api_key: true",
		"  compat:",
		"    codex_transport: false",
		"    reasoning_replay_fields: [123, 2026-08-20, null]",
		"providers:",
		"  - <<: *scalar_provider",
		"    models:",
		"      - id: 2026-08-20",
		"        thinking_effort: 123",
		"''', encoding='utf-8')",
		"        scalar_cfg = helper.load_yaml_file(scalar_fields)",
		"        scalar_provider = scalar_cfg['providers'][0]",
		"        assert scalar_provider['id'] == '123' and scalar_provider['base_url'] == '456' and scalar_provider['api_key'] == 'true', scalar_provider",
		"        assert scalar_provider['compat'] == {'codex_transport': 'false', 'reasoning_replay_fields': ['123', '2026-08-20', '']}, scalar_provider['compat']",
		"        scalar_model = scalar_provider['models'][0]",
		"        assert scalar_model['id'] == '2026-08-20' and scalar_model['thinking_effort'] == '123', scalar_model",
		"        assert [item.ref for item in selection.enumerate_candidates(scalar_cfg)] == ['123:2026-08-20']",
		"        duplicate = root / 'duplicate.yaml'",
		"        duplicate.write_text('runtime:\\n  tool_timeout: 10s\\n  tool_timeout: 20s\\n', encoding='utf-8')",
		"        try:",
		"            helper.load_yaml_file(duplicate)",
		"        except ValueError as exc:",
		"            assert \"duplicate YAML key 'tool_timeout'\" in str(exc), str(exc)",
		"        else:",
		"            raise AssertionError('duplicate YAML key was accepted')",
		"        for index, falsey_value in enumerate(['false\\n', '0\\n', '[]\\n']):",
		"            falsey = root / f'falsey-{index}.yaml'",
		"            falsey.write_text(falsey_value, encoding='utf-8')",
		"            try:",
		"                helper.load_yaml_file(falsey)",
		"            except ValueError as exc:",
		"                assert 'must contain a YAML mapping' in str(exc), str(exc)",
		"            else:",
		"                raise AssertionError(f'falsey non-mapping YAML was accepted: {falsey_value!r}')",
		"        assert helper.safe_ref('p:a/b') != helper.safe_ref('p:a__b')",
		"    finally:",
		"        for name, value in original.items():",
		"            if value is None:",
		"                os.environ.pop(name, None)",
		"            else:",
		"                os.environ[name] = value",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestProviderConfigLoaderMemoizesRemoteImportsAcrossLayers(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"import contextlib",
		"import os",
		"import tempfile",
		"from pathlib import Path",
		"from tests.eval.juex_eval import helper, selection",
		"with tempfile.TemporaryDirectory() as tmp:",
		"    root = Path(tmp)",
		"    home = root / 'home'",
		"    instance = root / 'instance'",
		"    default_config = home / '.juex' / 'juex.yaml'",
		"    effective_config = instance / 'juex.yaml'",
		"    explicit_config = root / 'explicit.yaml'",
		"    default_config.parent.mkdir(parents=True)",
		"    instance.mkdir(parents=True)",
		"    declaration = 'imports:\\n  - source: https://config.example/shared.yaml\\n'",
		"    default_config.write_text(declaration, encoding='utf-8')",
		"    effective_config.write_text(declaration, encoding='utf-8')",
		"    explicit_config.write_text(declaration, encoding='utf-8')",
		"    original_env = {name: os.environ.get(name) for name in ['HOME', 'USERPROFILE', 'JUEX_HOME']}",
		"    original_remote = helper._read_remote_config_import",
		"    original_lock = helper._config_import_cache_lock",
		"    remote_reads = []",
		"    lock_events = []",
		"    @contextlib.contextmanager",
		"    def fake_lock(_home):",
		"        lock_events.append('enter')",
		"        yield",
		"        lock_events.append('exit')",
		"    def fake_remote(identity, _parsed, _declaring, _cache_context_digest):",
		"        remote_reads.append(identity)",
		"        return 'models: [remote:model]\\nproviders:\\n  - id: remote\\n    protocol: openai/chat\\n    models: [{id: model}]\\n', True",
		"    os.environ.update({'HOME': str(home), 'USERPROFILE': str(home), 'JUEX_HOME': str(instance)})",
		"    helper._read_remote_config_import = fake_remote",
		"    helper._config_import_cache_lock = fake_lock",
		"    try:",
		"        cfg = helper.load_source_config(explicit_config)",
		"        assert remote_reads == ['https://config.example/shared.yaml'], remote_reads",
		"        assert lock_events == ['enter', 'exit'], lock_events",
		"        assert [item.ref for item in selection.enumerate_candidates(cfg)] == ['remote:model']",
		"    finally:",
		"        helper._read_remote_config_import = original_remote",
		"        helper._config_import_cache_lock = original_lock",
		"        for name, value in original_env.items():",
		"            if value is None:",
		"                os.environ.pop(name, None)",
		"            else:",
		"                os.environ[name] = value",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestProviderConfigLoaderUsesRuntimeImportCacheLock(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	program := strings.Join([]string{
		"import sys",
		"from pathlib import Path",
		"from tests.eval.juex_eval import helper",
		"with helper._config_import_cache_lock(Path(sys.argv[1])):",
		"    print('ready', flush=True)",
		"    sys.stdin.readline()",
	}, "\n")
	command := exec.Command("uv", "run", "python", "-c", program, home)
	command.Dir = root
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "ready" {
		t.Fatalf("Python lock helper did not become ready: line=%q err=%v", scanner.Text(), scanner.Err())
	}
	lockPath := filepath.Join(home, ".locks", "config-imports-cache.lock")
	lock, lockErr := homestore.AcquireLock(lockPath, homestore.LockTry)
	if lock != nil {
		_ = lock.Close()
	}
	if !errors.Is(lockErr, homestore.ErrLockBusy) {
		t.Fatalf("Go lock attempt while Python holds cache lock = %v, want ErrLockBusy", lockErr)
	}
	if _, err := stdin.Write([]byte("continue\n")); err != nil {
		t.Fatal(err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestProviderConfigLoaderRejectsUnknownRuntimeCacheFields(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	program := strings.Join([]string{
		"import datetime",
		"import hashlib",
		"import json",
		"import os",
		"import tempfile",
		"import urllib.error",
		"import urllib.parse",
		"from pathlib import Path",
		"from tests.eval.juex_eval import helper",
		"with tempfile.TemporaryDirectory() as tmp:",
		"    root = Path(tmp)",
		"    home = root / 'home'",
		"    instance = root / 'instance'",
		"    declaring = home / '.juex' / 'juex.yaml'",
		"    declaring.parent.mkdir(parents=True)",
		"    instance.mkdir(parents=True)",
		"    original_env = {name: os.environ.get(name) for name in ['HOME', 'USERPROFILE', 'JUEX_HOME']}",
		"    os.environ.update({'HOME': str(home), 'USERPROFILE': str(home), 'JUEX_HOME': str(instance)})",
		"    try:",
		"        identity = 'https://config.example/shared.yaml?token=secret'",
		"        parsed = urllib.parse.urlsplit(identity)",
		"        context_digest = helper._config_import_context_digest(root / 'work')",
		"        source_digest = hashlib.sha256(identity.encode('utf-8')).hexdigest()",
		"        declaring_digest = hashlib.sha256(str(helper._config_import_path_identity(declaring)).encode('utf-8')).hexdigest()",
		"        content = 'runtime:\\n  tool_timeout: 41s\\n'",
		"        record = {",
		"            'version': 3, 'source': helper._safe_remote_import_label(parsed),",
		"            'source_sha256': source_digest, 'declaring_sha256': declaring_digest,",
		"            'context_sha256': context_digest, 'fetched_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),",
		"            'content_sha256': 'sha256:' + hashlib.sha256(content.encode('utf-8')).hexdigest(),",
		"            'content': content, 'unexpected_field': 'must-reject',",
		"        }",
		"        cache_dir = instance / 'cache' / 'config-imports'",
		"        cache_dir.mkdir(parents=True)",
		"        cache_path = cache_dir / f'{source_digest}-{declaring_digest}-{context_digest}.json'",
		"        cache_path.write_text(json.dumps(record), encoding='utf-8')",
		"        cache_path.chmod(0o600)",
		"        assert helper._read_config_import_cache(identity, parsed, declaring, context_digest) is None",
		"        record.pop('unexpected_field')",
		"        record['fetched_at'] = datetime.datetime.now(datetime.timezone.utc).isoformat(sep=' ')",
		"        assert not helper._config_import_cache_is_current(record)",
		"        cache_path.write_text(json.dumps(record), encoding='utf-8')",
		"        assert helper._read_config_import_cache(identity, parsed, declaring, context_digest) is None",
		"        original_open = helper._open_remote_config_import",
		"        def not_modified(*_args, **_kwargs):",
		"            raise urllib.error.HTTPError(identity, 304, 'Not Modified', {}, None)",
		"        helper._open_remote_config_import = not_modified",
		"        try:",
		"            try:",
		"                helper._read_remote_config_import(identity, parsed, declaring, context_digest)",
		"            except ValueError as exc:",
		"                assert 'HTTP 304' in str(exc)",
		"            else:",
		"                raise AssertionError('invalid cache timestamp was reused after 304')",
		"        finally:",
		"            helper._open_remote_config_import = original_open",
		"        record['fetched_at'] = (datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(seconds=1)).strftime('%Y-%m-%dT%H:%M:%S.') + '1234567890Z'",
		"        assert helper._config_import_cache_is_current(record)",
		"        cache_path.write_text(json.dumps(record), encoding='utf-8')",
		"        assert helper._read_config_import_cache(identity, parsed, declaring, context_digest) is not None",
		"        journal_path = cache_dir / helper.CONFIG_IMPORT_JOURNAL_NAME",
		"        journal_path.write_text('{}', encoding='utf-8')",
		"        journal_path.chmod(0o600)",
		"        try:",
		"            with helper._config_import_cache_lock(instance):",
		"                pass",
		"        except ValueError as exc:",
		"            assert 'runtime publication recovery' in str(exc)",
		"        else:",
		"            raise AssertionError('eval cache reader accepted a pending runtime journal')",
		"    finally:",
		"        for name, value in original_env.items():",
		"            if value is None:",
		"                os.environ.pop(name, None)",
		"            else:",
		"                os.environ[name] = value",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestProviderConfigLoaderEnforcesOverallRemoteImportTimeout(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		for range 20 {
			if _, err := w.Write([]byte("x")); err != nil {
				return
			}
			flusher.Flush()
			time.Sleep(50 * time.Millisecond)
		}
	}))
	defer server.Close()
	bypassProxyForLoopbackServer(t, server.URL)
	program := strings.Join([]string{
		"import os",
		"import sys",
		"import tempfile",
		"import time",
		"import urllib.parse",
		"from pathlib import Path",
		"from tests.eval.juex_eval import helper",
		"with tempfile.TemporaryDirectory() as tmp:",
		"    root = Path(tmp)",
		"    original_env = {name: os.environ.get(name) for name in ['HOME', 'USERPROFILE', 'JUEX_HOME']}",
		"    original_timeout = helper.CONFIG_IMPORT_TIMEOUT_SECONDS",
		"    os.environ.update({'HOME': str(root / 'home'), 'USERPROFILE': str(root / 'home'), 'JUEX_HOME': str(root / 'instance')})",
		"    helper.CONFIG_IMPORT_TIMEOUT_SECONDS = 0.2",
		"    started = time.monotonic()",
		"    try:",
		"        identity = sys.argv[1] + '/slow.yaml'",
		"        try:",
		"            helper._read_remote_config_import(identity, urllib.parse.urlsplit(identity), root / 'juex.yaml', helper._config_import_standalone_digest())",
		"        except ValueError:",
		"            pass",
		"        else:",
		"            raise AssertionError('slow response exceeded the total deadline without failing')",
		"        assert time.monotonic() - started < 0.75",
		"    finally:",
		"        helper.CONFIG_IMPORT_TIMEOUT_SECONDS = original_timeout",
		"        for name, value in original_env.items():",
		"            if value is None:",
		"                os.environ.pop(name, None)",
		"            else:",
		"                os.environ[name] = value",
	}, "\n")
	runUV(t, root, "python", "-c", program, server.URL)
}

func TestProviderConfigLoaderDoesNotForwardValidatorsAcrossRedirects(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/original.yaml":
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
	bypassProxyForLoopbackServer(t, server.URL)
	program := strings.Join([]string{
		"import sys",
		"import time",
		"from tests.eval.juex_eval import helper",
		"headers = {'If-None-Match': '\"config-v1\"', 'If-Modified-Since': 'Fri, 22 Aug 2025 00:00:00 GMT'}",
		"with helper._open_remote_config_import(sys.argv[1] + '/original.yaml', headers, time.monotonic() + 5) as response:",
		"    assert response.status == 200, response.status",
		"    assert response.read().decode('utf-8') == 'runtime:\\n  tool_timeout: 42s\\n'",
	}, "\n")
	runUV(t, root, "python", "-c", program, server.URL)
}

func TestProviderConfigLoaderCanonicalizesMissingImportPathParents(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	program := strings.Join([]string{
		"import sys",
		"import tempfile",
		"from pathlib import Path",
		"from tests.eval.juex_eval import helper",
		"with tempfile.TemporaryDirectory() as tmp:",
		"    root = Path(tmp)",
		"    real = root / 'real'",
		"    alias = root / 'alias'",
		"    real.mkdir()",
		"    alias.symlink_to(real, target_is_directory=True)",
		"    candidate = alias / 'workspace' / '.juex' / 'juex.yaml'",
		"    before = helper._config_import_path_identity(candidate)",
		"    candidate.parent.mkdir(parents=True)",
		"    after = helper._config_import_path_identity(candidate)",
		"    assert before == after == real.resolve() / 'workspace' / '.juex' / 'juex.yaml', (before, after)",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestProviderConfigLoaderDoesNotMemoizeStaleImportsAcrossDeclarers(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"import os",
		"import tempfile",
		"from pathlib import Path",
		"from tests.eval.juex_eval import helper",
		"with tempfile.TemporaryDirectory() as tmp:",
		"    root = Path(tmp)",
		"    home = root / 'home'",
		"    instance = root / 'instance'",
		"    configs = [home / '.juex' / 'juex.yaml', instance / 'juex.yaml', root / 'explicit.yaml']",
		"    for config in configs:",
		"        config.parent.mkdir(parents=True, exist_ok=True)",
		"        config.write_text('imports:\\n  - source: https://config.example/shared.yaml\\n', encoding='utf-8')",
		"    original_env = {name: os.environ.get(name) for name in ['HOME', 'USERPROFILE', 'JUEX_HOME']}",
		"    original_remote = helper._read_remote_config_import",
		"    remote_reads = []",
		"    def fake_remote(identity, _parsed, declaring, _cache_context_digest):",
		"        remote_reads.append(str(declaring))",
		"        return f'providers:\\n  - id: provider-{len(remote_reads)}\\n    models: [{{id: model}}]\\n', False",
		"    os.environ.update({'HOME': str(home), 'USERPROFILE': str(home), 'JUEX_HOME': str(instance)})",
		"    helper._read_remote_config_import = fake_remote",
		"    try:",
		"        cfg = helper.load_source_config(configs[-1])",
		"        assert remote_reads == [str(path.resolve()) for path in configs], remote_reads",
		"        assert [provider['id'] for provider in cfg['providers']] == ['provider-1', 'provider-2', 'provider-3']",
		"    finally:",
		"        helper._read_remote_config_import = original_remote",
		"        for name, value in original_env.items():",
		"            if value is None:",
		"                os.environ.pop(name, None)",
		"            else:",
		"                os.environ[name] = value",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestProviderConfigSelectionMergesRepeatedProviderDeclarations(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"import tempfile",
		"from pathlib import Path",
		"from tests.eval.juex_eval import helper, selection",
		"cfg = {'providers': [",
		"    {'id': 'provider', 'protocol': 'openai/chat', 'protcol': 'misspelled', 'api_key': 'first-secret', 'capabilities': {'tools': False}, 'compat': {'codex_transprot': 'misspelled'}, 'models': [",
		"        {'id': 'first-only'}, {'id': 'shared', 'context_window': 16000, 'context_widow': 999, 'thinking_effort': 'low', 'compat': {'reasoning_replay_felds': ['misspelled']}},",
		"    ]},",
		"    {'id': 'provider', 'protocol': 'openai/responses', 'base_url': '   ', 'api_key': '  ', 'capabilities': {'tools': True, 'reasoning_effort': True}, 'compat': {'codex_transport': ' websocket '}, 'models': [",
		"        {'id': 'shared', 'context_window': 64000, 'thinking_effort': ' high ', 'capabilities': {'tools': True}}, {'id': 'second-only'},",
		"    ]},",
		"]}",
		"candidates = selection.enumerate_candidates(cfg)",
		"assert [item.ref for item in candidates] == ['provider:first-only', 'provider:second-only', 'provider:shared']",
		"shared = next(item for item in candidates if item.ref == 'provider:shared')",
		"assert shared.protocol == 'openai/responses' and shared.tools_capability == 'true'",
		"assert shared.reasoning_effort_capability == 'true' and shared.context_window == 64000",
		"assert shared.thinking_effort == '\"high\"'",
		"provider, model = helper.selected_provider_model(cfg, 'provider', 'shared')",
		"assert provider['protocol'] == 'openai/responses' and provider['base_url'] == '   ' and provider['api_key'] == '  '",
		"assert provider['compat']['codex_transport'] == 'websocket'",
		"assert model['context_window'] == 64000 and model['thinking_effort'] == 'high'",
		"assert provider['protcol'] == 'misspelled' and model['context_widow'] == 999",
		"with tempfile.TemporaryDirectory() as tmp:",
		"    output = Path(tmp) / 'selected.yaml'",
		"    helper.write_selected_config(cfg, 'provider', 'shared', output)",
		"    rendered = output.read_text(encoding='utf-8')",
		"    for invalid_field in ['protcol: misspelled', 'context_widow: 999', 'codex_transprot: misspelled', 'reasoning_replay_felds:']:",
		"        assert invalid_field in rendered, rendered",
		"    disabled = Path(tmp) / 'selected-disabled.yaml'",
		"    helper.write_selected_config(cfg, 'provider', 'shared', disabled, disable_tools=True)",
		"    disabled_cfg = helper.load_yaml_file(disabled)",
		"    disabled_provider = disabled_cfg['providers'][0]",
		"    assert disabled_provider['capabilities']['tools'] is False, disabled_provider",
		"    assert disabled_provider['models'][0]['capabilities']['tools'] is False, disabled_provider['models'][0]",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestProviderConfigSelectionEligibilityAndExactScope(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"from pathlib import Path",
		"from tests.eval.juex_eval import selection",
		"cfg = {'providers': [",
		"    {'id': 'provider-off', 'capabilities': {'tools': False}, 'models': [{'id': 'blocked'}, {'id': 'override', 'capabilities': {'tools': True}}]},",
		"    {'id': 'provider-on', 'capabilities': {'tools': True}, 'models': [",
		"        {'id': 'small', 'context_window': 16000},",
		"        {'id': 'large', 'context_window': 64000},",
		"        {'id': 'default-window'},",
		"        {'id': 'model-off', 'capabilities': {'tools': False}},",
		"    ]},",
		"]}",
		"provider_refs = [item.ref for item in selection.eligible_candidates(cfg, 'provider-smoke')]",
		"assert provider_refs == ['provider-off:override', 'provider-on:default-window', 'provider-on:large', 'provider-on:small'], provider_refs",
		"compaction_refs = [item.ref for item in selection.eligible_candidates(cfg, 'compaction', required_context_window=32000)]",
		"assert compaction_refs == ['provider-off:blocked', 'provider-off:override', 'provider-on:default-window', 'provider-on:large', 'provider-on:model-off'], compaction_refs",
		"selected, evidence = selection.select(cfg, kind='provider-smoke', config_path=Path('/tmp/config.yaml'), seed='unit', only=['provider-off:override'], command_prefix=['juex-eval', 'provider-smoke'])",
		"assert [item.ref for item in selected] == ['provider-off:override']",
		"assert '--only provider-off:override' in evidence.reproduction_command",
		"for ref in ['provider-off:blocked', 'provider-on:model-off', 'missing:model']:",
		"    try:",
		"        selection.select(cfg, kind='provider-smoke', config_path=Path('/tmp/config.yaml'), seed='unit', only=[ref], command_prefix=['juex-eval', 'provider-smoke'])",
		"    except selection.ProviderUnavailable as exc:",
		"        assert exc.failure_category == 'provider_unavailable'",
		"        assert exc.evidence.selected_refs == ()",
		"    else:",
		"        raise AssertionError(f'ineligible ref accepted: {ref}')",
		"all_selected, _ = selection.select(cfg, kind='provider-smoke', config_path=Path('/tmp/config.yaml'), seed='unit', all_models=True, command_prefix=['juex-eval', 'provider-smoke'])",
		"assert [item.ref for item in all_selected] == provider_refs",
		"malformed_known_fields = [",
		"    ({'providers': [{'id': [], 'models': []}]}, '.id must be a YAML string'),",
		"    ({'providers': [{'id': 'provider', 'protocol': [], 'models': []}]}, '.protocol must be a YAML string'),",
		"    ({'providers': [{'id': 'provider', 'capabilities': {'tools': 'yes'}, 'models': []}]}, '.capabilities.tools must be a YAML boolean'),",
		"    ({'providers': [{'id': 'provider', 'compat': {'codex_transport': []}, 'models': []}]}, '.compat.codex_transport must be a YAML string'),",
		"    ({'providers': [{'id': 'provider', 'compat': {'reasoning_replay_fields': 'reasoning'}, 'models': []}]}, '.compat.reasoning_replay_fields must be a YAML string sequence'),",
		"    ({'providers': [{'id': 'provider', 'models': [{'id': [], 'context_window': 32000}]}]}, '.id must be a YAML string'),",
		"    ({'providers': [{'id': 'provider', 'models': [{'id': 'model', 'thinking_effort': {}}]}]}, '.thinking_effort must be a YAML string'),",
		"    ({'providers': [{'id': 'provider', 'models': [{'id': 'model', 'context_window': [32000]}]}]}, '.context_window must be a YAML integer'),",
		"]",
		"for malformed_cfg, message in malformed_known_fields:",
		"    try:",
		"        selection.enumerate_candidates(malformed_cfg)",
		"    except ValueError as exc:",
		"        assert message in str(exc), (message, str(exc))",
		"    else:",
		"        raise AssertionError(f'malformed provider field accepted: {message}')",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestProviderSmokeDynamicScopesReportsAndPreservesSelectedFailure(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"import contextlib",
		"import io",
		"import json",
		"import os",
		"import shutil",
		"import tempfile",
		"from pathlib import Path",
		"from tests.eval.juex_eval import helper",
		"with tempfile.TemporaryDirectory() as tmp:",
		"    work = Path(tmp)",
		"    original_home = {name: os.environ.get(name) for name in ['HOME', 'USERPROFILE', 'JUEX_HOME']}",
		"    original_provider_env = {name: os.environ.get(name) for name in ['PROVIDER_API_BASE', 'PROVIDER_API_MODEL', 'PROVIDER_THINKING_EFFORT', 'JUEX_PROVIDER_SMOKE_ONLY']}",
		"    os.environ.update({'HOME': str(work / 'home'), 'USERPROFILE': str(work / 'home'), 'JUEX_HOME': str(work / 'instance')})",
		"    os.environ.update({'PROVIDER_API_BASE': 'https://inherited-a.invalid', 'PROVIDER_API_MODEL': 'inherited-model', 'PROVIDER_THINKING_EFFORT': 'low'})",
		"    os.environ.pop('JUEX_PROVIDER_SMOKE_ONLY', None)",
		"    config = work / 'provider config.yaml'",
		"    config.write_text('''environment:",
		"  variables:",
		"    PRIVATE_TOKEN: never-report-env-token",
		"providers:",
		"  - id: provider-off",
		"    api_key: never-report-disabled-key",
		"    capabilities: {tools: false}",
		"    models: [{id: blocked}]",
		"  - id: provider-on",
		"    api_key: never-report-live-key",
		"    headers: {Authorization: never-report-header}",
		"    models: [{id: alpha}, {id: beta}]",
		"''', encoding='utf-8')",
		"    remote_entry = work / 'remote-entry.yaml'",
		"    remote_entry.write_text('imports:\\n  - source: https://config.example/providers.yaml\\n', encoding='utf-8')",
		"    true_bin = shutil.which('true')",
		"    assert true_bin",
		"    captured = []",
		"    validated = []",
		"    remote_reads = []",
		"    fail_selected = False",
		"    def fake_case(ctx):",
		"        captured.append(ctx.row.ref)",
		"        return helper.SmokeResult(",
		"            run_id=ctx.run_id, ref=ctx.row.ref, provider_id=ctx.row.provider_id, model_id=ctx.row.model_id,",
		"            protocol=ctx.row.protocol, reasoning_effort_capability=ctx.row.reasoning_effort_capability,",
		"            tools_capability=ctx.row.tools_capability, thinking_effort=ctx.row.thinking_effort,",
		"            status='fail' if fail_selected else 'pass', error_stage='turn1' if fail_selected else '',",
		"            error='selected provider failed' if fail_selected else '', schedule_routing_status='not_run' if fail_selected else 'passed',",
		"        )",
		"    original_case = helper.run_provider_smoke_case",
		"    original_validate = helper.validate_source_config",
		"    original_validate_layers = helper.validate_source_layers",
		"    original_remote = helper._read_remote_config_import",
		"    helper.run_provider_smoke_case = fake_case",
		"    def fake_validate(_juex, source):",
		"        materialized = Path(source).read_text(encoding='utf-8')",
		"        validated.append(materialized)",
		"        if 'protcol:' in materialized:",
		"            raise ValueError('provider config is not loadable by Juex')",
		"        if 'runtime:\\n  typo:' in materialized:",
		"            raise ValueError('provider config is not loadable by Juex')",
		"    helper.validate_source_config = fake_validate",
		"    def fake_validate_layers(_juex, layers):",
		"        merged = {}",
		"        for layer in layers:",
		"            for source in [*layer.imports, layer.declaring]:",
		"                merged = helper._merge_source_config(merged, source)",
		"        source = work / 'validated-source-layers.yaml'",
		"        source.write_text(helper.dump_yaml(merged), encoding='utf-8')",
		"        fake_validate(_juex, source)",
		"    helper.validate_source_layers = fake_validate_layers",
		"    def fake_remote(identity, _parsed, _declaring, _cache_context_digest):",
		"        remote_reads.append(identity)",
		"        return 'models: [remote:model]\\nproviders:\\n  - id: remote\\n    api_key: remote-secret\\n    models: [{id: model}]\\n', True",
		"    helper._read_remote_config_import = fake_remote",
		"    def run(name, *scope, seed='stable', source=config):",
		"        captured.clear()",
		"        validated.clear()",
		"        remote_reads.clear()",
		"        report = work / f'report-{name}'",
		"        status = helper.provider_smoke([",
		"            '--juex', true_bin, '--config', str(source), '--selection-seed', seed,",
		"            '--report-dir', str(report), '--work-root', str(work / f'work-{name}'), '--run-id', name, *scope,",
		"        ])",
		"        summary = json.loads((report / 'summary.json').read_text(encoding='utf-8'))",
		"        markdown = (report / 'summary.md').read_text(encoding='utf-8')",
		"        return status, list(captured), summary, markdown",
		"    try:",
		"        status, refs, summary, markdown = run('default')",
		"        assert status == 0 and len(refs) == 1 and refs[0] in {'provider-on:alpha', 'provider-on:beta'}, (status, refs)",
		"        assert summary['selection_source'] == 'provider_config' and summary['selection_seed'] == 'stable', summary",
		"        assert summary['eligible_candidate_refs'] == ['provider-on:alpha', 'provider-on:beta'], summary",
		"        assert summary['resolved_config_path'] == str(config.resolve()), summary",
		"        assert '--selection-seed stable' in summary['reproduction_command'], summary",
		"        status, refs, summary, _ = run('remote-import', '--only', 'remote:model', source=remote_entry)",
		"        assert status == 0 and refs == ['remote:model'], (status, refs)",
		"        assert remote_reads == ['https://config.example/providers.yaml'], remote_reads",
		"        assert len(validated) == 2 and all('imports:' not in item and 'remote-secret' in item for item in validated), validated",
		"        status, refs, summary, _ = run('all', '--all-models')",
		"        assert status == 0 and refs == ['provider-on:alpha', 'provider-on:beta'], refs",
		"        status, refs, summary, _ = run('blocked', '--only', 'provider-off:blocked')",
		"        assert status == 1 and refs == [] and summary['failure_category'] == 'provider_unavailable', summary",
		"        assert summary['outcome'] == 'provider_unavailable' and summary['blocks_merge'] is True and summary['recommended_action'] == 'stop', summary",
		"        assert summary['selected_provider_models'] == [] and summary['eligible_candidate_count'] == 2, summary",
		"        status, refs, summary, _ = run('missing-config', source=work / 'missing.yaml')",
		"        assert status == 1 and refs == [] and summary['failure_category'] == 'environment_failure', summary",
		"        assert summary['outcome'] == 'environment_failure' and summary['matched_rule'] == 'environment-invalid-config' and summary['recommended_action'] == 'fix_environment', summary",
		"        empty_config = work / 'empty.yaml'",
		"        empty_config.write_text('providers: []\\n', encoding='utf-8')",
		"        status, refs, summary, _ = run('zero-candidates', source=empty_config)",
		"        assert status == 1 and refs == [] and summary['eligible_candidate_count'] == 0, summary",
		"        assert summary['outcome'] == 'provider_unavailable' and summary['matched_rule'] == 'provider-selection-unavailable', summary",
		"        invalid_container = work / 'invalid-container.yaml'",
		"        invalid_container.write_text('providers:\\n  provider-on:\\n    id: provider-on\\n    models: [{id: alpha}]\\n', encoding='utf-8')",
		"        status, refs, summary, _ = run('invalid-container', source=invalid_container)",
		"        assert status == 1 and refs == [] and summary['failure_category'] == 'environment_failure', summary",
		"        assert \"'providers' must be a YAML sequence\" in summary['error'], summary",
		"        invalid_full_schema = work / 'invalid-full-schema.yaml'",
		"        invalid_full_schema.write_text('providers:\\n  - id: provider-on\\n    protcol: openai/chat\\n    models: [{id: alpha}]\\n', encoding='utf-8')",
		"        status, refs, summary, _ = run('invalid-full-schema', '--only', 'provider-on:alpha', source=invalid_full_schema)",
		"        assert status == 1 and refs == [] and summary['error'] == 'provider config is not loadable by Juex', summary",
		"        invalid_unprojected_schema = work / 'invalid-unprojected-schema.yaml'",
		"        invalid_unprojected_schema.write_text('runtime:\\n  typo: true\\nproviders:\\n  - id: provider-on\\n    models: [{id: alpha}]\\n', encoding='utf-8')",
		"        status, refs, summary, _ = run('invalid-unprojected-schema', '--only', 'provider-on:alpha', source=invalid_unprojected_schema)",
		"        assert status == 1 and refs == [] and summary['error'] == 'provider config is not loadable by Juex', summary",
		"        duplicate_schema = work / 'duplicate-schema.yaml'",
		"        duplicate_schema.write_text('runtime:\\n  tool_timeout: 10s\\n  tool_timeout: 20s\\nproviders:\\n  - id: provider-on\\n    models: [{id: alpha}]\\n', encoding='utf-8')",
		"        status, refs, summary, _ = run('duplicate-schema', '--only', 'provider-on:alpha', source=duplicate_schema)",
		"        assert status == 1 and refs == [] and \"duplicate YAML key 'tool_timeout'\" in summary['error'], summary",
		"        falsey_import = work / 'falsey-import.yaml'",
		"        falsey_import.write_text('false\\n', encoding='utf-8')",
		"        falsey_entry = work / 'falsey-entry.yaml'",
		"        falsey_entry.write_text('imports:\\n  - source: ./falsey-import.yaml\\nproviders:\\n  - id: provider-on\\n    models: [{id: alpha}]\\n', encoding='utf-8')",
		"        status, refs, summary, _ = run('falsey-import', '--only', 'provider-on:alpha', source=falsey_entry)",
		"        assert status == 1 and refs == [] and 'must contain a YAML mapping' in summary['error'], summary",
		"        invalid_context = work / 'invalid-context.yaml'",
		"        invalid_context.write_text('providers:\\n  - id: provider-on\\n    models: [{id: alpha, context_window: [32000]}]\\n', encoding='utf-8')",
		"        status, refs, summary, _ = run('invalid-context', source=invalid_context)",
		"        assert status == 1 and refs == [] and summary['failure_category'] == 'environment_failure', summary",
		"        assert 'context_window must be a YAML integer' in summary['error'], summary",
		"        malformed = work / 'malformed.yaml'",
		"        malformed.write_text('providers:\\n  - id: bad\\n    api_key: never-report-malformed-key: [\\n', encoding='utf-8')",
		"        terminal = io.StringIO()",
		"        with contextlib.redirect_stdout(terminal), contextlib.redirect_stderr(terminal):",
		"            status, refs, summary, _ = run('malformed', source=malformed)",
		"        assert status == 1 and refs == [] and summary['error'] == 'provider config YAML is invalid', summary",
		"        assert summary['outcome'] == 'environment_failure' and summary['matched_rule'] == 'environment-invalid-config', summary",
		"        assert 'never-report-malformed-key' not in terminal.getvalue(), terminal.getvalue()",
		"        fail_selected = True",
		"        status, refs, summary, _ = run('selected-failure', '--only', 'provider-on:beta')",
		"        assert status == 1 and refs == ['provider-on:beta'], refs",
		"        assert summary['selected_provider_model'] == 'provider-on:beta' and summary['failed'] == 1, summary",
		"        assert summary['outcome'] == 'product_failure' and summary['recommended_action'] == 'fix_code', summary",
		"        report_text = ''.join(path.read_text(encoding='utf-8', errors='replace') for path in work.glob('report-*/*') if path.is_file())",
		"        for secret in ['never-report-env-token', 'never-report-disabled-key', 'never-report-live-key', 'never-report-header', 'never-report-malformed-key']:",
		"            assert secret not in report_text, secret",
		"        assert 'Selection source: `provider_config`' in markdown, markdown",
		"    finally:",
		"        helper.run_provider_smoke_case = original_case",
		"        helper.validate_source_config = original_validate",
		"        helper.validate_source_layers = original_validate_layers",
		"        helper._read_remote_config_import = original_remote",
		"        for name, value in original_home.items():",
		"            if value is None:",
		"                os.environ.pop(name, None)",
		"            else:",
		"                os.environ[name] = value",
		"        for name, value in original_provider_env.items():",
		"            if value is None:",
		"                os.environ.pop(name, None)",
		"            else:",
		"                os.environ[name] = value",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestCompactionDynamicSelectionWritesSummaryAndFiltersWindow(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"import contextlib",
		"import io",
		"import json",
		"import os",
		"import shutil",
		"import tempfile",
		"from argparse import Namespace",
		"from pathlib import Path",
		"from tests.eval.juex_eval import compaction",
		"with tempfile.TemporaryDirectory() as tmp:",
		"    work = Path(tmp)",
		"    original_home = {name: os.environ.get(name) for name in ['HOME', 'USERPROFILE', 'JUEX_HOME']}",
		"    original_provider_env = {name: os.environ.get(name) for name in ['PROVIDER_API_BASE', 'PROVIDER_API_MODEL', 'PROVIDER_THINKING_EFFORT']}",
		"    os.environ.update({'HOME': str(work / 'home'), 'USERPROFILE': str(work / 'home'), 'JUEX_HOME': str(work / 'instance')})",
		"    os.environ.update({'PROVIDER_API_BASE': 'https://inherited-a.invalid', 'PROVIDER_API_MODEL': 'inherited-model', 'PROVIDER_THINKING_EFFORT': 'low'})",
		"    config = work / 'juex.yaml'",
		"    config.write_text('''providers:",
		"  - id: provider",
		"    api_key: never-report-compaction-key",
		"    models:",
		"      - {id: small, context_window: 16000}",
		"      - {id: large, context_window: 64000}",
		"      - {id: default-window}",
		"''', encoding='utf-8')",
		"    true_bin = shutil.which('true')",
		"    assert true_bin",
		"    captured = []",
		"    validated = []",
		"    original_run_model = compaction.run_model",
		"    original_validate = compaction.helper.validate_source_config",
		"    original_validate_layers = compaction.helper.validate_source_layers",
		"    compaction.run_model = lambda args, cfg, model, out_root, temp_dirs: captured.append(model) or 0",
		"    def fake_validate(_juex, source):",
		"        materialized = Path(source).read_text(encoding='utf-8')",
		"        validated.append(materialized)",
		"        if 'runtime:\\n  typo:' in materialized:",
		"            raise ValueError('provider config is not loadable by Juex')",
		"    compaction.helper.validate_source_config = fake_validate",
		"    def fake_validate_layers(_juex, layers):",
		"        merged = {}",
		"        for layer in layers:",
		"            for source in [*layer.imports, layer.declaring]:",
		"                merged = compaction.helper._merge_source_config(merged, source)",
		"        source = work / 'validated-compaction-source-layers.yaml'",
		"        source.write_text(compaction.helper.dump_yaml(merged), encoding='utf-8')",
		"        fake_validate(_juex, source)",
		"    compaction.helper.validate_source_layers = fake_validate_layers",
		"    def run(name, only=None, all_models=False, source=config):",
		"        captured.clear()",
		"        validated.clear()",
		"        out = work / name",
		"        args = Namespace(only=only or [], all_models=all_models, selection_seed='seed-42', juex=true_bin,",
		"            config=str(source), out_root=str(out), run_id=name, context_window=32000, turn_timeout=10, keep_workdir=False)",
		"        status = compaction.run(args)",
		"        summary = json.loads((out / 'summary.json').read_text(encoding='utf-8'))",
		"        markdown = (out / 'summary.md').read_text(encoding='utf-8')",
		"        return status, list(captured), summary, markdown",
		"    try:",
		"        status, refs, summary, markdown = run('all', all_models=True)",
		"        assert status == 0 and refs == ['provider:default-window', 'provider:large'], refs",
		"        assert len(validated) == 1 and 'never-report-compaction-key' in validated[0], validated",
		"        assert summary['eligible_candidate_refs'] == refs and summary['selection_source'] == 'provider_config', summary",
		"        assert summary['redacted_config_hash'].startswith('sha256:'), summary",
		"        assert '--all-models' in summary['reproduction_command'] and '--config' in summary['reproduction_command'], summary",
		"        isolated_hash = summary['redacted_config_hash']",
		"        os.environ.update({'PROVIDER_API_BASE': 'https://inherited-b.invalid', 'PROVIDER_API_MODEL': 'other-model', 'PROVIDER_THINKING_EFFORT': 'high'})",
		"        status, refs, summary, _ = run('all-other-inherited', all_models=True)",
		"        assert status == 0 and refs == ['provider:default-window', 'provider:large'], refs",
		"        assert summary['redacted_config_hash'] == isolated_hash, summary",
		"        status, refs, summary, _ = run('small', only=['provider:small'])",
		"        assert status == 1 and refs == [] and summary['failure_category'] == 'provider_unavailable', summary",
		"        assert summary['selected_provider_models'] == [], summary",
		"        malformed = work / 'malformed.yaml'",
		"        malformed.write_text('providers:\\n  - id: bad\\n    headers: {Authorization: never-report-malformed-header: [}\\n', encoding='utf-8')",
		"        terminal = io.StringIO()",
		"        with contextlib.redirect_stdout(terminal), contextlib.redirect_stderr(terminal):",
		"            status, refs, summary, _ = run('malformed', source=malformed)",
		"        assert status == 1 and refs == [] and summary['error'] == 'provider config YAML is invalid', summary",
		"        assert summary['outcome'] == 'environment_failure' and summary['matched_rule'] == 'environment-invalid-config', summary",
		"        assert 'never-report-malformed-header' not in terminal.getvalue(), terminal.getvalue()",
		"        invalid_unprojected_schema = work / 'invalid-unprojected-schema.yaml'",
		"        invalid_unprojected_schema.write_text('runtime:\\n  typo: true\\nproviders:\\n  - id: provider\\n    models: [{id: large, context_window: 64000}]\\n', encoding='utf-8')",
		"        status, refs, summary, _ = run('invalid-unprojected-schema', only=['provider:large'], source=invalid_unprojected_schema)",
		"        assert status == 1 and refs == [] and summary['error'] == 'provider config is not loadable by Juex', summary",
		"        duplicate_schema = work / 'duplicate-schema.yaml'",
		"        duplicate_schema.write_text('runtime:\\n  tool_timeout: 10s\\n  tool_timeout: 20s\\nproviders:\\n  - id: provider\\n    models: [{id: large, context_window: 64000}]\\n', encoding='utf-8')",
		"        status, refs, summary, _ = run('duplicate-schema', only=['provider:large'], source=duplicate_schema)",
		"        assert status == 1 and refs == [] and \"duplicate YAML key 'tool_timeout'\" in summary['error'], summary",
		"        combined = (work / 'all' / 'summary.json').read_text() + (work / 'all' / 'summary.md').read_text()",
		"        combined += (work / 'malformed' / 'summary.json').read_text() + (work / 'malformed' / 'summary.md').read_text()",
		"        for secret in ['never-report-compaction-key', 'never-report-malformed-header']:",
		"            assert secret not in combined, combined",
		"        assert 'Selection source: `provider_config`' in markdown, markdown",
		"    finally:",
		"        compaction.run_model = original_run_model",
		"        compaction.helper.validate_source_config = original_validate",
		"        compaction.helper.validate_source_layers = original_validate_layers",
		"        for name, value in original_home.items():",
		"            if value is None:",
		"                os.environ.pop(name, None)",
		"            else:",
		"                os.environ[name] = value",
		"        for name, value in original_provider_env.items():",
		"            if value is None:",
		"                os.environ.pop(name, None)",
		"            else:",
		"                os.environ[name] = value",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestCompactionTurnIsolatesInheritedProviderRuntimeOverrides(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"import os",
		"import tempfile",
		"from argparse import Namespace",
		"from pathlib import Path",
		"from tests.eval.juex_eval import compaction, helper",
		"captured = {}",
		"original_run = helper.run_subprocess_with_timeout",
		"original_env = {name: os.environ.get(name) for name in helper.ISOLATED_PROVIDER_ENVIRONMENT_KEYS}",
		"for name in helper.ISOLATED_PROVIDER_ENVIRONMENT_KEYS:",
		"    os.environ[name] = f'inherited-{name.lower()}'",
		"def fake_run(command, timeout, **kwargs):",
		"    captured.update(kwargs['env'])",
		"    kwargs['stdout'].write(b'ok\\n')",
		"    return 0",
		"helper.run_subprocess_with_timeout = fake_run",
		"try:",
		"    with tempfile.TemporaryDirectory() as tmp:",
		"        work = Path(tmp)",
		"        prompt = work / 'prompt.txt'",
		"        output = work / 'output.txt'",
		"        prompt.write_text('test', encoding='utf-8')",
		"        args = Namespace(juex='/path/to/juex', context_window=32000, turn_timeout=10)",
		"        assert compaction.run_eval_turn(args, work, prompt, output) == 0",
		"        for name in helper.ISOLATED_PROVIDER_ENVIRONMENT_KEYS:",
		"            if name == 'PROVIDER_CONTEXT_WINDOW':",
		"                assert captured[name] == '32000', (name, captured[name])",
		"            else:",
		"                assert name not in captured, (name, captured.get(name))",
		"finally:",
		"    helper.run_subprocess_with_timeout = original_run",
		"    for name, value in original_env.items():",
		"        if value is None:",
		"            os.environ.pop(name, None)",
		"        else:",
		"            os.environ[name] = value",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestJuexSourceConfigValidationUsesCompleteConfigDoctor(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"import json",
		"import tempfile",
		"from pathlib import Path",
		"from types import SimpleNamespace",
		"from tests.eval.juex_eval import helper",
		"captured = []",
		"materialized = []",
		"original_run = helper.subprocess.run",
		"def fake_run(command, **kwargs):",
		"    captured.append((command, kwargs))",
		"    workspace = Path(command[2])",
		"    if workspace.name == 'work':",
		"        root = workspace.parent",
		"        materialized.append({path.relative_to(root).as_posix(): path.read_text(encoding='utf-8') for path in root.rglob('*.yaml')})",
		"    return SimpleNamespace(stdout=json.dumps({'checks': [{'name': 'config', 'status': 'ok'}]}), stderr='', returncode=6)",
		"helper.subprocess.run = fake_run",
		"try:",
		"    with tempfile.TemporaryDirectory() as tmp:",
		"        root = Path(tmp)",
		"        config = root / 'juex.yaml'",
		"        config.write_text('providers: []\\n', encoding='utf-8')",
		"        original_environment = {name: helper.os.environ.get(name) for name in [*helper.ISOLATED_PROVIDER_ENVIRONMENT_KEYS, 'CODEX_HOME', 'HOME', 'USERPROFILE', 'JUEX_HOME']}",
		"        helper.os.environ.update({name: f'inherited-{name.lower()}' for name in helper.ISOLATED_PROVIDER_ENVIRONMENT_KEYS})",
		"        helper.os.environ.pop('CODEX_HOME', None)",
		"        helper.os.environ['JUEX_HOME'] = str(root / 'effective-home')",
		"        helper.validate_source_config('/path/to/juex', config)",
		"        command, kwargs = captured[-1]",
		"        assert command[-4:] == ['doctor', '--offline', '--format', 'json'], command",
		"        assert command[3:5] == ['--config', str(config.resolve())], command",
		"        assert all(name not in kwargs['env'] for name in helper.ISOLATED_PROVIDER_ENVIRONMENT_KEYS), kwargs['env']",
		"        assert kwargs['env']['HOME'] == str(Path(command[2]) / 'home'), kwargs['env']['HOME']",
		"        assert kwargs['env']['USERPROFILE'] == str(Path(command[2]) / 'home'), kwargs['env']['USERPROFILE']",
		"        assert kwargs['env']['JUEX_HOME'] == str(Path(command[2]) / 'juex-home'), kwargs['env']['JUEX_HOME']",
		"        assert kwargs['env']['CODEX_HOME'] == str(Path.home() / '.codex'), kwargs['env']['CODEX_HOME']",
		"        home_config = root / 'effective-home' / 'juex.yaml'",
		"        home_config.parent.mkdir()",
		"        home_config.write_text('providers: []\\n', encoding='utf-8')",
		"        helper.validate_source_config('/path/to/juex', home_config)",
		"        command, kwargs = captured[-1]",
		"        assert '--config' not in command and kwargs['env']['JUEX_HOME'] == str(home_config.resolve().parent)",
		"        layers = [",
		"            helper.SourceConfigLayer('default-home', ({'fleet': {'addr': '127.0.0.1:5839'}},), {'providers': []}),",
		"            helper.SourceConfigLayer('effective-home', (), {'runtime': {'tool_timeout': '40s'}}),",
		"            helper.SourceConfigLayer('explicit', (), {'runtime': {'pending_input_ttl': '30s'}}),",
		"        ]",
		"        helper.validate_source_layers('/path/to/juex', layers)",
		"        command, kwargs = captured[-1]",
		"        assert command[-4:] == ['doctor', '--offline', '--format', 'json'], command",
		"        assert command[3] == '--config' and Path(command[4]).parts[-2:] == ('explicit', 'juex.yaml'), command",
		"        source_root = Path(command[2]).parent",
		"        assert kwargs['env']['HOME'] == str(source_root / 'home'), kwargs['env']",
		"        assert kwargs['env']['JUEX_HOME'] == str(source_root / 'juex-home'), kwargs['env']",
		"        files = materialized[-1]",
		"        assert set(files) == {'home/.juex/juex.yaml', 'home/.juex/imports/import-0.yaml', 'juex-home/juex.yaml', 'explicit/juex.yaml'}, files",
		"        assert 'imports:' in files['home/.juex/juex.yaml'] and 'fleet:' not in files['home/.juex/juex.yaml'], files",
		"        assert 'fleet:' in files['home/.juex/imports/import-0.yaml'], files",
		"        assert 'fleet:' not in files['explicit/juex.yaml'] and 'pending_input_ttl' in files['explicit/juex.yaml'], files",
		"        helper.subprocess.run = lambda *args, **kwargs: SimpleNamespace(stdout=json.dumps({'checks': [{'name': 'config', 'status': 'fail'}]}), stderr='never-report-secret', returncode=7)",
		"        try:",
		"            helper.validate_source_config('/path/to/juex', config)",
		"        except ValueError as exc:",
		"            assert str(exc) == 'provider config is not loadable by Juex'",
		"        else:",
		"            raise AssertionError('failed Juex config check was accepted')",
		"finally:",
		"    helper.subprocess.run = original_run",
		"    for name, value in original_environment.items():",
		"        if value is None:",
		"            helper.os.environ.pop(name, None)",
		"        else:",
		"            helper.os.environ[name] = value",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestEvalValidationPlanRulesAreDeterministicAndConservative(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"from tests.eval.juex_eval import validation_plan",
		"ChangedFile = validation_plan.ChangedFile",
		"cases = [",
		"    ('frontend/src/App.tsx', set(), {'web'}, {'integration', 'provider-smoke'}, 'frontend'),",
		"    ('internal/web/dist/index.html', {'./internal/web', './tests/e2e'}, {'web', 'race'}, {'integration', 'provider-smoke'}, 'embedded-web'),",
		"    ('internal/app/app.go', {'./internal/app', './tests/e2e'}, {'race'}, {'integration', 'provider-smoke'}, 'cross-boundary'),",
		"    ('internal/agentstate/store.go', {'./internal/agentstate', './tests/e2e'}, {'race'}, {'integration', 'provider-smoke'}, 'cross-boundary'),",
		"    ('internal/endpoint/endpoint.go', {'./internal/endpoint', './tests/e2e'}, {'race'}, {'integration', 'provider-smoke'}, 'cross-boundary'),",
		"    ('internal/eventcatalog/catalog.go', {'./internal/eventcatalog', './tests/e2e'}, set(), {'integration', 'provider-smoke'}, 'cross-boundary'),",
		"    ('internal/session/session.go', {'./internal/session', './tests/e2e'}, {'race'}, {'integration', 'provider-smoke'}, 'cross-boundary'),",
		"    ('internal/fleet/fleet.go', {'./internal/fleet', './tests/e2e'}, {'race'}, {'integration', 'provider-smoke'}, 'cross-boundary'),",
		"    ('internal/fleetweb/server.go', {'./internal/fleetweb', './tests/e2e'}, {'race'}, {'integration', 'provider-smoke'}, 'cross-boundary'),",
		"    ('internal/hooks/hooks.go', {'./internal/hooks', './tests/e2e'}, set(), {'integration', 'provider-smoke'}, 'cross-boundary'),",
		"    ('internal/mcp/client.go', {'./internal/mcp', './tests/e2e'}, {'race'}, {'integration', 'provider-smoke'}, 'cross-boundary'),",
		"    ('internal/observable/observable.go', {'./internal/observable', './tests/e2e'}, {'race'}, {'integration', 'provider-smoke'}, 'cross-boundary'),",
		"    ('internal/sandbox/sandbox.go', {'./internal/sandbox', './tests/e2e'}, set(), {'integration', 'provider-smoke'}, 'cross-boundary'),",
		"    ('internal/tools/builtin.go', {'./internal/tools', './tests/e2e'}, {'race'}, {'integration', 'provider-smoke'}, 'cross-boundary'),",
		"    ('internal/config/config.go', {'./internal/config', './tests/e2e'}, set(), {'integration', 'provider-smoke'}, 'cross-boundary'),",
		"    ('internal/cli/run.go', {'./internal/cli', './tests/e2e'}, set(), {'integration', 'provider-smoke'}, 'cross-boundary'),",
		"    ('internal/providerreadiness/readiness.go', {'./internal/providerreadiness', './tests/e2e'}, set(), {'integration', 'provider-smoke'}, 'cross-boundary'),",
		"    ('internal/events/bus.go', {'./internal/events', './tests/e2e'}, {'race'}, {'integration', 'provider-smoke'}, 'cross-boundary'),",
		"    ('internal/llm/openai_responses.go', {'./internal/llm', './tests/e2e'}, set(), {'integration', 'provider-smoke'}, 'live-runtime'),",
		"    ('internal/llm/openai_codex_websocket.go', {'./internal/llm', './tests/e2e'}, {'race'}, {'integration', 'provider-smoke'}, 'race-sensitive'),",
		"    ('internal/provenance/request_epoch.go', {'./internal/provenance', './tests/e2e'}, set(), {'integration', 'provider-smoke'}, 'cross-boundary'),",
		"    ('internal/runtime/compaction_policy.go', {'./internal/runtime', './tests/e2e'}, {'race'}, {'integration', 'provider-smoke', 'compaction'}, 'compaction'),",
		"    ('internal/runtime/context_projection.go', {'./internal/runtime', './tests/e2e'}, {'race'}, {'integration', 'provider-smoke', 'compaction'}, 'compaction'),",
		"    ('internal/runtime/policy/policy.go', {'./internal/runtime/policy', './tests/e2e'}, {'race'}, {'integration', 'provider-smoke', 'compaction'}, 'compaction'),",
		"    ('internal/runtime/module/policy.go', {'./internal/runtime/module', './tests/e2e'}, {'race'}, {'integration', 'provider-smoke', 'compaction'}, 'compaction'),",
		"    ('internal/llm/provider_projection.go', {'./internal/llm', './tests/e2e'}, set(), {'integration', 'provider-smoke', 'compaction'}, 'compaction'),",
		"    ('internal/llm/provider_projection_chunked_write.go', {'./internal/llm', './tests/e2e'}, set(), {'integration', 'provider-smoke', 'compaction'}, 'compaction'),",
		"    ('internal/llm/history.go', {'./internal/llm', './tests/e2e'}, set(), {'integration', 'provider-smoke', 'compaction'}, 'compaction'),",
		"    ('internal/session/transcript_index.go', {'./internal/session', './tests/e2e'}, {'race'}, {'integration', 'provider-smoke', 'compaction'}, 'compaction'),",
		"    ('internal/session/transcript_checkpoint.go', {'./internal/session', './tests/e2e'}, {'race'}, {'integration', 'provider-smoke', 'compaction'}, 'compaction'),",
		"    ('internal/session/transcript_reverse.go', {'./internal/session', './tests/e2e'}, {'race'}, {'integration', 'provider-smoke', 'compaction'}, 'compaction'),",
		"    ('tests/e2e/testdata/fake-mcp/server.py', {'./tests/e2e'}, set(), {'integration', 'provider-smoke'}, 'cross-boundary'),",
		"    ('internal/version/version.go', {'./internal/version'}, set(), set(), 'go-package'),",
		"    ('Makefile', {'./...'}, {'web', 'race'}, {'integration', 'provider-smoke', 'compaction'}, 'conservative'),",
		"    ('scripts/unknown-new-tool.py', {'./...'}, {'web', 'race'}, {'integration', 'provider-smoke', 'compaction'}, 'conservative'),",
		"    ('.agents/skills/example/SKILL.md', {'./...'}, {'web', 'race'}, {'integration', 'provider-smoke', 'compaction'}, 'conservative'),",
		"]",
		"for path, packages, candidate, final, rule in cases:",
		"    plan = validation_plan.plan_for_changes('focused', [ChangedFile('M', path)], base_sha='a' * 40, head_sha='b' * 40, dirty=True)",
		"    assert packages <= set(plan.focused_packages), (path, plan.as_dict())",
		"    assert candidate <= set(plan.candidate_flags), (path, plan.as_dict())",
		"    assert final <= set(plan.final_flags), (path, plan.as_dict())",
		"    assert any(rule in row.rule_id for row in plan.matched_rules), (path, plan.as_dict())",
		"docs = validation_plan.plan_for_changes('focused', [ChangedFile('M', 'docs/guide.md')], base_sha='a' * 40, head_sha='b' * 40, dirty=True)",
		"assert not docs.focused_packages and not docs.candidate_flags",
		"assert set(docs.final_flags) == {'integration', 'provider-smoke'}",
		"assert [row.rule_id for row in docs.matched_rules] == ['documentation-only', 'final-baseline']",
		"compaction_docs = validation_plan.plan_for_changes('focused', [ChangedFile('M', 'docs/compaction.md')], base_sha='a' * 40, head_sha='b' * 40, dirty=True)",
		"assert 'compaction' not in compaction_docs.final_flags and [row.rule_id for row in compaction_docs.matched_rules] == ['documentation-only', 'final-baseline']",
		"literal_backslash = validation_plan.plan_for_changes('focused', [ChangedFile('M', r'odd\\name.go')], base_sha='a' * 40, head_sha='b' * 40, dirty=True)",
		"assert literal_backslash.changed_files[0].path == r'odd\\name.go' and literal_backslash.focused_packages == ('./',), literal_backslash.as_dict()",
		"changes = [ChangedFile('M', 'internal/app/app.go'), ChangedFile('A', 'frontend/src/App.tsx')]",
		"first = validation_plan.plan_for_changes('focused', changes, base_sha='a' * 40, head_sha='b' * 40, dirty=True)",
		"second = validation_plan.plan_for_changes('focused', list(reversed(changes)), base_sha='a' * 40, head_sha='b' * 40, dirty=True)",
		"final_mode = validation_plan.plan_for_changes('final', changes, base_sha='a' * 40, head_sha='b' * 40, dirty=False)",
		"assert first.fingerprint == second.fingerprint == final_mode.fingerprint",
		"assert first.as_dict()['changed_files'] == second.as_dict()['changed_files']",
		"explanation = validation_plan.render_markdown(first)",
		"assert 'internal/app/app.go' in explanation and 'race' in explanation and 'cross-boundary' in explanation",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestEvalValidationPlanCollectsCleanAndDirtyGitChanges(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"import json",
		"import subprocess",
		"import tempfile",
		"from pathlib import Path",
		"from tests.eval.juex_eval import validation_plan",
		"def git(repo, *args):",
		"    return subprocess.run(['git', *args], cwd=repo, check=True, capture_output=True, text=True).stdout.strip()",
		"with tempfile.TemporaryDirectory() as tmp:",
		"    repo = Path(tmp) / 'repo'",
		"    (repo / 'internal' / 'app').mkdir(parents=True)",
		"    for name in ('app.go', 'old.go', 'deleted.go'):",
		"        (repo / 'internal' / 'app' / name).write_text('package app\\n', encoding='utf-8')",
		"    (repo / 'internal' / 'removed').mkdir(parents=True)",
		"    (repo / 'internal' / 'removed' / 'only.go').write_text('package removed\\n', encoding='utf-8')",
		"    (repo / 'internal' / 'legacy').mkdir(parents=True)",
		"    (repo / 'internal' / 'legacy' / 'only.go').write_text('package legacy\\n', encoding='utf-8')",
		"    git(repo, 'init', '--quiet')",
		"    git(repo, 'add', '.')",
		"    git(repo, '-c', 'user.name=Eval', '-c', 'user.email=eval@example.com', 'commit', '--quiet', '-m', 'base')",
		"    base = git(repo, 'rev-parse', 'HEAD')",
		"    git(repo, 'update-ref', 'refs/remotes/origin/main', base)",
		"    (repo / 'internal' / 'app' / 'app.go').write_text('package app\\n// committed\\n', encoding='utf-8')",
		"    git(repo, 'add', 'internal/app/app.go')",
		"    git(repo, '-c', 'user.name=Eval', '-c', 'user.email=eval@example.com', 'commit', '--quiet', '-m', 'change')",
		"    clean = validation_plan.collect_plan(repo, 'candidate')",
		"    explicit = validation_plan.collect_plan(repo, 'candidate', base=base)",
		"    assert clean.base_sha == base and clean.fingerprint == explicit.fingerprint",
		"    assert [row.path for row in clean.changed_files] == ['internal/app/app.go'], clean.as_dict()",
		"    git(repo, 'update-ref', '-d', 'refs/remotes/origin/main')",
		"    try:",
		"        validation_plan.collect_plan(repo, 'candidate')",
		"    except ValueError as exc:",
		"        assert 'merge-base origin/main HEAD failed' in str(exc)",
		"    else:",
		"        raise AssertionError('candidate plan guessed a base without origin/main')",
		"    git(repo, 'update-ref', 'refs/remotes/origin/main', base)",
		"    out = Path(tmp) / 'plan-output'",
		"    json_path, md_path = validation_plan.write_plan(out, clean)",
		"    assert json.loads(json_path.read_text(encoding='utf-8'))['fingerprint'] == clean.fingerprint",
		"    assert 'internal/app/app.go' in md_path.read_text(encoding='utf-8')",
		"    app = repo / 'internal' / 'app' / 'app.go'",
		"    app.write_text(app.read_text(encoding='utf-8') + '// staged\\n', encoding='utf-8')",
		"    git(repo, 'add', 'internal/app/app.go')",
		"    app.write_text(app.read_text(encoding='utf-8') + '// unstaged\\n', encoding='utf-8')",
		"    git(repo, 'mv', 'internal/app/old.go', 'internal/app/renamed.go')",
		"    (repo / 'internal' / 'newpkg').mkdir()",
		"    git(repo, 'mv', 'internal/legacy/only.go', 'internal/newpkg/only.go')",
		"    (repo / 'internal' / 'app' / 'deleted.go').unlink()",
		"    (repo / 'internal' / 'removed' / 'only.go').unlink()",
		"    (repo / 'internal' / 'app' / 'untracked.go').write_text('package app\\n', encoding='utf-8')",
		"    moved = validation_plan.plan_for_changes('focused', [validation_plan.ChangedFile('R', 'internal/newpkg/only.go', 'internal/legacy/only.go')], base_sha=base, head_sha=git(repo, 'rev-parse', 'HEAD'), dirty=True, repo_root=repo)",
		"    assert moved.focused_packages == ('./...',), moved.as_dict()",
		"    cross_package = validation_plan.plan_for_changes('focused', [validation_plan.ChangedFile('R', 'internal/newpkg/only.go', 'internal/app/old.go')], base_sha=base, head_sha=git(repo, 'rev-parse', 'HEAD'), dirty=True, repo_root=repo)",
		"    assert {'./internal/app', './internal/newpkg'} <= set(cross_package.focused_packages), cross_package.as_dict()",
		"    removed = validation_plan.plan_for_changes('focused', [validation_plan.ChangedFile('D', 'internal/removed/only.go')], base_sha=base, head_sha=git(repo, 'rev-parse', 'HEAD'), dirty=True, repo_root=repo)",
		"    assert removed.focused_packages == ('./...',), removed.as_dict()",
		"    dirty = validation_plan.collect_plan(repo, 'focused')",
		"    by_path = {row.path: row for row in dirty.changed_files}",
		"    assert {'internal/app/app.go', 'internal/app/renamed.go', 'internal/app/deleted.go', 'internal/app/untracked.go', 'internal/newpkg/only.go', 'internal/removed/only.go'} <= set(by_path), dirty.as_dict()",
		"    bad_bytes = b'bad_\\xff.txt'",
		"    bad_path = bad_bytes.decode('utf-8', 'surrogateescape')",
		"    bad_plan = validation_plan.plan_for_changes('focused', [validation_plan.ChangedFile('M', bad_path)], base_sha=base, head_sha=git(repo, 'rev-parse', 'HEAD'), dirty=True, repo_root=repo)",
		"    bad_json, bad_md = validation_plan.write_plan(Path(tmp) / 'non-utf8-plan', bad_plan)",
		"    payload = json.loads(bad_json.read_text(encoding='utf-8'))",
		"    assert any(row['path'].encode('utf-8', 'surrogateescape') == bad_bytes for row in payload['changed_files'])",
		"    assert r'bad_\\xff.txt' in bad_md.read_text(encoding='utf-8')",
		"    assert by_path['internal/app/renamed.go'].old_path == 'internal/app/old.go'",
		"    assert 'D' in by_path['internal/app/deleted.go'].status",
		"    assert 'U' in by_path['internal/app/untracked.go'].status",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestEvalValidationPlanChecksWorktreeStatusAsBytes(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"from pathlib import Path",
		"from tests.eval.juex_eval import validation_plan",
		"original_text = validation_plan._git_text",
		"original_bytes = validation_plan._git_bytes",
		"calls = []",
		"def fake_text(repo, args, error):",
		"    calls.append(('text', tuple(args)))",
		"    if args == ['rev-parse', 'HEAD']:",
		"        return 'a' * 40",
		"    if args and args[0] == 'status':",
		"        raise AssertionError('status must not use text decoding')",
		"    raise AssertionError(args)",
		"def fake_bytes(repo, args, error):",
		"    calls.append(('bytes', tuple(args)))",
		"    if args and args[0] == 'status':",
		"        return b'M bad_\\xff.txt\\0'",
		"    if args and args[0] in {'diff', 'ls-files'}:",
		"        return b''",
		"    raise AssertionError(args)",
		"try:",
		"    validation_plan._git_text = fake_text",
		"    validation_plan._git_bytes = fake_bytes",
		"    plan = validation_plan.collect_plan(Path('.'), 'focused')",
		"    assert plan.dirty is True",
		"    assert any(kind == 'bytes' and args[0] == 'status' for kind, args in calls), calls",
		"finally:",
		"    validation_plan._git_text = original_text",
		"    validation_plan._git_bytes = original_bytes",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestEvalVerificationTiersConsumeOneValidationPlan(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"from argparse import Namespace",
		"from tests.eval.juex_eval import cli, validation_plan",
		"changes = [validation_plan.ChangedFile('M', 'frontend/src/App.tsx'), validation_plan.ChangedFile('M', 'internal/runtime/compact.go')]",
		"plan = validation_plan.plan_for_changes('focused', changes, base_sha='a' * 40, head_sha='b' * 40, dirty=True)",
		"focused = Namespace(tier='focused', packages=[], planned=True)",
		"cli.apply_validation_plan(focused, plan)",
		"focused_steps = cli.verification_steps(focused)",
		"assert [step.label for step in focused_steps] == ['web-stub', 'go-test-focused', 'web-check', 'make-build-go']",
		"assert '-race' in focused_steps[1].command and './internal/runtime' in focused_steps[1].command and './tests/e2e' in focused_steps[1].command",
		"manual = Namespace(tier='focused', packages=['./internal/version'])",
		"manual_plan = cli.plan_with_cli_overrides(manual, plan)",
		"assert manual_plan.focused_packages == ('./internal/version',)",
		"assert any(row.rule_id == 'explicit-cli-override' for row in manual_plan.matched_rules)",
		"cli.apply_validation_plan(manual, manual_plan)",
		"manual_steps = cli.verification_steps(manual)",
		"assert [step.label for step in manual_steps] == ['web-stub', 'go-test-focused'] and '-race' not in manual_steps[1].command",
		"frontend_plan = validation_plan.plan_for_changes('focused', [validation_plan.ChangedFile('M', 'frontend/src/App.tsx')], base_sha='a' * 40, head_sha='b' * 40, dirty=True)",
		"frontend = Namespace(tier='focused', packages=[], planned=True)",
		"cli.apply_validation_plan(frontend, frontend_plan)",
		"assert [step.label for step in cli.verification_steps(frontend)] == ['web-check', 'make-build-go']",
		"candidate = Namespace(tier='candidate', race=False, web=False)",
		"cli.apply_validation_plan(candidate, plan)",
		"candidate_steps = cli.verification_steps(candidate)",
		"assert [step.label for step in candidate_steps] == ['web-stub', 'go-test-all-race', 'web-check', 'make-build-go']",
		"final = Namespace(tier='final', race=False, web=False, compaction=False, config='/tmp/provider.yaml', selection_seed='seed', run_id='unit', provider_timeout=7)",
		"cli.apply_validation_plan(final, plan)",
		"final_steps = cli.verification_steps(final)",
		"assert [step.label for step in final_steps] == ['web-stub', 'go-test-all-race', 'web-check', 'make-build-go', 'integration-contracts', 'live-integration', 'provider-model-smoke', 'compaction-eval']",
		"docs_plan = validation_plan.plan_for_changes('final', [validation_plan.ChangedFile('M', 'docs/guide.md')], base_sha='a' * 40, head_sha='b' * 40, dirty=False)",
		"docs_final = Namespace(tier='final', race=False, web=False, compaction=False, config='/tmp/provider.yaml', selection_seed='seed', run_id='unit', provider_timeout=7)",
		"cli.apply_validation_plan(docs_final, docs_plan)",
		"assert [step.label for step in cli.verification_steps(docs_final)] == ['web-stub', 'go-test-all', 'make-build', 'integration-contracts', 'live-integration', 'provider-model-smoke']",
		"forced_final = Namespace(tier='final', race=False, web=False, compaction=True)",
		"forced_plan = cli.plan_with_cli_overrides(forced_final, docs_plan)",
		"assert set(forced_plan.final_flags) == {'compaction', 'integration', 'provider-smoke'}",
		"assert forced_plan.fingerprint != docs_plan.fingerprint",
		"assert validation_plan.candidate_fingerprint(forced_plan) == validation_plan.candidate_fingerprint(docs_plan)",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestEvalPythonModuleAndShellWrappersExposeHelp(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	moduleHelp := runUV(t, root, "python", "-m", "tests.eval.juex_eval", "--help")
	for _, want := range []string{"plan", "verify", "development", "provider-smoke", "compaction"} {
		assertHelpContains(t, moduleHelp, want)
	}
	planHelp := runUV(t, root, "python", "-m", "tests.eval.juex_eval", "plan", "--help")
	assertHelpContains(t, planHelp, "--tier", "--base", "--output-dir", "--explain")

	verifyHelp := runUV(t, root, "python", "-m", "tests.eval.juex_eval", "verify", "--help")
	assertHelpContains(t, verifyHelp, "focused", "candidate", "final")
	focusedHelp := runUV(t, root, "python", "-m", "tests.eval.juex_eval", "verify", "focused", "--help")
	assertHelpContains(t, focusedHelp, "packages", "--planned", "--base", "--explain")
	candidateHelp := runUV(t, root, "python", "-m", "tests.eval.juex_eval", "verify", "candidate", "--help")
	assertHelpContains(t, candidateHelp, "--race", "--web", "--base", "--explain", "--run-id", "--report-dir")
	finalHelp := runUV(t, root, "python", "-m", "tests.eval.juex_eval", "verify", "final", "--help")
	assertHelpContains(t, finalHelp, "--race", "--web", "--compaction", "--base", "--explain", "--config", "--selection-seed", "--run-id", "--report-dir")

	providerHelp := runUV(t, root, "python", "-m", "tests.eval.juex_eval", "provider-smoke", "--help")
	assertHelpContains(t, providerHelp, "--only", "--all-models", "--config", "--selection-seed", "--report-dir")

	compactionHelp := runUV(t, root, "python", "-m", "tests.eval.juex_eval", "compaction", "--help")
	assertHelpContains(t, compactionHelp, "--only", "--all-models", "--config", "--selection-seed", "--report-dir")

	developmentHelp := runUV(t, root, "python", "-m", "tests.eval.juex_eval", "development", "--help")
	assertHelpContains(t, developmentHelp, "--only", "--compaction-only", "--report-dir")

	for _, script := range []string{
		"tests/eval/development_eval.sh",
		"tests/eval/provider_model_smoke.sh",
		"tests/eval/compaction_eval.sh",
	} {
		t.Run(script, func(t *testing.T) {
			if _, err := exec.LookPath("bash"); err != nil {
				t.Skip("bash not found; skipping shell wrapper test")
			}
			cmd := exec.Command("bash", filepath.Join(root, script), "--help")
			cmd.Dir = t.TempDir()
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%s --help failed: %v\n%s", script, err, out)
			}
			if !strings.Contains(strings.ToLower(string(out)), "usage:") {
				t.Fatalf("%s --help missing Usage:\n%s", script, out)
			}
		})
	}
}

func TestEvalDevelopmentStepBuilderUsesConsistentFlags(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"import json",
		"from argparse import Namespace",
		"from pathlib import Path",
		"from tests.eval.juex_eval import cli",
		"args = Namespace(",
		"    skip_tests=True,",
		"    no_provider_smoke=False,",
		"    compaction_eval=True,",
		"    run_id='unit',",
		"    config='/tmp/provider config.yaml',",
		"    selection_seed='repeatable',",
		"    provider_timeout=7,",
		"    provider_only='ark:model',",
		"    provider_all_models=False,",
		"    compaction_all_models=False,",
		"    compaction_only=['openai:model', 'ark:other'],",
		")",
		"steps, _, _ = cli.development_steps(args, Path('reports'))",
		"assert next(step.test_environment for step in steps if step.label == 'provider-model-smoke')",
		"print(json.dumps([{'label': step.label, 'command': step.command} for step in steps]))",
	}, "\n")
	out := runUV(t, root, "python", "-c", program)

	var steps []struct {
		Label   string   `json:"label"`
		Command []string `json:"command"`
	}
	if err := json.Unmarshal([]byte(out), &steps); err != nil {
		t.Fatalf("decode steps: %v\n%s", err, out)
	}

	providerCmd := findEvalCommand(t, steps, "provider-model-smoke")
	assertCommandFlagValue(t, providerCmd, "--only", "ark:model")
	assertCommandFlagValue(t, providerCmd, "--config", "/tmp/provider config.yaml")
	assertCommandFlagValue(t, providerCmd, "--selection-seed", "repeatable")
	assertCommandHasFlag(t, providerCmd, "--report-dir")
	assertCommandLacks(t, providerCmd, "--provider-only")

	compactionCmd := findEvalCommand(t, steps, "compaction-eval")
	assertCommandFlagValue(t, compactionCmd, "--only", "openai:model")
	assertCommandFlagValue(t, compactionCmd, "--only", "ark:other")
	assertCommandFlagValue(t, compactionCmd, "--config", "/tmp/provider config.yaml")
	assertCommandFlagValue(t, compactionCmd, "--selection-seed", "repeatable")
	assertCommandHasFlag(t, compactionCmd, "--report-dir")
	assertCommandLacks(t, compactionCmd, "--out-root")
}

func TestEvalDevelopmentUsesSingleCandidateGoSuite(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"import json",
		"from argparse import Namespace",
		"from pathlib import Path",
		"from tests.eval.juex_eval import cli",
		"args = Namespace(skip_tests=False, no_provider_smoke=True, compaction_eval=False, run_id='unit', config='/tmp/provider.yaml', selection_seed='repeatable', provider_timeout=7, provider_only='', provider_all_models=False, compaction_all_models=False, compaction_only=[])",
		"steps, _, _ = cli.development_steps(args, Path('reports'))",
		"print(json.dumps([step.label for step in steps]))",
	}, "\n")
	out := runUV(t, root, "python", "-c", program)
	var labels []string
	if err := json.Unmarshal([]byte(out), &labels); err != nil {
		t.Fatalf("decode labels: %v\n%s", err, out)
	}
	if !reflect.DeepEqual(labels, []string{"web-stub", "go-test-all", "make-build"}) {
		t.Fatalf("development labels = %q, want one full Go suite and one build", labels)
	}
}

func TestEvalVerifyFocusedPlansOnlyExplicitPackages(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"import json",
		"from argparse import Namespace",
		"from tests.eval.juex_eval import cli",
		"steps = cli.verification_steps(Namespace(tier='focused', packages=['./internal/app', './internal/runtime']))",
		"print(json.dumps([{'label': step.label, 'command': step.command, 'test_environment': step.test_environment} for step in steps]))",
	}, "\n")
	out := runUV(t, root, "python", "-c", program)

	var steps []struct {
		Label           string   `json:"label"`
		Command         []string `json:"command"`
		TestEnvironment bool     `json:"test_environment"`
	}
	if err := json.Unmarshal([]byte(out), &steps); err != nil {
		t.Fatalf("decode steps: %v\n%s", err, out)
	}
	if len(steps) != 2 || steps[0].Label != "web-stub" || steps[1].Label != "go-test-focused" {
		t.Fatalf("steps = %+v, want web stub followed by one focused test step", steps)
	}
	if !reflect.DeepEqual(steps[0].Command, []string{"make", "web-stub"}) {
		t.Fatalf("web stub command = %q", steps[0].Command)
	}
	want := []string{"go", "test", "./internal/app", "./internal/runtime", "-count=1"}
	if !reflect.DeepEqual(steps[1].Command, want) {
		t.Fatalf("command = %q, want %q", steps[1].Command, want)
	}
	if !steps[1].TestEnvironment {
		t.Fatal("focused test step must provision ripgrep")
	}
}

func TestEvalWindowsBashDiscoveryWalksGitExecutableAncestors(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"import tempfile",
		"from pathlib import Path",
		"from tests.eval.juex_eval import cli",
		"with tempfile.TemporaryDirectory() as tmp:",
		"    install = Path(tmp) / 'PortableGit'",
		"    git = install / 'mingw64' / 'bin' / 'git.exe'",
		"    bash = install / 'bin' / 'bash.exe'",
		"    git.parent.mkdir(parents=True)",
		"    bash.parent.mkdir(parents=True)",
		"    git.touch()",
		"    bash.touch()",
		"    resolved = cli.windows_bash_from_git(str(git))",
		"    assert resolved == str(bash.resolve()), (resolved, bash)",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestEvalGoTestEnvironmentRunsShellProvisionerThroughBash(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"import os",
		"from types import SimpleNamespace",
		"from tests.eval.juex_eval import cli",
		"calls = []",
		"original_run = cli.subprocess.run",
		"original_which = cli.shutil.which",
		"try:",
		"    cli.shutil.which = lambda name: None",
		"    def fake_run(command, **kwargs):",
		"        calls.append(command)",
		"        return SimpleNamespace(returncode=0, stdout='/tmp/rg\\n')",
		"    cli.subprocess.run = fake_run",
		"    env = cli.go_test_environment()",
		"    assert calls == [[cli.BASH_EXECUTABLE, cli.ENSURE_RIPGREP]], calls",
		"    expected = str(cli.REPO_ROOT / '.tmp' / 'dev-ripgrep' / 'juex-path') if os.name == 'nt' else '/tmp/rg'",
		"    assert env['PATH'].split(os.pathsep)[0] == expected, env['PATH']",
		"finally:",
		"    cli.subprocess.run = original_run",
		"    cli.shutil.which = original_which",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestEvalGoTestEnvironmentReusesRipgrepFromPath(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"import os",
		"from tests.eval.juex_eval import cli",
		"original_run = cli.subprocess.run",
		"original_which = cli.shutil.which",
		"try:",
		"    cli.shutil.which = lambda name: '/tmp/rg-bin/rg'",
		"    cli.subprocess.run = lambda *args, **kwargs: (_ for _ in ()).throw(AssertionError('unexpected provisioner'))",
		"    env = cli.go_test_environment()",
		"    expected = os.path.dirname(os.path.abspath('/tmp/rg-bin/rg'))",
		"    assert env['PATH'].split(os.pathsep)[0] == expected, env['PATH']",
		"finally:",
		"    cli.subprocess.run = original_run",
		"    cli.shutil.which = original_which",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestEvalVerifyCandidateRaceReplacesNormalSuite(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"import json",
		"from argparse import Namespace",
		"from tests.eval.juex_eval import cli",
		"def plan(race):",
		"    return [{'label': step.label, 'command': step.command, 'test_environment': step.test_environment} for step in cli.verification_steps(Namespace(tier='candidate', race=race, web=False))]",
		"print(json.dumps({'normal': plan(False), 'race': plan(True)}))",
	}, "\n")
	out := runUV(t, root, "python", "-c", program)

	var plans map[string][]struct {
		Label           string   `json:"label"`
		Command         []string `json:"command"`
		TestEnvironment bool     `json:"test_environment"`
	}
	if err := json.Unmarshal([]byte(out), &plans); err != nil {
		t.Fatalf("decode plans: %v\n%s", err, out)
	}
	if got := []string{plans["normal"][0].Label, plans["normal"][1].Label, plans["normal"][2].Label}; !reflect.DeepEqual(got, []string{"web-stub", "go-test-all", "make-build"}) {
		t.Fatalf("normal labels = %q", got)
	}
	if got := plans["normal"][0].Command; !reflect.DeepEqual(got, []string{"make", "web-stub"}) {
		t.Fatalf("normal web stub command = %q", got)
	}
	if got := plans["normal"][1].Command; !reflect.DeepEqual(got, []string{"go", "test", "./...", "-count=1"}) {
		t.Fatalf("normal test command = %q", got)
	}
	if got := []string{plans["race"][0].Label, plans["race"][1].Label, plans["race"][2].Label}; !reflect.DeepEqual(got, []string{"web-stub", "go-test-all-race", "make-build"}) {
		t.Fatalf("race labels = %q", got)
	}
	if got := plans["race"][1].Command; !reflect.DeepEqual(got, []string{"go", "test", "./...", "-race", "-count=1"}) {
		t.Fatalf("race test command = %q", got)
	}
	if !plans["normal"][1].TestEnvironment || !plans["race"][1].TestEnvironment {
		t.Fatal("candidate Go suites must provision ripgrep")
	}
}

func TestEvalVerifyCandidateWebCheckDoesNotRebuildFrontend(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"import json",
		"from argparse import Namespace",
		"from tests.eval.juex_eval import cli",
		"steps = cli.verification_steps(Namespace(tier='candidate', race=False, web=True))",
		"print(json.dumps([{'label': step.label, 'command': step.command} for step in steps]))",
	}, "\n")
	out := runUV(t, root, "python", "-c", program)

	var steps []struct {
		Label   string   `json:"label"`
		Command []string `json:"command"`
	}
	if err := json.Unmarshal([]byte(out), &steps); err != nil {
		t.Fatalf("decode steps: %v\n%s", err, out)
	}
	labels := make([]string, 0, len(steps))
	for _, step := range steps {
		labels = append(labels, step.Label)
	}
	if !reflect.DeepEqual(labels, []string{"web-stub", "go-test-all", "web-check", "make-build-go"}) {
		t.Fatalf("labels = %q", labels)
	}
	if !reflect.DeepEqual(steps[2].Command, []string{"make", "web-check"}) {
		t.Fatalf("web command = %q", steps[2].Command)
	}
	if !reflect.DeepEqual(steps[3].Command, []string{"make", "build-go"}) {
		t.Fatalf("binary command = %q", steps[3].Command)
	}
	for _, step := range steps {
		if reflect.DeepEqual(step.Command, []string{"make", "build"}) {
			t.Fatalf("WEB=1 plan retained duplicate frontend build: %+v", steps)
		}
	}
}

func TestEvalVerifyFinalExtendsCandidateWithConditionalLiveGates(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"import json",
		"from argparse import Namespace",
		"from tests.eval.juex_eval import cli",
		"def plan(compaction):",
		"    args = Namespace(tier='final', race=False, web=False, compaction=compaction, config='/tmp/provider config.yaml', selection_seed='repeatable', run_id='unit', provider_timeout=7)",
		"    return [{'label': step.label, 'command': step.command, 'environment': step.environment, 'test_environment': step.test_environment, 'retry_transient': step.retry_transient} for step in cli.verification_steps(args)]",
		"print(json.dumps({'default': plan(False), 'compaction': plan(True)}))",
	}, "\n")
	out := runUV(t, root, "python", "-c", program)

	var plans map[string][]struct {
		Label           string            `json:"label"`
		Command         []string          `json:"command"`
		Environment     map[string]string `json:"environment"`
		TestEnvironment bool              `json:"test_environment"`
		RetryTransient  bool              `json:"retry_transient"`
	}
	if err := json.Unmarshal([]byte(out), &plans); err != nil {
		t.Fatalf("decode plans: %v\n%s", err, out)
	}
	labels := func(steps []struct {
		Label           string            `json:"label"`
		Command         []string          `json:"command"`
		Environment     map[string]string `json:"environment"`
		TestEnvironment bool              `json:"test_environment"`
		RetryTransient  bool              `json:"retry_transient"`
	}) []string {
		out := make([]string, 0, len(steps))
		for _, step := range steps {
			out = append(out, step.Label)
		}
		return out
	}
	if got := labels(plans["default"]); !reflect.DeepEqual(got, []string{"web-stub", "go-test-all", "make-build", "integration-contracts", "live-integration", "provider-model-smoke"}) {
		t.Fatalf("default labels = %q", got)
	}
	if got := labels(plans["compaction"]); !reflect.DeepEqual(got, []string{"web-stub", "go-test-all", "make-build", "integration-contracts", "live-integration", "provider-model-smoke", "compaction-eval"}) {
		t.Fatalf("compaction labels = %q", got)
	}
	if !reflect.DeepEqual(plans["default"][3].Command, []string{"make", "integration-contracts"}) || plans["default"][3].RetryTransient {
		t.Fatalf("integration contracts = %+v", plans["default"][3])
	}
	if !reflect.DeepEqual(plans["default"][4].Command, []string{"make", "integration-live"}) || !plans["default"][4].RetryTransient {
		t.Fatalf("live integration = %+v", plans["default"][4])
	}
	if got := plans["default"][4].Environment["JUEX_PROVIDER_CONFIG"]; got != "/tmp/provider config.yaml" {
		t.Fatalf("integration provider config = %q", got)
	}
	provider := plans["default"][5].Command
	if !plans["default"][5].TestEnvironment {
		t.Fatal("provider smoke must inherit the provisioned ripgrep PATH")
	}
	assertCommandFlagValue(t, provider, "--juex", "./dist/juex")
	assertCommandFlagValue(t, provider, "--config", "/tmp/provider config.yaml")
	assertCommandFlagValue(t, provider, "--selection-seed", "repeatable")
	assertCommandFlagValue(t, provider, "--run-id", "unit")
	assertCommandFlagValue(t, provider, "--timeout", "7")
	compaction := plans["compaction"][6].Command
	assertCommandFlagValue(t, compaction, "--juex", "./dist/juex")
	assertCommandFlagValue(t, compaction, "--config", "/tmp/provider config.yaml")
	assertCommandFlagValue(t, compaction, "--selection-seed", "repeatable")
	assertCommandFlagValue(t, compaction, "--run-id", "unit")
}

func TestEvalVerificationExecutorStopsAtFirstFailure(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"from tests.eval.juex_eval import cli",
		"steps = [cli.VerificationStep('first', ['first']), cli.VerificationStep('fail', ['fail']), cli.VerificationStep('never', ['never'])]",
		"calls = []",
		"def run(step):",
		"    calls.append(step.label)",
		"    return 1 if step.label == 'fail' else 0",
		"status = cli.execute_verification_steps(steps, run)",
		"assert status == 1, status",
		"assert calls == ['first', 'fail'], calls",
		"calls.clear()",
		"status = cli.execute_development_steps(steps, run)",
		"assert status == 1, status",
		"assert calls == ['first', 'fail', 'never'], calls",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestEvalValidationOutcomeFixtures(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"from tests.eval.juex_eval import outcomes",
		"passed = outcomes.success(attempt_count=1)",
		"flaky = outcomes.success(attempt_count=2)",
		"product = outcomes.classify_failure('AssertionError: want 2, got 1', deterministic=True, exit_status=1)",
		"environment = outcomes.classify_failure('/bin/sh: go: command not found', deterministic=True, exit_status=127)",
		"provider = outcomes.classify_failure('JUEX_VALIDATION_OUTCOME {\"outcome\":\"provider_unavailable\",\"reason\":\"requested provider:model is not eligible\",\"matched_rule\":\"provider-selection-unavailable\"}', deterministic=False, exit_status=1)",
		"transient = outcomes.classify_failure('{\"error\":{\"retryable\":true,\"message\":\"upstream reset\"}}', deterministic=False, exit_status=1)",
		"plain_status = outcomes.classify_failure('codex websocket error: status 503: unavailable', deterministic=False, exit_status=1)",
		"missing_live_config = outcomes.classify_failure('JUEX_PROVIDER_CONFIG points to missing live provider config /tmp/missing.yaml', deterministic=False, exit_status=1)",
		"rows = [passed, flaky, product, environment, provider, transient]",
		"assert [row.outcome for row in rows] == ['passed', 'flaky_pass', 'product_failure', 'environment_failure', 'provider_unavailable', 'transient_failure'], rows",
		"assert set(outcomes.OUTCOME_VALUES) == {row.outcome for row in rows}",
		"assert product.matched_rule == 'deterministic-step-nonzero' and product.recommended_action == 'fix_code'",
		"assert environment.matched_rule == 'environment-command-missing' and environment.recommended_action == 'fix_environment'",
		"assert provider.matched_rule == 'provider-selection-unavailable' and provider.recommended_action == 'stop'",
		"assert transient.matched_rule == 'transient-structured-retryable' and transient.retryable is True",
		"assert plain_status.matched_rule == 'transient-http-status' and plain_status.retryable is True",
		"assert missing_live_config.outcome == 'environment_failure' and missing_live_config.matched_rule == 'environment-invalid-config' and missing_live_config.recommended_action == 'fix_environment'",
		"for diagnostic in ('AssertionError: permission denied', 'sandbox expected operation not permitted', 'want unauthorized provider error'):",
		"    deterministic_result = outcomes.classify_failure(diagnostic, deterministic=True, exit_status=1)",
		"    assert deterministic_result.outcome == 'product_failure' and deterministic_result.matched_rule == 'deterministic-step-nonzero', deterministic_result",
		"assert outcomes.classify_failure('provider request failed: unauthorized', deterministic=False, exit_status=1).matched_rule == 'environment-provider-credentials'",
		"assert outcomes.classify_failure(outcomes.marker(outcomes.invalid_config_failure('missing config')), deterministic=True, exit_status=1).outcome == 'product_failure'",
		"assert outcomes.classify_failure('{\"retryable\":true}', deterministic=True, exit_status=1).outcome == 'product_failure'",
		"assert outcomes.classify_failure(outcomes.marker(passed), deterministic=False, exit_status=1).outcome == 'product_failure'",
		"assert all(row.blocks_merge is (row.outcome not in {'passed', 'flaky_pass'}) for row in rows)",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestEvalMissingProviderConfigIsEnvironmentFailure(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"import argparse, json, sys, tempfile",
		"from pathlib import Path",
		"from tests.eval.juex_eval import compaction, helper",
		"with tempfile.TemporaryDirectory() as work:",
		"    root = Path(work)",
		"    missing = root / 'missing.yaml'",
		"    smoke_report = root / 'provider-smoke'",
		"    status = helper.provider_smoke(['--juex', sys.executable, '--config', str(missing), '--report-dir', str(smoke_report), '--work-root', str(root / 'smoke-work'), '--run-id', 'missing-config'])",
		"    smoke = json.loads((smoke_report / 'summary.json').read_text(encoding='utf-8'))",
		"    assert status == 1 and smoke['outcome'] == 'environment_failure' and smoke['matched_rule'] == 'environment-invalid-config' and smoke['recommended_action'] == 'fix_environment', smoke",
		"    compaction_report = root / 'compaction'",
		"    args = argparse.Namespace(only=[], all_models=False, selection_seed='unit', juex=sys.executable, config=str(missing), out_root=str(compaction_report), run_id='missing-config', context_window=32000, turn_timeout=1, keep_workdir=False)",
		"    status = compaction.run(args)",
		"    summary = json.loads((compaction_report / 'summary.json').read_text(encoding='utf-8'))",
		"    assert status == 1 and summary['outcome'] == 'environment_failure' and summary['matched_rule'] == 'environment-invalid-config' and summary['recommended_action'] == 'fix_environment', summary",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestEvalRecordedStepRetriesOnlyAllowlistedTransientFailureOnce(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"import tempfile",
		"from pathlib import Path",
		"from types import SimpleNamespace",
		"from tests.eval.juex_eval import cli",
		"original_run = cli.subprocess.run",
		"def exercise(step, attempts):",
		"    calls = []",
		"    def fake_run(command, **kwargs):",
		"        index = len(calls)",
		"        status, body = attempts[index]",
		"        calls.append(command)",
		"        kwargs['stdout'].write(body.encode('utf-8'))",
		"        return SimpleNamespace(returncode=status)",
		"    cli.subprocess.run = fake_run",
		"    with tempfile.TemporaryDirectory() as tmp:",
		"        result = cli.run_recorded_verification_step(step, Path(tmp), None)",
		"        logs = [Path(row['log']).read_text(encoding='utf-8') for row in result['attempts']]",
		"        assert len(logs) == len(calls)",
		"        return result, calls, logs",
		"try:",
		"    assertion, calls, logs = exercise(cli.VerificationStep('go-test-all', ['go', 'test', './...']), [(1, 'AssertionError: failed\\n'), (0, 'must not run\\n')])",
		"    assert assertion['outcome'] == 'product_failure' and len(calls) == 1 and logs == ['AssertionError: failed\\n'], assertion",
		"    transient_step = cli.VerificationStep('provider-model-smoke', ['provider-smoke'], retry_transient=True)",
		"    flaky, calls, logs = exercise(transient_step, [(1, '{\"retryable\":true}\\n'), (0, 'ok\\n')])",
		"    assert flaky['outcome'] == 'flaky_pass' and len(calls) == 2 and len(logs) == 2, flaky",
		"    assert flaky['initial_outcome'] == 'transient_failure' and [row['outcome'] for row in flaky['attempts']] == ['transient_failure', 'passed']",
		"    exhausted, calls, logs = exercise(transient_step, [(1, '{\"retryable\":true,\"attempt\":1}\\n'), (1, '{\"retryable\":true,\"attempt\":2}\\n'), (0, 'must not run\\n')])",
		"    assert exhausted['outcome'] == 'transient_failure' and exhausted['initial_outcome'] == 'transient_failure'",
		"    assert len(calls) == 2 and len(logs) == 2 and logs[0] != logs[1], exhausted",
		"    assert exhausted['blocks_merge'] is True and exhausted['recommended_action'] == 'stop'",
		"    with tempfile.TemporaryDirectory() as tmp:",
		"        root = Path(tmp)",
		"        (root / 'logs').mkdir()",
		"        report = root / 'provider-report'",
		"        report.mkdir()",
		"        archive_calls = []",
		"        def archive_run(command, **kwargs):",
		"            index = len(archive_calls)",
		"            archive_calls.append(command)",
		"            (report / 'raw-error.log').write_text(f'attempt={index + 1}\\n', encoding='utf-8')",
		"            kwargs['stdout'].write((('{\"retryable\":true}' if index == 0 else 'ok') + '\\n').encode('utf-8'))",
		"            return SimpleNamespace(returncode=1 if index == 0 else 0)",
		"        cli.subprocess.run = archive_run",
		"        archived = cli.run_recorded_verification_step(cli.VerificationStep('provider-model-smoke', ['provider-smoke', '--report-dir', str(report)], retry_transient=True), root / 'logs', None)",
		"        first_report = Path(archived['attempts'][0]['report'])",
		"        assert (first_report / 'raw-error.log').read_text(encoding='utf-8') == 'attempt=1\\n'",
		"        assert (report / 'raw-error.log').read_text(encoding='utf-8') == 'attempt=2\\n'",
		"finally:",
		"    cli.subprocess.run = original_run",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestEvalProviderRetryBudgetExcludesContractFailures(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"import tempfile",
		"from pathlib import Path",
		"from types import SimpleNamespace",
		"from tests.eval.juex_eval import contract_oracle, helper, outcomes, schedule_routing, selection",
		"row = selection.Candidate('provider', 'model', 'openai/chat', 'default', 'true', 'unset', 'provider:model')",
		"with tempfile.TemporaryDirectory() as tmp:",
		"    root = Path(tmp)",
		"    ctx = helper.ProviderSmokeContext(row, '/bin/true', {}, root, root / 'report', 'unit', 1, 1, str(root / 'codex'))",
		"    case = root / 'turn-case'",
		"    case.mkdir()",
		"    calls = []",
		"    original_turn = helper.run_turn",
		"    original_write = helper.write_selected_config",
		"    original_json = helper.json_file_value",
		"    original_sessions = helper.agent_sessions_dir",
		"    original_validate = schedule_routing.validate_outcome",
		"    try:",
		"        invalid_ctx = helper.ProviderSmokeContext(row, '/bin/true', {}, root, root / 'report', 'unit', 1, 2, str(root / 'codex'))",
		"        try:",
		"            helper.run_turn_with_retries(invalid_ctx, case, root / 'config.yaml', 'turn1', [])",
		"        except ValueError as exc:",
		"            assert str(exc) == 'provider smoke retries must be 0 or 1'",
		"        else:",
		"            raise AssertionError('retry budget above one was accepted')",
		"        def transient_turn(ctx, case_dir, config, label, args):",
		"            calls.append(label)",
		"            (case_dir / f'{label}.stderr.log').write_text('{\"retryable\":true,\"attempt\":%d}\\n' % len(calls), encoding='utf-8')",
		"            (case_dir / f'{label}.stdout.json').write_text('{}\\n', encoding='utf-8')",
		"            return 1",
		"        helper.run_turn = transient_turn",
		"        status, result, attempts = helper.run_turn_with_retries(ctx, case, root / 'config.yaml', 'turn1', [])",
		"        assert status == 1 and result.outcome == outcomes.TRANSIENT_FAILURE and attempts == 2 and len(calls) == 2",
		"        assert (case / 'turn1.attempt-1.stderr.log').is_file() and (case / 'turn1.attempt-2.stderr.log').is_file()",
		"        calls.clear()",
		"        def passing_turn(ctx, case_dir, config, label, args):",
		"            calls.append(label)",
		"            (case_dir / f'{label}.stdout.json').write_text('{\"session_id\":\"session\"}\\n', encoding='utf-8')",
		"            (case_dir / f'{label}.stderr.log').write_text('', encoding='utf-8')",
		"            return 0",
		"        helper.run_turn = passing_turn",
		"        helper.write_selected_config = lambda *args, **kwargs: None",
		"        helper.json_file_value = lambda *args: 'session'",
		"        helper.agent_sessions_dir = lambda *args: root / 'sessions'",
		"        failed_report = contract_oracle.ContractReport(False, ['contract assertion failed'])",
		"        schedule_routing.validate_outcome = lambda *args: SimpleNamespace(kind=helper.SCENARIO_CAPABILITY_FAILED, report=failed_report)",
		"        expectation = schedule_routing.ScheduleRoutingExpectation('id', 21600, 'content', 'token')",
		"        scenario = helper.run_schedule_routing_case(ctx, root / 'artifacts', expectation)",
		"        assert scenario.validation_outcome.outcome == outcomes.PRODUCT_FAILURE, scenario",
		"        assert scenario.attempt_count == 1 and calls == ['turn1'], calls",
		"    finally:",
		"        helper.run_turn = original_turn",
		"        helper.write_selected_config = original_write",
		"        helper.json_file_value = original_json",
		"        helper.agent_sessions_dir = original_sessions",
		"        schedule_routing.validate_outcome = original_validate",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestEvalCompactionPropagatesTransientTurnOutcome(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"import tempfile",
		"from argparse import Namespace",
		"from pathlib import Path",
		"from tests.eval.juex_eval import compaction, outcomes",
		"with tempfile.TemporaryDirectory() as tmp:",
		"    root = Path(tmp)",
		"    out = root / 'reports'",
		"    args = Namespace(config=str(root / 'config.yaml'), keep_workdir=True, context_window=32000, turn_timeout=1, juex='/bin/true')",
		"    original_write = compaction.helper.write_selected_config",
		"    original_validate = compaction.helper.validate_source_config",
		"    original_turn = compaction.run_eval_turn",
		"    validated = []",
		"    try:",
		"        compaction.helper.write_selected_config = lambda *args, **kwargs: None",
		"        compaction.helper.validate_source_config = lambda juex, config: validated.append((juex, Path(config)))",
		"        def timed_out(args, work, prompt, output):",
		"            output.write_text('', encoding='utf-8')",
		"            return 124",
		"        compaction.run_eval_turn = timed_out",
		"        status = compaction.run_model(args, {}, 'provider:model', out, [])",
		"        assert status == 1",
		"        assert len(validated) == 1 and validated[0][0] == '/bin/true' and validated[0][1].name == 'juex.yaml', validated",
		"        result = compaction.load_model_outcome(out / compaction.helper.safe_ref('provider:model'), status)",
		"        assert result.outcome == 'transient_failure' and result.retryable is True and result.matched_rule == 'transient-provider-timeout', result",
		"        aggregate = compaction.aggregate_compaction_outcome([{'provider_model': 'provider:model', 'status': 'fail', **result.as_dict()}])",
		"        propagated = outcomes.classify_failure(outcomes.marker(aggregate), deterministic=False, exit_status=1)",
		"        assert propagated == aggregate and propagated.retryable is True, (propagated, aggregate)",
		"        product = compaction.aggregate_compaction_outcome([{'provider_model': 'provider:model', 'status': 'fail', **outcomes.ValidationOutcome('product_failure', 'score contract failed', 'compaction-quality-contract', True, 'fix_code').as_dict()}])",
		"        assert product.outcome == 'product_failure' and product.retryable is False",
		"    finally:",
		"        compaction.helper.write_selected_config = original_write",
		"        compaction.helper.validate_source_config = original_validate",
		"        compaction.run_eval_turn = original_turn",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestEvalOutcomeSummaryDistinguishesCodeEnvironmentAndStopActions(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"from tests.eval.juex_eval import outcomes, verification",
		"def row(label, result):",
		"    return {'label': label, 'execution_state': 'executed', **result.as_dict()}",
		"product = verification.summarize_outcomes([row('test', outcomes.classify_failure('assertion failed', deterministic=True, exit_status=1))])",
		"environment = verification.summarize_outcomes([row('build', outcomes.classify_failure('go: command not found', deterministic=True, exit_status=127))])",
		"provider = verification.summarize_outcomes([row('live', outcomes.classify_failure('provider_unavailable: no eligible provider:model', deterministic=False, exit_status=1))])",
		"passed = verification.summarize_outcomes([row('test', outcomes.success(attempt_count=1)), row('live', outcomes.success(attempt_count=2))])",
		"assert (product['failure_type'], product['recommended_action']) == ('code_failure', 'fix_code')",
		"assert (environment['failure_type'], environment['recommended_action']) == ('validation_incomplete', 'fix_environment')",
		"assert (provider['failure_type'], provider['recommended_action']) == ('validation_incomplete', 'stop')",
		"assert passed['blocks_merge'] is False and passed['failure_type'] is None and passed['recommended_action'] == 'continue'",
		"rendered = verification.render_terminal_summary(provider)",
		"assert 'blocks_merge=true' in rendered and 'validation_incomplete' in rendered and 'action=stop' in rendered, rendered",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestEvalVerificationCleanWorktreePolicy(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"import tempfile",
		"from argparse import Namespace",
		"from pathlib import Path",
		"from tests.eval.juex_eval import cli, validation_plan, verification",
		"calls = []",
		"snapshot = verification.RepositorySnapshot('a' * 40, 'feature/test', False)",
		"original_clean = cli.require_clean_worktree",
		"original_steps = cli.verification_steps",
		"original_resolved_path = cli.selection.resolved_path",
		"original_snapshot = verification.repository_snapshot",
		"original_environment = verification.environment_fingerprint",
		"original_find = verification.find_reusable_candidate",
		"original_write = verification.write_record",
		"original_provider_summary = cli.provider_record_summary",
		"original_plan = validation_plan.collect_plan",
		"try:",
		"    cli.require_clean_worktree = lambda: (calls.append('pre-clean') or snapshot)",
		"    cli.verification_steps = lambda args: []",
		"    cli.selection.resolved_path = lambda path: path",
		"    verification.repository_snapshot = lambda root: (calls.append('post-clean') or snapshot)",
		"    verification.environment_fingerprint = lambda **kwargs: 'sha256:environment'",
		"    verification.find_reusable_candidate = lambda *args: verification.ReuseDecision(None, {}, [])",
		"    verification.write_record = lambda *args: None",
		"    cli.provider_record_summary = lambda args: {}",
		"    validation_plan.collect_plan = lambda root, mode, base=None: validation_plan.plan_for_changes(mode, [], base_sha='b' * 40, head_sha=snapshot.head_sha, dirty=(mode == 'focused'))",
		"    assert cli.run_verify(Namespace(tier='focused', packages=[], planned=True)) == 0",
		"    assert calls == [], calls",
		"    with tempfile.TemporaryDirectory() as tmp:",
		"        common = dict(run_id='unit', race=False, web=False, report_dir=str(Path(tmp) / 'candidate'))",
		"        assert cli.run_verify(Namespace(tier='candidate', **common)) == 0",
		"        assert calls == ['pre-clean', 'post-clean'], calls",
		"        final = dict(common, report_dir=str(Path(tmp) / 'final'), config='/tmp/config', compaction=False, selection_seed='seed', provider_timeout=7)",
		"        assert cli.run_verify(Namespace(tier='final', **final)) == 0",
		"        assert calls == ['pre-clean', 'post-clean', 'pre-clean', 'post-clean'], calls",
		"finally:",
		"    cli.require_clean_worktree = original_clean",
		"    cli.verification_steps = original_steps",
		"    cli.selection.resolved_path = original_resolved_path",
		"    verification.repository_snapshot = original_snapshot",
		"    verification.environment_fingerprint = original_environment",
		"    verification.find_reusable_candidate = original_find",
		"    verification.write_record = original_write",
		"    cli.provider_record_summary = original_provider_summary",
		"    validation_plan.collect_plan = original_plan",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestEvalRequireCleanWorktreeRejectsDirtyRepository(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"import subprocess",
		"import tempfile",
		"from pathlib import Path",
		"from tests.eval.juex_eval import cli",
		"original_root = cli.REPO_ROOT",
		"try:",
		"    with tempfile.TemporaryDirectory() as tmp:",
		"        repo = Path(tmp)",
		"        subprocess.run(['git', 'init', '--quiet'], cwd=repo, check=True)",
		"        subprocess.run(['git', '-c', 'user.name=Eval', '-c', 'user.email=eval@example.com', 'commit', '--quiet', '--allow-empty', '-m', 'initial'], cwd=repo, check=True)",
		"        subprocess.run(['git', 'config', 'status.showUntrackedFiles', 'no'], cwd=repo, check=True)",
		"        cli.REPO_ROOT = repo",
		"        cli.require_clean_worktree()",
		"        (repo / 'dirty.txt').write_text('dirty\\n', encoding='utf-8')",
		"        try:",
		"            cli.require_clean_worktree()",
		"        except ValueError as exc:",
		"            assert str(exc) == 'candidate and final verification require a clean worktree'",
		"        else:",
		"            raise AssertionError('dirty repository was accepted')",
		"finally:",
		"    cli.REPO_ROOT = original_root",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestEvalCommitVerificationRejectsDirtyBeforePlanningOrPreparation(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"from argparse import Namespace",
		"from tests.eval.juex_eval import cli, validation_plan, verification",
		"calls = []",
		"original_snapshot = verification.repository_snapshot",
		"original_steps = cli.verification_steps",
		"original_environment = cli.go_test_environment",
		"try:",
		"    verification.repository_snapshot = lambda root: (calls.append('snapshot') or verification.RepositorySnapshot('a' * 40, 'feature/test', True))",
		"    cli.verification_steps = lambda args: (_ for _ in ()).throw(AssertionError('planned dirty verification'))",
		"    cli.go_test_environment = lambda: (_ for _ in ()).throw(AssertionError('prepared dirty verification'))",
		"    for tier in ('candidate', 'final'):",
		"        args = Namespace(tier=tier, run_id='dirty', report_dir='', race=False, web=False, compaction=False, config='/tmp/config.yaml', selection_seed='seed', provider_timeout=7)",
		"        try:",
		"            cli.run_verify(args)",
		"        except ValueError as exc:",
		"            assert str(exc) == 'candidate and final verification require a clean worktree', exc",
		"        else:",
		"            raise AssertionError(f'{tier} accepted a dirty worktree')",
		"    assert calls == ['snapshot', 'snapshot'], calls",
		"finally:",
		"    verification.repository_snapshot = original_snapshot",
		"    cli.verification_steps = original_steps",
		"    cli.go_test_environment = original_environment",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestEvalCommitVerificationRecordSchemaAndReuseInvalidation(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"import json",
		"import os",
		"import subprocess",
		"import tempfile",
		"from pathlib import Path",
		"from tests.eval.juex_eval import cli, outcomes, verification",
		"snapshot = verification.RepositorySnapshot('a' * 40, 'feature/test', False)",
		"steps = [cli.VerificationStep('go-test-all', ['go', 'test', './...']), cli.VerificationStep('make-build', ['make', 'build'])]",
		"assert {'GOFLAGS', 'GOWORK', 'GOEXPERIMENT'} <= set(verification.GO_ENV_FINGERPRINT_KEYS), verification.GO_ENV_FINGERPRINT_KEYS",
		"for invalid_run_id in ('.', '..'):",
		"    try:",
		"        verification.validate_run_id(invalid_run_id)",
		"    except ValueError:",
		"        pass",
		"    else:",
		"        raise AssertionError(f'unsafe run ID accepted: {invalid_run_id}')",
		"original_goflags = os.environ.get('GOFLAGS')",
		"try:",
		"    os.environ.pop('GOFLAGS', None)",
		"    base_environment = verification.environment_fingerprint(web=False)",
		"    os.environ['GOFLAGS'] = '-tags=verification_unit'",
		"    assert verification.environment_fingerprint(web=False) != base_environment",
		"finally:",
		"    if original_goflags is None:",
		"        os.environ.pop('GOFLAGS', None)",
		"    else:",
		"        os.environ['GOFLAGS'] = original_goflags",
		"with tempfile.TemporaryDirectory() as tmp:",
		"    root = Path(tmp)",
		"    provider_config = root / 'provider.yaml'",
		"    provider_config.write_text('models: [alpha:one]\\n', encoding='utf-8')",
		"    test_env = os.environ.copy()",
		"    test_env.update({",
		"        'HOME': str(root / 'home'),",
		"        'USERPROFILE': str(root / 'profile'),",
		"        'JUEX_HOME': str(root / 'juex-home'),",
		"        'CODEX_HOME': str(root / 'codex-home'),",
		"        'JUEX_PROVIDER_CONFIG': str(provider_config),",
		"    })",
		"    inherited_environment = verification.environment_fingerprint(web=False, repo_root=root, test_environment=test_env)",
		"    for name in ('HOME', 'USERPROFILE', 'JUEX_HOME', 'CODEX_HOME', 'JUEX_PROVIDER_CONFIG'):",
		"        changed = dict(test_env)",
		"        changed[name] += '-changed'",
		"        assert verification.environment_fingerprint(web=False, repo_root=root, test_environment=changed) != inherited_environment, name",
		"    provider_config.write_text('models: [alpha:two]\\n', encoding='utf-8')",
		"    assert verification.environment_fingerprint(web=False, repo_root=root, test_environment=test_env) != inherited_environment",
		"with tempfile.TemporaryDirectory() as tmp:",
		"    repo = Path(tmp) / 'repo'",
		"    repo.mkdir()",
		"    subprocess.run(['git', 'init', '--quiet'], cwd=repo, check=True)",
		"    subprocess.run(['git', '-c', 'user.name=Eval', '-c', 'user.email=eval@example.com', 'commit', '--quiet', '--allow-empty', '-m', 'initial'], cwd=repo, check=True)",
		"    untagged_environment = verification.environment_fingerprint(web=False, repo_root=repo)",
		"    subprocess.run(['git', 'tag', 'verification-v1'], cwd=repo, check=True)",
		"    assert verification.environment_fingerprint(web=False, repo_root=repo) != untagged_environment",
		"with tempfile.TemporaryDirectory() as tmp:",
		"    first_rg = Path(tmp) / 'rg-first'",
		"    second_rg = Path(tmp) / 'rg-second'",
		"    first_rg.write_bytes(b'first ripgrep')",
		"    second_rg.write_bytes(b'second ripgrep')",
		"    original_which = verification.shutil.which",
		"    try:",
		"        verification.shutil.which = lambda name, path=None: str(first_rg) if name == 'rg' else original_which(name, path=path)",
		"        first_environment = verification.environment_fingerprint(web=False)",
		"        verification.shutil.which = lambda name, path=None: str(second_rg) if name == 'rg' else original_which(name, path=path)",
		"        assert verification.environment_fingerprint(web=False) != first_environment",
		"    finally:",
		"        verification.shutil.which = original_which",
		"plan = verification.plan_fingerprint(steps)",
		"environment = verification.stable_fingerprint({'platform': 'test', 'go': 'go1.test'})",
		"with tempfile.TemporaryDirectory() as tmp:",
		"    reports = Path(tmp) / 'reports'",
		"    report_dir = verification.default_report_dir(reports, snapshot, 'candidate-unit')",
		"    assert report_dir == reports / 'development-validation' / snapshot.head_sha / 'candidate-unit', report_dir",
		"    rows = []",
		"    for index, step in enumerate(steps):",
		"        row = verification.planned_step_record(step)",
		"        row.update({'execution_state': 'executed', 'started_at': f'2026-08-21T00:00:0{index}Z', 'duration': 0.25, 'exit_status': 0, 'log': str(report_dir / 'command-logs' / f'{step.label}.log'), 'attempts': [], **outcomes.success(attempt_count=1).as_dict()})",
		"        rows.append(row)",
		"    record = verification.build_record(",
		"        tier='candidate', run_id='candidate-unit', snapshot=snapshot, plan_fingerprint=plan,",
		"        environment_fingerprint=environment, steps=rows, status='pass',",
		"        reused=[], executed=[step.label for step in steps], invalidated=[],",
		"        provider_summary={'selected_provider_model': 'provider:model', 'redacted_config_hash': 'sha256:redacted', 'api_key': 'must-not-appear'},",
		"    )",
		"    verification.write_record(report_dir, record)",
		"    parsed = json.loads((report_dir / 'record.json').read_text(encoding='utf-8'))",
		"    assert parsed['schema_version'] == verification.SCHEMA_VERSION",
		"    assert parsed['tier'] == 'candidate' and parsed['head_sha'] == snapshot.head_sha and parsed['dirty'] is False",
		"    assert parsed['provider_model_ref'] == 'provider:model' and parsed['redacted_config_hash'] == 'sha256:redacted'",
		"    assert all({'command', 'started_at', 'duration', 'exit_status', 'log'} <= row.keys() for row in parsed['steps']), parsed['steps']",
		"    assert 'must-not-appear' not in (report_dir / 'record.json').read_text(encoding='utf-8')",
		"    assert 'must-not-appear' not in (report_dir / 'record.md').read_text(encoding='utf-8')",
		"    matched = verification.find_reusable_candidate(reports, snapshot, plan, environment, steps)",
		"    assert matched.source == report_dir / 'record.json', matched",
		"    assert set(matched.reusable) == {'go-test-all', 'make-build'} and not matched.invalidated, matched",
		"    changed_sha = verification.find_reusable_candidate(reports, verification.RepositorySnapshot('b' * 40, 'feature/test', False), plan, environment, steps)",
		"    assert not changed_sha.reusable and any(item['reason'] == 'head_sha mismatch' for item in changed_sha.invalidated), changed_sha",
		"    changed_plan = verification.find_reusable_candidate(reports, snapshot, verification.stable_fingerprint({'plan': 'changed'}), environment, steps)",
		"    assert not changed_plan.reusable and any(item['reason'] == 'plan_fingerprint mismatch' for item in changed_plan.invalidated), changed_plan",
		"    changed_environment = verification.find_reusable_candidate(reports, snapshot, plan, verification.stable_fingerprint({'environment': 'changed'}), steps)",
		"    assert not changed_environment.reusable and any(item['reason'] == 'environment_fingerprint mismatch' for item in changed_environment.invalidated), changed_environment",
		"    changed_artifact = verification.find_reusable_candidate(reports, snapshot, plan, environment, steps, {'dist/juex': {'sha256': 'sha256:changed', 'size': 1}})",
		"    assert not changed_artifact.reusable and any(item['reason'] == 'build artifact mismatch' for item in changed_artifact.invalidated), changed_artifact",
		"    preserved = verification.preserve_candidate_record(report_dir)",
		"    assert preserved == report_dir / 'candidate-record.json' and preserved.is_file(), preserved",
		"    assert (report_dir / 'candidate-record.md').is_file() and not (report_dir / 'record.json').exists()",
		"    final_record = dict(record, tier='final', status='fail')",
		"    verification.write_record(report_dir, final_record)",
		"    retry = verification.find_reusable_candidate(reports, snapshot, plan, environment, steps)",
		"    assert retry.source == preserved and set(retry.reusable) == {'go-test-all', 'make-build'}, retry",
		"    refreshed_record = dict(record, started_at='2026-08-21T01:00:00Z', completed_at='2026-08-21T01:00:01Z')",
		"    verification.write_record(report_dir, refreshed_record)",
		"    refreshed_preserved = verification.preserve_candidate_record(report_dir)",
		"    assert refreshed_preserved == preserved",
		"    assert json.loads(preserved.read_text(encoding='utf-8')) == refreshed_record",
		"    final_steps = [*steps, cli.VerificationStep('live-integration', ['make', 'integration']), cli.VerificationStep('provider-model-smoke', ['provider-smoke'])]",
		"    assert [step.label for step in final_steps if step.label not in matched.reusable] == ['live-integration', 'provider-model-smoke']",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestEvalFinalExecutesOnlyLiveStepsWhenCandidateIsReusable(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"import tempfile",
		"from argparse import Namespace",
		"from pathlib import Path",
		"from tests.eval.juex_eval import cli, outcomes, validation_plan, verification",
		"snapshot = verification.RepositorySnapshot('a' * 40, 'feature/test', False)",
		"candidate = cli.candidate_verification_steps(race=False, web=False)",
		"reusable = {step.label: {'execution_state': 'executed', 'started_at': '2026-08-21T00:00:00Z', 'duration': 1.0, 'exit_status': 0, 'log': f'/candidate/{step.label}.log', 'attempts': [], 'initial_outcome': 'passed', **outcomes.success(attempt_count=1).as_dict()} for step in candidate}",
		"calls = []",
		"records = []",
		"original_clean = cli.require_clean_worktree",
		"original_snapshot = verification.repository_snapshot",
		"original_environment = verification.environment_fingerprint",
		"original_find = verification.find_reusable_candidate",
		"original_run = cli.run_recorded_verification_step",
		"original_test_environment = cli.go_test_environment",
		"original_resolved_path = cli.selection.resolved_path",
		"original_provider_summary = cli.provider_record_summary",
		"original_write = verification.write_record",
		"original_plan = validation_plan.collect_plan",
		"try:",
		"    cli.require_clean_worktree = lambda: snapshot",
		"    verification.repository_snapshot = lambda root: snapshot",
		"    verification.environment_fingerprint = lambda **kwargs: 'sha256:environment'",
		"    verification.find_reusable_candidate = lambda *args: verification.ReuseDecision(Path('/candidate/record.json'), reusable, [])",
		"    cli.go_test_environment = lambda: {}",
		"    cli.selection.resolved_path = lambda path: Path(path)",
		"    cli.provider_record_summary = lambda args: {'selected_provider_model': 'provider:model', 'redacted_config_hash': 'sha256:redacted'}",
		"    validation_plan.collect_plan = lambda root, mode, base=None: validation_plan.plan_for_changes(mode, [validation_plan.ChangedFile('M', 'internal/providerreadiness/readiness.go')], base_sha='b' * 40, head_sha=snapshot.head_sha, dirty=False)",
		"    def fake_run(step, log_dir, test_env):",
		"        calls.append(step.label)",
		"        return {'execution_state': 'executed', 'started_at': '2026-08-21T00:00:01Z', 'duration': 2.0, 'exit_status': 0, 'log': str(log_dir / f'{step.label}.log'), 'attempts': [], 'initial_outcome': 'passed', **outcomes.success(attempt_count=1).as_dict()}",
		"    cli.run_recorded_verification_step = fake_run",
		"    verification.write_record = lambda report_dir, record: records.append((report_dir, record))",
		"    with tempfile.TemporaryDirectory() as tmp:",
		"        args = Namespace(tier='final', run_id='final-unit', report_dir=tmp, race=False, web=False, compaction=False, config='/tmp/config.yaml', selection_seed='seed', provider_timeout=7)",
		"        assert cli.run_verify(args) == 0",
		"        assert calls == ['integration-contracts', 'live-integration', 'provider-model-smoke'], calls",
		"        report_dir, record = records[-1]",
		"        assert report_dir == Path(tmp) / 'development-validation' / snapshot.head_sha / 'final-unit', report_dir",
		"        assert record['reused'] == ['web-stub', 'go-test-all', 'make-build'], record",
		"        assert record['executed'] == calls and record['status'] == 'pass', record",
		"        assert [row['execution_state'] for row in record['steps']] == ['reused', 'reused', 'reused', 'executed', 'executed', 'executed'], record['steps']",
		"        assert [row['outcome'] for row in record['steps']] == ['passed'] * 6 and record['blocks_merge'] is False, record",
		"finally:",
		"    cli.require_clean_worktree = original_clean",
		"    verification.repository_snapshot = original_snapshot",
		"    verification.environment_fingerprint = original_environment",
		"    verification.find_reusable_candidate = original_find",
		"    cli.run_recorded_verification_step = original_run",
		"    cli.go_test_environment = original_test_environment",
		"    cli.selection.resolved_path = original_resolved_path",
		"    cli.provider_record_summary = original_provider_summary",
		"    verification.write_record = original_write",
		"    validation_plan.collect_plan = original_plan",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestEvalVerifyFocusedRunsThroughPublicCLI(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	out := runUV(t, root, "python", "-m", "tests.eval.juex_eval", "verify", "focused", "./internal/version")
	if !strings.Contains(out, "ok  go-test-focused") {
		t.Fatalf("focused verification did not report success:\n%s", out)
	}
}

func TestMakeBuildGoDoesNotBuildFrontend(t *testing.T) {
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("make", "-n", "build-go")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make -n build-go failed: %v\n%s", err, out)
	}
	rendered := string(out)
	if !strings.Contains(rendered, "go build") {
		t.Fatalf("build-go does not compile the binary:\n%s", rendered)
	}
	if strings.Contains(rendered, "pnpm") {
		t.Fatalf("build-go rebuilt the frontend:\n%s", rendered)
	}
}

func TestMakeWebStubPreparesMissingAssetsWithoutOverwriting(t *testing.T) {
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	run := func() {
		cmd := exec.Command("make", "--no-print-directory", "-f", filepath.Join(root, "Makefile"), "web-stub")
		cmd.Dir = work
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("make web-stub failed: %v\n%s", err, out)
		}
	}

	run()
	index := filepath.Join(work, "internal", "web", "dist", "index.html")
	data, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "<!doctype html><html><body></body></html>\n"; got != want {
		t.Fatalf("stub index = %q, want %q", got, want)
	}
	if err := os.WriteFile(index, []byte("real frontend\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run()
	data, err = os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "real frontend\n" {
		t.Fatalf("web-stub overwrote existing frontend assets: %q", got)
	}
}

func TestMakeWebCheckBuildsAndSynchronizesFrontendOnce(t *testing.T) {
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("make", "-n", "web-check")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make -n web-check failed: %v\n%s", err, out)
	}
	rendered := string(out)
	if count := strings.Count(rendered, "pnpm build"); count != 1 {
		t.Fatalf("web-check pnpm build count = %d, want 1:\n%s", count, rendered)
	}
	if !strings.Contains(rendered, "cp -R frontend/dist/. internal/web/dist/") {
		t.Fatalf("web-check did not synchronize the built embed assets:\n%s", rendered)
	}
}

func TestMakeVerificationTargetsAreThinPythonAdapters(t *testing.T) {
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		args []string
		want string
	}{
		{name: "plan", args: []string{"-n", "verify-plan", "TIER=final", "BASE=abc123", "EXPLAIN=1"}, want: "plan --tier final --base abc123 --explain"},
		{name: "planned-focused", args: []string{"-n", "verify-focused", "PLANNED=1"}, want: "verify focused --planned"},
		{name: "focused", args: []string{"-n", "verify-focused", "PKGS=./internal/app ./internal/runtime"}, want: "verify focused ./internal/app ./internal/runtime"},
		{name: "candidate", args: []string{"-n", "verify-candidate", "RACE=1", "WEB=1"}, want: "verify candidate --race --web"},
		{name: "final", args: []string{"-n", "verify-final", "RACE=1", "WEB=1", "COMPACTION=1"}, want: "verify final --race --web --compaction"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("make", tc.args...)
			cmd.Dir = root
			cmd.Env = commandEnv(nil, "BASE", "COMPACTION", "EXPLAIN", "MAKEFLAGS", "MAKELEVEL", "MFLAGS", "PKGS", "PLANNED", "RACE", "TIER", "WEB")
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("make %v failed: %v\n%s", tc.args, err, out)
			}
			rendered := string(out)
			if !strings.Contains(rendered, tc.want) {
				t.Fatalf("make output missing %q:\n%s", tc.want, rendered)
			}
			if strings.Contains(rendered, "go test") {
				t.Fatalf("Make target contains orchestration instead of a thin CLI adapter:\n%s", rendered)
			}
		})
	}
}

func TestMakeVerifyFocusedRejectsUnscopedDefault(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("make", "verify-focused", "PKGS=")
	cmd.Dir = root
	cmd.Env = commandEnv(nil, "MAKEFLAGS", "MAKELEVEL", "MFLAGS", "PLANNED")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("unscoped focused verification succeeded:\n%s", out)
	}
	if !strings.Contains(string(out), "packages or --planned") {
		t.Fatalf("unscoped focused verification did not explain opt-in:\n%s", out)
	}
}

func TestEvalDevelopmentGoTestsRunDirectly(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"import json",
		"from argparse import Namespace",
		"from pathlib import Path",
		"from tests.eval.juex_eval import cli",
		"args = Namespace(",
		"    skip_tests=False,",
		"    no_provider_smoke=True,",
		"    compaction_eval=False,",
		"    run_id='unit',",
		"    config='/tmp/provider.yaml',",
		"    selection_seed='repeatable',",
		"    provider_timeout=7,",
		"    provider_only='',",
		"    provider_all_models=False,",
		"    compaction_all_models=False,",
		"    compaction_only=[],",
		")",
		"steps, _, _ = cli.development_steps(args, Path('reports'))",
		"print(json.dumps([{'label': step.label, 'command': step.command} for step in steps]))",
	}, "\n")
	out := runUV(t, root, "python", "-c", program)

	var steps []struct {
		Label   string   `json:"label"`
		Command []string `json:"command"`
	}
	if err := json.Unmarshal([]byte(out), &steps); err != nil {
		t.Fatalf("decode steps: %v\n%s", err, out)
	}
	command := findEvalCommand(t, steps, "go-test-all")
	if !reflect.DeepEqual(command, []string{"go", "test", "./...", "-count=1"}) {
		t.Fatalf("go-test-all command = %q, want direct Go invocation", command)
	}
	for _, step := range steps {
		if step.Label == "go-test-e2e" {
			t.Fatalf("development eval retained duplicate e2e step: %+v", steps)
		}
	}
}

func TestMakeTargetsRunGoTestsDirectly(t *testing.T) {
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	makePath, err := exec.LookPath("make")
	if err != nil {
		t.Skip("make is required")
	}

	for _, tt := range []struct {
		target string
		want   string
	}{
		{target: "test", want: `PATH="$(scripts/ensure-ripgrep.sh):$PATH" go test ./...`},
		{target: "race", want: `PATH="$(scripts/ensure-ripgrep.sh):$PATH" go test ./... -race -count=1`},
		{target: "integration-contracts", want: `PATH="$(scripts/ensure-ripgrep.sh):$PATH" go test -tags=integration ./tests/e2e/... -skip '^TestLiveConfigs_' -count=1 -v`},
		{target: "integration-live", want: `PATH="$(scripts/ensure-ripgrep.sh):$PATH" go test -tags=integration ./tests/e2e/... -run '^TestLiveConfigs_' -count=1 -v`},
	} {
		t.Run(tt.target, func(t *testing.T) {
			cmd := exec.Command(makePath, "-n", tt.target)
			cmd.Dir = root
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("make -n %s failed: %v\n%s", tt.target, err, out)
			}
			if got := strings.TrimSpace(string(out)); got != tt.want {
				t.Fatalf("make -n %s = %q, want %q", tt.target, got, tt.want)
			}
		})
	}
}

func TestEnsureRipgrepRedirectsGoTelemetry(t *testing.T) {
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not found; skipping shell provisioning test")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	fakeRoot := t.TempDir()
	scriptDir := filepath.Join(fakeRoot, "scripts")
	binDir := filepath.Join(fakeRoot, "bin")
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ensureBody, err := os.ReadFile(filepath.Join(root, "scripts", "ensure-ripgrep.sh"))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		filepath.Join(scriptDir, "ensure-ripgrep.sh"): string(ensureBody),
		filepath.Join(scriptDir, "prepare-ripgrep.sh"): strings.Join([]string{
			"#!/bin/sh",
			`while test "$#" -gt 0; do`,
			`  if test "$1" = --output; then shift; output="$1"; fi`,
			`  shift`,
			`done`,
			`mkdir -p "$output/juex-path"`,
			`: >"$output/juex-path/rg"`,
			`chmod +x "$output/juex-path/rg"`,
		}, "\n") + "\n",
		filepath.Join(binDir, "go"): strings.Join([]string{
			"#!/bin/sh",
			`case "$TEST_TELEMETRY_DIR" in *juex-ripgrep-telemetry.*) ;; *) echo "unsafe telemetry: $TEST_TELEMETRY_DIR" >&2; exit 91;; esac`,
			`printf '%s\n' "$TEST_TELEMETRY_DIR" >>"$FAKE_GO_LOG"`,
			`case "$2" in GOOS) printf 'linux\n' ;; GOARCH) printf 'amd64\n' ;; *) exit 92 ;; esac`,
		}, "\n") + "\n",
	}
	for _, name := range []string{"dirname", "mktemp", "rm", "mkdir", "chmod"} {
		toolPath, err := exec.LookPath(name)
		if err != nil {
			t.Skipf("%s not found; skipping shell provisioning test", name)
		}
		files[filepath.Join(binDir, name)] = "#!/bin/sh\nexec \"" + filepath.ToSlash(toolPath) + "\" \"$@\"\n"
	}
	for path, body := range files {
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	telemetryParent := filepath.Join(fakeRoot, "tmp")
	if err := os.MkdirAll(telemetryParent, 0o755); err != nil {
		t.Fatal(err)
	}
	goLog := filepath.Join(fakeRoot, "go.log")
	cmd := exec.Command(bashPath, filepath.Join(scriptDir, "ensure-ripgrep.sh"))
	cmd.Env = commandEnv(map[string]string{
		"PATH":               binDir,
		"TMPDIR":             telemetryParent,
		"TEST_TELEMETRY_DIR": filepath.Join(fakeRoot, "real-go-telemetry"),
		"FAKE_GO_LOG":        goLog,
	})
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("provision ripgrep with isolated Go telemetry: %v\n%s", err, out)
	}
	gotPath := strings.ReplaceAll(strings.TrimSpace(string(out)), `\`, "/")
	if !strings.HasSuffix(gotPath, "/.tmp/dev-ripgrep/juex-path") {
		t.Fatalf("ensure-ripgrep output = %q", out)
	}
	if _, err := os.Stat(filepath.Join(fakeRoot, ".tmp", "dev-ripgrep", "juex-path", "rg")); err != nil {
		t.Fatalf("ensure-ripgrep did not create cached executable: %v", err)
	}
	logged, err := os.ReadFile(goLog)
	if err != nil {
		t.Fatal(err)
	}
	for _, telemetryDir := range strings.Fields(string(logged)) {
		if telemetryDir == filepath.Join(fakeRoot, "real-go-telemetry") {
			t.Fatalf("go env inherited real telemetry directory: %s", telemetryDir)
		}
		if _, err := os.Stat(telemetryDir); !os.IsNotExist(err) {
			t.Fatalf("temporary Go telemetry directory still exists: %s: %v", telemetryDir, err)
		}
	}
}

func commandEnv(overrides map[string]string, unset ...string) []string {
	excluded := make(map[string]struct{}, len(overrides)+len(unset))
	for key := range overrides {
		excluded[key] = struct{}{}
	}
	for _, key := range unset {
		excluded[key] = struct{}{}
	}
	env := make([]string, 0, len(os.Environ())+len(overrides))
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if _, skip := excluded[key]; !skip {
			env = append(env, item)
		}
	}
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}
	return env
}

func TestEvalHelpersTolerateProgrammaticNone(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"from argparse import Namespace",
		"from tests.eval.juex_eval import cli, compaction",
		"command = []",
		"cli.append_repeated(command, '--only', None)",
		"assert command == []",
		"args = Namespace(",
		"    only=None,",
		"    all_models=False,",
		"    context_window=32000,",
		"    juex='/no/such/juex',",
		"    config='/no/such/config',",
		"    out_root='',",
		"    keep_workdir=False,",
		")",
		"try:",
		"    compaction.run(args)",
		"except ValueError as exc:",
		"    assert 'Missing executable' in str(exc)",
		"else:",
		"    raise AssertionError('expected missing executable error')",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestEvalDefaultReportDirsUseTmpRoot(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"from tests.eval.juex_eval import helper",
		"for kind in ['provider-model-smoke', 'development-validation', 'compaction-eval']:",
		"    path = helper.default_report_dir(kind, 'run-id').as_posix()",
		"    assert path.endswith(f'/.tmp/reports/{kind}/run-id'), path",
		"    assert '/docs/reports/' not in path, path",
		"for bad_run_id in ['', ' ', '../run', 'nested/run', r'nested\\run']:",
		"    try:",
		"        helper.default_report_dir('provider-model-smoke', bad_run_id)",
		"    except ValueError:",
		"        pass",
		"    else:",
		"        raise AssertionError(f'expected invalid run_id: {bad_run_id!r}')",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestEvalHelpersResolveAgentHomeSessions(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"import json",
		"import tempfile",
		"from pathlib import Path",
		"from tests.eval.juex_eval import compaction, helper",
		"with tempfile.TemporaryDirectory() as tmp:",
		"    work = Path(tmp) / 'work'",
		"    juex_home = work / 'home' / '.juex'",
		"    marker = work / '.juex' / 'juex.local.json'",
		"    marker.parent.mkdir(parents=True)",
		"    marker.write_text(json.dumps({'agent_id': 'abcd23'}), encoding='utf-8')",
		"    expected = juex_home / 'agents' / 'abcd23' / 'sessions'",
		"    assert helper.agent_sessions_dir(work, juex_home) == expected",
		"    assert compaction.session_root(work) == expected",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestCompactionEvalScoresAuthoritativeGoalAndNotes(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"import json",
		"import tempfile",
		"from pathlib import Path",
		"from tests.eval.juex_eval import compaction",
		"with tempfile.TemporaryDirectory() as tmp:",
		"    work = Path(tmp)",
		"    marker = work / '.juex' / 'juex.local.json'",
		"    marker.parent.mkdir(parents=True)",
		"    marker.write_text(json.dumps({'agent_id': 'abcd23'}), encoding='utf-8')",
		"    session = work / 'home' / '.juex' / 'agents' / 'abcd23' / 'sessions' / 'session-1'",
		"    session.mkdir(parents=True)",
		"    (session / 'goal_state.json').write_text(json.dumps(compaction.AUTHORITATIVE_GOAL), encoding='utf-8')",
		"    (session / 'notes.md').write_text(compaction.AUTHORITATIVE_NOTES, encoding='utf-8')",
		"    goal = compaction.AUTHORITATIVE_GOAL",
		"    summary = '\\n'.join([",
		"        'Goal',",
		"        f\"description: {goal['description']}\",",
		"        f\"acceptance: {goal['acceptance']}\",",
		"        f\"status: {goal['status']}\",",
		"        'Critical Context', 'facts', 'Constraints & Preferences', 'none',",
		"        'Progress', 'mapped', 'Key Decisions', 'preserve state', 'Next Steps',",
		"        compaction.AUTHORITATIVE_OPEN_NOTE.rstrip('.'), 'Relevant Files', 'notes.md', 'Tool Failures', 'none',",
		"    ])",
		"    message = {'kind': 'compact', 'blocks': [{'type': 'text', 'text': summary}]}",
		"    (session / 'conversation.jsonl').write_text(json.dumps(message) + '\\n', encoding='utf-8')",
		"    answer = compaction.AUTHORITATIVE_COMPLETED_NOTE.rstrip('.') + '\\n' + compaction.AUTHORITATIVE_OPEN_NOTE.rstrip('.')",
		"    result = compaction.score_authoritative_state(work, answer)",
		"    assert result['score'] == 30, result",
		"    assert all(result['checks'].values()), result",
		"    bad = compaction.score_authoritative_state(work, answer.replace('scorecard', 'report'))",
		"    assert not bad['checks']['notes_recited_after_compaction'], bad",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestEvalWriteSelectedConfigUsesColonModelRef(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"import tempfile",
		"from pathlib import Path",
		"from tests.eval.juex_eval import helper",
		"cfg = {'providers': [{'id': 'openrouter', 'models': [{'id': 'meta-llama/llama-3'}]}]}",
		"with tempfile.TemporaryDirectory() as tmp:",
		"    out = Path(tmp) / 'juex.yaml'",
		"    helper.write_selected_config(cfg, 'openrouter', 'meta-llama/llama-3', out)",
		"    text = out.read_text(encoding='utf-8')",
		"    assert 'openrouter:meta-llama/llama-3' in text and 'models:' in text, text",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestEvalCompactionModelRefParserTrimsWhitespace(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"from tests.eval.juex_eval import compaction",
		"model, provider, model_id = compaction.parse_model_ref('  openrouter : meta-llama/llama-3  ')",
		"assert model == 'openrouter:meta-llama/llama-3', model",
		"assert provider == 'openrouter', provider",
		"assert model_id == 'meta-llama/llama-3', model_id",
		"for bad in ['openrouter/meta', ' : model', 'provider: ']:",
		"    try:",
		"        compaction.parse_model_ref(bad)",
		"    except ValueError:",
		"        pass",
		"    else:",
		"        raise AssertionError(f'expected invalid model ref: {bad!r}')",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestEvalAgentSmokeToolEventContract(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"import json",
		"import tempfile",
		"from pathlib import Path",
		"from tests.eval.juex_eval import contract_oracle, helper",
		"token = 'contract-token'",
		"with tempfile.TemporaryDirectory() as tmp:",
		"    conversation = Path(tmp) / 'conversation.jsonl'",
		"    events = Path(tmp) / 'events.jsonl'",
		"    conv_rows = [",
		"        {'role': 'assistant', 'blocks': [",
		"            {'type': 'tool_use', 'tool_use_id': 'read_1', 'tool_name': 'read'},",
		"            {'type': 'tool_use', 'tool_use_id': 'write_1', 'tool_name': 'write'},",
		"            {'type': 'tool_use', 'tool_use_id': 'edit_1', 'tool_name': 'edit'},",
		"            {'type': 'tool_use', 'tool_use_id': 'grep_1', 'tool_name': 'grep'},",
		"            {'type': 'tool_use', 'tool_use_id': 'call_1', 'tool_name': 'exec_command', 'input': {'tty': True}},",
		"            {'type': 'tool_use', 'tool_use_id': 'call_2', 'tool_name': 'write_stdin'},",
		"        ]},",
		"        {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'call_1', 'content': f'TTY-DONE {token}\\nProcess exited with code 0'}]},",
		"    ]",
		"    conversation.write_text('\\n'.join(json.dumps(row) for row in conv_rows) + '\\n', encoding='utf-8')",
		"    rows = [",
		"        {'type': 'tool.completed', 'payload': {'name': 'exec_command', 'tool_use_id': 'call_1', 'timeout_seconds': 5, 'len': 42, 'outcome': {'block': {'type': 'tool_result', 'content': 'INSTALL 10%\\r\\nPROMPT approve install?'}}, 'result': {'session_id': 3, 'running': True, 'chunk_id': 2, 'original_bytes': 42, 'original_token_count': 11}}},",
		"        {'type': 'tool.completed', 'payload': {'name': 'write_stdin', 'tool_use_id': 'call_2', 'timeout_seconds': 5, 'len': 18, 'outcome': {'block': {'type': 'tool_result', 'content': f'TTY-DONE {token}'}}, 'result': {'running': False, 'exit_code': 0, 'chunk_id': 5, 'original_bytes': 18, 'original_token_count': 5}}},",
		"    ]",
		"    events.write_text('\\n'.join(json.dumps(row) for row in rows) + '\\n', encoding='utf-8')",
		"    report = contract_oracle.validate_agent_smoke_contract(conversation, events, token)",
		"    assert report.passed, report.message()",
		"    ok, msg = contract_oracle.events_have_agent_smoke_terminal_results(events, token)",
		"    assert ok, msg",
		"    broken = Path(tmp) / 'broken-events.jsonl'",
		"    broken_rows = [dict(row) for row in rows]",
		"    broken_rows[-1] = {'type': 'tool.completed', 'payload': {'name': 'write_stdin', 'tool_use_id': 'call_2', 'content': '', 'result': {'running': False, 'exit_code': 0}}}",
		"    broken.write_text('\\n'.join(json.dumps(row) for row in broken_rows) + '\\n', encoding='utf-8')",
		"    ok, msg = helper.events_have_agent_smoke_terminal_results(broken, token)",
		"    assert not ok and 'TTY-DONE token' in msg, msg",
		"    broken_rows = [dict(row) for row in rows]",
		"    broken_rows[-1] = {'type': 'tool.completed', 'payload': {'name': 'write_stdin', 'tool_use_id': 'call_2'}}",
		"    broken.write_text('\\n'.join(json.dumps(row) for row in broken_rows) + '\\n', encoding='utf-8')",
		"    ok, msg = helper.events_have_agent_smoke_terminal_results(broken, token)",
		"    assert not ok and 'structured write_stdin result' in msg, msg",
		"    broken_rows = [dict(row) for row in rows]",
		"    broken_rows[-2] = {'type': 'tool.completed', 'payload': {'name': 'exec_command', 'tool_use_id': 'call_1', 'content': 'INSTALL 10%\\r\\nPROMPT approve install?', 'result': {'running': True, 'session_id': True}}}",
		"    broken.write_text('\\n'.join(json.dumps(row) for row in broken_rows) + '\\n', encoding='utf-8')",
		"    ok, msg = contract_oracle.events_have_agent_smoke_terminal_results(broken, token)",
		"    assert not ok and 'structured exec_command running result' in msg, msg",
		"    broken_rows = [dict(row) for row in rows]",
		"    broken_rows[-1] = {'type': 'tool.completed', 'payload': {'name': 'write_stdin', 'tool_use_id': 'call_2', 'content': f'TTY-DONE {token}', 'result': {'running': False, 'exit_code': False}}}",
		"    broken.write_text('\\n'.join(json.dumps(row) for row in broken_rows) + '\\n', encoding='utf-8')",
		"    ok, msg = contract_oracle.events_have_agent_smoke_terminal_results(broken, token)",
		"    assert not ok and 'structured write_stdin result' in msg, msg",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestScheduleRoutingEvalContract(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"import copy",
		"import json",
		"import tempfile",
		"from pathlib import Path",
		"from tests.eval.juex_eval import schedule_routing",
		"expect = schedule_routing.ScheduleRoutingExpectation(",
		"    schedule_id='schedule-routing-eval',",
		"    every_seconds=21600,",
		"    content='schedule routing token 6h',",
		"    completion_token='SCHEDULE_ROUTING_PASS token-6h',",
		")",
		"prompt = schedule_routing.build_prompt(expect)",
		"assert 'schedule_create' not in prompt, prompt",
		"assert 'observable_create' not in prompt, prompt",
		"assert 'juex-observables' not in prompt, prompt",
		"assert 'Do not run commands' not in prompt, prompt",
		"assert 'shell polling' in prompt, prompt",
		"assert 'schedule-routing-eval' in prompt and 'schedule routing token 6h' in prompt, prompt",
		"def rows():",
		"    return [",
		"        {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'list-1', 'tool_name': 'observable_list', 'input': {}}]},",
		"        {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'list-1', 'tool_name': 'observable_list', 'content': '{\"observables\": []}'}]},",
		"        {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'create-1', 'tool_name': 'schedule_create', 'input': {'id': expect.schedule_id, 'interval': {'every_seconds': expect.every_seconds}, 'observation': {'content': expect.content}}}]},",
		"        {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'create-1', 'tool_name': 'schedule_create', 'content': '{\"id\": \"schedule-routing-eval\"}'}]},",
		"        {'role': 'assistant', 'blocks': [{'type': 'text', 'text': expect.completion_token}]},",
		"    ]",
		"def config():",
		"    return {'observables': [{'id': expect.schedule_id, 'type': 'schedule', 'schedule_config': {'interval': {'every_seconds': expect.every_seconds}, 'observation': {'content': expect.content}}}]}",
		"def validate(work, conv_rows=None, cfg=None, raw_conversation=None, raw_config=None):",
		"    conversation = work / 'conversation.jsonl'",
		"    observables = work / 'observables.json'",
		"    if raw_conversation is None:",
		"        conversation.write_text('\\n'.join(json.dumps(row) for row in (rows() if conv_rows is None else conv_rows)) + '\\n', encoding='utf-8')",
		"    else:",
		"        conversation.write_text(raw_conversation, encoding='utf-8')",
		"    if raw_config is None:",
		"        observables.write_text(json.dumps(config() if cfg is None else cfg), encoding='utf-8')",
		"    else:",
		"        observables.write_text(raw_config, encoding='utf-8')",
		"    return schedule_routing.validate_contract(conversation, observables, expect)",
		"def reject(report, needle):",
		"    assert not report.passed, report",
		"    assert needle in report.message(), report.message()",
		"with tempfile.TemporaryDirectory() as tmp:",
		"    work = Path(tmp)",
		"    report = validate(work)",
		"    assert report.passed, report.message()",
		"    broken = rows()",
		"    broken[0], broken[2] = broken[2], broken[0]",
		"    reject(validate(work, broken), 'before')",
		"    broken = rows()",
		"    broken[0] = {'role': 'assistant', 'blocks': [broken[0]['blocks'][0], broken[2]['blocks'][0]]}",
		"    del broken[2]",
		"    reject(validate(work, broken), 'same assistant message')",
		"    broken = rows()",
		"    broken[0]['blocks'].insert(0, {'type': 'text', 'text': expect.completion_token})",
		"    del broken[-1]",
		"    reject(validate(work, broken), 'after successful schedule_create')",
		"    for index, label in [(1, 'observable_list'), (3, 'schedule_create')]:",
		"        broken = rows()",
		"        broken[index]['blocks'][0]['is_error'] = True",
		"        reject(validate(work, broken), label)",
		"        broken = rows()",
		"        del broken[index]",
		"        reject(validate(work, broken), label)",
		"    parallel = rows()",
		"    parallel[0]['blocks'].insert(0, {'type': 'tool_use', 'tool_use_id': 'skill-1', 'tool_name': 'skill_load', 'input': {'name': 'juex-observables'}})",
		"    parallel[1]['blocks'].append({'type': 'tool_result', 'tool_use_id': 'skill-1', 'tool_name': 'skill_load', 'content': 'guide unavailable', 'is_error': True})",
		"    report = validate(work, parallel)",
		"    assert report.passed, report.message()",
		"    late = rows()",
		"    late.insert(4, {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'skill-2', 'tool_name': 'skill_load', 'input': {'name': 'juex-observables'}}]})",
		"    late.insert(5, {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'skill-2', 'tool_name': 'skill_load', 'content': '# JueX Observables'}]})",
		"    report = validate(work, late)",
		"    assert report.passed, report.message()",
		"    inspected = rows()",
		"    inspected.insert(2, {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'inspect-1', 'tool_name': 'exec_command', 'input': {'cmd': 'ls .juex'}}]})",
		"    inspected.insert(3, {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'inspect-1', 'tool_name': 'exec_command', 'content': 'observables.json'}]})",
		"    report = validate(work, inspected)",
		"    assert report.passed, report.message()",
		"    for command in ('systemd-run --help', 'systemd-run --help >/dev/null', 'systemd-run --version', '(systemd-run --help)', '( systemd-run --help )', 'command -v systemd-run', '>/tmp/log', '2>/dev/null', \"printf '%s\\\\n' systemd-run\", \"bash -c \\\"printf '%s\\\\n' systemd-run\\\"\", \"printf '%s\\\\n' 'while true; sleep 21600 &'\", \"printf '%s' 'line one\\nwatch echo tick'\", \"printf '%s' '$(watch echo tick)'\", \"printf '%s' '{ watch echo tick; }'\", \"printf '%s' '(watch echo tick)'\", '(( watch + 1 )) || true', 'f(){ watch echo tick; }', \"function f { crontab jobs.txt; }\", \"eval 'printf %s watch'\", \"eval 'systemd-run --help'\"):",
		"        inspected_scheduler = rows()",
		"        inspected_scheduler.insert(2, {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'inspect-scheduler-1', 'tool_name': 'exec_command', 'input': {'cmd': command}}]})",
		"        inspected_scheduler.insert(3, {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'inspect-scheduler-1', 'tool_name': 'exec_command', 'content': 'inspection'}]})",
		"        report = validate(work, inspected_scheduler)",
		"        assert report.passed, report.message()",
		"    for command in ('crontab -l', '(crontab -l)', '( crontab -l )', 'crontab -u root -l', 'crontab -l 2>/dev/null || true', 'crontab -l >/tmp/current-cron', 'crontab -l 2>&1'):",
		"        inspected_crontab = rows()",
		"        inspected_crontab.insert(2, {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'inspect-crontab-1', 'tool_name': 'exec_command', 'input': {'cmd': command}}]})",
		"        inspected_crontab.insert(3, {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'inspect-crontab-1', 'tool_name': 'exec_command', 'content': 'no crontab'}]})",
		"        report = validate(work, inspected_crontab)",
		"        assert report.passed, report.message()",
		"    interactive_inspection = rows()",
		"    interactive_inspection.insert(2, {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'inspect-stdin-1', 'tool_name': 'write_stdin', 'input': {'session_id': 42, 'chars': 'pwd\\n'}}]})",
		"    interactive_inspection.insert(3, {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'inspect-stdin-1', 'tool_name': 'write_stdin', 'content': '/tmp'}]})",
		"    report = validate(work, interactive_inspection)",
		"    assert report.passed, report.message()",
		"    for command in ('while true; do echo tick; sleep 21600; done &', 'while sleep 21600; do echo tick; done &', 'until env sleep 21600; do echo tick; done &', \"while bash -c 'sleep 21600'; do echo tick; done &\", 'select item in tick; do sleep 21600; done &', \"eval 'while sleep 21600; do echo tick; done &'\"):",
		"        polling = rows()",
		"        polling.insert(2, {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'poll-1', 'tool_name': 'exec_command', 'input': {'cmd': command}}]})",
		"        polling.insert(3, {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'poll-1', 'tool_name': 'exec_command', 'content': 'started'}]})",
		"        reject(validate(work, polling), 'shell scheduling command')",
		"    for command in ('crontab jobs.txt', 'crontab jobs.txt >/dev/null', 'crontab -e', 'crontab -r', \"bash -c 'crontab jobs.txt'\", \"bash -c 'exec crontab jobs.txt'\", 'exec crontab jobs.txt', 'command exec crontab jobs.txt', 'builtin exec crontab jobs.txt', 'echo ok\\ncrontab jobs.txt', \"eval 'crontab jobs.txt'\", 'echo $(crontab jobs.txt)', '{ crontab jobs.txt; }', '! { crontab jobs.txt; }', 'time { crontab jobs.txt; }', '(crontab jobs.txt)', '( crontab jobs.txt )', '2>/dev/null crontab jobs.txt', 'if true; then crontab jobs.txt; fi', 'case x in x) crontab jobs.txt;; esac', 'function f { crontab jobs.txt; }; f'):",
		"        cron_scheduling = rows()",
		"        cron_scheduling.insert(2, {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'cron-1', 'tool_name': 'exec_command', 'input': {'cmd': command}}]})",
		"        cron_scheduling.insert(3, {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'cron-1', 'tool_name': 'exec_command', 'content': 'configured'}]})",
		"        reject(validate(work, cron_scheduling), 'shell scheduling command')",
		"    for command in ('systemd-run --on-active=6h echo tick', 'systemd-run echo --help', 'env systemd-run --on-active=6h echo tick', \"bash -c 'systemd-run --on-active=6h echo tick'\", 'echo ok\\nsystemd-run --on-active=6h echo tick'):",
		"        managed_scheduling = rows()",
		"        managed_scheduling.insert(2, {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'systemd-run-1', 'tool_name': 'exec_command', 'input': {'cmd': command}}]})",
		"        managed_scheduling.insert(3, {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'systemd-run-1', 'tool_name': 'exec_command', 'content': 'started'}]})",
		"        reject(validate(work, managed_scheduling), 'shell scheduling command')",
		"    for command in ('nohup sleep 21600 &', 'nohup sleep 21600s &', 'nohup sleep 360m >/dev/null &', 'setsid sleep 6h', 'setsid sleep 360m', 'setsid sleep 0.25d'):",
		"        detached_sleep = rows()",
		"        detached_sleep.insert(2, {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'detached-sleep-1', 'tool_name': 'exec_command', 'input': {'cmd': command}}]})",
		"        detached_sleep.insert(3, {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'detached-sleep-1', 'tool_name': 'exec_command', 'content': 'started'}]})",
		"        reject(validate(work, detached_sleep), 'shell scheduling command')",
		"    for command in ('watch echo tick', '/usr/bin/watch -n21600 echo tick', 'sudo -u root watch --interval 21600 echo tick', 'env FOO=bar watch --interval=21600 echo tick', 'env -u FOO watch echo tick', \"env -S 'watch echo tick'\", 'command -p watch echo tick', 'exec watch echo tick', 'exec -a poll watch echo tick', 'exec -cl watch echo tick', 'command exec watch echo tick', 'builtin exec watch echo tick', \"bash -c 'watch --interval 21600 echo tick'\", \"bash -c 'exec watch echo tick'\", \"bash -o pipefail -c 'watch echo tick'\", \"sh -lc 'watch echo tick'\", 'echo ok\\nwatch echo tick', \"bash -c 'echo ok\\nwatch echo tick'\", \"eval 'watch echo tick'\", \"eval \\\"eval 'watch echo tick'\\\"\", 'echo $(watch echo tick)', \"bash -c 'echo $(watch echo tick)'\", 'echo `watch echo tick`', 'cat <(watch echo tick)', '{ watch echo tick; }', '! { watch echo tick; }', 'time { watch echo tick; }', '(watch echo tick)', '( watch echo tick )', '>/tmp/log watch echo tick', \"bash -c '{ watch echo tick; }'\", 'if true; then watch echo tick; fi', 'case x in x) watch echo tick;; esac', 'f(){ watch echo tick; }; f', 'f(){ echo ready; watch echo tick; }; f', \"bash -c 'f(){ watch echo tick; }; f'\"):",
		"        watch_polling = rows()",
		"        watch_polling.insert(2, {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'watch-1', 'tool_name': 'exec_command', 'input': {'cmd': command}}]})",
		"        watch_polling.insert(3, {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'watch-1', 'tool_name': 'exec_command', 'content': 'started'}]})",
		"        reject(validate(work, watch_polling), 'shell scheduling command')",
		"    mentioned_watch = rows()",
		"    mentioned_watch.insert(2, {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'mention-watch-1', 'tool_name': 'exec_command', 'input': {'cmd': \"printf '%s\\\\n' 'watch --interval 21600 is forbidden'\"}}]})",
		"    mentioned_watch.insert(3, {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'mention-watch-1', 'tool_name': 'exec_command', 'content': 'watch --interval 21600 is forbidden'}]})",
		"    report = validate(work, mentioned_watch)",
		"    assert report.passed, report.message()",
		"    shell_mentioned_watch = rows()",
		"    shell_mentioned_watch.insert(2, {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'shell-mention-watch-1', 'tool_name': 'exec_command', 'input': {'cmd': \"bash -c \\\"printf '%s\\\\n' 'watch --interval 21600 is forbidden'\\\"\"}}]})",
		"    shell_mentioned_watch.insert(3, {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'shell-mention-watch-1', 'tool_name': 'exec_command', 'content': 'watch --interval 21600 is forbidden'}]})",
		"    report = validate(work, shell_mentioned_watch)",
		"    assert report.passed, report.message()",
		"    env_mentioned_watch = rows()",
		"    env_mentioned_watch.insert(2, {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'env-mention-watch-1', 'tool_name': 'exec_command', 'input': {'cmd': 'env printf %s watch'}}]})",
		"    env_mentioned_watch.insert(3, {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'env-mention-watch-1', 'tool_name': 'exec_command', 'content': 'watch'}]})",
		"    report = validate(work, env_mentioned_watch)",
		"    assert report.passed, report.message()",
		"    for command in ('command -v watch', 'command -V watch'):",
		"        inspected_watch = rows()",
		"        inspected_watch.insert(2, {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'inspect-watch-1', 'tool_name': 'exec_command', 'input': {'cmd': command}}]})",
		"        inspected_watch.insert(3, {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'inspect-watch-1', 'tool_name': 'exec_command', 'content': '/usr/bin/watch'}]})",
		"        report = validate(work, inspected_watch)",
		"        assert report.passed, report.message()",
		"    env_split_mentioned_watch = rows()",
		"    env_split_mentioned_watch.insert(2, {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'env-split-mention-watch-1', 'tool_name': 'exec_command', 'input': {'cmd': \"env -S 'printf %s watch'\"}}]})",
		"    env_split_mentioned_watch.insert(3, {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'env-split-mention-watch-1', 'tool_name': 'exec_command', 'content': 'watch'}]})",
		"    report = validate(work, env_split_mentioned_watch)",
		"    assert report.passed, report.message()",
		"    interactive_polling = rows()",
		"    interactive_polling.insert(2, {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'shell-1', 'tool_name': 'exec_command', 'input': {'cmd': 'bash', 'tty': True}}]})",
		"    interactive_polling.insert(3, {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'shell-1', 'tool_name': 'exec_command', 'content': 'running', 'is_error': False}]})",
		"    interactive_polling.insert(4, {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'poll-stdin-1', 'tool_name': 'write_stdin', 'input': {'session_id': 42, 'chars': 'while true; do echo tick; sleep 21600; done &\\n'}}]})",
		"    interactive_polling.insert(5, {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'poll-stdin-1', 'tool_name': 'write_stdin', 'content': 'started'}]})",
		"    reject(validate(work, interactive_polling), 'shell scheduling command')",
		"    eval_polling = rows()",
		"    eval_polling.insert(2, {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'eval-stdin-1', 'tool_name': 'write_stdin', 'input': {'session_id': 42, 'chars': \"eval 'watch echo tick'\\n\"}}]})",
		"    eval_polling.insert(3, {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'eval-stdin-1', 'tool_name': 'write_stdin', 'content': 'started'}]})",
		"    reject(validate(work, eval_polling), 'shell scheduling command')",
		"    verified = rows()",
		"    verified.insert(4, {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'list-2', 'tool_name': 'observable_list', 'input': {}}]})",
		"    verified.insert(5, {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'list-2', 'tool_name': 'observable_list', 'content': '{\"observables\": [{\"id\": \"schedule-routing-eval\"}]}'}]})",
		"    report = validate(work, verified)",
		"    assert report.passed, report.message()",
		"    retried = rows()",
		"    retried.insert(2, {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'create-bad', 'tool_name': 'schedule_create', 'input': {'id': expect.schedule_id, 'interval': {'every_seconds': expect.every_seconds}, 'catch_up': {'mode': 'skip'}, 'observation': {'content': expect.content}}}]})",
		"    retried.insert(3, {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'create-bad', 'tool_name': 'schedule_create', 'content': 'catch_up.mode is invalid', 'is_error': True}]})",
		"    report = validate(work, retried)",
		"    assert report.passed, report.message()",
		"    early_retry = rows()",
		"    early_retry.insert(0, {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'create-too-early', 'tool_name': 'schedule_create', 'input': {'id': expect.schedule_id, 'interval': {'every_seconds': expect.every_seconds}, 'observation': {'content': expect.content}}}]})",
		"    early_retry.insert(1, {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'create-too-early', 'tool_name': 'schedule_create', 'content': 'invalid', 'is_error': True}]})",
		"    reject(validate(work, early_retry), 'before every schedule_create')",
		"    for forbidden in sorted(schedule_routing.FORBIDDEN_TOOLS):",
		"        broken = rows()",
		"        broken.insert(2, {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'bad-1', 'tool_name': forbidden, 'input': {}}]})",
		"        reject(validate(work, broken), forbidden)",
		"    broken = rows()",
		"    broken.insert(4, copy.deepcopy(broken[2]))",
		"    broken[4]['blocks'][0]['tool_use_id'] = 'create-2'",
		"    broken.insert(5, {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'create-2', 'tool_name': 'schedule_create', 'content': '{\"id\": \"schedule-routing-eval\"}'}]})",
		"    reject(validate(work, broken), 'exactly one successful schedule_create')",
		"    bad_config = config()",
		"    bad_config['observables'][0]['schedule_config']['interval']['every_seconds'] = 60",
		"    reject(validate(work, cfg=bad_config), 'every_seconds')",
		"    bad_config = config()",
		"    bad_config['observables'][0]['id'] = 'wrong-id'",
		"    reject(validate(work, cfg=bad_config), 'persisted id')",
		"    bad_config = config()",
		"    bad_config['observables'][0]['type'] = 'command'",
		"    reject(validate(work, cfg=bad_config), 'persisted type')",
		"    bad_config = config()",
		"    bad_config['observables'][0]['schedule_config']['observation']['content'] = 'wrong content'",
		"    reject(validate(work, cfg=bad_config), 'observation.content')",
		"    bad_config = config()",
		"    del bad_config['observables'][0]['schedule_config']",
		"    reject(validate(work, cfg=bad_config), 'schedule_config')",
		"    bad_config = config()",
		"    bad_config['observables'][0]['source'] = {'type': 'schedule'}",
		"    reject(validate(work, cfg=bad_config), 'unknown fields')",
		"    bad_config = config()",
		"    bad_config['observables'][0]['command_config'] = {'command': 'sleep'}",
		"    reject(validate(work, cfg=bad_config), 'unknown fields')",
		"    bad_config = config()",
		"    bad_config['observables'][0].update({'command': 'sleep', 'args': ['1'], 'observation': {'content': 'old'}})",
		"    reject(validate(work, cfg=bad_config), 'command, observation')",
		"    bad_config = config()",
		"    bad_config['observables'].append(copy.deepcopy(bad_config['observables'][0]))",
		"    reject(validate(work, cfg=bad_config), 'exactly one')",
		"    reject(validate(work, raw_conversation='{bad json\\n'), 'invalid JSON')",
		"    reject(validate(work, raw_config='{bad json\\n'), 'invalid JSON')",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestScheduleRoutingEvalMonthlyContract(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"import copy",
		"import json",
		"import tempfile",
		"from pathlib import Path",
		"from tests.eval.juex_eval import schedule_routing",
		"expect = schedule_routing.ScheduleRoutingExpectation.monthly(",
		"    schedule_id='monthly-routing-eval',",
		"    timezone='Asia/Shanghai',",
		"    days=(1, 15, 31),",
		"    times=('09:00', '17:30'),",
		"    content='monthly routing token',",
		"    completion_token='SCHEDULE_ROUTING_PASS monthly',",
		")",
		"prompt = schedule_routing.build_prompt(expect)",
		"assert 'every month' in prompt and 'Asia/Shanghai' in prompt, prompt",
		"assert 'calendar day(s) 1, 15, 31' in prompt and '09:00, 17:30' in prompt, prompt",
		"create_input = {'id': expect.schedule_id, 'timezone': expect.timezone, 'monthly': {'days': list(expect.monthly_days), 'times': list(expect.monthly_times)}, 'observation': {'content': expect.content}}",
		"rows = [",
		"    {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'list-1', 'tool_name': 'observable_list', 'input': {}}]},",
		"    {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'list-1', 'content': '{\"observables\": []}'}]},",
		"    {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'create-1', 'tool_name': 'schedule_create', 'input': create_input}]},",
		"    {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'create-1', 'content': '{}'}]},",
		"    {'role': 'assistant', 'blocks': [{'type': 'text', 'text': expect.completion_token}]},",
		"]",
		"config = {'observables': [{'id': expect.schedule_id, 'type': 'schedule', 'schedule_config': {'timezone': expect.timezone, 'monthly': {'days': list(expect.monthly_days), 'times': list(expect.monthly_times)}, 'observation': {'content': expect.content}}}]} ",
		"with tempfile.TemporaryDirectory() as tmp:",
		"    work = Path(tmp)",
		"    conversation = work / 'conversation.jsonl'",
		"    observables = work / 'observables.json'",
		"    conversation.write_text('\\n'.join(json.dumps(row) for row in rows) + '\\n', encoding='utf-8')",
		"    observables.write_text(json.dumps(config), encoding='utf-8')",
		"    report = schedule_routing.validate_contract(conversation, observables, expect)",
		"    assert report.passed, report.message()",
		"    assert schedule_routing._create_input_matches(create_input, expect)",
		"    for invalid in [",
		"        {**create_input, 'timezone': 'UTC'},",
		"        {**create_input, 'interval': {'every_seconds': 21600}},",
		"        {**create_input, 'monthly': {'days': [1, 15], 'times': list(expect.monthly_times)}},",
		"        {**create_input, 'monthly': {'days': list(expect.monthly_days), 'times': ['9:00']}},",
		"    ]:",
		"        assert not schedule_routing._create_input_matches(invalid, expect), invalid",
		"    wrong = copy.deepcopy(config)",
		"    wrong['observables'][0]['schedule_config']['monthly']['days'] = [1]",
		"    observables.write_text(json.dumps(wrong), encoding='utf-8')",
		"    report = schedule_routing.validate_contract(conversation, observables, expect)",
		"    assert not report.passed and 'monthly.days' in report.message(), report.message()",
		"seeded = schedule_routing.ScheduleRoutingExpectation.monthly(schedule_id='requested', timezone='Asia/Shanghai', days=(1,), times=('09:00',), content='seeded', completion_token='done', existing_schedule_id='existing')",
		"seed = schedule_routing.seeded_observables_config(seeded)",
		"assert seed['observables'][0]['schedule_config']['monthly'] == {'days': [1], 'times': ['09:00']}, seed",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestScheduleRoutingFailureClassification(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"import json",
		"import tempfile",
		"from pathlib import Path",
		"from tests.eval.juex_eval import schedule_routing",
		"expect = schedule_routing.ScheduleRoutingExpectation('schedule-routing-eval', 21600, 'token', 'SCHEDULE_ROUTING_PASS token')",
		"rows = [",
		"    {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'list-1', 'tool_name': 'observable_list', 'input': {}}]},",
		"    {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'list-1', 'content': '{}'}]},",
		"    {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'create-1', 'tool_name': 'schedule_create', 'input': {'id': expect.schedule_id, 'interval': {'every_seconds': expect.every_seconds}, 'observation': {'content': expect.content}}}]},",
		"    {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'create-1', 'content': '{}'}]},",
		"    {'role': 'assistant', 'blocks': [{'type': 'text', 'text': expect.completion_token}]},",
		"]",
		"config = {'observables': [{'id': expect.schedule_id, 'type': 'schedule', 'schedule_config': {'interval': {'every_seconds': expect.every_seconds}, 'observation': {'content': expect.content}}}]}",
		"valid_input = rows[2]['blocks'][0]['input']",
		"assert schedule_routing._create_input_matches(valid_input, expect)",
		"assert not schedule_routing._schedule_config_matches({'interval': {'every_seconds': 60}, 'observation': {'content': expect.content}}, expect)",
		"for invalid_input in [",
		"    {**valid_input, 'unexpected': True},",
		"    {**valid_input, 'once': {'at': '2026-07-20T12:00:00Z'}},",
		"    {**valid_input, 'interval': {'every_seconds': expect.every_seconds, 'extra': 1}},",
		"    {**valid_input, 'catch_up': {'mode': 'latest', 'max_lateness_minutes': 0}},",
		"    {**valid_input, 'observation': {'content': expect.content, 'extra': True}},",
		"]:",
		"    assert not schedule_routing._create_input_matches(invalid_input, expect), invalid_input",
		"with tempfile.TemporaryDirectory() as tmp:",
		"    work = Path(tmp)",
		"    conversation = work / 'conversation.jsonl'",
		"    observables = work / 'observables.json'",
		"    conversation.write_text('\\n'.join(json.dumps(row) for row in rows) + '\\n', encoding='utf-8')",
		"    observables.write_text(json.dumps(config), encoding='utf-8')",
		"    outcome = schedule_routing.validate_outcome(conversation, observables, expect)",
		"    assert outcome.kind == 'passed' and outcome.report.passed, outcome",
		"    bad_config = json.loads(json.dumps(config))",
		"    bad_config['observables'][0]['schedule_config']['interval']['every_seconds'] = 60",
		"    observables.write_text(json.dumps(bad_config), encoding='utf-8')",
		"    outcome = schedule_routing.validate_outcome(conversation, observables, expect)",
		"    assert outcome.kind == 'hard_failed' and not outcome.report.passed, outcome",
		"    wrong_rows = json.loads(json.dumps(rows))",
		"    wrong_rows[2]['blocks'][0]['input']['id'] = 'wrong-id'",
		"    wrong_config = json.loads(json.dumps(config))",
		"    wrong_config['observables'][0]['id'] = 'wrong-id'",
		"    conversation.write_text('\\n'.join(json.dumps(row) for row in wrong_rows) + '\\n', encoding='utf-8')",
		"    observables.write_text(json.dumps(wrong_config), encoding='utf-8')",
		"    outcome = schedule_routing.validate_outcome(conversation, observables, expect)",
		"    assert outcome.kind == 'capability_failed' and not outcome.report.passed, outcome",
		"    errored_rows = json.loads(json.dumps(rows))",
		"    errored_rows[3]['blocks'][0]['is_error'] = True",
		"    conversation.write_text('\\n'.join(json.dumps(row) for row in errored_rows) + '\\n', encoding='utf-8')",
		"    observables.unlink()",
		"    outcome = schedule_routing.validate_outcome(conversation, observables, expect)",
		"    assert outcome.kind == 'hard_failed' and not outcome.report.passed, outcome",
		"    invalid_catch_up_rows = json.loads(json.dumps(errored_rows))",
		"    invalid_catch_up_rows[2]['blocks'][0]['input']['catch_up'] = {'mode': 'skip'}",
		"    conversation.write_text('\\n'.join(json.dumps(row) for row in invalid_catch_up_rows) + '\\n', encoding='utf-8')",
		"    outcome = schedule_routing.validate_outcome(conversation, observables, expect)",
		"    assert outcome.kind == 'capability_failed' and not outcome.report.passed, outcome",
		"    valid_catch_up_rows = json.loads(json.dumps(errored_rows))",
		"    valid_catch_up_rows[2]['blocks'][0]['input']['catch_up'] = {'mode': 'latest', 'max_lateness_minutes': 120}",
		"    conversation.write_text('\\n'.join(json.dumps(row) for row in valid_catch_up_rows) + '\\n', encoding='utf-8')",
		"    outcome = schedule_routing.validate_outcome(conversation, observables, expect)",
		"    assert outcome.kind == 'hard_failed' and not outcome.report.passed, outcome",
		"    missing_create_result_rows = json.loads(json.dumps(rows))",
		"    del missing_create_result_rows[3]",
		"    conversation.write_text('\\n'.join(json.dumps(row) for row in missing_create_result_rows) + '\\n', encoding='utf-8')",
		"    outcome = schedule_routing.validate_outcome(conversation, observables, expect)",
		"    assert outcome.kind == 'hard_failed' and not outcome.report.passed, outcome",
		"    recovered_rows = json.loads(json.dumps(rows))",
		"    failed_create = json.loads(json.dumps(recovered_rows[2]))",
		"    failed_create['blocks'][0]['tool_use_id'] = 'create-failed'",
		"    failed_result = json.loads(json.dumps(recovered_rows[3]))",
		"    failed_result['blocks'][0]['tool_use_id'] = 'create-failed'",
		"    failed_result['blocks'][0]['is_error'] = True",
		"    recovered_rows[2:2] = [failed_create, failed_result]",
		"    conversation.write_text('\\n'.join(json.dumps(row) for row in recovered_rows) + '\\n', encoding='utf-8')",
		"    observables.write_text(json.dumps(config), encoding='utf-8')",
		"    outcome = schedule_routing.validate_outcome(conversation, observables, expect)",
		"    assert outcome.kind == 'passed' and outcome.report.passed, outcome",
		"    list_errored_rows = json.loads(json.dumps(rows))",
		"    list_errored_rows[1]['blocks'][0]['is_error'] = True",
		"    conversation.write_text('\\n'.join(json.dumps(row) for row in list_errored_rows) + '\\n', encoding='utf-8')",
		"    outcome = schedule_routing.validate_outcome(conversation, observables, expect)",
		"    assert outcome.kind == 'hard_failed' and not outcome.report.passed, outcome",
		"    missing_list_result_rows = json.loads(json.dumps(rows))",
		"    del missing_list_result_rows[1]",
		"    conversation.write_text('\\n'.join(json.dumps(row) for row in missing_list_result_rows) + '\\n', encoding='utf-8')",
		"    outcome = schedule_routing.validate_outcome(conversation, observables, expect)",
		"    assert outcome.kind == 'hard_failed' and not outcome.report.passed, outcome",
		"    list_recovered_rows = json.loads(json.dumps(rows))",
		"    failed_list = json.loads(json.dumps(list_recovered_rows[0]))",
		"    failed_list['blocks'][0]['tool_use_id'] = 'list-failed'",
		"    failed_list_result = json.loads(json.dumps(list_recovered_rows[1]))",
		"    failed_list_result['blocks'][0]['tool_use_id'] = 'list-failed'",
		"    failed_list_result['blocks'][0]['is_error'] = True",
		"    list_recovered_rows[0:0] = [failed_list, failed_list_result]",
		"    conversation.write_text('\\n'.join(json.dumps(row) for row in list_recovered_rows) + '\\n', encoding='utf-8')",
		"    outcome = schedule_routing.validate_outcome(conversation, observables, expect)",
		"    assert outcome.kind == 'passed' and outcome.report.passed, outcome",
		"    capability_rows = [{'role': 'assistant', 'blocks': [{'type': 'text', 'text': 'I cannot schedule this.'}]}]",
		"    conversation.write_text('\\n'.join(json.dumps(row) for row in capability_rows) + '\\n', encoding='utf-8')",
		"    outcome = schedule_routing.validate_outcome(conversation, observables, expect)",
		"    assert outcome.kind == 'capability_failed' and not outcome.report.passed, outcome",
		"    conversation.unlink()",
		"    outcome = schedule_routing.validate_outcome(conversation, observables, expect)",
		"    assert outcome.kind == 'hard_failed' and not outcome.report.passed, outcome",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestScheduleRoutingSeededEquivalentContract(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"import copy",
		"import json",
		"import subprocess",
		"import sys",
		"import tempfile",
		"from pathlib import Path",
		"from tests.eval.juex_eval import schedule_routing",
		"expect = schedule_routing.ScheduleRoutingExpectation(",
		"    schedule_id='schedule-routing-requested',",
		"    every_seconds=21600,",
		"    content='schedule routing seeded token',",
		"    completion_token='SCHEDULE_ROUTING_PASS seeded',",
		"    existing_schedule_id='schedule-routing-existing',",
		")",
		"assert expect.variant == schedule_routing.SEEDED_EQUIVALENT_VARIANT, expect",
		"assert schedule_routing.variant_for_run_id('variant-0') == schedule_routing.EMPTY_VARIANT",
		"assert schedule_routing.variant_for_run_id('variant-4') == schedule_routing.SEEDED_EQUIVALENT_VARIANT",
		"assert schedule_routing.variant_for_run_id('variant-4') == schedule_routing.variant_for_run_id('variant-4')",
		"child = subprocess.check_output([sys.executable, '-c', \"from tests.eval.juex_eval import schedule_routing; print(schedule_routing.variant_for_run_id('variant-4'))\"], text=True).strip()",
		"assert child == schedule_routing.SEEDED_EQUIVALENT_VARIANT, child",
		"prompt = schedule_routing.build_prompt(expect)",
		"assert 'same cadence and observation content' in prompt, prompt",
		"assert 'finish with a final line exactly' in prompt, prompt",
		"assert expect.existing_schedule_id not in prompt, prompt",
		"seed = schedule_routing.seeded_observables_config(expect)",
		"seed_entry = seed['observables'][0]",
		"assert seed_entry['id'] == expect.existing_schedule_id, seed",
		"assert seed_entry['id'] != expect.schedule_id, seed",
		"assert seed_entry['schedule_config']['interval']['every_seconds'] == expect.every_seconds, seed",
		"assert seed_entry['schedule_config']['observation']['content'] == expect.content, seed",
		"listed = {'observables': [{'id': expect.existing_schedule_id, 'source_type': 'schedule', 'schedule_config': copy.deepcopy(seed_entry['schedule_config']), 'schedule': {'summary': 'every 21600s'}, 'state': 'running'}]}",
		"def rows():",
		"    return [",
		"        {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'list-1', 'tool_name': 'observable_list', 'input': {}}]},",
		"        {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'list-1', 'tool_name': 'observable_list', 'content': json.dumps(listed)}]},",
		"        {'role': 'assistant', 'blocks': [{'type': 'text', 'text': 'Equivalent existing Schedule found; no duplicate created.\\n\\n' + expect.completion_token}]},",
		"    ]",
		"def validate(work, conv_rows=None, cfg=None):",
		"    conversation = work / 'conversation.jsonl'",
		"    observables = work / 'observables.json'",
		"    conversation.write_text('\\n'.join(json.dumps(row) for row in (rows() if conv_rows is None else conv_rows)) + '\\n', encoding='utf-8')",
		"    observables.write_text(json.dumps(seed if cfg is None else cfg), encoding='utf-8')",
		"    return schedule_routing.validate_contract(conversation, observables, expect)",
		"def reject(report, needle):",
		"    assert not report.passed, report",
		"    assert needle in report.message(), report.message()",
		"with tempfile.TemporaryDirectory() as tmp:",
		"    work = Path(tmp)",
		"    report = validate(work)",
		"    assert report.passed, report.message()",
		"    outcome = schedule_routing.validate_outcome(work / 'conversation.jsonl', work / 'observables.json', expect)",
		"    assert outcome.kind == 'passed', outcome",
		"    recovered = rows()",
		"    recovered[0]['blocks'].append({'type': 'tool_use', 'tool_use_id': 'create-failed', 'tool_name': 'schedule_create', 'input': {'id': expect.schedule_id, 'interval': {'every_seconds': expect.every_seconds}, 'catch_up': {'mode': 'skip'}, 'observation': {'content': expect.content}}})",
		"    recovered[1]['blocks'].append({'type': 'tool_result', 'tool_use_id': 'create-failed', 'tool_name': 'schedule_create', 'content': 'catch_up.mode is invalid', 'is_error': True})",
		"    report = validate(work, recovered)",
		"    assert report.passed, report.message()",
		"    missing_create_result = rows()",
		"    missing_create_result[0]['blocks'].append({'type': 'tool_use', 'tool_use_id': 'create-missing', 'tool_name': 'schedule_create', 'input': {'id': expect.schedule_id, 'interval': {'every_seconds': expect.every_seconds}, 'observation': {'content': expect.content}}})",
		"    validate(work, missing_create_result)",
		"    outcome = schedule_routing.validate_outcome(work / 'conversation.jsonl', work / 'observables.json', expect)",
		"    assert outcome.kind == 'hard_failed', outcome",
		"    blind = rows()",
		"    blind.insert(2, {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'create-1', 'tool_name': 'schedule_create', 'input': {'id': expect.schedule_id, 'interval': {'every_seconds': expect.every_seconds}, 'observation': {'content': expect.content}}}]})",
		"    blind.insert(3, {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'create-1', 'tool_name': 'schedule_create', 'content': json.dumps({'id': expect.schedule_id})}]})",
		"    duplicate = copy.deepcopy(seed)",
		"    duplicate['observables'].append({'id': expect.schedule_id, 'type': 'schedule', 'schedule_config': copy.deepcopy(seed_entry['schedule_config'])})",
		"    blind_report = validate(work, blind, duplicate)",
		"    reject(blind_report, 'schedule_create')",
		"    reject(blind_report, 'exactly one entry')",
		"    outcome = schedule_routing.validate_outcome(work / 'conversation.jsonl', work / 'observables.json', expect)",
		"    assert outcome.kind == 'capability_failed', outcome",
		"    errored_list = rows()",
		"    errored_list[1]['blocks'][0]['is_error'] = True",
		"    validate(work, errored_list)",
		"    outcome = schedule_routing.validate_outcome(work / 'conversation.jsonl', work / 'observables.json', expect)",
		"    assert outcome.kind == 'hard_failed', outcome",
		"    validate(work)",
		"    (work / 'observables.json').unlink()",
		"    outcome = schedule_routing.validate_outcome(work / 'conversation.jsonl', work / 'observables.json', expect)",
		"    assert outcome.kind == 'hard_failed', outcome",
		"    for tool_name in ('observable_delete', 'observable_stop', 'observable_create'):",
		"        mutated = rows()",
		"        mutated.insert(2, {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'mutate-1', 'tool_name': tool_name, 'input': {'id': expect.existing_schedule_id}}]})",
		"        mutated.insert(3, {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'mutate-1', 'tool_name': tool_name, 'content': '{}'}]})",
		"        reject(validate(work, mutated), tool_name)",
		"    command_created = rows()",
		"    command_created.insert(2, {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'command-create-1', 'tool_name': 'observable_create', 'input': {'id': 'model-created', 'command': 'printf'}}]})",
		"    command_created.insert(3, {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'command-create-1', 'tool_name': 'observable_create', 'content': '{}'}]})",
		"    command_config = copy.deepcopy(seed)",
		"    command_config['observables'].append({'id': 'model-created', 'type': 'command', 'command_config': {'command': 'printf'}})",
		"    validate(work, command_created, command_config)",
		"    outcome = schedule_routing.validate_outcome(work / 'conversation.jsonl', work / 'observables.json', expect)",
		"    assert outcome.kind == 'capability_failed', outcome",
		"    early = rows()",
		"    early.insert(0, early.pop())",
		"    reject(validate(work, early), 'after successful observable_list')",
		"    trailing = rows()",
		"    trailing[-1]['blocks'][0]['text'] = expect.completion_token + '\\nextra text'",
		"    reject(validate(work, trailing), 'after successful observable_list')",
		"    hidden = rows()",
		"    hidden[1]['blocks'][0]['content'] = json.dumps({'observables': [{'id': expect.existing_schedule_id, 'source_type': 'schedule', 'schedule': {'summary': 'every 21600s'}}]})",
		"    reject(validate(work, hidden), 'equivalent seeded schedule')",
		"    outcome = schedule_routing.validate_outcome(work / 'conversation.jsonl', work / 'observables.json', expect)",
		"    assert outcome.kind == 'hard_failed', outcome",
		"    stopped = rows()",
		"    stopped_listed = copy.deepcopy(listed)",
		"    stopped_listed['observables'][0]['state'] = 'stopped'",
		"    stopped[1]['blocks'][0]['content'] = json.dumps(stopped_listed)",
		"    reject(validate(work, stopped), 'running equivalent seeded schedule')",
		"    outcome = schedule_routing.validate_outcome(work / 'conversation.jsonl', work / 'observables.json', expect)",
		"    assert outcome.kind == 'hard_failed', outcome",
		"    explained_stopped = rows()",
		"    explained_stopped.insert(0, {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'stop-first', 'tool_name': 'observable_stop', 'input': {'id': expect.existing_schedule_id}}]})",
		"    explained_stopped.insert(1, {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'stop-first', 'tool_name': 'observable_stop', 'content': '{}'}]})",
		"    explained_stopped[3]['blocks'][0]['content'] = json.dumps(stopped_listed)",
		"    validate(work, explained_stopped)",
		"    outcome = schedule_routing.validate_outcome(work / 'conversation.jsonl', work / 'observables.json', expect)",
		"    assert outcome.kind == 'capability_failed', outcome",
		"    wrong_id = copy.deepcopy(seed)",
		"    wrong_id['observables'][0]['id'] = expect.schedule_id",
		"    reject(validate(work, cfg=wrong_id), 'persisted id')",
		"try:",
		"    schedule_routing.ScheduleRoutingExpectation('same', 21600, 'x', 'done', existing_schedule_id='same')",
		"except ValueError:",
		"    pass",
		"else:",
		"    raise AssertionError('matching requested and existing ids must be rejected')",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestScheduleRoutingEvalReportingIncludesSelectionEvidence(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"import json",
		"import tempfile",
		"from pathlib import Path",
		"from tests.eval.juex_eval import helper, outcomes",
		"with tempfile.TemporaryDirectory() as tmp:",
		"    work = Path(tmp)",
		"    result = helper.SmokeResult(",
		"        run_id='unit', ref='provider:model', provider_id='provider', model_id='model',",
		"        protocol='openai', reasoning_effort_capability='default', tools_capability='default', thinking_effort='unset',",
		"        status='fail', schedule_routing_status='failed',",
		"        schedule_routing_variant='seeded-equivalent', schedule_routing_existing_id='schedule-routing-existing',",
		"        error_stage='schedule-routing', error='model did not call schedule_create', artifacts='cases/provider_model',",
		"    )",
		"    assert result.as_dict()['schedule_routing_existing_id'] == 'schedule-routing-existing'",
		"    summary = {",
		"        'run_id': 'unit', 'juex': './dist/juex', 'config': '/resolved/config.yaml', 'work_root': 'cleaned',",
		"        'selection_source': 'provider_config', 'selected_provider_model': 'provider:model',",
		"        'selected_provider_models': ['provider:model'], 'selection_seed': 'seed-1',",
		"        'eligible_candidate_count': 1, 'eligible_candidate_refs': ['provider:model'],",
		"        'resolved_config_path': '/resolved/config.yaml', 'redacted_config_hash': 'sha256:abc',",
		"        'reproduction_command': 'juex-eval provider-smoke --config /resolved/config.yaml --selection-seed seed-1',",
		"        'selection_mode': 'seeded', 'failure_category': None, 'error': None,",
		"        'outcome': 'product_failure', 'reason': 'schedule contract failed', 'matched_rule': 'schedule-routing-contract',",
		"        'blocks_merge': True, 'recommended_action': 'fix_code', 'retryable': False,",
		"        'total': 1, 'passed': 0, 'failed': 1, 'tool_use_recorded': 1, 'exec_command_tool_use_recorded': 1,",
		"        'tty_recorded': 1, 'stdin_recorded': 1, 'filesystem_verified': 1, 'terminal_event_verified': 1,",
		"        'thinking_observed': 0, 'schedule_routing_verified': 0, 'schedule_routing_failures': 1,",
		"        'schedule_routing_variant': 'seeded-equivalent',",
		"        'results_jsonl_path': 'results.jsonl',",
		"    }",
		"    summary_json = work / 'summary.json'",
		"    summary_md = work / 'summary.md'",
		"    helper.write_smoke_summary(summary_json, summary_md, summary, [result])",
		"    parsed = json.loads(summary_json.read_text(encoding='utf-8'))",
		"    assert parsed['total'] == 1 and parsed['schedule_routing_failures'] == 1, parsed",
		"    assert parsed['selected_provider_model'] == 'provider:model', parsed",
		"    markdown = summary_md.read_text(encoding='utf-8')",
		"    assert 'Schedule routing failures: 1' in markdown, markdown",
		"    assert 'failed (optional' not in markdown, markdown",
		"    assert 'Selection seed: `seed-1`' in markdown, markdown",
		"    assert 'Schedule routing variant: `seeded-equivalent`' in markdown, markdown",
		"    assert 'Outcome: `product_failure`' in markdown and 'Blocks merge: true' in markdown, markdown",
		"    assert '| not_observed | failed | seeded-equivalent | schedule-routing |' in markdown, markdown",
		"    commands = work / 'commands.jsonl'",
		"    commands.write_text(json.dumps({'label': 'provider-model-smoke', 'execution_state': 'executed', 'exit_status': 1, 'log': 'provider.log', 'attempts': [], **outcomes.ValidationOutcome('product_failure', 'schedule contract failed', 'schedule-routing-contract', True, 'fix_code').as_dict()}) + '\\n', encoding='utf-8')",
		"    record_json = work / 'record.json'",
		"    record_md = work / 'record.md'",
		"    helper.write_development_record(work, 'unit', commands, summary_json, '', 1, record_json, record_md)",
		"    record_data = json.loads(record_json.read_text(encoding='utf-8'))",
		"    assert record_data['blocks_merge'] is True and record_data['failure_type'] == 'code_failure' and record_data['recommended_action'] == 'fix_code', record_data",
		"    record = record_md.read_text(encoding='utf-8')",
		"    assert 'Schedule routing failures: 1' in record, record",
		"    assert 'Selection source: provider_config' in record, record",
		"    assert 'Reproduction command: juex-eval provider-smoke' in record, record",
		"    assert 'Schedule routing variant: seeded-equivalent' in record, record",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestScheduleRoutingEvalRetriesUseFreshAttempts(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"import json",
		"import tempfile",
		"from pathlib import Path",
		"from tests.eval.juex_eval import helper, schedule_routing",
		"expect = schedule_routing.ScheduleRoutingExpectation('schedule-routing-eval', 21600, 'retry token', 'SCHEDULE_ROUTING_PASS retry', existing_schedule_id='schedule-routing-existing')",
		"seed = schedule_routing.seeded_observables_config(expect)",
		"def valid_rows():",
		"    return [",
		"        {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'skill-1', 'tool_name': 'skill_load', 'input': {'name': 'juex-observables'}}]},",
		"        {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'skill-1', 'content': 'loaded'}]},",
		"        {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'list-1', 'tool_name': 'observable_list', 'input': {}}]},",
		"        {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'list-1', 'content': json.dumps({'observables': [{'id': expect.existing_schedule_id, 'source_type': 'schedule', 'state': 'running', 'schedule_config': seed['observables'][0]['schedule_config']}]})}]},",
		"        {'role': 'assistant', 'blocks': [{'type': 'text', 'text': expect.completion_token}]},",
		"    ]",
		"with tempfile.TemporaryDirectory() as tmp:",
		"    work = Path(tmp)",
		"    row = helper.MatrixRow('provider', 'model', 'openai', 'default', 'default', 'unset', 'provider:model')",
		"    ctx = helper.ProviderSmokeContext(row, '/fake/juex', {'providers': []}, work / 'work', work / 'report', 'unit', 5, 1, str(work / 'codex'))",
		"    attempts = []",
		"    attempt_seeds = []",
		"    def fake_write_config(cfg, provider_id, model_id, output_path):",
		"        output_path.parent.mkdir(parents=True, exist_ok=True)",
		"        output_path.write_text('models: [provider:model]\\n', encoding='utf-8')",
		"    def fake_run_turn(ctx, case_dir, case_config, label, args):",
		"        attempts.append(case_dir)",
		"        attempt_seeds.append(json.loads((case_dir / '.juex' / 'observables.json').read_text(encoding='utf-8')))",
		"        case_dir.mkdir(parents=True, exist_ok=True)",
		"        (case_dir / 'turn1.stdout.json').write_text('{}\\n', encoding='utf-8')",
		"        (case_dir / 'turn1.stderr.log').write_text('timeout\\n', encoding='utf-8')",
		"        if len(attempts) == 1:",
		"            (case_dir / '.juex' / 'observables.json').write_text(json.dumps({'observables': [{'id': 'dirty-attempt-1'}]}), encoding='utf-8')",
		"            return 124",
		"        session_id = f'session-attempt-{len(attempts)}'",
		"        (case_dir / 'turn1.stdout.json').write_text(json.dumps({'session_id': session_id, 'blocks': [{'type': 'text', 'text': expect.completion_token}]}) + '\\n', encoding='utf-8')",
		"        agent_id = 'abcd23'",
		"        (case_dir / '.juex' / 'juex.local.json').write_text(json.dumps({'agent_id': agent_id}), encoding='utf-8')",
		"        session = case_dir / 'home' / '.juex' / 'agents' / agent_id / 'sessions' / session_id",
		"        session.mkdir(parents=True)",
		"        conversation_rows = valid_rows()",
		"        if len(attempts) == 2:",
		"            conversation_rows.insert(4, {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'create-duplicate', 'tool_name': 'schedule_create', 'input': {'id': expect.schedule_id, 'interval': {'every_seconds': expect.every_seconds}, 'observation': {'content': expect.content}}}]})",
		"            conversation_rows.insert(5, {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'create-duplicate', 'content': '{}'}]})",
		"            duplicate = json.loads(json.dumps(seed))",
		"            duplicate['observables'].append({'id': expect.schedule_id, 'type': 'schedule', 'schedule_config': seed['observables'][0]['schedule_config']})",
		"            (case_dir / '.juex' / 'observables.json').write_text(json.dumps(duplicate), encoding='utf-8')",
		"        (session / 'conversation.jsonl').write_text('\\n'.join(json.dumps(row) for row in conversation_rows) + '\\n', encoding='utf-8')",
		"        (session / 'events.jsonl').write_text(json.dumps({'type': 'session.completed'}) + '\\n', encoding='utf-8')",
		"        return 0",
		"    original_write_config = helper.write_selected_config",
		"    original_run_turn = helper.run_turn",
		"    helper.write_selected_config = fake_write_config",
		"    helper.run_turn = fake_run_turn",
		"    try:",
		"        outcome = helper.run_schedule_routing_case(ctx, work / 'report' / 'cases' / 'provider_model', expect)",
		"    finally:",
		"        helper.write_selected_config = original_write_config",
		"        helper.run_turn = original_run_turn",
		"    assert outcome.kind == 'capability_failed' and not outcome.report.passed, outcome.report.message()",
		"    assert outcome.validation_outcome.outcome == 'product_failure', outcome.validation_outcome",
		"    assert outcome.session_id == 'session-attempt-2', outcome.session_id",
		"    assert len(attempts) == 2 and len(set(attempts)) == 2, attempts",
		"    assert [path.name for path in attempts] == ['attempt-1', 'attempt-2'], attempts",
		"    assert [item['observables'][0]['id'] for item in attempt_seeds] == [expect.existing_schedule_id] * 2, attempt_seeds",
		"    artifacts = work / 'report' / 'cases' / 'provider_model' / 'schedule-routing'",
		"    assert (artifacts / 'attempt-1' / 'turn1.stderr.log').is_file(), artifacts",
		"    dirty = json.loads((artifacts / 'attempt-1' / 'observables.json').read_text(encoding='utf-8'))",
		"    assert dirty['observables'][0]['id'] == 'dirty-attempt-1', dirty",
		"    failed_contract = json.loads((artifacts / 'attempt-2' / 'contract.json').read_text(encoding='utf-8'))",
		"    assert failed_contract['outcome'] == 'capability_failed', failed_contract",
		"    assert failed_contract['passed'] is False and any('exactly one entry' in issue for issue in failed_contract['issues']), failed_contract",
		"    assert not (artifacts / 'attempt-3').exists(), artifacts",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func runUV(t *testing.T, root string, args ...string) string {
	t.Helper()
	baseArgs := []string{"run", "--quiet", "--project", root}
	cmd := exec.Command("uv", append(baseArgs, args...)...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("uv command failed: %v\n%s", err, out)
	}
	return string(out)
}

func TestIsolateWriteModelConfigHomesPreservesResolvedGoCaches(t *testing.T) {
	t.Setenv("GOCACHE", "")
	t.Setenv("GOMODCACHE", "")
	want := resolvedGoCacheEnvironment(t)

	isolateWriteModelConfigHomes(t)

	for name, value := range want {
		if got := os.Getenv(name); got != value {
			t.Errorf("%s after HOME isolation = %q, want %q", name, got, value)
		}
	}
}

func resolvedGoCacheEnvironment(t *testing.T) map[string]string {
	t.Helper()
	cmd := exec.Command("go", "env", "-json", "GOCACHE", "GOMODCACHE")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("resolve Go cache environment: %v", err)
	}
	values := map[string]string{}
	if err := json.Unmarshal(out, &values); err != nil {
		t.Fatalf("decode Go cache environment: %v", err)
	}
	for _, name := range []string{"GOCACHE", "GOMODCACHE"} {
		if strings.TrimSpace(values[name]) == "" {
			t.Fatalf("go env %s is empty", name)
		}
	}
	return values
}

func isolateWriteModelConfigHomes(t *testing.T) {
	t.Helper()
	goCaches := resolvedGoCacheEnvironment(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("JUEX_HOME", filepath.Join(home, "juex-home"))
	t.Setenv("CODEX_HOME", filepath.Join(home, "codex-home"))
	for name, value := range goCaches {
		t.Setenv(name, value)
	}
}

func bypassProxyForLoopbackServer(t *testing.T, rawURL string) {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse loopback test server URL: %v", err)
	}
	host := parsed.Hostname()
	if host == "" {
		t.Fatalf("loopback test server URL has no host: %q", rawURL)
	}
	noProxy := host
	if inherited := strings.TrimSpace(os.Getenv("NO_PROXY")); inherited != "" {
		noProxy += "," + inherited
	}
	t.Setenv("NO_PROXY", noProxy)
}

func assertHelpContains(t *testing.T, help string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(help, want) {
			t.Fatalf("help missing %q:\n%s", want, help)
		}
	}
}

func findEvalCommand(t *testing.T, steps []struct {
	Label   string   `json:"label"`
	Command []string `json:"command"`
}, label string) []string {
	t.Helper()
	for _, step := range steps {
		if step.Label == label {
			return step.Command
		}
	}
	t.Fatalf("missing step %q: %#v", label, steps)
	return nil
}

func assertCommandFlagValue(t *testing.T, command []string, flag, value string) {
	t.Helper()
	for index, part := range command {
		if part == flag && index+1 < len(command) && command[index+1] == value {
			return
		}
	}
	t.Fatalf("command missing %s %s: %#v", flag, value, command)
}

func assertCommandHasFlag(t *testing.T, command []string, flag string) {
	t.Helper()
	for _, part := range command {
		if part == flag {
			return
		}
	}
	t.Fatalf("command missing %s: %#v", flag, command)
}

func assertCommandLacks(t *testing.T, command []string, forbidden string) {
	t.Helper()
	for _, part := range command {
		if part == forbidden {
			t.Fatalf("command should not contain %s: %#v", forbidden, command)
		}
	}
}
