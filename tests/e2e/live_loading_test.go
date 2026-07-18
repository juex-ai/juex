package e2e

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/endpoint"
	"github.com/juex-ai/juex/internal/observable"
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
	builtinFound := map[string]bool{
		"juex-chunked-write": false,
		"juex-observables":   false,
		"juex-session-state": false,
	}
	for _, skill := range plan.Skills {
		if _, ok := builtinFound[skill.Name]; !ok {
			continue
		}
		if skill.Path != "builtin://skills/"+skill.Name+"/SKILL.md" {
			t.Errorf("builtin skill path = %q for %s", skill.Path, skill.Name)
		}
		builtinFound[skill.Name] = true
	}
	for name, found := range builtinFound {
		if !found {
			t.Errorf("builtin skill %s not in compiled-binary plan: %+v", name, plan.Skills)
		}
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

func TestLiveBinary_MigratesAndRebindsAgentState(t *testing.T) {
	bin := buildJuex(t)
	root := t.TempDir()
	home := filepath.Join(root, "home")
	work := filepath.Join(root, "workspace")
	sessionID := "20260717T120000-migrate1"
	legacySession := filepath.Join(work, ".juex", "sessions", sessionID)
	observationTime := time.Date(2026, 7, 18, 8, 0, 0, 0, time.UTC)
	legacyObservableRoot := filepath.Join(work, ".juex", "observables")
	observation := observable.ObservationRecord{
		ID:            "obs-live-migration",
		ObservableID:  "daily-brief",
		SourceEventID: "schedule:daily-brief:2026-07-18T08:00:00Z",
		Kind:          "reminder",
		Severity:      "info",
		WindowStart:   observationTime,
		WindowEnd:     observationTime,
		Content:       "migrated daily brief",
		OriginalChars: 20,
		State:         observable.ObservationStateDelivered,
		TargetSession: sessionID,
		CreatedAt:     observationTime,
		DeliveredAt:   observationTime.Add(time.Second),
	}
	observationData, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	scheduleStateData, err := json.Marshal(observable.ScheduleStateRecord{
		ObservableID:           observation.ObservableID,
		LastEvaluatedAt:        observationTime,
		LastEmittedScheduledAt: observationTime,
		UpdatedAt:              observationTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{
		filepath.Join(legacySession, "conversation.jsonl"):          `{"role":"user","blocks":[{"type":"text","text":"migrated"}]}` + "\n",
		filepath.Join(work, ".juex", "memory", "MEMORY.md"):         "# migrated memory\n",
		filepath.Join(work, ".juex", "history.json"):                "{\"sessions\":[]}\n",
		filepath.Join(work, ".juex", "juex.yaml"):                   "{}\n",
		filepath.Join(work, ".juex", "observables.json"):            "{\"observables\":[]}\n",
		filepath.Join(legacyObservableRoot, "observations.jsonl"):   string(observationData) + "\n",
		filepath.Join(legacyObservableRoot, "schedule_state.jsonl"): string(scheduleStateData) + "\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	stdout, stderr, err := runAgentStateCommand(bin, home, work, "sessions", "list")
	if err != nil {
		t.Fatalf("sessions list after migration: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, sessionID) || !strings.Contains(stderr, "migrated workspace runtime state") {
		t.Fatalf("migration output missing evidence\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	var marker struct {
		AgentID string `json:"agent_id"`
	}
	markerData, err := os.ReadFile(filepath.Join(work, ".juex", "juex.local.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(markerData, &marker); err != nil {
		t.Fatal(err)
	}
	agentDir := filepath.Join(home, "agents", marker.AgentID)
	for _, path := range []string{
		filepath.Join(agentDir, "sessions", sessionID, "conversation.jsonl"),
		filepath.Join(agentDir, "memory", "MEMORY.md"),
		filepath.Join(agentDir, "history.json"),
		filepath.Join(agentDir, "observables", "observations.jsonl"),
		filepath.Join(agentDir, "observables", "schedule_state.jsonl"),
		filepath.Join(work, ".juex", "juex.yaml"),
		filepath.Join(work, ".juex", "observables.json"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected migrated or retained path %s: %v", path, err)
		}
	}
	for _, path := range []string{
		filepath.Join(work, ".juex", "sessions"),
		filepath.Join(work, ".juex", "memory"),
		filepath.Join(work, ".juex", "history.json"),
		filepath.Join(work, ".juex", "observables"),
	} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("legacy state remains at %s: %v", path, err)
		}
	}
	observationStore := observable.NewStore(filepath.Join(agentDir, "observables"), observable.StoreOptions{})
	migratedObservation, ok, err := observationStore.Observation(observation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || migratedObservation.State != observable.ObservationStateDelivered ||
		migratedObservation.SourceEventID != observation.SourceEventID ||
		migratedObservation.TargetSession != sessionID {
		t.Fatalf("migrated observation = %+v ok=%v", migratedObservation, ok)
	}
	existing, created, err := observationStore.RecordObservationOnce(observable.ObservationRecord{
		ID:            "duplicate-live-migration",
		ObservableID:  observation.ObservableID,
		SourceEventID: observation.SourceEventID,
		Content:       "must not be appended",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created || existing.ID != observation.ID || existing.State != observable.ObservationStateDelivered {
		t.Fatalf("migrated dedupe = %+v created=%v", existing, created)
	}
	observations, err := observationStore.ListObservations(observable.ObservationFilter{ObservableID: observation.ObservableID})
	if err != nil {
		t.Fatal(err)
	}
	if len(observations) != 1 {
		t.Fatalf("observations after migrated dedupe = %+v", observations)
	}
	scheduleStates, err := observationStore.LatestScheduleStates()
	if err != nil {
		t.Fatal(err)
	}
	scheduleState, ok := scheduleStates[observation.ObservableID]
	if !ok || !scheduleState.LastEvaluatedAt.Equal(observationTime) ||
		!scheduleState.LastEmittedScheduledAt.Equal(observationTime) {
		t.Fatalf("migrated schedule state = %+v ok=%v", scheduleState, ok)
	}

	moved := filepath.Join(root, "moved-workspace")
	if err := os.Rename(work, moved); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err = runAgentStateCommand(bin, home, moved, "sessions", "list")
	if err != nil {
		t.Fatalf("sessions list after move: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, sessionID) || !strings.Contains(stderr, "workspace for agent") || !strings.Contains(stderr, "moved") {
		t.Fatalf("move output missing evidence\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(moved, ".juex", "observables.json")); err != nil {
		t.Fatalf("moved workspace observable config: %v", err)
	}
	movedObservation, ok, err := observationStore.Observation(observation.ID)
	if err != nil || !ok || movedObservation.SourceEventID != observation.SourceEventID {
		t.Fatalf("observation after move = %+v ok=%v err=%v", movedObservation, ok, err)
	}

	copied := filepath.Join(root, "copied-workspace")
	if err := os.MkdirAll(filepath.Join(copied, ".juex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(copied, ".juex", "juex.local.json"), markerData, 0o644); err != nil {
		t.Fatal(err)
	}
	_, stderr, err = runAgentStateCommand(bin, home, copied, "sessions", "list")
	if err == nil {
		t.Fatal("copied workspace unexpectedly reused the original identity")
	}
	if !strings.Contains(stderr, "appears to be a copy") || !strings.Contains(stderr, "remove") {
		t.Fatalf("copy error is not actionable:\n%s", stderr)
	}
}

func TestLiveBinary_EndpointOnlyServeHasNoExtraTCPListener(t *testing.T) {
	bin := buildJuex(t)
	for _, test := range []struct {
		name               string
		args               []string
		scannerUnavailable bool
	}{
		{name: "flagless"},
		{name: "explicit headless", args: []string{"--headless"}},
		{name: "listener scanner unavailable", scannerUnavailable: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			process := startLiveServe(t, bin, test.args...)
			defer process.stop()

			target, err := endpoint.Parse(process.runtime.Endpoint)
			if err != nil {
				t.Fatal(err)
			}
			if test.scannerUnavailable {
				t.Setenv("PATH", t.TempDir())
			}
			assertProcessTCPListeners(t, process.cmd.Process.Pid, target)
			waitForServeOutput(t, process.stdout, "juex serve agent endpoint listening on ")
			if strings.Contains(process.stdout.String(), "agent JSON/SSE API (no web UI)") {
				t.Fatalf("endpoint-only serve reported a TCP API:\n%s", process.stdout.String())
			}

			client := target.NewClient()
			for path, want := range map[string]int{"/healthz": http.StatusOK, "/": http.StatusNotFound} {
				request, err := http.NewRequest(http.MethodGet, target.URL(path), nil)
				if err != nil {
					t.Fatal(err)
				}
				requestCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				response, err := client.Do(request.WithContext(requestCtx))
				cancel()
				if err != nil {
					t.Fatalf("GET %s through %s: %v", path, process.runtime.Endpoint, err)
				}
				_ = response.Body.Close()
				if response.StatusCode != want {
					t.Fatalf("GET %s status = %d, want %d", path, response.StatusCode, want)
				}
			}
		})
	}
}

func TestLiveBinary_ExplicitServeTCPHasFriendlyPointer(t *testing.T) {
	bin := buildJuex(t)
	process := startLiveServe(t, bin, "--addr", "127.0.0.1:0")
	defer process.stop()

	target, err := endpoint.Parse(process.runtime.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	tcpAddress := waitForServeTCPAddress(t, process.stdout)
	assertProcessTCPListeners(t, process.cmd.Process.Pid, target, tcpAddress)

	for path, want := range map[string]int{
		"/healthz":          http.StatusOK,
		"/":                 http.StatusOK,
		"/some-browser-url": http.StatusOK,
		"/api/not-a-route":  http.StatusNotFound,
	} {
		response, err := http.Get("http://" + tcpAddress + path)
		if err != nil {
			t.Fatalf("GET TCP %s: %v", path, err)
		}
		body, readErr := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if response.StatusCode != want {
			t.Fatalf("GET TCP %s status = %d, want %d: %s", path, response.StatusCode, want, body)
		}
		if want == http.StatusOK && path != "/healthz" {
			for _, pointer := range []string{"agent JSON/SSE API", "no web UI", "juex fleet serve", "127.0.0.1:5839"} {
				if !strings.Contains(string(body), pointer) {
					t.Fatalf("GET TCP %s body missing %q:\n%s", path, pointer, body)
				}
			}
		}
	}
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(data)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type liveServeProcess struct {
	cmd     *exec.Cmd
	done    chan error
	stdout  *lockedBuffer
	stderr  *lockedBuffer
	runtime endpoint.Runtime
	once    sync.Once
}

func startLiveServe(t *testing.T, bin string, serveArgs ...string) *liveServeProcess {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	work := filepath.Join(root, "workspace")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(work, "juex.yaml")
	configBody := "model: openai:test-model\n" +
		"providers:\n" +
		"  - id: openai\n" +
		"    base_url: https://example.invalid\n" +
		"    api_key: test-key\n" +
		"    models:\n" +
		"      - id: test-model\n"
	if err := os.WriteFile(configFile, []byte(configBody), 0o644); err != nil {
		t.Fatal(err)
	}

	commandArgs := append([]string{"-C", work, "--config", configFile, "serve"}, serveArgs...)
	cmd := exec.Command(bin, commandArgs...)
	cmd.Env = filteredEnv(
		"HOME", "USERPROFILE", "JUEX_HOME", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_NOSYSTEM",
	)
	cmd.Env = append(cmd.Env,
		"HOME="+home,
		"USERPROFILE="+home,
		"JUEX_HOME="+home,
		"GIT_CONFIG_GLOBAL="+filepath.Join(home, "gitconfig"),
		"GIT_CONFIG_NOSYSTEM=1",
	)
	stdout := &lockedBuffer{}
	stderr := &lockedBuffer{}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var runtimeState endpoint.Runtime
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf(
				"serve exited before readiness: %v\nstdout:\n%s\nstderr:\n%s",
				err,
				stdout.String(),
				stderr.String(),
			)
		default:
		}
		markerData, err := os.ReadFile(filepath.Join(work, ".juex", "juex.local.json"))
		if err == nil {
			var marker struct {
				AgentID string `json:"agent_id"`
			}
			if json.Unmarshal(markerData, &marker) == nil && marker.AgentID != "" {
				runtimeState, err = endpoint.ReadRuntime(filepath.Join(home, "agents", marker.AgentID))
				if err == nil {
					break
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if runtimeState.Endpoint == "" {
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("runtime endpoint was not published\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}
	return &liveServeProcess{
		cmd:     cmd,
		done:    done,
		stdout:  stdout,
		stderr:  stderr,
		runtime: runtimeState,
	}
}

func (p *liveServeProcess) stop() {
	p.once.Do(func() {
		_ = p.cmd.Process.Kill()
		<-p.done
	})
}

func waitForServeOutput(t *testing.T, output *lockedBuffer, want string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		body := output.String()
		if strings.Contains(body, want) {
			return body
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("serve output missing %q:\n%s", want, output.String())
	return ""
}

func waitForServeTCPAddress(t *testing.T, output *lockedBuffer) string {
	t.Helper()
	const prefix = "juex serve agent JSON/SSE API (no web UI) listening on http://"
	body := waitForServeOutput(t, output, prefix)
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	t.Fatalf("serve output did not contain a parseable TCP address:\n%s", body)
	return ""
}

var errProcessListenerScanUnavailable = errors.New("process TCP listener scan unavailable")

func assertProcessTCPListeners(t *testing.T, pid int, endpointTarget endpoint.Target, additional ...string) {
	t.Helper()
	want := append([]string(nil), additional...)
	if endpointTarget.Network() == "tcp" {
		want = append(want, endpointTarget.Address())
	}
	deadline := time.Now().Add(5 * time.Second)
	var (
		got     []string
		scanErr error
	)
	for time.Now().Before(deadline) {
		got, scanErr = processTCPListeners(pid)
		if errors.Is(scanErr, errProcessListenerScanUnavailable) {
			t.Logf("skipping process listener scan: %v", scanErr)
			return
		}
		if scanErr == nil && sameStringSet(got, want) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("TCP listeners for pid %d = %v, want %v (scan error: %v)", pid, got, want, scanErr)
}

func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	counts := make(map[string]int, len(left))
	for _, item := range left {
		counts[item]++
	}
	for _, item := range right {
		counts[item]--
		if counts[item] < 0 {
			return false
		}
	}
	return true
}

func runAgentStateCommand(bin, home, work string, args ...string) (string, string, error) {
	commandArgs := append([]string{"-C", work}, args...)
	cmd := exec.Command(bin, commandArgs...)
	cmd.Env = filteredEnv(
		"HOME", "USERPROFILE", "JUEX_HOME", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_NOSYSTEM",
	)
	cmd.Env = append(cmd.Env,
		"HOME="+home,
		"USERPROFILE="+home,
		"JUEX_HOME="+home,
		"GIT_CONFIG_GLOBAL="+filepath.Join(home, "gitconfig"),
		"GIT_CONFIG_NOSYSTEM=1",
	)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func filteredEnv(remove ...string) []string {
	removed := make(map[string]struct{}, len(remove))
	for _, key := range remove {
		removed[key] = struct{}{}
	}
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, skip := removed[key]; !skip {
			env = append(env, entry)
		}
	}
	return env
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
