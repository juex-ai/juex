package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/agentstate"
	"github.com/juex-ai/juex/internal/endpoint"
	"github.com/juex-ai/juex/internal/fleet"
)

func TestFleetStatusReportsRunningBinaryVersionSkew(t *testing.T) {
	if testing.Short() {
		t.Skip("compiled-binary fleet status is slow")
	}
	binary := buildJuex(t)
	home := t.TempDir()
	workspace := t.TempDir()
	agentDir := writeFleetE2EAgent(t, home, workspace, "aaaaaaaa")
	environment := fleetE2EEnvironment(home)

	binding, err := endpoint.Listen(context.Background(), agentDir, "0.0.0-old")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/identity", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(binding.Runtime()); err != nil {
			t.Errorf("encode endpoint identity: %v", err)
		}
	})
	server := &http.Server{Handler: mux}
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- server.Serve(binding.Listener())
	}()
	if err := binding.Publish(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = server.Close()
		_ = binding.Close()
		<-serveDone
	})

	stdout, stderr, err := runFleetE2E(binary, environment, "", "status", "--format", "json")
	if err != nil {
		t.Fatalf("fleet status: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	var statuses []fleet.AgentStatus
	if err := json.Unmarshal([]byte(stdout), &statuses); err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 ||
		statuses[0].RuntimeHealth != fleet.RuntimeHealthy ||
		statuses[0].BinaryVersion != "0.0.0-old" {
		t.Fatalf("statuses = %+v", statuses)
	}
	for _, want := range []string{"0.0.0-old", "not restarted automatically"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("version skew warning missing %q:\n%s", want, stderr)
		}
	}
}

func TestFleetServeUsesHomeAddressAndExplicitFlagWins(t *testing.T) {
	if testing.Short() {
		t.Skip("compiled-binary fleet serve is slow")
	}
	binary := buildJuex(t)
	home := t.TempDir()
	environment := fleetE2EEnvironment(home)

	defaulted := startFleetSupervisorWithArgs(t, binary, environment)
	if got := waitFleetWebReady(t, defaulted); got != "127.0.0.1:5839" {
		t.Fatalf("default fleet address = %q, want 127.0.0.1:5839", got)
	}
	killSupervisor(t, defaulted)

	configAddr := availableFleetAddress(t)
	if err := os.WriteFile(
		filepath.Join(home, "juex.yaml"),
		[]byte("fleet:\n  addr: "+configAddr+"\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	configured := startFleetSupervisorWithArgs(t, binary, environment)
	if got := waitFleetWebReady(t, configured); got != configAddr {
		t.Fatalf("configured fleet address = %q, want %q", got, configAddr)
	}
	killSupervisor(t, configured)

	explicit := startFleetSupervisorWithArgs(t, binary, environment, "--addr", "127.0.0.1:0")
	if got := waitFleetWebReady(t, explicit); got == configAddr {
		t.Fatalf("explicit --addr did not override configured address %q", configAddr)
	}
	killSupervisor(t, explicit)
}

func TestFleetLogsExplainsMissingLogForAdoptedAgent(t *testing.T) {
	if testing.Short() {
		t.Skip("compiled-binary fleet adoption is slow")
	}
	binary := buildJuex(t)
	home := t.TempDir()
	workspace := t.TempDir()
	agentID := "aaaaaaaa"
	agentDir := writeFleetE2EAgent(t, home, workspace, agentID)
	environment := fleetE2EEnvironment(home)

	standaloneOutput, err := os.Create(filepath.Join(t.TempDir(), "standalone.log"))
	if err != nil {
		t.Fatal(err)
	}
	standalone := exec.Command(binary, "-C", workspace, "serve", "--headless")
	standalone.Env = environment
	standalone.Stdout = standaloneOutput
	standalone.Stderr = standaloneOutput
	if err := standalone.Start(); err != nil {
		_ = standaloneOutput.Close()
		t.Fatal(err)
	}
	if err := standaloneOutput.Close(); err != nil {
		t.Fatal(err)
	}
	standaloneDone := make(chan error, 1)
	go func() { standaloneDone <- standalone.Wait() }()
	t.Cleanup(func() {
		runtimeState, readErr := endpoint.ReadRuntime(agentDir)
		if readErr == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = endpoint.RequestShutdown(ctx, runtimeState)
			cancel()
		}
		select {
		case <-standaloneDone:
		case <-time.After(3 * time.Second):
			_ = standalone.Process.Kill()
			<-standaloneDone
		}
	})

	runtimeState := waitFleetRuntime(t, agentDir)
	probeFleetRuntime(t, runtimeState)
	supervisor := startFleetSupervisor(t, binary, environment)
	t.Cleanup(func() {
		if supervisor.cmd.ProcessState == nil {
			_ = supervisor.cmd.Process.Kill()
			_ = supervisor.cmd.Wait()
		}
	})
	waitSupervisorReady(t, supervisor, "adopted")
	killSupervisor(t, supervisor)
	if _, err := os.Stat(filepath.Join(agentDir, "logs", "fleet.log")); !os.IsNotExist(err) {
		t.Fatalf("standalone serve unexpectedly created fleet.log: %v", err)
	}

	stdout, stderr, err := runFleetE2E(binary, environment, "", "logs", agentID)

	if exitCode(err) != 3 {
		t.Fatalf("fleet logs exit = %d, want 3\nstdout:\n%s\nstderr:\n%s", exitCode(err), stdout, stderr)
	}
	for _, want := range []string{
		"no fleet-owned log is available",
		"started externally",
		"terminal",
		"service logs",
		"stdout/stderr redirection",
	} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("fleet logs stderr missing %q:\n%s", want, stderr)
		}
	}
	for _, unwanted := range []string{"open ", "no such file"} {
		if strings.Contains(stderr, unwanted) {
			t.Fatalf("fleet logs stderr contains raw filesystem error %q:\n%s", unwanted, stderr)
		}
	}
	if stdout != "" {
		t.Fatalf("fleet logs stdout = %q, want empty", stdout)
	}
	if _, err := os.Stat(filepath.Join(agentDir, "logs", "fleet.log")); !os.IsNotExist(err) {
		t.Fatalf("fleet logs command created fleet.log: %v", err)
	}
}

