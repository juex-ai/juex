//go:build linux && hosted

package e2e

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/execution/hosted"
	"github.com/juex-ai/juex/internal/execution/native"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

type hostedPeer struct {
	connection  *websocket.Conn
	environment execprotocol.Environment
	replies     chan execprotocol.Envelope
}

func TestHostedDockerProtocolIsolationAndRebuild(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Fatal("hosted test requires an isolated Linux network namespace with root")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan *hostedPeer, 8)
	tenant, user := uuid.NewString(), uuid.NewString()
	specs := []hosted.Spec{{EnvironmentID: uuid.NewString(), AgentID: uuid.NewString(), TenantID: tenant, UserID: user, Credential: rand.Text() + rand.Text(), Slot: 3000, Memory: 512 << 20, NanoCPUs: 1000000000}, {EnvironmentID: uuid.NewString(), AgentID: uuid.NewString(), TenantID: tenant, UserID: user, Credential: rand.Text() + rand.Text(), Slot: 3001, Memory: 512 << 20, NanoCPUs: 1000000000}}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var spec *hosted.Spec
		for i := range specs {
			if r.Header.Get("Authorization") == "Bearer "+specs[i].Credential {
				spec = &specs[i]
			}
		}
		if spec == nil || r.URL.Path != "/device/connect" {
			http.Error(w, "denied", http.StatusForbidden)
			return
		}
		connection, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = connection.CloseNow() }()
		var hello execprotocol.Envelope
		if wsjson.Read(ctx, connection, &hello) != nil || hello.Environment == nil || hello.Environment.ID != spec.EnvironmentID {
			return
		}
		if wsjson.Write(ctx, connection, execprotocol.Envelope{Version: execprotocol.Version, Type: "welcome", Grants: map[string][]execprotocol.Capability{spec.AgentID: {execprotocol.Files, execprotocol.Shell, execprotocol.MCP}}}) != nil {
			return
		}
		peer := &hostedPeer{connection: connection, environment: *hello.Environment, replies: make(chan execprotocol.Envelope, 2)}
		select {
		case ready <- peer:
		case <-ctx.Done():
			return
		}
		for {
			var reply execprotocol.Envelope
			if wsjson.Read(ctx, connection, &reply) != nil {
				return
			}
			select {
			case peer.replies <- reply:
			case <-ctx.Done():
				return
			}
		}
	}))
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	server.Listener = listener
	server.StartTLS()
	t.Cleanup(func() { cancel(); server.Close() })
	forbidden, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = forbidden.Close() })
	_, forbiddenPort, _ := net.SplitHostPort(forbidden.Addr().String())
	_, portText, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	address, err := netip.ParseAddr(os.Getenv("JUEX_HOSTED_TEST_IP"))
	if err != nil {
		t.Fatal("JUEX_HOSTED_TEST_IP must name the namespace host address", err)
	}
	config := hosted.Config{Socket: os.Getenv("JUEX_DOCKER_SOCKET"), Root: t.TempDir(), GuestBinary: os.Getenv("JUEX_GUEST_BINARY"), Image: os.Getenv("JUEX_HOSTED_IMAGE"), Pool: netip.MustParsePrefix("172.30.0.0/16"), Control: netip.AddrPortFrom(address, uint16(port)), Server: "https://example.com:" + portText, CA: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), Protected: []netip.Prefix{netip.MustParsePrefix("172.19.0.0/16")}}
	// This isolated lab uses slirp DNS. Only its DNS ports are allowed; ordinary
	// private destinations remain denied, including DNS-rebinding answers.
	config.DNS = []netip.Addr{netip.MustParseAddr("10.0.2.3")}
	if err := os.Chmod(config.Root, 0700); err != nil {
		t.Fatal(err)
	}
	backend, err := hosted.New(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 60*time.Second)
		defer stop()
		for _, spec := range specs {
			if err := backend.Stop(cleanup, spec); err != nil {
				t.Error(err)
			}
			if err := backend.RemoveContainer(cleanup, spec); err != nil {
				t.Error(err)
			}
		}
		if err := backend.Close(); err != nil {
			t.Error(err)
		}
	})
	connect := func(spec hosted.Spec) *hostedPeer {
		t.Helper()
		if _, err := backend.Ensure(ctx, spec); err != nil {
			t.Fatal(err)
		}
		select {
		case peer := <-ready:
			if peer.environment.ID != spec.EnvironmentID {
				t.Fatal("unexpected environment", peer.environment.ID)
			}
			return peer
		case <-time.After(20 * time.Second):
			t.Fatal("guest did not establish authenticated TLS connection")
			return nil
		}
	}
	call := func(peer *hostedPeer, request execprotocol.Envelope) execprotocol.Envelope {
		t.Helper()
		request.Version, request.ID = execprotocol.Version, rand.Text()
		callCtx, stop := context.WithTimeout(ctx, 5*time.Second)
		defer stop()
		if err := wsjson.Write(callCtx, peer.connection, request); err != nil {
			t.Fatal(err)
		}
		select {
		case reply := <-peer.replies:
			if reply.ID != request.ID || reply.Error != "" {
				t.Fatal("protocol result", reply.ID, reply.Error)
			}
			return reply
		case <-callCtx.Done():
			t.Fatal("protocol response timeout")
			return execprotocol.Envelope{}
		}
	}
	run := func(peer *hostedPeer, spec hosted.Spec, id, kind string, args any) execprotocol.Snapshot {
		t.Helper()
		data, _ := json.Marshal(args)
		request := execprotocol.Request{Version: execprotocol.Version, ID: id, AgentID: spec.AgentID, Kind: kind, Arguments: data}
		call(peer, execprotocol.Envelope{Type: "submit", Request: &request})
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			reply := call(peer, execprotocol.Envelope{Type: "query", AgentID: spec.AgentID, OperationID: id, Limit: 64 << 10})
			if reply.Snapshot != nil && reply.Snapshot.State.Terminal() {
				return *reply.Snapshot
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("operation did not settle", id)
		return execprotocol.Snapshot{}
	}
	a := connect(specs[0])
	b := connect(specs[1])
	if _, err := backend.Ensure(ctx, specs[0]); err != nil {
		t.Fatal("idempotent container lookup", err)
	}
	write := run(a, specs[0], "write", "write", native.FileArguments{Path: "persistent.txt", Content: "agent-a"})
	if write.State != execprotocol.Completed {
		t.Fatal(write)
	}
	read := run(b, specs[1], "other-agent-read", "read", native.FileArguments{Path: "persistent.txt"})
	if read.State != execprotocol.Failed {
		t.Fatal("workspaces shared", read)
	}
	for _, path := range []string{"/var/lib/juex-control/enrollment.json", "/var/lib/juex-control/journal/identity.json", "/var/run/docker.sock"} {
		result := run(a, specs[0], "private-"+rand.Text(), "read", native.FileArguments{Path: path})
		if result.State != execprotocol.Failed {
			t.Fatal("control path readable", path, result)
		}
	}
	command := native.CommandArguments{Command: "printf once >> counter; printf home > $HOME/persistent; id -u", TTY: true}
	result := run(a, specs[0], "effect-once", "exec_command", command)
	if result.State != execprotocol.Completed || !strings.Contains(result.Text(), "1000") {
		t.Fatal(result)
	}
	for _, endpoint := range []string{fmt.Sprintf("%s/%s", address, forbiddenPort), "169.254.169.254/80", "172.19.0.2/5432", "172.30.187.146/443", "100.64.0.1/443"} {
		result := run(a, specs[0], "blocked-"+rand.Text(), "exec_command", native.CommandArguments{Command: "timeout 1 /bin/bash -c 'echo probe > /dev/tcp/" + endpoint + "'"})
		if result.State == execprotocol.Completed {
			t.Fatal("protected destination reachable", endpoint)
		}
	}
	if err := backend.Stop(ctx, specs[0]); err != nil {
		t.Fatal(err)
	}
	if err := backend.RemoveContainer(ctx, specs[0]); err != nil {
		t.Fatal(err)
	}
	rebuilt := connect(specs[0])
	if rebuilt.environment.JournalID != a.environment.JournalID {
		t.Fatal("rebuild lost operation journal")
	}
	result = run(rebuilt, specs[0], "effect-once", "exec_command", command)
	if result.State != execprotocol.Completed {
		t.Fatal(result)
	}
	result = run(rebuilt, specs[0], "verify-persistence", "exec_command", native.CommandArguments{Command: "cat counter; cat persistent.txt; cat $HOME/persistent"})
	if result.State != execprotocol.Completed || result.Text() != "onceagent-ahome" {
		t.Fatal("rebuild replayed an effect or lost persistent files", result)
	}
	// Install this test's network helper as an ordinary workspace artifact. It
	// uses the same UID/network as other user programs, not Docker exec or the host.
	for name, source := range map[string]string{"network-probe": os.Args[0], "public-ca.pem": "/etc/ssl/certs/ca-certificates.crt"} {
		data, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(config.Root, specs[0].EnvironmentID, "workspace", name)
		if err := os.WriteFile(path, data, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(path, 1000, 1000); err != nil {
			t.Fatal(err)
		}
	}
	result = run(rebuilt, specs[0], "public-https", "exec_command", native.CommandArguments{Command: "/workspace/network-probe -test.run=^TestHostedNetworkProbe$", Environment: map[string]string{"JUEX_HOSTED_NETWORK_PROBE": "https://public.ecr.aws/v2/", "SSL_CERT_FILE": "/workspace/public-ca.pem"}})
	if result.State != execprotocol.Completed || !strings.Contains(result.Text(), "HTTPS status 401") {
		t.Fatal("public DNS/TLS unavailable", result)
	}
}

func TestHostedNetworkProbe(t *testing.T) {
	target := os.Getenv("JUEX_HOSTED_NETWORK_PROBE")
	if target == "" {
		return
	}
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Get(target)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_ = response.Body.Close()
	if response.TLS == nil || len(response.TLS.VerifiedChains) == 0 {
		fmt.Fprintln(os.Stderr, "TLS was not verified")
		os.Exit(2)
	}
	fmt.Println("HTTPS status", response.StatusCode)
	os.Exit(0)
}
