package native

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/juex-ai/juex/internal/execution/blob"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Config struct {
	StateDirectory   string
	EnvironmentID    string
	WorkingDirectory string
	Grants           map[string][]execprotocol.Capability
	Concurrency      int
	OutputLimit      int64
	StorageLimit     int64
	FileStorageLimit int64
	Retention        time.Duration
	ProcessUser      *ProcessUser
}

type operation struct {
	record    record
	cancel    context.CancelFunc
	stdin     io.WriteCloser
	stdinMu   sync.Mutex
	session   *mcp.ClientSession
	fileReady chan struct{}
}

type Engine struct {
	config      Config
	mu          sync.Mutex
	operations  map[string]*operation
	ctx         context.Context
	cancel      context.CancelFunc
	workers     sync.WaitGroup
	slots       chan struct{}
	connections chan struct{}
	observers   chan struct{}
	lock        *os.File
	fault       error
	allowed     map[string][]execprotocol.Capability
	identity    stateIdentity
	files       *blob.Store
}

func (e *Engine) Identity() (environment, journal string) {
	return e.identity.EnvironmentID, e.identity.JournalID
}

func Open(config Config) (*Engine, error) {
	if !filepath.IsAbs(config.StateDirectory) || config.EnvironmentID == "" {
		return nil, execprotocol.ErrInvalid
	}
	if config.ProcessUser != nil {
		copy := *config.ProcessUser
		if err := validateProcessUser(copy); err != nil {
			return nil, err
		}
		config.ProcessUser = &copy
	}
	if config.WorkingDirectory == "" {
		var err error
		config.WorkingDirectory, err = os.UserHomeDir()
		if err != nil {
			return nil, err
		}
	}
	if !filepath.IsAbs(config.WorkingDirectory) {
		return nil, execprotocol.ErrInvalid
	}
	if info, err := os.Stat(config.WorkingDirectory); err != nil || !info.IsDir() {
		return nil, execprotocol.ErrInvalid
	}
	if config.Concurrency == 0 {
		config.Concurrency = 3
	}
	if config.OutputLimit == 0 {
		config.OutputLimit = 8 << 20
	}
	if config.StorageLimit == 0 {
		config.StorageLimit = 512 << 20
	}
	if config.FileStorageLimit == 0 {
		config.FileStorageLimit = 1 << 30
	}
	if config.Retention == 0 {
		config.Retention = 7 * 24 * time.Hour
	}
	if config.Concurrency < 1 || config.Concurrency > 100 || config.OutputLimit < 1024 || config.StorageLimit < config.OutputLimit || config.FileStorageLimit < 1 || config.Retention < 0 {
		return nil, execprotocol.ErrInvalid
	}
	grants := make(map[string][]execprotocol.Capability, len(config.Grants))
	for agent, capabilities := range config.Grants {
		grants[agent] = slices.Clone(capabilities)
	}
	config.Grants = grants
	if err := os.MkdirAll(filepath.Join(config.StateDirectory, "operations"), 0700); err != nil {
		return nil, err
	}
	if info, err := os.Stat(config.StateDirectory); err != nil || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("execution state directory must be private to the current user")
	}
	lock, err := acquireLock(filepath.Join(config.StateDirectory, "executor.lock"))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := &Engine{config: config, allowed: grants, operations: make(map[string]*operation), ctx: ctx, cancel: cancel, slots: make(chan struct{}, config.Concurrency), connections: make(chan struct{}, 16), observers: make(chan struct{}, 8), lock: lock}
	if err := e.checkIdentity(); err != nil {
		cancel()
		_ = lock.Close()
		return nil, err
	}
	e.files, err = blob.Open(filepath.Join(config.StateDirectory, "files"))
	if err != nil {
		cancel()
		_ = lock.Close()
		return nil, err
	}
	if err := e.load(); err != nil {
		cancel()
		_ = e.files.Close()
		_ = lock.Close()
		return nil, err
	}
	return e, nil
}

