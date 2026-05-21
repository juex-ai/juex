package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestLiveBinary_LoadsSkillsAndMCP builds the real `juex` binary, points
// it at a tempdir containing both a skill and an mcp.json that launches a
// real Python MCP server (via `uv run`), and asserts that
// `juex run --dry-run --json` reports both pieces in the resulting plan.
//
// This complements TestEndToEnd_FullStack (in-process, mocked LLM) by
// proving the live binary subprocess wires everything correctly using a
// realistic MCP server (the official Python SDK — most MCP servers in
// the wild are Python).
func TestLiveBinary_LoadsSkillsAndMCP(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows: bash-tool defaults differ; this test is unix-focused")
	}
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}

	bin := buildJuex(t)
	mcpScript := pythonMCPScript(t)

	work := t.TempDir()
	if err := writeSkillFile(work, "trim-tool", "trim trailing whitespace"); err != nil {
		t.Fatal(err)
	}
	if err := writeMCPConfig(work, "uv", []string{"run", mcpScript}); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(work, ".juex", "juex.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("provider:\n  id: openai\n  base_url: https://example\n  api_key: k\n  model: m\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin,
		"--cwd", work,
		"--config", configPath,
		"run", "--dry-run", "--json", "x")
	// Use Output() so stdout (the JSON plan) is not contaminated by stderr
	// (uv's "Installed N packages" log on first run).
	out, err := cmd.Output()
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok || ee.ExitCode() != 10 {
			t.Fatalf("juex exit: %v\nstdout:\n%s\nstderr:\n%s", err, out, ee.Stderr)
		}
	}

	var plan struct {
		Tools  []string `json:"tools"`
		Skills []struct {
			Name string `json:"name"`
			Path string `json:"path"`
		} `json:"skills"`
	}
	if err := json.Unmarshal(out, &plan); err != nil {
		t.Fatalf("parse plan: %v\noutput:\n%s", err, out)
	}

	// MCP server started + tool registered.
	have := map[string]bool{}
	for _, name := range plan.Tools {
		have[name] = true
	}
	if !have["mcp__local__echo"] {
		t.Errorf("mcp__local__echo not in tool list (MCP server not loaded?). tools=%v", plan.Tools)
	}

	// Skill loaded: name + absolute path appear in the dry-run plan.
	skillFound := false
	for _, s := range plan.Skills {
		if s.Name == "trim-tool" {
			skillFound = true
			wantPath := filepath.Join(work, ".agents", "skills", "trim-tool", "SKILL.md")
			if s.Path != wantPath {
				t.Errorf("trim-tool path = %q, want %q", s.Path, wantPath)
			}
		}
	}
	if !skillFound {
		t.Errorf("trim-tool not in plan.skills (skills not loaded?). skills=%+v", plan.Skills)
	}
}

// TestLiveBinary_SchemaIncludesAllSubcommands runs `juex schema` and
// verifies every documented subcommand shows up. Cheap — proves the
// binary wires cobra correctly.
func TestLiveBinary_SchemaIncludesAllSubcommands(t *testing.T) {
	bin := buildJuex(t)
	out, err := exec.Command(bin, "schema").Output()
	if err != nil {
		t.Fatal(err)
	}
	body := string(out)
	for _, want := range []string{
		`"name": "juex"`,
		`"name": "run"`,
		`"name": "repl"`,
		`"name": "version"`,
		`"name": "schema"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("schema missing %q. full output:\n%s", want, body)
		}
	}
}

// buildJuex compiles the real juex binary into the test's tempdir.
func buildJuex(t *testing.T) string {
	t.Helper()
	name := "juex"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	out := filepath.Join(t.TempDir(), name)
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", out, "./cmd/juex")
	cmd.Dir = root
	if buildOut, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build juex: %v\n%s", err, buildOut)
	}
	return out
}

// pythonMCPScript returns the absolute path to the fake MCP server script.
func pythonMCPScript(t *testing.T) string {
	t.Helper()
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, "tests", "e2e", "testdata", "fake-mcp", "server.py")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("fake MCP script missing at %s: %v", p, err)
	}
	return p
}

func writeSkillFile(workDir, name, description string) error {
	dir := filepath.Join(workDir, ".agents", "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	body := "---\nname: " + name + "\ndescription: " + description + "\ntype: model-invocable\n---\nFull skill body."
	return os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644)
}

func writeMCPConfig(workDir, command string, args []string) error {
	cfg := map[string]any{
		"mcpServers": map[string]any{
			"local": map[string]any{
				"command": command,
				"args":    args,
			},
		},
	}
	body, _ := json.MarshalIndent(cfg, "", "  ")
	dir := filepath.Join(workDir, ".agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "mcp.json"), body, 0o644)
}

// findRepoRoot walks up from cwd until it sees go.mod.
func findRepoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := wd
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", os.ErrNotExist
}
