package e2e

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestLiveBinary_LoadsSkillsAndMCP builds the real `juex` binary, points
// it at a tempdir containing both a skill and an mcp.json that launches a
// real Python MCP server (via the project uv environment), and asserts that
// `juex run --dry-run --json` reports both pieces in the resulting plan.
//
// This complements TestEndToEnd_FullStack (in-process, mocked LLM) by
// proving the live binary subprocess wires everything correctly using a
// realistic MCP server (the official Python SDK — most MCP servers in
// the wild are Python).
func TestLiveBinary_LoadsSkillsAndMCP(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}

	bin := buildJuex(t)
	mcpScript := pythonMCPScript(t)
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	work := t.TempDir()
	if err := writeSkillFile(work, "trim-tool", "trim trailing whitespace"); err != nil {
		t.Fatal(err)
	}
	if err := writeMCPConfig(work, "uv", []string{"run", "--quiet", "--project", root, "python", mcpScript}); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(work, ".juex", "juex.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	configBody := "model: openai:m\n" +
		"providers:\n" +
		"  - id: openai\n" +
		"    base_url: https://example\n" +
		"    api_key: k\n" +
		"    models:\n" +
		"      - id: m\n"
	if err := os.WriteFile(configPath, []byte(configBody), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin,
		"--cwd", work,
		"--config", configPath,
		"run", "--dry-run", "--json", "x")
	home := t.TempDir()
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"USERPROFILE="+home,
		"CODEX_HOME="+filepath.Join(home, "missing-codex-home"),
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	out := stdout.Bytes()
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok || ee.ExitCode() != 10 {
			t.Fatalf("juex exit: %v\nstdout:\n%s\nstderr:\n%s", err, out, stderr.String())
		}
	}
	if strings.Contains(stderr.String(), "JUEX-FAKE-MCP-STDERR") {
		t.Fatalf("MCP server stderr leaked to CLI stderr:\n%s", stderr.String())
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

func TestLiveBinary_LoadsExtensionSkillsAndMCP(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}

	bin := buildJuex(t)
	mcpScript := pythonMCPScript(t)
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	work := t.TempDir()
	extDir := filepath.Join(work, ".juex", "extensions", "demo")
	if err := writeExtensionSkillFile(extDir, "ext-skill", "extension provided skill"); err != nil {
		t.Fatal(err)
	}
	if err := writeMCPConfigFile(
		filepath.Join(extDir, "mcp.json"),
		"extlocal",
		"uv",
		[]string{"run", "--quiet", "--project", root, "python", mcpScript},
	); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(work, ".juex", "juex.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	configBody := "model: openai:m\n" +
		"providers:\n" +
		"  - id: openai\n" +
		"    base_url: https://example\n" +
		"    api_key: k\n" +
		"    models:\n" +
		"      - id: m\n"
	if err := os.WriteFile(configPath, []byte(configBody), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin,
		"--cwd", work,
		"--config", configPath,
		"run", "--dry-run", "--json", "x")
	home := t.TempDir()
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"USERPROFILE="+home,
		"CODEX_HOME="+filepath.Join(home, "missing-codex-home"),
	)
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

	have := map[string]bool{}
	for _, name := range plan.Tools {
		have[name] = true
	}
	if !have["mcp__extlocal__echo"] {
		t.Errorf("mcp__extlocal__echo not in tool list (extension MCP server not loaded?). tools=%v", plan.Tools)
	}

	skillFound := false
	for _, s := range plan.Skills {
		if s.Name == "ext-skill" {
			skillFound = true
			wantPath := filepath.Join(extDir, "skills", "ext-skill", "SKILL.md")
			if s.Path != wantPath {
				t.Errorf("ext-skill path = %q, want %q", s.Path, wantPath)
			}
		}
	}
	if !skillFound {
		t.Errorf("ext-skill not in plan.skills (extension skills not loaded?). skills=%+v", plan.Skills)
	}
}