func (e *Engine) Close() error {
	e.mu.Lock()
	e.cancel()
	e.mu.Unlock()
	e.workers.Wait()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.lock == nil {
		return nil
	}
	err := errors.Join(e.files.Close(), e.lock.Close())
	e.lock = nil
	return err
}

func (e *Engine) Submit(request execprotocol.Request) (execprotocol.Snapshot, error) {
	if err := request.Validate(); err != nil {
		return execprotocol.Snapshot{}, err
	}
	var arguments map[string]any
	decoder := json.NewDecoder(bytes.NewReader(request.Arguments))
	decoder.UseNumber()
	if err := decoder.Decode(&arguments); err != nil || arguments == nil {
		return execprotocol.Snapshot{}, execprotocol.ErrInvalid
	}
	request.Arguments, _ = json.Marshal(arguments)
	encoded, err := json.Marshal(request)
	if err != nil {
		return execprotocol.Snapshot{}, execprotocol.ErrInvalid
	}
	fileStatus, fileReserve, err := fileAdmission(request)
	if err != nil {
		return execprotocol.Snapshot{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ctx.Err() != nil || e.fault != nil {
		return execprotocol.Snapshot{}, execprotocol.ErrUnavailable
	}
	if !slices.Contains(e.allowed[request.AgentID], execprotocol.RequiredCapability(request.Kind)) {
		return execprotocol.Snapshot{}, execprotocol.ErrDenied
	}
	if existing := e.operations[request.ID]; existing != nil {
		if existing.record.Hash != digest(encoded) {
			return execprotocol.Snapshot{}, execprotocol.ErrConflict
		}
		return e.snapshotLocked(existing, 0, 64<<10)
	}
	if e.reservedBytes()+int64(len(encoded))+4096+e.config.OutputLimit > e.config.StorageLimit {
		return execprotocol.Snapshot{}, execprotocol.ErrQuota
	}
	if fileReserve > e.config.FileStorageLimit-e.reservedFileBytes() {
		return execprotocol.Snapshot{}, execprotocol.ErrQuota
	}
	output, err := os.OpenFile(e.path(request.ID, ".output"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return execprotocol.Snapshot{}, execprotocol.ErrUnavailable
	}
	if err := output.Close(); err != nil {
		return execprotocol.Snapshot{}, execprotocol.ErrUnavailable
	}
	op := &operation{record: record{Request: request, Hash: digest(encoded), State: execprotocol.Accepted, CreatedAt: time.Now().UTC(), File: fileStatus, FileReserved: fileReserve}}
	if err := e.save(&op.record); err != nil {
		return execprotocol.Snapshot{}, err
	}
	e.operations[request.ID] = op
	if request.Kind == "import_file" {
		op.fileReady = make(chan struct{})
		if _, err := e.files.Begin(e.fileID(request.ID), fileStatus.Manifest); err != nil {
			op.record.State = execprotocol.Failed
			op.record.Error = err.Error()
			_ = e.save(&op.record)
			return e.snapshotLocked(op, 0, 64<<10)
		}
	}
	e.workers.Add(1)
	go func() { defer e.workers.Done(); e.execute(op) }()
	return e.snapshotLocked(op, 0, 64<<10)
}

// Restrict applies the platform's current grants within the locally approved
// ceiling. A remote update cannot grant a new Agent or capability by itself.
func (e *Engine) Restrict(grants map[string][]execprotocol.Capability) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.allowed = make(map[string][]execprotocol.Capability)
	for agent, capabilities := range grants {
		for _, capability := range capabilities {
			if slices.Contains(e.config.Grants[agent], capability) {
				e.allowed[agent] = append(e.allowed[agent], capability)
			}
		}
	}
	for _, op := range e.operations {
		if op.record.State.Terminal() || slices.Contains(e.allowed[op.record.Request.AgentID], execprotocol.RequiredCapability(op.record.Request.Kind)) {
			continue
		}
		op.record.CancelRequested = true
		if op.record.State == execprotocol.Accepted {
			op.record.State = execprotocol.Cancelled
		}
		if err := e.save(&op.record); err != nil {
			return err
		}
		if op.cancel != nil {
			op.cancel()
		}
	}
	return nil
}

func (e *Engine) Snapshot(agent, id string, cursor int64, limit int) (execprotocol.Snapshot, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	operation := e.operations[id]
	if operation == nil {
		return execprotocol.Snapshot{}, execprotocol.ErrNotFound
	}
	if operation.record.Request.AgentID != agent {
		return execprotocol.Snapshot{}, execprotocol.ErrDenied
	}
	return e.snapshotLocked(operation, cursor, limit)
}

func (e *Engine) snapshotLocked(operation *operation, cursor int64, limit int) (execprotocol.Snapshot, error) {
	r := operation.record
	if cursor < 0 || cursor > r.OutputBytes || limit < 1 || limit > 256<<10 {
		return execprotocol.Snapshot{}, execprotocol.ErrInvalid
	}
	snapshot := execprotocol.Snapshot{Version: execprotocol.Version, EnvironmentID: e.config.EnvironmentID, ID: r.Request.ID, AgentID: r.Request.AgentID, Kind: r.Request.Kind, State: r.State, NextCursor: cursor, OutputBytes: r.OutputBytes, Truncated: r.Truncated, OutputExpired: r.OutputExpired, ExitCode: r.ExitCode, Error: r.Error, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
	if r.File != nil {
		status := *r.File
		snapshot.File = &status
		snapshot.FileExpired = r.FileExpired
	}
	if r.OutputExpired {
		return snapshot, nil
	}
	file, err := os.Open(e.path(r.Request.ID, ".output"))
	if err != nil {
		return execprotocol.Snapshot{}, execprotocol.ErrUnavailable
	}
	defer file.Close()
	buffer := make([]byte, min(int64(limit), r.OutputBytes-cursor))
	n, err := file.ReadAt(buffer, cursor)
	if err != nil && !errors.Is(err, io.EOF) {
		return execprotocol.Snapshot{}, execprotocol.ErrUnavailable
	}
	snapshot.Output, snapshot.NextCursor = buffer[:n], cursor+int64(n)
	return snapshot, nil
}

func (e *Engine) Acknowledge(agent, id string, cursor int64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	op := e.operations[id]
	if op == nil {
		return execprotocol.ErrNotFound
	}
	if op.record.Request.AgentID != agent {
		return execprotocol.ErrDenied
	}
	if !op.record.State.Terminal() || cursor != op.record.OutputBytes {
		return execprotocol.ErrConflict
	}
	if op.record.AcknowledgedAt == nil {
		now := time.Now().UTC()
		op.record.AcknowledgedAt = &now
		if op.record.Request.Kind == "import_file" || op.record.Request.Kind == "export_file" && op.record.State != execprotocol.Completed && op.record.State != execprotocol.Unknown {
			op.record.FileAcknowledgedAt = &now
		}
		return e.save(&op.record)
	}
	return nil
}

func (e *Engine) Cancel(agent, id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	op := e.operations[id]
	if op == nil {
		return execprotocol.ErrNotFound
	}
	if op.record.Request.AgentID != agent {
		return execprotocol.ErrDenied
	}
	if op.record.State.Terminal() {
		return nil
	}
	op.record.CancelRequested = true
	if err := e.save(&op.record); err != nil {
		return err
	}
	if op.cancel != nil {
		op.cancel()
	}
	// A running file write or stdin write may already have external effects.
	// The worker, not this request, decides the observed terminal result.
	if op.record.State == execprotocol.Accepted {
		op.record.State = execprotocol.Cancelled
		return e.save(&op.record)
	}
	return nil
}

func (e *Engine) execute(op *operation) {
	if op.fileReady != nil {
		ctx, cancel := context.WithCancel(e.ctx)
		defer cancel()
		e.mu.Lock()
		if op.record.State != execprotocol.Accepted || op.record.CancelRequested {
			e.mu.Unlock()
			return
		}
		op.cancel = cancel
		e.mu.Unlock()
		select {
		case <-ctx.Done():
			e.finish(op, nil, ctx.Err())
			return
		case <-op.fileReady:
		}
	}
	// Stdin must not queue behind the command waiting to consume it.
	if op.record.Request.Kind != "observe_command" && op.record.Request.Kind != "write_stdin" && op.record.Request.Kind != "mcp_connect" && op.record.Request.Kind != "mcp_close" {
		select {
		case e.slots <- struct{}{}:
			defer func() { <-e.slots }()
		case <-e.ctx.Done():
			e.finish(op, nil, context.Canceled)
			return
		}
	}
	e.mu.Lock()
	if op.record.State != execprotocol.Accepted {
		e.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(e.ctx)
	op.cancel, op.record.State = cancel, execprotocol.Running
	err := e.save(&op.record)
	e.mu.Unlock()
	defer cancel()
	if err != nil {
		return
	}
	if ctx.Err() != nil {
		e.finish(op, nil, ctx.Err())
		return
	}
	var exit *int
	switch op.record.Request.Kind {
	case "observe_command":
		exit, err = e.observeCommand(ctx, op)
	case "run_hook":
		exit, err = e.runHook(ctx, op)
	case "exec_command":
		exit, err = e.command(ctx, op)
	case "write_stdin":
		err = e.writeStdin(ctx, op)
	case "mcp_connect":
		err = e.connectMCP(ctx, op)
	case "mcp_call", "mcp_list", "mcp_close":
		err = e.useMCP(ctx, op)
	case "export_file", "import_file":
		err = e.transferFile(ctx, op)
	default:
		err = e.fileOperation(ctx, op)
	}
	e.finish(op, exit, err)
}

func (e *Engine) finish(op *operation, exit *int, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if op.record.State.Terminal() {
		return
	}
	if e.fault != nil {
		err = execprotocol.ErrUnavailable
	}
	op.record.State, op.record.ExitCode = execprotocol.Completed, exit
	if err != nil {
		op.record.State, op.record.Error = execprotocol.Failed, err.Error()
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			op.record.State = execprotocol.Cancelled
		}
		if errors.Is(err, execprotocol.ErrOutcomeUnknown) || errors.Is(err, execprotocol.ErrUnavailable) {
			op.record.State = execprotocol.Unknown
		}
	}
	op.stdin, op.cancel, op.session = nil, nil, nil
	_ = e.save(&op.record)
}

type outputWriter struct {
	engine    *Engine
	operation *operation
}

func (w outputWriter) Write(data []byte) (int, error) {
	return w.write(data, false)
}

func (w outputWriter) write(data []byte, strict bool) (int, error) {
	e := w.engine
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.fault != nil {
		return 0, execprotocol.ErrUnavailable
	}
	r := &w.operation.record
	count := len(data)
	remaining := max(int64(0), e.config.OutputLimit-r.OutputBytes)
	if int64(count) > remaining {
		r.Truncated = true
		if strict {
			if err := e.save(r); err != nil {
				return 0, err
			}
			return 0, execprotocol.ErrQuota
		}
		data = data[:remaining]
	}
	if len(data) > 0 {
		file, err := os.OpenFile(e.path(r.Request.ID, ".output"), os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			e.fault = err
			return 0, execprotocol.ErrUnavailable
		}
		n, writeErr := file.Write(data)
		syncErr, closeErr := file.Sync(), file.Close()
		if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
			e.fault = err
			return 0, execprotocol.ErrUnavailable
		}
		r.OutputBytes += int64(n)
	}
	if err := e.save(r); err != nil {
		return 0, err
	}
	return count, nil
}

func decodeArguments(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return execprotocol.ErrInvalid
	}
	return nil
}
