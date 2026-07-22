package endpoint

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/juex-ai/juex/internal/homestore"
)

const (
	socketFileName  = "api.sock"
	runtimeFileName = "runtime.json"
	lockFileName    = "endpoint.lock"
)

type Runtime struct {
	AgentID       string    `json:"agent_id"`
	InstanceID    string    `json:"instance_id"`
	PID           int       `json:"pid"`
	Endpoint      string    `json:"endpoint"`
	StartedAt     time.Time `json:"started_at"`
	BinaryVersion string    `json:"binary_version,omitempty"`
}

func (r Runtime) Matches(other Runtime) bool {
	return sameRuntime(r, other)
}

type AgentAlreadyRunningError struct {
	AgentDir string
	Endpoint string
}

func (e *AgentAlreadyRunningError) Error() string {
	if e.Endpoint != "" {
		return fmt.Sprintf("endpoint: agent already running at %s", e.Endpoint)
	}
	return fmt.Sprintf("endpoint: agent already running or starting in %s", e.AgentDir)
}

type Binding struct {
	mu             sync.Mutex
	listener       net.Listener
	lock           *homestore.Lock
	runtime        Runtime
	agentDir       string
	socketPath     string
	fallbackReason error
	published      bool
	closed         bool
}

type Maintenance struct {
	lock *homestore.Lock
}

type listenDependencies struct {
	listen     func(network, address string) (net.Listener, error)
	dial       func(ctx context.Context, network, address string) (net.Conn, error)
	lstat      func(string) (os.FileInfo, error)
	remove     func(string) error
	chmod      func(string, os.FileMode) error
	now        func() time.Time
	pid        func() int
	instanceID func() (string, error)
	lock       func(string) (*homestore.Lock, error)
}

func defaultListenDependencies() listenDependencies {
	return listenDependencies{
		listen: net.Listen,
		dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, network, address)
		},
		lstat:  os.Lstat,
		remove: os.Remove,
		chmod:  os.Chmod,
		now:    time.Now,
		pid:    os.Getpid,
		instanceID: func() (string, error) {
			var value [16]byte
			if _, err := rand.Read(value[:]); err != nil {
				return "", err
			}
			return fmt.Sprintf("%x", value[:]), nil
		},
		lock: acquireEndpointLock,
	}
}

func Listen(ctx context.Context, agentDir, binaryVersion string) (*Binding, error) {
	binding, err := listenWithDependencies(ctx, agentDir, defaultListenDependencies())
	if err != nil {
		return nil, err
	}
	binding.runtime.BinaryVersion = strings.TrimSpace(binaryVersion)
	return binding, nil
}

func listenWithDependencies(ctx context.Context, agentDir string, deps listenDependencies) (*Binding, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	absoluteDir, err := filepath.Abs(filepath.Clean(agentDir))
	if err != nil {
		return nil, fmt.Errorf("endpoint: resolve agent directory: %w", err)
	}
	if err := validateAgentDirectory(absoluteDir); err != nil {
		return nil, err
	}
	lock, err := deps.lock(absoluteDir)
	if err != nil {
		if errors.Is(err, homestore.ErrLockBusy) {
			return nil, &AgentAlreadyRunningError{AgentDir: absoluteDir}
		}
		return nil, fmt.Errorf("endpoint: lock agent directory %s: %w", absoluteDir, err)
	}
	if err := validateAgentDirectory(absoluteDir); err != nil {
		_ = lock.Close()
		return nil, err
	}

	socketPath := filepath.Join(absoluteDir, socketFileName)
	listener, fallbackReason, err := listenPreferred(ctx, socketPath, deps)
	if err != nil {
		_ = lock.Close()
		return nil, err
	}
	target, err := targetForListener(listener, socketPath)
	if err != nil {
		_ = listener.Close()
		_ = lock.Close()
		return nil, err
	}
	instanceID, err := deps.instanceID()
	if err != nil {
		_ = listener.Close()
		_ = lock.Close()
		return nil, fmt.Errorf("endpoint: generate runtime instance identity: %w", err)
	}
	return &Binding{
		listener: listener,
		lock:     lock,
		runtime: Runtime{
			AgentID:    filepath.Base(absoluteDir),
			InstanceID: instanceID,
			PID:        deps.pid(),
			Endpoint:   target.URI(),
			StartedAt:  deps.now().UTC().Round(0),
		},
		agentDir:       absoluteDir,
		socketPath:     socketPath,
		fallbackReason: fallbackReason,
	}, nil
}

