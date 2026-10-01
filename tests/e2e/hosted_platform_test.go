//go:build linux && hosted && postgres

package e2e

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/app/managed"
	"github.com/juex-ai/juex/internal/entrypoints/executionhttp"
	serverrpc "github.com/juex-ai/juex/internal/entrypoints/platformrpc"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/execution/hosted"
	"github.com/juex-ai/juex/internal/execution/native"
	executionrpc "github.com/juex-ai/juex/internal/execution/rpc"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/extensionpolicy"
	"github.com/juex-ai/juex/internal/foundation/lifecycle"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
)

func TestHostedPlatformContainerLifecycle(t *testing.T) {
	f := executionDatabase(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	managementListener := platformListener(t)
	managementServer, err := serverrpc.NewManagement(managementListener, platformrpc.CredentialsAt(f.credentials, "management"), f.authority)
	if err != nil {
		t.Fatal(err)
	}
	runPlatformRPC(t, managementServer)
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	_, portText, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	address := netip.MustParseAddr(os.Getenv("JUEX_HOSTED_TEST_IP"))
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "enrollment.key")
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	if err := os.WriteFile(keyFile, key, 0600); err != nil {
		t.Fatal(err)
	}
	configuration := managed.HostedConfiguration{KeyFile: keyFile, IdleSeconds: 1, Memory: 512 << 20, NanoCPUs: 1000000000, Backend: hosted.Config{Socket: os.Getenv("JUEX_DOCKER_SOCKET"), WorkspaceRoot: os.Getenv("JUEX_HOSTED_STORAGE_ROOT"), StorageIdentity: os.Getenv("JUEX_HOSTED_STORAGE_ID"), Root: root, GuestBinary: os.Getenv("JUEX_GUEST_BINARY"), Image: os.Getenv("JUEX_HOSTED_IMAGE"), Pool: netip.MustParsePrefix("172.30.0.0/16"), Control: netip.AddrPortFrom(address, uint16(port)), Server: "https://execution:" + portText, DNS: []netip.Addr{netip.MustParseAddr("10.0.2.3")}, Protected: []netip.Prefix{netip.MustParsePrefix("172.19.0.0/16")}}}
	data, _ := json.Marshal(configuration)
	configPath := filepath.Join(t.TempDir(), "hosted.json")
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	identity := platformrpc.CredentialsAt(f.credentials, "execution")
	databaseURL, err := url.Parse(os.Getenv("JUEX_TEST_POSTGRES_URL"))
	if err != nil {
		t.Fatal(err)
	}
	databaseURL.Path = "/" + f.pool.Config().ConnConfig.Database
	app, err := managed.OpenExecution(ctx, managed.ExecutionConfig{DatabaseURL: databaseURL.String(), ManagementAddress: managementListener.Addr().String(), Credentials: identity, HostedConfiguration: configPath, HostedListen: listener.Addr().String(), BlobDirectory: filepath.Join(t.TempDir(), "blobs")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	f.execution = app.Service
	deviceHTTP := executionhttp.New(ctx, app.Service)
	server := &http.Server{Handler: deviceHTTP.HostedHandler(), ReadHeaderTimeout: 5 * time.Second}
	pair, err := tls.LoadX509KeyPair(identity.Certificate, identity.Key)
	if err != nil {
		t.Fatal(err)
	}
	httpDone, appDone := make(chan error, 1), make(chan struct{})
	go func() {
		httpDone <- server.Serve(tls.NewListener(listener, &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair}}))
	}()
	go func() { defer close(appDone); app.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		stop, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		_ = deviceHTTP.Shutdown(stop)
		_ = server.Shutdown(stop)
		<-httpDone
		<-appDone
	})
	rpcListener := platformListener(t)
	rpcServer, err := serverrpc.NewExecution(rpcListener, identity, app.Service, app.Pool.Ping)
	if err != nil {
		t.Fatal(err)
	}
	runPlatformRPC(t, rpcServer)
	client, err := executionrpc.NewClient(rpcListener.Addr().String(), platformrpc.CredentialsAt(f.credentials, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	envs, err := client.Environments(ctx, f.actor, f.tenant, f.agent.ID)
	if err != nil || len(envs) != 1 {
		t.Fatal(envs, err)
	}
	environment := envs[0]
	scope, err := app.Service.Authority.Agent(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	fence := execprotocol.AuthorityFence{ActorEpoch: scope.ActorAuthorizationEpoch, MembershipEpoch: scope.MembershipExecutionEpoch, AgentEpoch: scope.AgentExecutionEpoch}
	cleanupBackend, err := hosted.New(configuration.Backend)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stop, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		cancel()
		<-appDone
		spec := hosted.Spec{EnvironmentID: environment.ID, AgentID: f.agent.ID, TenantID: f.tenant, UserID: f.actor, Slot: 0, Memory: configuration.Memory, NanoCPUs: configuration.NanoCPUs}
		if err := cleanupBackend.Stop(stop, spec); err != nil {
			t.Error(err)
		}
		if err := cleanupBackend.RemoveContainer(stop, spec); err != nil {
			t.Error(err)
		}
		if err := os.RemoveAll(filepath.Join(configuration.Backend.WorkspaceRoot, environment.ID)); err != nil {
			t.Error(err)
		}
		_ = cleanupBackend.Close()
	})
	submit := func(id, command string) execution.Operation {
		t.Helper()
		request := nativeRequest(t, id, "exec_command", native.CommandArguments{Command: command})
		request.AgentID = f.agent.ID
		op, err := client.SubmitFenced(ctx, f.actor, f.tenant, environment.ID, request, 0, fence)
		if err != nil {
			t.Fatal(err)
		}
		return op
	}
	submit("once", "printf once >> /workspace/counter; printf ready")
	op := executionEventually(t, f, environment.ID, "once", func(op execution.Operation) bool { return op.Acknowledged })
	if op.State != "completed" || op.Snapshot.Text() != "ready" {
		t.Fatal(op)
	}
	hook := nativeRequest(t, "hosted-hook", "run_hook", execprotocol.HookCommand{Command: []string{"/bin/sh", "-c", "cat > /workspace/hook-input; id -u; printf correction >&2; exit 2"}, Input: json.RawMessage(`{"event_name":"PostToolUse"}`), TimeoutMS: 10000, MaxOutputBytes: 8192})
	hook.AgentID = f.agent.ID
	hook.AuthorizationVersion = environment.AuthorizationVersion
	if _, err := client.SubmitFenced(ctx, f.actor, f.tenant, environment.ID, hook, 0, fence); err != nil {
		t.Fatal(err)
	}
	hookResult := executionEventually(t, f, environment.ID, hook.ID, func(op execution.Operation) bool { return op.Acknowledged })
	var output execprotocol.HookOutput
	if err := json.Unmarshal(hookResult.Snapshot.Output, &output); err != nil || hookResult.State != "completed" || hookResult.Snapshot.ExitCode == nil || *hookResult.Snapshot.ExitCode != 2 || output.Stdout != "1000\n" || output.Stderr != "correction" {
		t.Fatal("hosted hook did not run with Agent identity and policy result", hookResult, output, err)
	}
	submit("extension-files", `mkdir -p /workspace/example; printf '%s' '{"manifest_version":2,"name":"hosted","version":"1","skills":[{"id":"guide","path":"SKILL.md"}]}' > /workspace/example/juex.extension.json; printf '%s' 'hosted skill' > /workspace/example/SKILL.md`)
	executionEventually(t, f, environment.ID, "extension-files", func(op execution.Operation) bool { return op.Acknowledged })
	inspect := nativeRequest(t, "hosted-extension-inspect", "inspect_extension", native.FileArguments{Path: "/workspace/example"})
	inspect.AgentID = f.agent.ID
	if _, err := client.SubmitFenced(ctx, f.actor, f.tenant, environment.ID, inspect, 0, fence); err != nil {
		t.Fatal(err)
	}
	inspected := executionEventually(t, f, environment.ID, inspect.ID, func(op execution.Operation) bool { return op.Acknowledged })
	var catalog extensionpolicy.Catalog
	if inspected.State != "completed" || json.Unmarshal(inspected.Snapshot.Output, &catalog) != nil || catalog.Validate() != nil || catalog.Skills[0].Content != "hosted skill" {
		t.Fatal("hosted inspection", inspected)
	}
	binding := uuid.NewString()
	submit("extension-path-command", `mkdir -p /workspace/example/bin; printf '%s\n' '#!/bin/sh' 'test "$(id -u)" = 1000 || exit 1; printf retained > "$JUEX_EXT_DATA_DIR/state"; printf "%s" "$JUEX_EXT_DATA_DIR"' > /workspace/example/bin/extension-probe; chmod 700 /workspace/example/bin/extension-probe`)
	executionEventually(t, f, environment.ID, "extension-path-command", func(op execution.Operation) bool { return op.Acknowledged })
	extension := nativeRequest(t, "hosted-extension-data", "run_hook", execprotocol.HookCommand{Command: []string{"extension-probe"}, Environment: map[string]string{"PATH": "${JUEX_EXT_DIR}/bin:/usr/bin:/bin"}, Input: json.RawMessage(`{}`), TimeoutMS: 10000, MaxOutputBytes: 8192, Extension: &execprotocol.ExtensionContext{BindingID: binding, Directory: "/workspace/example"}, WorkingDirectory: "/workspace/example"})
	extension.AgentID = f.agent.ID
	if _, err := client.SubmitFenced(ctx, f.actor, f.tenant, environment.ID, extension, 0, fence); err != nil {
		t.Fatal(err)
	}
	extensionResult := executionEventually(t, f, environment.ID, extension.ID, func(op execution.Operation) bool { return op.Acknowledged })
	var extensionOutput execprotocol.HookOutput
	if extensionResult.State != "completed" || json.Unmarshal(extensionResult.Snapshot.Output, &extensionOutput) != nil || !strings.HasPrefix(extensionOutput.Stdout, "/home/agent/.local/share/juex/extensions/") {
		t.Fatal("hosted extension identity/data", extensionResult, extensionOutput)
	}
	submit("background", "printf started; sleep 60")
	executionEventually(t, f, environment.ID, "background", func(op execution.Operation) bool { return op.State == "running" })
	if _, err := f.pool.Exec(ctx, `UPDATE execution.hosted SET last_activity=clock_timestamp()-interval '1 hour'`); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	device, err := f.executionStore.Device(ctx, environment.ID)
	if err != nil || !device.Online {
		t.Fatal("live process reclaimed", device, err)
	}
	if err := client.Cancel(ctx, f.actor, f.tenant, f.agent.ID, environment.ID, "background"); err != nil {
		t.Fatal(err)
	}
	executionEventually(t, f, environment.ID, "background", func(op execution.Operation) bool { return op.Acknowledged })
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		device, err = f.executionStore.Device(ctx, environment.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !device.Online {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if device.Online {
		t.Fatal("idle container did not sleep")
	}
	submit("read-after-sleep", "cat /workspace/counter")
	op = executionEventually(t, f, environment.ID, "read-after-sleep", func(op execution.Operation) bool { return op.Acknowledged })
	if op.State != "completed" || op.Snapshot.Text() != "once" {
		t.Fatal("wake lost workspace", op)
	}
	submit("extension-after-sleep", "cat "+extensionOutput.Stdout+"/state")
	retained := executionEventually(t, f, environment.ID, "extension-after-sleep", func(op execution.Operation) bool { return op.Acknowledged })
	if retained.State != "completed" || retained.Snapshot.Text() != "retained" {
		t.Fatal("wake lost extension data", retained)
	}
	if op := submit("once", "printf once >> /workspace/counter; printf ready"); op.State != "completed" {
		t.Fatal("old operation replayed", op)
	}
	if _, err := f.directory.SetAgentArchived(ctx, f.actor, f.tenant, f.agent.ID, f.agent.Version, true); err != nil {
		t.Fatal(err)
	}
	request := lifecycle.Request{Target: lifecycle.Target{ID: uuid.NewString(), TenantID: f.tenant, UserID: f.actor, FleetID: scope.FleetID, AgentIDs: []string{f.agent.ID}}, Phase: lifecycle.Fence}
	if _, err := app.Service.Purge(ctx, request); err != nil {
		t.Fatal(err)
	}
	request.Phase = lifecycle.Erase
	deadline = time.Now().Add(30 * time.Second)
	var receipt lifecycle.Receipt
	for time.Now().Before(deadline) {
		receipt, err = app.Service.Purge(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		if receipt.DataRemoved {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !receipt.DataRemoved || receipt.HostedPending != 0 {
		t.Fatal("hosted cleanup did not settle", receipt)
	}
	for _, root := range []string{configuration.Backend.Root, configuration.Backend.WorkspaceRoot} {
		if _, err := os.Stat(filepath.Join(root, environment.ID)); !os.IsNotExist(err) {
			t.Fatal("hosted cleanup retained data", root, err)
		}
	}
	if _, err := client.Environments(ctx, f.actor, f.tenant, f.agent.ID); err == nil {
		t.Fatal("archived Agent recreated purged environment")
	}
}
