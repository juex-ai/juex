package observable

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/juex-ai/juex/internal/environment"
	"github.com/juex-ai/juex/internal/sandbox"
)

type runnerOptions struct {
	spec          commandRuntimeSpec
	runID         string
	workDir       string
	agentStateDir string
	artifactDir   string
	environment   environment.Snapshot
	sandboxPolicy sandbox.Policy
	sandboxRunner sandbox.Runner
	store         *Store
	submit        func(context.Context, ObservationRecord) bool
	runtime       RuntimeContext
	source        string
	extension     bool
}

type runner struct {
	opts       runnerOptions
	filePolicy sandbox.FilePolicy
	pipe       *Pipeline
	batcher    *Batcher
	cmd        *exec.Cmd
	mu         sync.Mutex
	wg         sync.WaitGroup
	flushCh    chan struct{}
}

func newRunner(opts runnerOptions) *runner {
	pipe, _ := newCommandPipeline(opts.spec)
	filePolicy := sandbox.NewFilePolicy(sandbox.FilePolicyOptions{
		Policy:        opts.sandboxPolicy,
		WorkDir:       opts.workDir,
		AgentStateDir: opts.agentStateDir,
		ReadOnlyPaths: []string{opts.artifactDir},
	})
	return &runner{
		opts:       opts,
		filePolicy: filePolicy,
		pipe:       pipe,
		batcher: newCommandBatcher(opts.spec, opts.store, BatcherOptions{
			RunID: opts.runID, WorkDir: opts.workDir, AgentStateDir: opts.agentStateDir, ArtifactDir: opts.artifactDir,
			PathGuard: filePolicy,
		}),
	}
}

func (r *runner) start(callCtx context.Context, runCtx context.Context) (*exec.Cmd, error) {
	if callCtx == nil {
		callCtx = context.Background()
	}
	if err := callCtx.Err(); err != nil {
		return nil, err
	}
	runtimeSpec, reserved, err := prepareCommandRuntime(r.opts.spec, r.opts.workDir, r.opts.runtime, r.opts.extension)
	if err != nil {
		return nil, err
	}
	cwd := r.opts.workDir
	if runtimeSpec.CWD != "" {
		cwd = runtimeSpec.CWD
		if !filepath.IsAbs(cwd) && r.opts.workDir != "" {
			cwd = filepath.Join(r.opts.workDir, cwd)
		}
	}
	command, err := r.opts.environment.LookPathInDir(runtimeSpec.Command, cwd)
	if err != nil {
		return nil, err
	}
	spec := sandbox.ExecSpec{
		Binary: command,
		Args:   append([]string(nil), runtimeSpec.Args...),
		Dir:    cwd,
		Env:    r.env(runtimeSpec.Env, reserved),
	}
	if r.opts.extension && r.opts.runtime.ExtensionDataDir != "" {
		if r.opts.runtime.PrepareExtensionDataDir == nil {
			return nil, fmt.Errorf("observable: extension source %s has a data directory without a prepare callback", r.opts.source)
		}
		if err := callCtx.Err(); err != nil {
			return nil, err
		}
		if err := r.opts.runtime.PrepareExtensionDataDir(); err != nil {
			return nil, err
		}
		if err := callCtx.Err(); err != nil {
			return nil, err
		}
	}
	if r.opts.sandboxPolicy.Enabled {
		sandboxRunner := r.opts.sandboxRunner
		if sandboxRunner == nil {
			sandboxRunner = sandbox.DefaultRunner{}
		}
		prepared, err := sandboxRunner.Prepare(callCtx, sandbox.Request{
			Policy:     r.opts.sandboxPolicy,
			WorkDir:    r.opts.workDir,
			FilePolicy: r.filePolicy,
			Spec:       spec,
		})
		if err != nil {
			return nil, err
		}
		spec = prepared
	}
	if err := callCtx.Err(); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(runCtx, spec.Binary, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	configureObservableCommand(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := callCtx.Err(); err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	r.cmd = cmd
	r.flushCh = make(chan struct{})
	if r.watchesStream(StreamStdout) {
		r.wg.Add(1)
		go r.readStream(StreamStdout, stdout)
	} else {
		r.wg.Add(1)
		go r.drainStream(stdout)
	}
	if r.watchesStream(StreamStderr) {
		r.wg.Add(1)
		go r.readStream(StreamStderr, stderr)
	} else {
		r.wg.Add(1)
		go r.drainStream(stderr)
	}
	r.wg.Add(1)
	go r.flushLoop(r.flushCh)
	return cmd, nil
}

func (r *runner) wait() (*int, error) {
	if r == nil || r.cmd == nil {
		return nil, nil
	}
	err := r.cmd.Wait()
	if r.flushCh != nil {
		close(r.flushCh)
	}
	r.wg.Wait()
	var exitCode *int
	if r.cmd.ProcessState != nil {
		code := r.cmd.ProcessState.ExitCode()
		exitCode = &code
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitCode, err
		}
		return exitCode, err
	}
	return exitCode, nil
}

func (r *runner) flush(reason string) ([]ObservationRecord, error) {
	if r == nil || r.batcher == nil {
		return nil, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var firstErr error
	units, err := r.pipe.Flush()
	if err != nil {
		firstErr = err
		units = append(units, parseErrorUnit("", err))
	}
	for _, unit := range units {
		if _, addErr := r.batcher.Add(unit); addErr != nil && firstErr == nil {
			firstErr = addErr
		}
	}
	flushed, err := r.batcher.Flush(reason)
	if err != nil && firstErr == nil {
		firstErr = err
	}
	return flushed, firstErr
}

func (r *runner) flushLoop(stop <-chan struct{}) {
	defer r.wg.Done()
	ticker := time.NewTicker(flushPollInterval(time.Duration(r.opts.spec.Batch.IntervalSeconds) * time.Second))
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			var records []ObservationRecord
			r.mu.Lock()
			flushed, err := r.batcher.FlushDue(time.Now().UTC(), "interval")
			if err == nil {
				records = append(records, flushed...)
			}
			r.mu.Unlock()
			r.deliver(records)
		}
	}
}

