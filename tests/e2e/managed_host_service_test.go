//go:build postgres && native_service

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/app/managed"
	serverrpc "github.com/juex-ai/juex/internal/entrypoints/platformrpc"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/execution/host"
	"github.com/juex-ai/juex/internal/execution/hostservice"
	"github.com/juex-ai/juex/internal/execution/native"
	executionrpc "github.com/juex-ai/juex/internal/execution/rpc"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/lifecycle"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/management"
)

// This test starts only temporary services. Both binaries and PostgreSQL must
// be explicitly provided; it never discovers or restarts a user's deployment.
func TestManagedHostServicesPreserveTwoAgentsAcrossExecutionRestart(t *testing.T) {
	executorBinary, executionBinary := os.Getenv("JUEX_EXECUTOR_BINARY"), os.Getenv("JUEX_EXECUTION_BINARY")
	if !filepath.IsAbs(executorBinary) || !filepath.IsAbs(executionBinary) {
		t.Fatal("candidate Execution and executor binary paths are required")
	}
	f := executionDatabase(t)
	ctx := context.Background()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"workspace", "control", "maintenance"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "maintenance/admission.lock"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	reserve := func() (string, string) {
		l, err := net.Listen("tcp", "0.0.0.0:0")
		if err != nil {
			t.Fatal(err)
		}
		_, port, _ := net.SplitHostPort(l.Addr().String())
		if err := l.Close(); err != nil {
			t.Fatal(err)
		}
		return "0.0.0.0:" + port, "127.0.0.1:" + port
	}
	rpcBind, rpcAddress := reserve()
	deviceBind, deviceAddress := reserve()
	managementListener := platformListener(t)
	managementServer, err := serverrpc.NewManagement(managementListener, platformrpc.CredentialsAt(f.credentials, "management"), f.authority)
	if err != nil {
		t.Fatal(err)
	}
	runPlatformRPC(t, managementServer)
	configuration := managed.HostConfiguration{Backend: host.Config{Root: filepath.Join(root, "workspace"), ControlRoot: filepath.Join(root, "control"), Identity: uuid.NewString(), Executable: executorBinary, Server: "http://" + deviceAddress, InsecureHTTP: true}, KeyFile: filepath.Join(root, "key"), IdleSeconds: 300}
	if err := os.WriteFile(configuration.KeyFile, make([]byte, 32), 0600); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(configuration)
	if err := os.WriteFile(filepath.Join(root, "host.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	client, err := executionrpc.NewClient(rpcAddress, platformrpc.CredentialsAt(f.credentials, "management"))
	if err != nil {
		t.Fatal(err)
	}
	var process *exec.Cmd
	var exited chan error
	stop := func() {
		if process == nil {
			return
		}
		if err := process.Process.Signal(syscall.SIGTERM); err != nil {
			t.Error(err)
		}
		select {
		case err := <-exited:
			if err != nil {
				t.Error("Execution exit", err)
			}
		case <-time.After(20 * time.Second):
			_ = process.Process.Kill()
			<-exited
			t.Error("Execution did not stop gracefully")
		}
		process = nil
	}
	start := func() {
		log, err := os.OpenFile(filepath.Join(root, "execution.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			t.Fatal(err)
		}
		process = exec.Command(executionBinary, "--management", managementListener.Addr().String(), "--credentials", f.credentials, "serve", "--listen", rpcBind, "--device-listen", deviceBind, "--host-config", filepath.Join(root, "host.json"), "--blob-root", filepath.Join(root, "blobs"))
		for _, value := range os.Environ() {
			if !strings.HasPrefix(value, "JUEX_") {
				process.Env = append(process.Env, value)
			}
		}
		process.Env = append(process.Env, "JUEX_DATABASE_URL="+f.pool.Config().ConnString(), "JUEX_MAINTENANCE_DIR="+filepath.Join(root, "maintenance"))
		process.Stdout, process.Stderr = log, log
		if err := process.Start(); err != nil {
			_ = log.Close()
			t.Fatal(err)
		}
		exited = make(chan error, 1)
		cmd, done := process, exited
		go func() { err := cmd.Wait(); _ = log.Close(); done <- err }()
		runtimeEventually(t, func() bool { return client.Health(ctx) == nil })
	}
	t.Cleanup(stop)
	start()
	agent2, err := f.directory.CreateAgent(ctx, f.actor, f.tenant, f.actor, management.AgentConfig{Name: "Second managed Host"})
	if err != nil {
		t.Fatal(err)
	}
	type device struct {
		agent   string
		env     execprotocol.Environment
		manager *hostservice.Manager
		status  hostservice.Status
	}
	var devices []device
	var ownedEnvironments []string
	t.Cleanup(func() {
		stop()
		for _, id := range ownedEnvironments {
			control := filepath.Join(configuration.Backend.ControlRoot, id)
			if _, err := os.Stat(control); os.IsNotExist(err) {
				continue
			}
			manager, err := hostservice.New(control, executorBinary)
			if err != nil {
				t.Error(err)
				continue
			}
			if err := manager.Remove(ctx); err != nil {
				t.Error("remove owned Host service", err)
			}
		}
	})
	wait := func(agent, environment, id string, ready func(execution.Operation) bool) execution.Operation {
		var op execution.Operation
		runtimeEventually(t, func() bool {
			var err error
			op, err = client.Operation(ctx, f.actor, f.tenant, agent, environment, id, 0, 64<<10)
			return err == nil && ready(op)
		})
		return op
	}
	submit := func(agent, environment, id, kind string, args any) {
		r := nativeRequest(t, id, kind, args)
		r.AgentID = agent
		if _, err := client.Submit(ctx, f.actor, f.tenant, environment, r, 0); err != nil {
			t.Fatal(err)
		}
	}
	for _, agent := range []string{f.agent.ID, agent2.ID} {
		envs, err := client.Environments(ctx, f.actor, f.tenant, agent)
		if err != nil || len(envs) != 1 || !envs[0].Default || envs[0].Kind != "native" {
			t.Fatal(envs, err)
		}
		env := envs[0]
		ownedEnvironments = append(ownedEnvironments, env.ID)
		submit(agent, env.ID, "host-home", "exec_command", native.CommandArguments{Command: `printf '%s\n' "$HOME" "$PWD"; printf own > "$HOME/proof"; printf own > proof`})
		op := wait(agent, env.ID, "host-home", func(op execution.Operation) bool { return op.Acknowledged })
		home := filepath.Join(configuration.Backend.Root, env.ID, "home")
		if op.State != "completed" || op.Snapshot.Text() != home+"\n"+env.WorkingDirectory+"\n" {
			t.Fatal("Host cwd/HOME", op.State, op.Snapshot.Text())
		}
		m, err := hostservice.New(filepath.Join(configuration.Backend.ControlRoot, env.ID), executorBinary)
		if err != nil {
			t.Fatal(err)
		}
		status, err := m.Status()
		if err != nil || !status.Running {
			t.Fatal(status, err)
		}
		devices = append(devices, device{agent: agent, env: env, manager: m, status: status})
		submit(agent, env.ID, "host-file", "write", native.FileArguments{Path: "native-file", Content: agent})
		file := wait(agent, env.ID, "host-file", func(op execution.Operation) bool { return op.Acknowledged })
		if file.State != "completed" {
			t.Fatal("native file operation", file.State, file.Snapshot.Error)
		}
		if data, err := os.ReadFile(filepath.Join(env.WorkingDirectory, "native-file")); err != nil || string(data) != agent {
			t.Fatal(string(data), err)
		}
		submit(agent, env.ID, "host-hook", "run_hook", execprotocol.HookCommand{Command: []string{"/bin/sh", "-c", `printf '%s' "$HOME"`}, Input: json.RawMessage(`{}`), TimeoutMS: 2000, MaxOutputBytes: 1024})
		hook := wait(agent, env.ID, "host-hook", func(op execution.Operation) bool { return op.Acknowledged })
		var hookOutput execprotocol.HookOutput
		if hook.State != "completed" || json.Unmarshal(hook.Snapshot.Output, &hookOutput) != nil || hookOutput.Stdout != home {
			t.Fatal("Host hook HOME", hook.State, hookOutput)
		}
		submit(agent, env.ID, "host-pty", "exec_command", native.CommandArguments{Command: `printf 'ready\n'; read value; printf 'pty:%s\n' "$value"`, TTY: true})
		ready := wait(agent, env.ID, "host-pty", func(op execution.Operation) bool { return strings.Contains(op.Snapshot.Text(), "ready") })
		submit(agent, env.ID, "host-stdin", "write_stdin", native.StdinArguments{OperationID: "host-pty", Chars: "host-mode\n", After: ready.Snapshot.NextCursor, YieldTimeMS: 100})
		pty := wait(agent, env.ID, "host-pty", func(op execution.Operation) bool { return op.Acknowledged })
		if pty.State != "completed" || !strings.Contains(pty.Snapshot.Text(), "pty:host-mode") {
			t.Fatal("Host PTY", pty.State, pty.Snapshot.Text())
		}
		wait(agent, env.ID, "host-stdin", func(op execution.Operation) bool { return op.Acknowledged })
		submit(agent, env.ID, "host-mcp", "mcp_connect", native.MCPArguments{Command: os.Args[0], Args: []string{"-test.run=^TestNativeExecutorMCPHelper$"}, Environment: map[string]string{"JUEX_NATIVE_MCP_HELPER": "1", "JUEX_NATIVE_MCP_COUNTER": filepath.Join(env.WorkingDirectory, "mcp-calls")}})
		wait(agent, env.ID, "host-mcp", func(op execution.Operation) bool { return strings.Contains(op.Snapshot.Text(), `"type":"connected"`) })
		submit(agent, env.ID, "host-mcp-call", "mcp_call", native.MCPArguments{ConnectionID: "host-mcp", Name: "echo", Arguments: map[string]any{"text": "host-mode"}})
		mcp := wait(agent, env.ID, "host-mcp-call", func(op execution.Operation) bool { return op.Acknowledged })
		if mcp.State != "completed" || !strings.Contains(mcp.Snapshot.Text(), "echo:host-mode") {
			t.Fatal("Host MCP", mcp.State, mcp.Snapshot.Text())
		}
		if err := client.Cancel(ctx, f.actor, f.tenant, agent, env.ID, "host-mcp"); err != nil {
			t.Fatal(err)
		}
		wait(agent, env.ID, "host-mcp", func(op execution.Operation) bool { return op.State == "cancelled" && op.Acknowledged })
		submit(agent, env.ID, "host-observer", "observe_command", execprotocol.ObservableCommand{Command: []string{"/bin/sh", "-c", `printf started >> starts; while :; do printf 'tick\n'; sleep 0.1; done`}})
		wait(agent, env.ID, "host-observer", func(op execution.Operation) bool { return strings.Contains(op.Snapshot.Text(), "tick") })
	}
	if devices[0].env.ID == devices[1].env.ID || devices[0].status.PID == devices[1].status.PID {
		t.Fatal("Host Agents share an executor")
	}
	stop()
	for _, d := range devices {
		status, err := d.manager.Status()
		if err != nil || !status.Running || status.Fingerprint != d.status.Fingerprint {
			t.Fatal("Execution restart stopped executor", status, err)
		}
	}
	start()
	for _, d := range devices {
		submit(d.agent, d.env.ID, "after-restart", "exec_command", native.CommandArguments{Command: `cat starts; printf '|'; cat proof "$HOME/proof"`})
		op := wait(d.agent, d.env.ID, "after-restart", func(op execution.Operation) bool { return op.Acknowledged })
		if op.State != "completed" || op.Snapshot.Text() != "started|ownown" {
			t.Fatal("restart replayed observer or lost state", op.State, op.Snapshot.Text())
		}
		status, err := d.manager.Status()
		if err != nil || status.Fingerprint != d.status.Fingerprint {
			t.Fatal(status, err)
		}
	}
	// One observer must report its cancellation after reconnecting. The other
	// environment tests a database rollback after its owned files were deleted.
	d := devices[1]
	if err := client.Cancel(ctx, f.actor, f.tenant, d.agent, d.env.ID, "host-observer"); err != nil {
		t.Fatal(err)
	}
	wait(d.agent, d.env.ID, "host-observer", func(op execution.Operation) bool { return op.State == "cancelled" && op.Acknowledged })
	stop()
	requests := make([]lifecycle.Request, len(devices))
	for i, d := range devices {
		scope, err := f.execution.Authority.Agent(ctx, f.actor, f.tenant, d.agent, true)
		if err != nil {
			t.Fatal(err)
		}
		requests[i] = lifecycle.Request{Target: lifecycle.Target{ID: uuid.NewString(), TenantID: f.tenant, UserID: f.actor, FleetID: scope.FleetID, AgentIDs: []string{d.agent}}, Phase: lifecycle.Erase}
		if _, err := f.executionStore.Purge(ctx, requests[i]); err != nil {
			t.Fatal(err)
		}
	}
	backend, err := host.New(configuration.Backend)
	if err != nil {
		t.Fatal(err)
	}
	rollbackStore := &rollbackManagedPurge{ManagedRepository: f.executionStore, environmentID: d.env.ID, fail: true}
	manager := execution.ManagedManager{Store: rollbackStore, Backend: backend, Authority: f.execution.Authority, Key: make([]byte, 32)}
	if err := manager.Reconcile(ctx); !errors.Is(err, errPurgeCommit) {
		t.Fatal("did not reach injected commit failure", err)
	}
	for _, root := range []string{configuration.Backend.Root, configuration.Backend.ControlRoot} {
		if _, err := os.Stat(filepath.Join(root, d.env.ID)); !os.IsNotExist(err) {
			t.Fatal("owned files were not removed before rollback", err)
		}
	}
	if err := manager.Reconcile(ctx); err != nil {
		t.Fatal("cleanup could not resume after database rollback", err)
	}
	start()
	for i, d := range devices {
		runtimeEventually(t, func() bool {
			receipt, err := f.executionStore.Purge(ctx, requests[i])
			return err == nil && receipt.DataRemoved && receipt.Unconfirmed == 0 && receipt.EnvironmentsPending == 0
		})
		op, err := f.executionStore.Operation(ctx, d.env.ID, "host-observer", 0, 1)
		if err != nil || op.State != "cancelled" || !op.Acknowledged {
			t.Fatal("Host purge lost confirmed cancellation", op.State, op.Acknowledged, err)
		}
		for _, root := range []string{configuration.Backend.Root, configuration.Backend.ControlRoot} {
			if _, err := os.Stat(filepath.Join(root, d.env.ID)); !os.IsNotExist(err) {
				t.Fatal("owned files remain after purge", err)
			}
		}
	}
}

var errPurgeCommit = errors.New("injected purge commit failure")

type rollbackManagedPurge struct {
	execution.ManagedRepository
	environmentID string
	fail          bool
}

func (s *rollbackManagedPurge) ManagedIDs(context.Context) ([]string, error) {
	return []string{s.environmentID}, nil
}

func (s *rollbackManagedPurge) LockManaged(ctx context.Context, id string, action func(execution.ManagedResource) (execution.ManagedResult, error)) error {
	return s.ManagedRepository.LockManaged(ctx, id, func(r execution.ManagedResource) (execution.ManagedResult, error) {
		result, err := action(r)
		if err == nil && result.Purged && s.fail {
			s.fail = false
			return result, errPurgeCommit
		}
		return result, err
	})
}
