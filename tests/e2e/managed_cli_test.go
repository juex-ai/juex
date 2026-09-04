package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/agentstate"
	"github.com/juex-ai/juex/internal/fleet"
	"github.com/juex-ai/juex/internal/fleetweb"
	"github.com/juex-ai/juex/internal/processmetrics"
	"github.com/juex-ai/juex/internal/thread"
)

type unavailableFleetMetrics struct{}

func (unavailableFleetMetrics) Sample(context.Context, string, int) (processmetrics.Usage, error) {
	return processmetrics.Usage{}, errors.New("metrics unavailable for test")
}

func TestManagedCLISelectorsAndResourceBoundaries(t *testing.T) {
	if testing.Short() {
		t.Skip("compiled-binary managed CLI test is slow")
	}
	bin := buildJuex(t)
	home := t.TempDir()
	workspace := t.TempDir()
	secondWorkspace := t.TempDir()

	stdout, stderr, err := runJuexHomeCommand(bin, home, "agent", "send", "--cwd", workspace, "hello")
	if processExitCode(err) != 3 || !strings.Contains(stderr, "juex agent add") {
		t.Fatalf("unregistered send exit=%d\nstdout:\n%s\nstderr:\n%s", processExitCode(err), stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(home, "agents")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unregistered send created Agent registry: %v", err)
	}

	stdout, stderr, err = runJuexHomeCommand(bin, home, "agent", "add", workspace, "--name", "managed")
	if err != nil {
		t.Fatalf("agent add: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	added, err := agentstate.ResolveExisting(agentstate.Options{HomeDir: home, WorkDir: workspace})
	if err != nil || !strings.Contains(stdout, added.Agent.ID) {
		t.Fatalf("agent add output: %v\n%s", err, stdout)
	}

	stdout, stderr, err = runJuexHomeCommand(bin, home, "agent", "show", "--agent", "managed", "--json")
	if err != nil || !strings.Contains(stdout, added.Agent.ID) || !strings.Contains(stdout, `"runtime_health": "stopped"`) {
		t.Fatalf("show by exact name: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}

	if stdout, stderr, err = runJuexHomeCommand(bin, home, "agent", "add", secondWorkspace, "--name", "managed"); err != nil {
		t.Fatalf("second agent add: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	stdout, stderr, err = runJuexHomeCommand(bin, home, "agent", "show", "--agent", "managed")
	if processExitCode(err) != 5 {
		t.Fatalf("ambiguous name exit=%d\nstdout:\n%s\nstderr:\n%s", processExitCode(err), stdout, stderr)
	}

	stdout, stderr, err = runJuexHomeCommand(bin, home, "agent", "show", "--agent", added.Agent.ID, "--cwd", workspace)
	if processExitCode(err) != 2 {
		t.Fatalf("competing selectors exit=%d\nstdout:\n%s\nstderr:\n%s", processExitCode(err), stdout, stderr)
	}
	stdout, stderr, err = runJuexHomeCommand(bin, home, "thread", "send", "0", "--cwd", workspace, "hello")
	if processExitCode(err) != 2 || !strings.Contains(stderr, "juex agent send") {
		t.Fatalf("Main routed through thread send exit=%d\nstdout:\n%s\nstderr:\n%s", processExitCode(err), stdout, stderr)
	}

	stdout, stderr, err = runJuexHomeCommand(bin, home, "fleet", "status", "--format", "json")
	if err != nil {
		t.Fatalf("fleet status: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	effectiveHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	var fleetStatus map[string]any
	if err := json.Unmarshal([]byte(stdout), &fleetStatus); err != nil || fleetStatus["effective_home"] != effectiveHome || fleetStatus["address"] == nil || fleetStatus["reachable"] == nil {
		t.Fatalf("fleet status output: %v\n%s", err, stdout)
	}
}

func TestManagedCLIResourceLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("compiled-binary managed CLI lifecycle is slow")
	}
	bin := buildJuex(t)
	home := t.TempDir()
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, ".juex"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFleetProviderConfig(t, workspace, "http://127.0.0.1:1")

	stdout, stderr, err := runJuexHomeCommand(bin, home, "agent", "add", workspace, "--name", "flow")
	if err != nil {
		t.Fatalf("agent add: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	registered, err := agentstate.ResolveExisting(agentstate.Options{HomeDir: home, WorkDir: workspace})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _, _ = runJuexHomeCommand(bin, home, "agent", "stop", "--agent", registered.Agent.ID)
	})

	stdout, stderr, err = runJuexHomeCommand(bin, home, "agent", "start", "--agent", "flow")
	if err != nil || !strings.Contains(stdout, "healthy") {
		t.Fatalf("agent start: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	stdout, stderr, err = runJuexHomeCommand(bin, home, "agent", "config", "--cwd", workspace, "--json")
	if err != nil || !strings.Contains(stdout, registered.Agent.ID) || !strings.Contains(stdout, "juex.yaml") || strings.Contains(stdout, "test-key") {
		t.Fatalf("agent config locator: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}

	stdout, stderr, err = runJuexHomeCommand(bin, home, "thread", "create", "--agent", registered.Agent.ID, "--alias", "worker")
	if err != nil {
		t.Fatalf("thread create: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	var created thread.Info
	if err := json.Unmarshal([]byte(stdout), &created); err != nil || !thread.ValidWorkerID(created.ID) {
		t.Fatalf("thread create output: %v\n%s", err, stdout)
	}
	stdout, stderr, err = runJuexHomeCommand(bin, home, "thread", "list", "--cwd", workspace, "--format", "json")
	if err != nil || !strings.Contains(stdout, created.ID) {
		t.Fatalf("thread list: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	stdout, stderr, err = runJuexHomeCommand(bin, home, "thread", "show", created.ID, "--agent", "flow", "--json")
	if err != nil || !strings.Contains(stdout, created.ID) {
		t.Fatalf("thread show: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	stdout, stderr, err = runJuexHomeCommand(bin, home, "thread", "rename", created.ID, "reviewer", "--cwd", workspace)
	if err != nil || !strings.Contains(stdout, "reviewer") {
		t.Fatalf("thread rename: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	stdout, stderr, err = runJuexHomeCommand(bin, home, "thread", "stop", "reviewer", "--agent", registered.Agent.ID)
	if err != nil || !strings.Contains(stdout, `"cancelled": false`) {
		t.Fatalf("thread stop: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	stdout, stderr, err = runJuexHomeCommand(bin, home, "agent", "show", "--agent", registered.Agent.ID, "--json")
	if err != nil || !strings.Contains(stdout, `"runtime_health": "healthy"`) {
		t.Fatalf("thread stop changed Agent Runtime: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}

	bundlePath := filepath.Join(t.TempDir(), "worker.tar.gz")
	stdout, stderr, err = runJuexHomeCommand(bin, home, "thread", "bundle", "reviewer", "--cwd", workspace, "--out", bundlePath)
	if err != nil {
		t.Fatalf("thread bundle: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if _, err := os.Stat(bundlePath); err != nil {
		t.Fatalf("thread bundle output: %v", err)
	}

	for _, operation := range []string{"archive", "unarchive", "archive"} {
		stdout, stderr, err = runJuexHomeCommand(bin, home, "thread", operation, "reviewer", "--agent", registered.Agent.ID)
		if err != nil {
			t.Fatalf("thread %s: %v\nstdout:\n%s\nstderr:\n%s", operation, err, stdout, stderr)
		}
	}
	stdout, stderr, err = runJuexHomeCommand(bin, home, "thread", "list", "--agent", registered.Agent.ID, "--archived", "--format", "json")
	if err != nil || !strings.Contains(stdout, created.ID) {
		t.Fatalf("archived thread list: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	stdout, stderr, err = runJuexHomeCommand(bin, home, "thread", "delete", "reviewer", "--cwd", workspace, "--yes")
	if err != nil || !strings.Contains(stdout, `"deleted": true`) {
		t.Fatalf("thread delete: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}

	stdout, stderr, err = runJuexHomeCommand(bin, home, "agent", "logs", "--agent", registered.Agent.ID, "--lines", "50")
	if err != nil || !strings.Contains(stdout, "agent endpoint listening") {
		t.Fatalf("agent logs: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	stdout, stderr, err = runJuexHomeCommand(bin, home, "agent", "stop", "--cwd", workspace)
	if err != nil || !strings.Contains(stdout, "stopped") {
		t.Fatalf("agent stop: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	stdout, stderr, err = runJuexHomeCommand(bin, home, "agent", "remove", "--agent", registered.Agent.ID, "--yes")
	if err != nil || !strings.Contains(stdout, "Removed "+registered.Agent.ID) {
		t.Fatalf("agent remove: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(home, "agents", registered.Agent.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed Agent state remains: %v", err)
	}
	if _, err := os.Stat(workspace); err != nil {
		t.Fatalf("Agent removal changed Workspace: %v", err)
	}
}

func TestManagedCLIManagesOrphanedAgentByExplicitSelector(t *testing.T) {
	if testing.Short() {
		t.Skip("compiled-binary managed CLI orphan lifecycle is slow")
	}
	bin := buildJuex(t)
	home := t.TempDir()
	workspaceRoot := t.TempDir()
	workspace := filepath.Join(workspaceRoot, "workspace")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, ".juex"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFleetProviderConfig(t, workspace, "http://127.0.0.1:1")

	stdout, stderr, err := runJuexHomeCommand(bin, home, "agent", "add", workspace, "--name", "orphan")
	if err != nil {
		t.Fatalf("agent add: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	registered, err := agentstate.ResolveExisting(agentstate.Options{HomeDir: home, WorkDir: workspace})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _, _ = runJuexHomeCommand(bin, home, "agent", "stop", "--agent", registered.Agent.ID)
	})
	if stdout, stderr, err = runJuexHomeCommand(bin, home, "agent", "start", "--agent", "orphan"); err != nil {
		t.Fatalf("agent start: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if err := os.Rename(workspace, filepath.Join(workspaceRoot, "moved-workspace")); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err = runJuexHomeCommand(bin, home, "agent", "show", "--agent", "orphan", "--json")
	if err != nil || !strings.Contains(stdout, `"binding": "orphaned"`) {
		t.Fatalf("show orphaned Agent: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	stdout, stderr, err = runJuexHomeCommand(bin, home, "agent", "stop", "--agent", "orphan")
	if err != nil || !strings.Contains(stdout, "stopped") {
		t.Fatalf("stop orphaned Agent: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	stdout, stderr, err = runJuexHomeCommand(bin, home, "agent", "disable", "--agent", "orphan")
	if err != nil || !strings.Contains(stdout, "enabled=false") {
		t.Fatalf("disable orphaned Agent: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	stdout, stderr, err = runJuexHomeCommand(bin, home, "agent", "remove", "--agent", "orphan", "--yes")
	if err != nil || !strings.Contains(stdout, "Removed "+registered.Agent.ID) {
		t.Fatalf("remove orphaned Agent: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
}

func TestFleetStatusReportsReachableSupervisorWhenMetricsUnavailable(t *testing.T) {
	if testing.Short() {
		t.Skip("compiled-binary Fleet status API test is slow")
	}
	bin := buildJuex(t)
	home := t.TempDir()
	manager, err := fleet.New(fleet.Options{HomeDir: home, Executable: bin})
	if err != nil {
		t.Fatal(err)
	}
	server, err := fleetweb.New(fleetweb.Options{
		Manager:        manager,
		ProcessMetrics: unavailableFleetMetrics{},
	})
	if err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(server.Handler())
	defer api.Close()
	if err := os.WriteFile(
		filepath.Join(home, "juex.yaml"),
		[]byte("fleet:\n  addr: "+strings.TrimPrefix(api.URL, "http://")+"\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := runJuexHomeCommand(bin, home, "fleet", "status", "--format", "json")
	if err != nil {
		t.Fatalf("fleet status: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	var status struct {
		Running   bool   `json:"running"`
		Reachable bool   `json:"reachable"`
		Problem   string `json:"problem"`
	}
	if err := json.Unmarshal([]byte(stdout), &status); err != nil {
		t.Fatal(err)
	}
	if !status.Running || !status.Reachable || !strings.Contains(status.Problem, "HTTP 503") {
		t.Fatalf("status = %+v\nstdout:\n%s", status, stdout)
	}
}