func TestFleetLifecycleAndSupervisorAdoption(t *testing.T) {
	if testing.Short() {
		t.Skip("compiled-binary fleet lifecycle is slow")
	}
	binary := buildJuex(t)
	home := t.TempDir()
	workspace := t.TempDir()
	agentID := "aaaaaaaa"
	agentDir := writeFleetE2EAgent(t, home, workspace, agentID)
	environment := fleetE2EEnvironment(home)

	t.Cleanup(func() {
		runtimeState, err := endpoint.ReadRuntime(agentDir)
		if err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = endpoint.RequestShutdown(ctx, runtimeState)
		cancel()
		process, _ := os.FindProcess(runtimeState.PID)
		_ = process.Kill()
	})

	if stdout, stderr, err := runFleetE2E(binary, environment, "", "start", agentID); err != nil {
		t.Fatalf("fleet start: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	firstRuntime := waitFleetRuntime(t, agentDir)
	probeFleetRuntime(t, firstRuntime)
	waitFleetHealth(t, binary, environment, agentID, fleet.RuntimeHealthy)

	stdout, stderr, err := runFleetE2E(binary, environment, "", "logs", agentID, "--lines", "50")
	if err != nil {
		t.Fatalf("fleet logs: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, "juex serve agent endpoint listening") {
		t.Fatalf("fleet log missing serve readiness:\n%s", stdout)
	}

	if stdout, stderr, err := runFleetE2E(binary, environment, "", "restart", agentID); err != nil {
		t.Fatalf("fleet restart: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	secondRuntime := waitFleetRuntime(t, agentDir)
	if secondRuntime.InstanceID == firstRuntime.InstanceID {
		t.Fatalf("restart reused runtime instance id %q", secondRuntime.InstanceID)
	}
	probeFleetRuntime(t, secondRuntime)

	if stdout, stderr, err := runFleetE2E(binary, environment, "", "stop", agentID); err != nil {
		t.Fatalf("fleet stop: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	waitFleetHealth(t, binary, environment, agentID, fleet.RuntimeStopped)

	firstSupervisor := startFleetSupervisor(t, binary, environment)
	waitSupervisorReady(t, firstSupervisor, "started")
	supervisedRuntime := waitFleetRuntime(t, agentDir)
	probeFleetRuntime(t, supervisedRuntime)
	killSupervisor(t, firstSupervisor)
	probeFleetRuntime(t, supervisedRuntime)

	secondSupervisor := startFleetSupervisor(t, binary, environment)
	waitSupervisorReady(t, secondSupervisor, "adopted")
	adoptedRuntime := waitFleetRuntime(t, agentDir)
	if !adoptedRuntime.Matches(supervisedRuntime) {
		t.Fatalf("supervisor restart replaced adopted runtime: before=%+v after=%+v", supervisedRuntime, adoptedRuntime)
	}
	killSupervisor(t, secondSupervisor)
	probeFleetRuntime(t, adoptedRuntime)

	process, err := os.FindProcess(adoptedRuntime.PID)
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Kill(); err != nil {
		t.Fatalf("force kill agent: %v", err)
	}
	waitFleetHealth(t, binary, environment, agentID, fleet.RuntimeUnhealthy)

	if err := os.RemoveAll(workspace); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err = runFleetE2E(binary, environment, "n\n", "gc")
	if err != nil {
		t.Fatalf("fleet gc deny: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if _, err := os.Stat(agentDir); err != nil {
		t.Fatalf("denied GC removed agent directory: %v", err)
	}
	stdout, stderr, err = runFleetE2E(binary, environment, "", "gc", "--yes")
	if err != nil {
		t.Fatalf("fleet gc --yes: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if _, err := os.Stat(agentDir); !os.IsNotExist(err) {
		t.Fatalf("confirmed GC preserved agent directory: %v", err)
	}
}

type fleetSupervisor struct {
	cmd    *exec.Cmd
	lines  <-chan string
	stderr *bytes.Buffer
}

func startFleetSupervisor(t *testing.T, binary string, environment []string) *fleetSupervisor {
	t.Helper()
	return startFleetSupervisorWithArgs(t, binary, environment, "--addr", "127.0.0.1:0")
}

func startFleetSupervisorWithArgs(t *testing.T, binary string, environment []string, args ...string) *fleetSupervisor {
	t.Helper()
	commandArgs := append([]string{"fleet", "serve"}, args...)
	command := exec.Command(binary, commandArgs...)
	command.Env = environment
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatalf("start fleet supervisor: %v", err)
	}
	lines := make(chan string, 32)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()
	return &fleetSupervisor{cmd: command, lines: lines, stderr: &stderr}
}

func availableFleetAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

func waitFleetWebReady(t *testing.T, supervisor *fleetSupervisor) string {
	t.Helper()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	const prefix = "juex fleet listening on http://"
	for {
		select {
		case line, ok := <-supervisor.lines:
			if !ok {
				t.Fatalf("fleet web exited before ready; stderr:\n%s", supervisor.stderr.String())
			}
			if strings.HasPrefix(line, prefix) {
				return strings.TrimPrefix(line, prefix)
			}
		case <-deadline.C:
			_ = supervisor.cmd.Process.Kill()
			t.Fatalf("fleet web did not become ready; stderr:\n%s", supervisor.stderr.String())
		}
	}
}

func waitSupervisorReady(t *testing.T, supervisor *fleetSupervisor, expectedAction string) {
	t.Helper()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	seenAction := false
	for {
		select {
		case line, ok := <-supervisor.lines:
			if !ok {
				t.Fatalf("fleet supervisor exited before ready; stderr:\n%s", supervisor.stderr.String())
			}
			if strings.Contains(line, ": "+expectedAction+":") {
				seenAction = true
			}
			if strings.Contains(line, "fleet: ready:") {
				if !seenAction {
					t.Fatalf("fleet supervisor became ready without %s action", expectedAction)
				}
				return
			}
		case <-deadline.C:
			_ = supervisor.cmd.Process.Kill()
			t.Fatalf("fleet supervisor did not become ready; stderr:\n%s", supervisor.stderr.String())
		}
	}
}

func killSupervisor(t *testing.T, supervisor *fleetSupervisor) {
	t.Helper()
	if err := supervisor.cmd.Process.Kill(); err != nil {
		t.Fatalf("kill fleet supervisor: %v", err)
	}
	if err := supervisor.cmd.Wait(); err == nil {
		t.Fatal("killed fleet supervisor exited successfully")
	}
}

func waitFleetRuntime(t *testing.T, agentDir string) endpoint.Runtime {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		runtimeState, err := endpoint.ReadRuntime(agentDir)
		if err == nil {
			return runtimeState
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("runtime metadata did not appear under %s", agentDir)
	return endpoint.Runtime{}
}

func probeFleetRuntime(t *testing.T, runtimeState endpoint.Runtime) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := endpoint.Probe(ctx, runtimeState); err != nil {
		t.Fatalf("probe runtime %+v: %v", runtimeState, err)
	}
}

func waitFleetHealth(
	t *testing.T,
	binary string,
	environment []string,
	agentID string,
	want fleet.RuntimeHealth,
) fleet.AgentStatus {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var last []fleet.AgentStatus
	var lastErr error
	for time.Now().Before(deadline) {
		stdout, _, err := runFleetE2E(binary, environment, "", "status", "--format", "json")
		if err == nil {
			if decodeErr := json.Unmarshal([]byte(stdout), &last); decodeErr == nil &&
				len(last) == 1 &&
				last[0].ID == agentID &&
				last[0].RuntimeHealth == want {
				return last[0]
			} else if decodeErr != nil {
				lastErr = decodeErr
			}
		} else {
			lastErr = err
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("fleet health did not become %s; last=%+v err=%v", want, last, lastErr)
	return fleet.AgentStatus{}
}

func runFleetE2E(
	binary string,
	environment []string,
	stdin string,
	args ...string,
) (string, string, error) {
	command := exec.Command(binary, append([]string{"fleet"}, args...)...)
	command.Env = environment
	command.Stdin = strings.NewReader(stdin)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	return stdout.String(), stderr.String(), err
}

func fleetE2EEnvironment(home string) []string {
	environment := filteredEnv(
		"HOME",
		"USERPROFILE",
		"JUEX_HOME",
		"GIT_CONFIG_GLOBAL",
		"GIT_CONFIG_NOSYSTEM",
		"PROVIDER_API_ID",
		"PROVIDER_API_BASE",
		"PROVIDER_API_KEY",
		"PROVIDER_API_MODEL",
	)
	return append(
		environment,
		"HOME="+home,
		"USERPROFILE="+home,
		"JUEX_HOME="+home,
		"GIT_CONFIG_GLOBAL="+filepath.Join(home, "gitconfig"),
		"GIT_CONFIG_NOSYSTEM=1",
		"PROVIDER_API_ID=openai",
		"PROVIDER_API_BASE=http://127.0.0.1:1",
		"PROVIDER_API_KEY=test-key",
		"PROVIDER_API_MODEL=test-model",
	)
}

func writeFleetE2EAgent(t *testing.T, home, workspace, id string) string {
	t.Helper()
	agentDir := filepath.Join(home, "agents", id)
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	agent := agentstate.Agent{
		ID:        id,
		Name:      "fleet-e2e",
		Workspace: workspace,
		Enabled:   true,
		Autostart: true,
		CreatedAt: time.Now().UTC(),
	}
	writeFleetE2EJSON(t, filepath.Join(agentDir, "agent.json"), agent)
	if err := os.MkdirAll(filepath.Join(workspace, ".juex"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFleetE2EJSON(
		t,
		filepath.Join(workspace, ".juex", "juex.local.json"),
		agentstate.Marker{AgentID: id},
	)
	return agentDir
}

func writeFleetE2EJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
