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
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestListenPublishesReachableRuntime(t *testing.T) {
	agentDir := t.TempDir()
	binding, err := Listen(context.Background(), agentDir)
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
	runtimeState, err := ReadRuntime(agentDir)
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

func TestListenFallsBackToLoopbackTCP(t *testing.T) {
	deps := defaultListenDependencies()
	deps.listen = func(network, address string) (net.Listener, error) {
		if network == "unix" {
			return nil, syscall.EAFNOSUPPORT
		}
		return net.Listen(network, address)
	}
	binding, err := listenWithDependencies(context.Background(), t.TempDir(), deps)
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

	binding, err := listenWithDependencies(context.Background(), t.TempDir(), deps)
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

			_, err := listenWithDependencies(context.Background(), t.TempDir(), deps)
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
	agentDir := t.TempDir()
	deps := defaultListenDependencies()
	deps.listen = func(network, address string) (net.Listener, error) {
		if network == "unix" {
			return nil, syscall.EAFNOSUPPORT
		}
		return net.Listen(network, address)
	}
	first, err := listenWithDependencies(context.Background(), agentDir, deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := first.Close(); err != nil {
			t.Errorf("close first binding: %v", err)
		}
	})

	_, err = Listen(context.Background(), agentDir)
	var running *AgentAlreadyRunningError
	if !errors.As(err, &running) {
		t.Fatalf("second listen error = %T %v, want AgentAlreadyRunningError", err, err)
	}
}

func TestCloseDoesNotRemoveReplacedRuntime(t *testing.T) {
	agentDir := t.TempDir()
	binding, err := Listen(context.Background(), agentDir)
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
	got, err := ReadRuntime(agentDir)
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
	owner := Runtime{PID: 42, Endpoint: "tcp://127.0.0.1:43123", StartedAt: startedAt}
	current := Runtime{PID: 42, Endpoint: "tcp://127.0.0.1:43123", StartedAt: serializedStartedAt}
	if !sameRuntime(current, owner) {
		t.Fatalf("same runtime instant was treated as a different owner: current=%+v owner=%+v", current, owner)
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
