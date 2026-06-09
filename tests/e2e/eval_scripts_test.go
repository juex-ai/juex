package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLiveModelRotationScript(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	work := t.TempDir()
	modelList := filepath.Join(work, "live-models.yaml")
	state := filepath.Join(work, "rotation.json")
	if err := os.WriteFile(modelList, []byte(strings.Join([]string{
		"provider_smoke_models:",
		"  - provider/a",
		"  - provider/b",
		"  - provider/c",
		"compaction_eval_models:",
		"  - compaction/a",
		"  - compaction/b",
		"",
	}, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := runRotation(t, root, modelList, state, "select", "--section", "provider_smoke_models"); got != "provider/a" {
		t.Fatalf("initial provider selection = %q, want provider/a", got)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatalf("select should not create state file, stat err=%v", err)
	}

	runRotation(t, root, modelList, state, "mark-success", "--section", "provider_smoke_models", "--model", "provider/a")
	if got := runRotation(t, root, modelList, state, "select", "--section", "provider_smoke_models"); got != "provider/b" {
		t.Fatalf("rotated provider selection = %q, want provider/b", got)
	}

	runRotation(t, root, modelList, state, "mark-success", "--section", "compaction_eval_models", "--model", "compaction/a")
	if got := runRotation(t, root, modelList, state, "select", "--section", "compaction_eval_models"); got != "compaction/b" {
		t.Fatalf("rotated compaction selection = %q, want compaction/b", got)
	}

	raw, err := os.ReadFile(state)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Sections map[string]struct {
			LastSuccessful string `json:"last_successful"`
		} `json:"sections"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	if got := parsed.Sections["provider_smoke_models"].LastSuccessful; got != "provider/a" {
		t.Fatalf("provider last_successful = %q, want provider/a", got)
	}
	if got := parsed.Sections["compaction_eval_models"].LastSuccessful; got != "compaction/a" {
		t.Fatalf("compaction last_successful = %q, want compaction/a", got)
	}
}

func runRotation(t *testing.T, root, modelList, state string, args ...string) string {
	t.Helper()
	baseArgs := []string{
		"run",
		"--quiet",
		"--project",
		root,
		"python",
		filepath.Join(root, "scripts/live_model_rotation.py"),
		"--model-list",
		modelList,
		"--state",
		state,
	}
	cmd := exec.Command("uv", append(baseArgs, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("rotation command failed: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}