func TestLiveBinary_ModelFlagUsesUserGlobalProvider(t *testing.T) {
	bin := buildJuex(t)
	work := t.TempDir()
	home := t.TempDir()

	configPath := filepath.Join(home, ".juex", "juex.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	configBody := `model: openai:gpt-default
providers:
  - id: openai
    base_url: https://global.example
    api_key: sk-global
    models:
      - id: gpt-default
      - id: gpt-global
`
	if err := os.WriteFile(configPath, []byte(configBody), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin,
		"--cwd", work,
		"--model", "openai:gpt-global",
		"run", "--dry-run", "--json", "x")
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"USERPROFILE="+home,
		"CODEX_HOME="+filepath.Join(home, "missing-codex-home"),
	)
	out, err := cmd.Output()
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok || ee.ExitCode() != 10 {
			t.Fatalf("juex exit: %v\nstdout:\n%s\nstderr:\n%s", err, out, ee.Stderr)
		}
	}

	var plan struct {
		ProviderID string `json:"provider_id"`
		Model      string `json:"model"`
		BaseURL    string `json:"base_url"`
		WorkDir    string `json:"work_dir"`
	}
	if err := json.Unmarshal(out, &plan); err != nil {
		t.Fatalf("parse plan: %v\noutput:\n%s", err, out)
	}
	if plan.ProviderID != "openai" || plan.Model != "gpt-global" || plan.BaseURL != "https://global.example" || plan.WorkDir != work {
		t.Fatalf("plan = %+v", plan)
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
		`"name": "bundle"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("schema missing %q. full output:\n%s", want, body)
		}
	}
}

func TestLiveBinary_BundleCreatesRedactedArchive(t *testing.T) {
	bin := buildJuex(t)
	work := t.TempDir()
	home := t.TempDir()
	sessionID := "20260614T120000-e2ebundle"
	sessionDir := filepath.Join(work, ".juex", "sessions", sessionID)
	for name, body := range map[string]string{
		"session.json":       `{"kind":"primary"}`,
		"conversation.jsonl": `{"role":"user","blocks":[{"type":"text","text":"api_key=sk-e2e-secret"}]}` + "\n",
		"events.jsonl":       `{"type":"x","payload":{"token_usage":{"input_tokens":1}}}` + "\n",
		"logs/juex.log":      "Bearer e2e-token\n",
	} {
		path := filepath.Join(sessionDir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	outPath := filepath.Join(work, "debug.tar.gz")
	cmd := exec.Command(bin, "-C", work, "bundle", "--session", sessionID, "--out", outPath)
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"USERPROFILE="+home,
		"CODEX_HOME="+filepath.Join(home, "missing-codex-home"),
		"PROVIDER_API_KEY=sk-env-secret",
	)
	stdout, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("juex bundle: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, ee.Stderr)
		}
		t.Fatal(err)
	}
	var result struct {
		Path      string `json:"path"`
		SessionID string `json:"session_id"`
		Files     int    `json:"files"`
		Redacted  bool   `json:"redacted"`
	}
	if err := json.Unmarshal(stdout, &result); err != nil {
		t.Fatalf("parse result: %v\n%s", err, stdout)
	}
	if result.Path != outPath || result.SessionID != sessionID || result.Files == 0 || !result.Redacted {
		t.Fatalf("result = %+v", result)
	}
	files := readE2EBundleArchive(t, outPath)
	body := string(files["juex-debug-bundle/session/conversation.jsonl"]) + string(files["juex-debug-bundle/session/logs/juex.log"]) + string(files["juex-debug-bundle/runtime.json"])
	for _, leaked := range []string{"sk-e2e-secret", "e2e-token", "sk-env-secret"} {
		if strings.Contains(body, leaked) {
			t.Fatalf("bundle leaked %q:\n%s", leaked, body)
		}
	}
	if !strings.Contains(body, "[REDACTED]") {
		t.Fatalf("bundle missing redaction marker:\n%s", body)
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

func readE2EBundleArchive(t *testing.T, path string) map[string][]byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	files := map[string][]byte{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.FileInfo().IsDir() {
			continue
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		files[h.Name] = body
	}
	return files
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

func writeExtensionSkillFile(extensionDir, name, description string) error {
	dir := filepath.Join(extensionDir, "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	body := "---\nname: " + name + "\ndescription: " + description + "\ntype: model-invocable\n---\nFull skill body."
	return os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644)
}

func writeMCPConfig(workDir, command string, args []string) error {
	return writeMCPConfigFile(filepath.Join(workDir, ".agents", "mcp.json"), "local", command, args)
}

func writeMCPConfigFile(path, serverName, command string, args []string) error {
	cfg := map[string]any{
		"mcpServers": map[string]any{
			serverName: map[string]any{
				"command": command,
				"args":    args,
			},
		},
	}
	body, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, body, 0o644)
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
