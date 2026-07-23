package endpoint

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

type testAddress struct {
	id       string
	stateDir string
	lockPath string
}

func (a testAddress) ID() string               { return a.id }
func (a testAddress) StateDir() string         { return a.stateDir }
func (a testAddress) EndpointLockPath() string { return a.lockPath }

func TestEndpointUsesExplicitAgentAddress(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state", "not-the-agent-id")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	address := testAddress{
		id:       "abcdefghijklmnop",
		stateDir: stateDir,
		lockPath: filepath.Join(root, "unrelated-guards", "resident.guard"),
	}

	binding, err := Listen(context.Background(), address, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := binding.Publish(); err != nil {
		t.Fatal(err)
	}
	runtimeState, err := ReadRuntime(address)
	if err != nil {
		t.Fatal(err)
	}
	if runtimeState.AgentID != address.id {
		t.Fatalf("runtime agent id = %q, want %q", runtimeState.AgentID, address.id)
	}
	if _, err := AcquireMaintenance(address); err == nil {
		t.Fatal("maintenance acquired while binding owns explicit lock")
	}
	if _, err := os.Stat(address.lockPath); err != nil {
		t.Fatalf("explicit lock path: %v", err)
	}
	if err := RemoveRuntime(address, runtimeState); err != nil {
		t.Fatal(err)
	}
	if err := binding.Close(); err != nil {
		t.Fatal(err)
	}
	maintenance, err := AcquireMaintenance(address)
	if err != nil {
		t.Fatal(err)
	}
	if err := maintenance.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotAddressRequiresExplicitAbsolutePaths(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name    string
		address testAddress
	}{
		{
			name: "missing id",
			address: testAddress{
				stateDir: filepath.Join(root, "state"),
				lockPath: filepath.Join(root, "guard"),
			},
		},
		{
			name: "relative state directory",
			address: testAddress{
				id:       "abcdefghijklmnop",
				stateDir: "relative-state",
				lockPath: filepath.Join(root, "guard"),
			},
		},
		{
			name: "relative endpoint lock",
			address: testAddress{
				id:       "abcdefghijklmnop",
				stateDir: filepath.Join(root, "state"),
				lockPath: "relative-guard",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := snapshotAddress(test.address); err == nil {
				t.Fatal("snapshotAddress() succeeded, want validation error")
			}
		})
	}
}

func TestReadRuntimeChecksExplicitAddressIdentity(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	address := testAddress{
		id:       "abcdefghijklmnop",
		stateDir: stateDir,
		lockPath: filepath.Join(root, "guard"),
	}
	runtimeState := Runtime{
		AgentID:    "ponmlkjihgfedcba",
		InstanceID: "instance",
		PID:        42,
		Endpoint:   "tcp://127.0.0.1:43123",
		StartedAt:  time.Now().UTC(),
	}
	if err := writeRuntime(filepath.Join(stateDir, runtimeFileName), runtimeState); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRuntime(address); err == nil || !strings.Contains(err.Error(), "mismatched agent identity") {
		t.Fatalf("ReadRuntime() error = %v, want mismatched agent identity", err)
	}
}