func (r *runner) readStream(stream string, reader io.Reader) {
	defer r.wg.Done()
	buf := make([]byte, 4096)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			var records []ObservationRecord
			r.mu.Lock()
			units, acceptErr := r.pipe.Accept(stream, buf[:n])
			for _, unit := range units {
				flushed, addErr := r.batcher.Add(unit)
				if addErr == nil {
					records = append(records, flushed...)
				}
			}
			if acceptErr != nil {
				if _, addErr := r.batcher.Add(parseErrorUnit(stream, acceptErr)); addErr == nil {
					flushed, flushErr := r.batcher.Flush("parse_error")
					if flushErr == nil {
						records = append(records, flushed...)
					}
				}
			}
			r.mu.Unlock()
			r.deliver(records)
		}
		if err != nil {
			return
		}
	}
}

func (r *runner) drainStream(reader io.Reader) {
	defer r.wg.Done()
	_, _ = io.Copy(io.Discard, reader)
}

func (r *runner) deliver(records []ObservationRecord) {
	for _, record := range records {
		if r.opts.submit != nil {
			r.opts.submit(context.Background(), record)
		}
	}
}

func parseErrorUnit(stream string, err error) ParsedUnit {
	return ParsedUnit{
		Stream:     stream,
		Kind:       "observable_parse_error",
		Severity:   "error",
		Content:    err.Error(),
		ReceivedAt: time.Now().UTC(),
	}
}

func flushPollInterval(interval time.Duration) time.Duration {
	if interval <= 0 {
		return time.Second
	}
	if interval < time.Second {
		return interval
	}
	return time.Second
}

func (r *runner) watchesStream(stream string) bool {
	for _, value := range r.opts.spec.Streams {
		if value == stream {
			return true
		}
	}
	return false
}

func (r *runner) env(child, reserved map[string]string) []string {
	env := r.opts.environment.Environ(
		child,
		reserved,
	)
	if r.opts.extension {
		return env
	}
	return stripExtensionEnvironment(env)
}

func prepareCommandRuntime(spec commandRuntimeSpec, workDir string, runtime RuntimeContext, extension bool) (commandRuntimeSpec, map[string]string, error) {
	out := spec
	out.Args = append([]string(nil), spec.Args...)
	out.Env = cloneStringMap(spec.Env)
	reserved := map[string]string{
		"WORKDIR":      workDir,
		"JUEX_WORKDIR": workDir,
	}
	if extension {
		reserved["JUEX_EXT_DIR"] = runtime.ExtensionDir
		reserved["JUEX_EXT_DATA_DIR"] = runtime.ExtensionDataDir
	} else {
		for key := range out.Env {
			if key == "JUEX_EXT_DIR" || key == "JUEX_EXT_DATA_DIR" {
				return commandRuntimeSpec{}, nil, fmt.Errorf("observable: project definition cannot set reserved environment key %s", key)
			}
		}
	}
	var err error
	if out.Command, err = expandRuntimeValue(out.Command, reserved, extension); err != nil {
		return commandRuntimeSpec{}, nil, err
	}
	for index := range out.Args {
		if out.Args[index], err = expandRuntimeValue(out.Args[index], reserved, extension); err != nil {
			return commandRuntimeSpec{}, nil, err
		}
	}
	if out.CWD, err = expandRuntimeValue(out.CWD, reserved, extension); err != nil {
		return commandRuntimeSpec{}, nil, err
	}
	for key, value := range out.Env {
		if out.Env[key], err = expandRuntimeValue(value, reserved, extension); err != nil {
			return commandRuntimeSpec{}, nil, err
		}
	}
	return out, reserved, nil
}

func stripExtensionEnvironment(env []string) []string {
	out := make([]string, 0, len(env))
	for _, item := range env {
		key := item
		if index := strings.IndexByte(item, '='); index >= 0 {
			key = item[:index]
		}
		if strings.EqualFold(key, "JUEX_EXT_DIR") || strings.EqualFold(key, "JUEX_EXT_DATA_DIR") {
			continue
		}
		out = append(out, item)
	}
	return out
}
