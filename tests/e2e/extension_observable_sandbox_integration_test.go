//go:build integration

package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/agentstate"
	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/observable"
)

func TestIntegration_ExtensionObservableSandboxScopesWritableData(t *testing.T) {
	switch runtime.GOOS {
	case "darwin":
		if _, err := exec.LookPath("sandbox-exec"); err != nil {
			t.Skip("sandbox-exec is unavailable")
		}
	case "linux":
		if _, err := exec.LookPath("bwrap"); err != nil {
			t.Skip("bubblewrap is unavailable")
		}
	default:
		t.Skipf("sandbox backend is unavailable on %s", runtime.GOOS)
	}

	root, err := os.MkdirTemp(".", ".extension-observable-sandbox-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("remove sandbox fixture: %v", err)
		}
	})

	work := filepath.Join(root, "workspace")
	home := filepath.Join(root, "home")
	address, err := agentstate.NewAgentAddress(home, "abcdef")
	if err != nil {
		t.Fatal(err)
	}
	siblingData := filepath.Join(address.StateDir(), "extensions", "sibling")
	agentOther := filepath.Join(address.StateDir(), "other")
	for _, path := range []string{work, siblingData, agentOther} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	extensionDir := filepath.Join(home, "extensions", "demo")
	body, err := json.Marshal(map[string]any{
		"observables": []map[string]any{{
			"id":   "extension-sandbox-probe",
			"type": "command",
			"command_config": map[string]any{
				"command": "/bin/sh",
				"args": []string{"-c", strings.Join([]string{
					`if printf own > "$JUEX_EXT_DATA_DIR/own.txt"; then own=ok; else own=failed; fi`,
					`if printf sibling > "$SIBLING_DATA/blocked.txt" 2>/dev/null; then sibling=unexpected; else sibling=blocked; fi`,
					`if printf agent > "$AGENT_OTHER/blocked.txt" 2>/dev/null; then agent=unexpected; else agent=blocked; fi`,
					`printf '{"type":"sandbox_probe","level":"info","content":"own=%s sibling=%s agent=%s"}\n' "$own" "$sibling" "$agent"`,
				}, "\n")},
				"env": map[string]string{
					"SIBLING_DATA": siblingData,
					"AGENT_OTHER":  agentOther,
				},
				"streams": []string{"stdout"},
				"parser": map[string]string{
					"type":           "jsonl",
					"content_field":  "content",
					"kind_field":     "type",
					"severity_field": "level",
				},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(extensionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extensionDir, "juex.extension.json"), []byte(`{"manifest_version":1,"name":"demo","version":"1.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extensionDir, "observables.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}

	a, err := app.New(app.Options{
		Config: config.Config{
			ProviderProtocol: "openai/chat",
			WorkDir:          work,
			HomeJuexDir:      home,
			AgentAddress:     address,
			Extensions: config.ExtensionPolicy{
				Allow:      []string{"demo"},
				Configured: true,
			},
			Sandbox: config.SandboxPolicy{
				Enabled: true,
				FileSystem: config.FileSystemSandboxPolicy{
					OutsideWorkspace: config.OutsideWorkspaceReadOnly,
				},
				Network: config.NetworkSandboxPolicy{Enabled: true},
			},
		},
		Provider:   &bareScriptProvider{},
		WorkDir:    work,
		DisableMCP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.CloseAndWait()

	deadline := time.Now().Add(10 * time.Second)
	var records []observable.ObservationRecord
	for time.Now().Before(deadline) {
		records, err = a.Observables().Observations(observable.ObservationFilter{
			ObservableID: "extension-sandbox-probe",
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(records) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(records) != 1 || records[0].Content != "own=ok sibling=blocked agent=blocked" {
		status, _ := a.Observables().StatusByID("extension-sandbox-probe")
		t.Fatalf("observations = %+v, status = %+v", records, status)
	}

	ownData := filepath.Join(address.StateDir(), "extensions", "demo", "own.txt")
	if content, err := os.ReadFile(ownData); err != nil || string(content) != "own" {
		t.Fatalf("own data = %q err=%v", content, err)
	}
	for _, blocked := range []string{
		filepath.Join(siblingData, "blocked.txt"),
		filepath.Join(agentOther, "blocked.txt"),
	} {
		if _, err := os.Stat(blocked); !os.IsNotExist(err) {
			t.Fatalf("blocked path %s was written, stat err=%v", blocked, err)
		}
	}

	status, err := a.Observables().StatusByID("extension-sandbox-probe")
	if err != nil {
		t.Fatal(err)
	}
	if status.Source != "ext:demo" {
		t.Fatalf("source = %q, want ext:demo", status.Source)
	}
}