func TestListenPublishesReachableRuntime(t *testing.T) {
	agentDir := newEndpointAgentDir(t)
	address := addressForAgentDir(agentDir)
	binding, err := Listen(context.Background(), address, "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = binding.Close() })

	if _, err := os.Stat(filepath.Join(agentDir, "runtime.json")); !os.IsNotExist(err) {
		t.Fatalf("runtime.json exists before Publish: %v", err)
	}

	target, err := Parse(binding.Runtime().Endpoint)
	if err != nil {
		t.Fatalf("parse binding endpoint: %v", err)
	}
	switch target.Network() {
	case "unix":
		info, err := os.Stat(target.Address())
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Fatalf("socket permissions = %o, want 600", info.Mode().Perm())
		}
		if runtime.GOOS == "windows" && info.Mode().Perm()&0o200 == 0 {
			t.Fatalf("socket permissions = %o, want writable", info.Mode().Perm())
		}
	case "tcp":
		if binding.FallbackReason() == nil {
			t.Fatal("TCP endpoint has no fallback reason")
		}
	default:
		t.Fatalf("network = %q", target.Network())
	}

	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprintln(w, "ok")
		}),
		ReadHeaderTimeout: time.Second,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(binding.Listener()) }()

	response, err := target.NewClient().Get(target.URL("/healthz"))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}

	if err := binding.Publish(); err != nil {
		t.Fatal(err)
	}
	runtimeState, err := ReadRuntime(address)
	if err != nil {
		t.Fatal(err)
	}
	if runtimeState != binding.Runtime() {
		t.Fatalf("runtime = %+v, want %+v", runtimeState, binding.Runtime())
	}
	if runtimeState.PID != os.Getpid() || runtimeState.StartedAt.IsZero() ||
		runtimeState.StartedAt.Location() != time.UTC {
		t.Fatalf("runtime metadata = %+v", runtimeState)
	}
	if runtimeState.AgentID != filepath.Base(agentDir) || runtimeState.InstanceID == "" {
		t.Fatalf("runtime identity = %+v", runtimeState)
	}
	if runtimeState.BinaryVersion != "1.2.3" {
		t.Fatalf("binary version = %q", runtimeState.BinaryVersion)
	}

	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-serveErr:
		if err != nil && err != http.ErrServerClosed {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not stop")
	}
	if err := binding.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(agentDir, "runtime.json")); !os.IsNotExist(err) {
		t.Fatalf("runtime.json remains after close: %v", err)
	}
}

func TestRuntimeMatchesTreatsMissingVersionAsCompatible(t *testing.T) {
	base := Runtime{
		AgentID:    "aaaaaaaa",
		InstanceID: "instance",
		PID:        42,
		Endpoint:   "tcp://127.0.0.1:1234",
		StartedAt:  time.Date(2026, 7, 18, 4, 0, 0, 0, time.UTC),
	}
	old := base
	current := base
	current.BinaryVersion = "2.0.0"
	other := base
	other.BinaryVersion = "1.0.0"

	if !old.Matches(current) || !current.Matches(old) {
		t.Fatal("missing binary version must remain compatible")
	}
	if current.Matches(other) {
		t.Fatal("different non-empty binary versions must not match")
	}
}

func TestListenFallsBackToLoopbackTCP(t *testing.T) {
	deps := defaultListenDependencies()
	deps.listen = func(network, address string) (net.Listener, error) {
		if network == "unix" {
			return nil, syscall.EAFNOSUPPORT
		}
		return net.Listen(network, address)
	}
	binding, err := listenWithDependencies(context.Background(), addressForAgentDir(newEndpointAgentDir(t)), deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := binding.Close(); err != nil {
			t.Errorf("close binding: %v", err)
		}
	})

	target, err := Parse(binding.Runtime().Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if target.Network() != "tcp" || binding.FallbackReason() == nil {
		t.Fatalf("binding = %s, fallback = %v", target.URI(), binding.FallbackReason())
	}
	host, _, err := net.SplitHostPort(target.Address())
	if err != nil {
		t.Fatal(err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		t.Fatalf("fallback host = %q, want loopback", host)
	}
}

func TestListenRemovesConfirmedStaleSocketOnce(t *testing.T) {
	var unixAttempts atomic.Int32
	var removed atomic.Bool
	deps := defaultListenDependencies()
	deps.listen = func(network, address string) (net.Listener, error) {
		if network != "unix" {
			t.Fatalf("unexpected network %q", network)
		}
		if unixAttempts.Add(1) == 1 {
			return nil, os.NewSyscallError("listen", syscall.EADDRINUSE)
		}
		return &stubListener{addr: &net.UnixAddr{Name: address, Net: "unix"}}, nil
	}
	deps.dial = func(context.Context, string, string) (net.Conn, error) {
		return nil, os.NewSyscallError("connect", syscall.ECONNREFUSED)
	}
	deps.lstat = func(string) (os.FileInfo, error) {
		return stubFileInfo{mode: os.ModeSocket | 0o600}, nil
	}
	deps.remove = func(string) error {
		removed.Store(true)
		return nil
	}
	deps.chmod = func(string, os.FileMode) error { return nil }

	binding, err := listenWithDependencies(context.Background(), addressForAgentDir(newEndpointAgentDir(t)), deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := binding.Close(); err != nil {
			t.Errorf("close binding: %v", err)
		}
	})
	if !removed.Load() || unixAttempts.Load() != 2 {
		t.Fatalf("removed = %v, unix attempts = %d", removed.Load(), unixAttempts.Load())
	}
}