func AcquireMaintenance(agentDir string) (*Maintenance, error) {
	absoluteDir, err := filepath.Abs(filepath.Clean(agentDir))
	if err != nil {
		return nil, fmt.Errorf("endpoint: resolve agent directory: %w", err)
	}
	if err := validateAgentDirectory(absoluteDir); err != nil {
		return nil, err
	}
	lock, err := acquireEndpointLock(absoluteDir)
	if err != nil {
		if errors.Is(err, homestore.ErrLockBusy) {
			return nil, &AgentAlreadyRunningError{AgentDir: absoluteDir}
		}
		return nil, fmt.Errorf("endpoint: lock agent directory %s for maintenance: %w", absoluteDir, err)
	}
	if err := validateAgentDirectory(absoluteDir); err != nil {
		_ = lock.Close()
		return nil, err
	}
	return &Maintenance{lock: lock}, nil
}

func (m *Maintenance) Close() error {
	if m == nil {
		return nil
	}
	return m.lock.Close()
}

func validateAgentDirectory(agentDir string) error {
	info, err := os.Lstat(agentDir)
	if err != nil {
		return fmt.Errorf("endpoint: inspect agent directory %s: %w", agentDir, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("endpoint: agent directory %s is not a real directory", agentDir)
	}
	return nil
}

func acquireEndpointLock(agentDir string) (*homestore.Lock, error) {
	parent := filepath.Dir(agentDir)
	if filepath.Base(parent) == "agents" {
		parent = filepath.Dir(parent)
	}
	return homestore.New(parent).Lock(homestore.EndpointLocks, filepath.Base(agentDir), homestore.LockTry)
}

func listenPreferred(ctx context.Context, socketPath string, deps listenDependencies) (net.Listener, error, error) {
	listener, err := deps.listen("unix", socketPath)
	if err == nil {
		if chmodErr := deps.chmod(socketPath, 0o600); chmodErr == nil {
			return listener, nil, nil
		} else {
			_ = listener.Close()
			_ = deps.remove(socketPath)
			err = fmt.Errorf("set unix socket permissions: %w", chmodErr)
		}
	} else if errors.Is(err, syscall.EADDRINUSE) {
		listener, recoveredFallback, retryErr := recoverStaleSocket(ctx, socketPath, deps)
		if retryErr == nil {
			if recoveredFallback != nil {
				return listener, recoveredFallback, nil
			}
			if chmodErr := deps.chmod(socketPath, 0o600); chmodErr != nil {
				_ = listener.Close()
				_ = deps.remove(socketPath)
				return fallbackTCP(fmt.Errorf("set unix socket permissions: %w", chmodErr), deps)
			}
			return listener, nil, nil
		}
		var running *AgentAlreadyRunningError
		if errors.As(retryErr, &running) {
			return nil, nil, retryErr
		}
		return nil, nil, retryErr
	}
	return fallbackTCP(err, deps)
}

func recoverStaleSocket(
	ctx context.Context,
	socketPath string,
	deps listenDependencies,
) (net.Listener, error, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	connection, dialErr := deps.dial(probeCtx, "unix", socketPath)
	if dialErr == nil {
		_ = connection.Close()
		return nil, nil, &AgentAlreadyRunningError{Endpoint: unixURI(socketPath)}
	}
	if !definitelyStale(dialErr) {
		return nil, nil, fmt.Errorf(
			"endpoint: socket %s is occupied but could not be probed safely: %w",
			socketPath,
			dialErr,
		)
	}
	info, statErr := deps.lstat(socketPath)
	switch {
	case errors.Is(statErr, os.ErrNotExist):
	case statErr != nil:
		return nil, nil, fmt.Errorf("endpoint: inspect stale socket %s: %w", socketPath, statErr)
	case info.Mode()&os.ModeSocket == 0:
		return nil, nil, fmt.Errorf("endpoint: refusing to remove non-socket occupant at %s", socketPath)
	default:
		if err := deps.remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, nil, fmt.Errorf("endpoint: remove stale socket %s: %w", socketPath, err)
		}
	}
	listener, err := deps.listen("unix", socketPath)
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			return nil, nil, fmt.Errorf(
				"endpoint: socket %s remained occupied after one stale cleanup attempt: %w",
				socketPath,
				err,
			)
		}
		return fallbackTCP(err, deps)
	}
	return listener, nil, nil
}

