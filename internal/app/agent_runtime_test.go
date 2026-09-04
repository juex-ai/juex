package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/agentstate"
	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/environment"
)

func TestResolveAgentRuntimeExpandsDefaultsAndKeepsSnapshotStable(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	address, err := agentstate.NewAgentAddress(home, "abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(address.StateDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	extensionDir := filepath.Join(work, ".juex", "extensions", "lark-cli")
	manifestPath := filepath.Join(extensionDir, "juex.extension.json")
	mustWriteAppTestFile(t, manifestPath, `{
  "manifest_version": 1,
  "name": "lark-cli",
  "version": "1.0.0",
  "agent": {"environment":{"variables":{
    "LARKSUITE_CLI_CONFIG_DIR":"${JUEX_EXT_DATA_DIR}",
    "LARKSUITE_CLI_DATA_DIR":"${JUEX_EXT_DIR}/data",
    "LARK_WORKDIR":"${WORKDIR}:${JUEX_WORKDIR}"
  }}}
}`)
	cfg := config.Config{
		WorkDir: work, AgentAddress: address,
		Extensions: config.ExtensionPolicy{Allow: []string{"lark-cli"}, Configured: true},
	}

	resolution, err := ResolveAgentRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	wantDataDir := filepath.Join(address.StateDir(), "extensions", "lark-cli")
	if got, _ := resolution.Environment().Lookup("LARKSUITE_CLI_CONFIG_DIR"); got != wantDataDir {
		t.Fatalf("config dir = %q, want %q", got, wantDataDir)
	}
	if got, _ := resolution.Environment().Lookup("LARKSUITE_CLI_DATA_DIR"); got != extensionDir+"/data" {
		t.Fatalf("literal placeholder expansion = %q, want %q", got, extensionDir+"/data")
	}
	if got, _ := resolution.Environment().Lookup("LARK_WORKDIR"); got != work+":"+work {
		t.Fatalf("workdir = %q", got)
	}
	if root := resolution.ExtensionsRuntime().RootDir; root != filepath.Join(address.StateDir(), "extensions") {
		t.Fatalf("extensions root = %q", root)
	}
	if _, err := os.Stat(resolution.ExtensionsRuntime().RootDir); !os.IsNotExist(err) {
		t.Fatalf("resolution created extensions root: %v", err)
	}

	mustWriteAppTestFile(t, manifestPath, strings.ReplaceAll(
		`{"manifest_version":1,"name":"lark-cli","version":"1.0.0","agent":{"environment":{"variables":{"LARKSUITE_CLI_CONFIG_DIR":"old"}}}}`,
		"old", "new",
	))
	if got, _ := resolution.Environment().Lookup("LARKSUITE_CLI_CONFIG_DIR"); got != wantDataDir {
		t.Fatalf("existing resolution changed after manifest edit: %q", got)
	}
	updated, err := ResolveAgentRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := updated.Environment().Lookup("LARKSUITE_CLI_CONFIG_DIR"); got != "new" {
		t.Fatalf("new resolution = %q", got)
	}
}

func TestInspectAgentRuntimeValidatesDefaultsBeforeAgentCreation(t *testing.T) {
	work := t.TempDir()
	extensionDir := filepath.Join(work, ".juex", "extensions", "demo")
	mustWriteAppTestFile(t, filepath.Join(extensionDir, "juex.extension.json"), `{
  "manifest_version":1,
  "name":"demo",
  "version":"1.0.0",
  "agent":{"environment":{"variables":{
    "DEMO_DATA":"${JUEX_EXT_DATA_DIR}/cache"
  }}}
}`)
	cfg := config.Config{
		WorkDir:    work,
		Extensions: config.ExtensionPolicy{Allow: []string{"demo"}, Configured: true},
	}

	if _, err := ResolveAgentRuntime(cfg); err == nil || !strings.Contains(err.Error(), "JUEX_EXT_DATA_DIR") {
		t.Fatalf("runtime resolution without Agent = %v, want unresolved data directory", err)
	}
	inspection, err := InspectAgentRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	declarations := inspection.EnvironmentDeclarations()
	if len(declarations) != 1 || declarations[0].Name != "DEMO_DATA" || declarations[0].Status != environment.DefaultStatusEffective {
		t.Fatalf("inspection declarations = %+v", declarations)
	}
	if inspection.ExtensionsRuntime().RootDir != "" {
		t.Fatalf("inspection minted Agent extension root %q", inspection.ExtensionsRuntime().RootDir)
	}
}

func TestResolveAgentRuntimeShadowsAndDeduplicatesWithoutValues(t *testing.T) {
	const shadowValue = "agent-value"
	t.Setenv("SHADOWED_EXTENSION_DEFAULT", "")
	home := t.TempDir()
	work := t.TempDir()
	address, err := agentstate.NewAgentAddress(home, "abcdef")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alpha", "beta"} {
		dir := filepath.Join(work, ".juex", "extensions", name)
		mustWriteAppTestFile(t, filepath.Join(dir, "juex.extension.json"), `{
  "manifest_version":1,"name":"`+name+`","version":"1.0.0",
  "agent":{"environment":{"variables":{
    "SHADOWED_EXTENSION_DEFAULT":"`+shadowValue+`",
    "SHARED_EXTENSION_DEFAULT":"same"
  }}}
}`)
	}
	resolution, err := ResolveAgentRuntime(config.Config{
		WorkDir: work, AgentAddress: address,
		Extensions: config.ExtensionPolicy{Allow: []string{"alpha", "beta"}, Configured: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := resolution.Environment().Lookup("SHADOWED_EXTENSION_DEFAULT"); !ok || got != "" {
		t.Fatalf("empty shadow = %q, %v", got, ok)
	}
	declarations := resolution.EnvironmentDeclarations()
	statuses := map[string][]environment.DefaultStatus{}
	for _, declaration := range declarations {
		statuses[declaration.Name] = append(statuses[declaration.Name], declaration.Status)
		encoded := declaration.Name + declaration.Source + declaration.ManifestPath + declaration.ShadowedBySource + declaration.ShadowedByPath
		if strings.Contains(encoded, shadowValue) || strings.Contains(encoded, "same") {
			t.Fatalf("declaration leaked value: %+v", declaration)
		}
	}
	if got := statuses["SHADOWED_EXTENSION_DEFAULT"]; len(got) != 2 || got[0] != environment.DefaultStatusShadowed || got[1] != environment.DefaultStatusShadowed {
		t.Fatalf("shadow statuses = %+v", got)
	}
	if got := statuses["SHARED_EXTENSION_DEFAULT"]; len(got) != 2 || got[0] != environment.DefaultStatusEffective || got[1] != environment.DefaultStatusDeduplicated {
		t.Fatalf("dedup statuses = %+v", got)
	}
	redacted, changed := resolution.Environment().RedactConfiguredValues([]byte(shadowValue + " same"))
	if !changed || strings.Contains(string(redacted), shadowValue) || strings.Contains(string(redacted), "same") {
		t.Fatalf("redaction = %q, changed=%v", redacted, changed)
	}
}

func TestResolveAgentRuntimeRejectsConflictsAndUnsupportedExpansionWithoutValues(t *testing.T) {
	const (
		firstValue  = "first-runtime-value"
		secondValue = "second-runtime-value"
	)
	tests := []struct {
		name      string
		variables map[string]map[string]string
		want      string
		secrets   []string
	}{
		{
			name: "conflict",
			variables: map[string]map[string]string{
				"alpha": {"CONFLICTING_DEFAULT": firstValue},
				"beta":  {"CONFLICTING_DEFAULT": secondValue},
			},
			want: "conflicts between", secrets: []string{firstValue, secondValue},
		},
		{
			name:      "unknown placeholder",
			variables: map[string]map[string]string{"alpha": {"BAD_DEFAULT": "${HOME}"}},
			want:      "placeholder", secrets: []string{"${HOME}"},
		},
		{
			name:      "shell expansion",
			variables: map[string]map[string]string{"alpha": {"BAD_DEFAULT": "$HOME"}},
			want:      "shell-style", secrets: []string{"$HOME"},
		},
		{
			name:      "command substitution",
			variables: map[string]map[string]string{"alpha": {"BAD_DEFAULT": "$(secret-command)"}},
			want:      "shell-style", secrets: []string{"secret-command"},
		},
		{
			name:      "backticks",
			variables: map[string]map[string]string{"alpha": {"BAD_DEFAULT": "`secret-command`"}},
			want:      "backticks", secrets: []string{"secret-command"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			work := t.TempDir()
			address, err := agentstate.NewAgentAddress(home, "abcdef")
			if err != nil {
				t.Fatal(err)
			}
			var allow []string
			for name, variables := range tt.variables {
				allow = append(allow, name)
				dir := filepath.Join(work, ".juex", "extensions", name)
				body := `{"manifest_version":1,"name":"` + name + `","version":"1.0.0","agent":{"environment":{"variables":{`
				first := true
				for key, value := range variables {
					if !first {
						body += ","
					}
					first = false
					body += `"` + key + `":"` + value + `"`
				}
				body += `}}}}`
				mustWriteAppTestFile(t, filepath.Join(dir, "juex.extension.json"), body)
			}
			sortStringsForTest(allow)
			_, err = ResolveAgentRuntime(config.Config{
				WorkDir: work, AgentAddress: address,
				Extensions: config.ExtensionPolicy{Allow: allow, Configured: true},
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
			for _, secret := range tt.secrets {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error leaked %q: %v", secret, err)
				}
			}
		})
	}
}

func TestResolveAgentRuntimeUsesDistinctAgentDataDirectories(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	extensionDir := filepath.Join(work, ".juex", "extensions", "demo")
	mustWriteAppTestFile(t, filepath.Join(extensionDir, "juex.extension.json"), `{
  "manifest_version":1,"name":"demo","version":"1.0.0",
  "agent":{"environment":{"variables":{"DEMO_DATA":"${JUEX_EXT_DATA_DIR}"}}}
}`)
	values := map[string]string{}
	for _, id := range []string{"abcdef", "ghijkl"} {
		address, err := agentstate.NewAgentAddress(home, id)
		if err != nil {
			t.Fatal(err)
		}
		resolution, err := ResolveAgentRuntime(config.Config{
			WorkDir: work, AgentAddress: address,
			Extensions: config.ExtensionPolicy{Allow: []string{"demo"}, Configured: true},
		})
		if err != nil {
			t.Fatal(err)
		}
		values[id], _ = resolution.Environment().Lookup("DEMO_DATA")
	}
	if values["abcdef"] == values["ghijkl"] || !strings.Contains(values["abcdef"], filepath.Join("agents", "abcdef")) || !strings.Contains(values["ghijkl"], filepath.Join("agents", "ghijkl")) {
		t.Fatalf("Agent values = %#v", values)
	}
}

func sortStringsForTest(values []string) {
	for i := range values {
		for j := i + 1; j < len(values); j++ {
			if values[j] < values[i] {
				values[i], values[j] = values[j], values[i]
			}
		}
	}
}