func TestListenRejectsLiveOrUnsafeSocketOccupants(t *testing.T) {
	tests := []struct {
		name       string
		dial       func(context.Context, string, string) (net.Conn, error)
		info       os.FileInfo
		wantRunErr bool
	}{
		{
			name: "live socket",
			dial: func(context.Context, string, string) (net.Conn, error) {
				client, server := net.Pipe()
				_ = server.Close()
				return client, nil
			},
			info:       stubFileInfo{mode: os.ModeSocket | 0o600},
			wantRunErr: true,
		},
		{
			name: "regular file",
			dial: func(context.Context, string, string) (net.Conn, error) {
				return nil, os.NewSyscallError("connect", syscall.ECONNREFUSED)
			},
			info: stubFileInfo{mode: 0o600},
		},
		{
			name: "ambiguous timeout",
			dial: func(context.Context, string, string) (net.Conn, error) {
				return nil, context.DeadlineExceeded
			},
			info: stubFileInfo{mode: os.ModeSocket | 0o600},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deps := defaultListenDependencies()
			deps.listen = func(string, string) (net.Listener, error) {
				return nil, os.NewSyscallError("listen", syscall.EADDRINUSE)
			}
			deps.dial = test.dial
			deps.lstat = func(string) (os.FileInfo, error) { return test.info, nil }
			deps.remove = func(string) error {
				t.Fatal("unsafe occupant was removed")
				return nil
			}

			_, err := listenWithDependencies(context.Background(), addressForAgentDir(newEndpointAgentDir(t)), deps)
			if err == nil {
				t.Fatal("listen succeeded, want error")
			}
			var running *AgentAlreadyRunningError
			if errors.As(err, &running) != test.wantRunErr {
				t.Fatalf("error = %T %v, already-running = %v", err, err, errors.As(err, &running))
			}
		})
	}
}

func TestFallbackBindingKeepsExclusiveAgentLock(t *testing.T) {
	agentDir := newEndpointAgentDir(t)
	address := addressForAgentDir(agentDir)
	deps := defaultListenDependencies()
	deps.listen = func(network, address string) (net.Listener, error) {
		if network == "unix" {
			return nil, syscall.EAFNOSUPPORT
		}
		return net.Listen(network, address)
	}
	first, err := listenWithDependencies(context.Background(), address, deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := first.Close(); err != nil {
			t.Errorf("close first binding: %v", err)
		}
	})

	_, err = Listen(context.Background(), address, "test")
	var running *AgentAlreadyRunningError
	if !errors.As(err, &running) {
		t.Fatalf("second listen error = %T %v, want AgentAlreadyRunningError", err, err)
	}
}