func definitelyStale(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, os.ErrNotExist)
}

func fallbackTCP(reason error, deps listenDependencies) (net.Listener, error, error) {
	listener, err := deps.listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, fmt.Errorf("endpoint: unix listen failed (%v), loopback fallback failed: %w", reason, err)
	}
	return listener, reason, nil
}

func targetForListener(listener net.Listener, socketPath string) (Target, error) {
	switch listener.Addr().Network() {
	case "unix", "unixpacket":
		return Parse(unixURI(socketPath))
	case "tcp", "tcp4", "tcp6":
		return Parse("tcp://" + listener.Addr().String())
	default:
		return Target{}, fmt.Errorf("endpoint: unsupported listener network %q", listener.Addr().Network())
	}
}

func (b *Binding) Listener() net.Listener {
	return b.listener
}

func (b *Binding) Runtime() Runtime {
	return b.runtime
}

func (b *Binding) FallbackReason() error {
	return b.fallbackReason
}

func (b *Binding) Publish() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return errors.New("endpoint: publish closed binding")
	}
	if b.published {
		return nil
	}
	if err := writeRuntime(filepath.Join(b.agentDir, runtimeFileName), b.runtime); err != nil {
		return fmt.Errorf("endpoint: publish runtime: %w", err)
	}
	b.published = true
	return nil
}

func (b *Binding) Close() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true

	var errs []error
	if b.published {
		if err := removeOwnedRuntime(filepath.Join(b.agentDir, runtimeFileName), b.runtime); err != nil {
			errs = append(errs, err)
		}
	}
	if b.listener != nil {
		if err := b.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			errs = append(errs, err)
		}
	}
	if b.runtime.Endpoint != "" {
		if target, err := Parse(b.runtime.Endpoint); err == nil && target.Network() == "unix" {
			if err := os.Remove(b.socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, err)
			}
		}
	}
	if b.lock != nil {
		if err := b.lock.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func ReadRuntime(agentDir string) (Runtime, error) {
	path := filepath.Join(agentDir, runtimeFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		return Runtime{}, err
	}
	var state Runtime
	if err := json.Unmarshal(data, &state); err != nil {
		return Runtime{}, fmt.Errorf("endpoint: decode %s: %w", path, err)
	}
	if state.AgentID == "" || state.InstanceID == "" || state.PID <= 0 || state.StartedAt.IsZero() {
		return Runtime{}, fmt.Errorf("endpoint: %s contains invalid process metadata", path)
	}
	if state.AgentID != filepath.Base(filepath.Clean(agentDir)) {
		return Runtime{}, fmt.Errorf("endpoint: %s contains mismatched agent identity", path)
	}
	if _, err := Parse(state.Endpoint); err != nil {
		return Runtime{}, fmt.Errorf("endpoint: %s contains invalid endpoint: %w", path, err)
	}
	return state, nil
}

func RemoveRuntime(agentDir string, expected Runtime) error {
	current, err := ReadRuntime(agentDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !sameRuntime(current, expected) {
		return &IdentityMismatchError{Expected: expected, Actual: current}
	}
	path := filepath.Join(agentDir, runtimeFileName)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("endpoint: remove runtime %s: %w", path, err)
	}
	return homestore.SyncDir(agentDir)
}

func removeOwnedRuntime(path string, owner Runtime) error {
	current, err := ReadRuntime(filepath.Dir(path))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !sameRuntime(current, owner) {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("endpoint: remove owned runtime %s: %w", path, err)
	}
	return homestore.SyncDir(filepath.Dir(path))
}

func sameRuntime(left, right Runtime) bool {
	return left.AgentID == right.AgentID &&
		left.InstanceID == right.InstanceID &&
		left.PID == right.PID &&
		left.Endpoint == right.Endpoint &&
		left.StartedAt.Equal(right.StartedAt) &&
		(left.BinaryVersion == "" ||
			right.BinaryVersion == "" ||
			left.BinaryVersion == right.BinaryVersion)
}