func TestCloseDoesNotRemoveReplacedRuntime(t *testing.T) {
	agentDir := newEndpointAgentDir(t)
	address := addressForAgentDir(agentDir)
	binding, err := Listen(context.Background(), address, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := binding.Publish(); err != nil {
		t.Fatal(err)
	}
	replacement := binding.Runtime()
	replacement.PID++
	replacement.StartedAt = replacement.StartedAt.Add(time.Second)
	if err := writeRuntime(filepath.Join(agentDir, runtimeFileName), replacement); err != nil {
		t.Fatal(err)
	}
	if err := binding.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := ReadRuntime(address)
	if err != nil {
		t.Fatal(err)
	}
	if got != replacement {
		t.Fatalf("runtime = %+v, want replacement %+v", got, replacement)
	}
}

func TestRuntimeOwnershipIgnoresMonotonicClockRepresentation(t *testing.T) {
	startedAt := time.Now()
	serializedStartedAt := startedAt.Round(0)
	if !startedAt.Equal(serializedStartedAt) {
		t.Fatal("test setup changed wall-clock instant")
	}
	owner := Runtime{
		AgentID:    "abcdefghijklmnop",
		InstanceID: "instance-one",
		PID:        42,
		Endpoint:   "tcp://127.0.0.1:43123",
		StartedAt:  startedAt,
	}
	current := Runtime{
		AgentID:    "abcdefghijklmnop",
		InstanceID: "instance-one",
		PID:        42,
		Endpoint:   "tcp://127.0.0.1:43123",
		StartedAt:  serializedStartedAt,
	}
	if !sameRuntime(current, owner) {
		t.Fatalf("same runtime instant was treated as a different owner: current=%+v owner=%+v", current, owner)
	}
}

func TestListenDoesNotRecreateMissingAgentDirectory(t *testing.T) {
	agentDir := newEndpointAgentDir(t)
	address := addressForAgentDir(agentDir)
	if err := os.Remove(agentDir); err != nil {
		t.Fatal(err)
	}

	if _, err := Listen(context.Background(), address, "test"); err == nil {
		t.Fatal("Listen succeeded for a missing agent directory")
	}
	if _, err := os.Stat(agentDir); !os.IsNotExist(err) {
		t.Fatalf("agent directory was recreated: %v", err)
	}
}

func TestMaintenanceAndListenShareExternalLock(t *testing.T) {
	agentDir := newEndpointAgentDir(t)
	address := addressForAgentDir(agentDir)
	maintenance, err := AcquireMaintenance(address)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := maintenance.Close(); err != nil {
			t.Errorf("close maintenance: %v", err)
		}
	}()

	_, err = Listen(context.Background(), address, "test")
	var running *AgentAlreadyRunningError
	if !errors.As(err, &running) {
		t.Fatalf("Listen error = %T %v, want AgentAlreadyRunningError", err, err)
	}
	if _, err := os.Stat(address.lockPath); err != nil {
		t.Fatalf("external endpoint lock: %v", err)
	}
	if _, err := os.Stat(filepath.Join(agentDir, "endpoint.lock")); !os.IsNotExist(err) {
		t.Fatalf("lock exists inside agent directory: %v", err)
	}
}

func TestActiveBindingBlocksMaintenance(t *testing.T) {
	agentDir := newEndpointAgentDir(t)
	address := addressForAgentDir(agentDir)
	binding, err := Listen(context.Background(), address, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := binding.Close(); err != nil {
			t.Errorf("close binding: %v", err)
		}
	}()

	_, err = AcquireMaintenance(address)
	var running *AgentAlreadyRunningError
	if !errors.As(err, &running) {
		t.Fatalf("AcquireMaintenance error = %T %v, want AgentAlreadyRunningError", err, err)
	}
}

func newEndpointAgentDir(t *testing.T) string {
	t.Helper()
	agentDir := filepath.Join(t.TempDir(), "agents", "abcdefghijklmnop")
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return agentDir
}

func addressForAgentDir(agentDir string) testAddress {
	id := filepath.Base(agentDir)
	home := filepath.Dir(filepath.Dir(agentDir))
	return testAddress{
		id:       id,
		stateDir: agentDir,
		lockPath: filepath.Join(home, ".locks", "endpoints", id+".lock"),
	}
}

type stubListener struct {
	addr   net.Addr
	closed atomic.Bool
}

func (l *stubListener) Accept() (net.Conn, error) {
	return nil, io.EOF
}

func (l *stubListener) Close() error {
	l.closed.Store(true)
	return nil
}

func (l *stubListener) Addr() net.Addr {
	return l.addr
}

type stubFileInfo struct {
	mode os.FileMode
}

func (stubFileInfo) Name() string        { return "api.sock" }
func (stubFileInfo) Size() int64         { return 0 }
func (f stubFileInfo) Mode() os.FileMode { return f.mode }
func (stubFileInfo) ModTime() time.Time  { return time.Time{} }
func (stubFileInfo) IsDir() bool         { return false }
func (stubFileInfo) Sys() any            { return nil }
