package observable

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/juex-ai/juex/internal/sandbox"
)

type runnerOptions struct {
	spec          Spec
	runID         string
	workDir       string
	sandboxPolicy sandbox.Policy
	sandboxRunner sandbox.Runner
	store         *Store
	deliver       func(context.Context, ObservationRecord) error
}

type runner struct {
	opts    runnerOptions
	pipe    *Pipeline
	batcher *Batcher
	cmd     *exec.Cmd
	mu      sync.Mutex
	wg      sync.WaitGroup
	flushCh chan struct{}
}

func newRunner(opts runnerOptions) *runner {
	pipe, _ := NewPipeline(opts.spec)
	return &runner{
		opts:    opts,
		pipe:    pipe,
		batcher: NewBatcher(opts.spec, opts.store, BatcherOptions{RunID: opts.runID}),
	}
}

func (r *runner) start(callCtx context.Context, runCtx context.Context) (*exec.Cmd, error) {
	if callCtx == nil {
		callCtx = context.Background()
	}
	cwd := r.opts.workDir
	if r.opts.spec.CWD != "" {
		cwd = ExpandVariables(r.opts.spec.CWD, r.opts.workDir)
		if !filepath.IsAbs(cwd) && r.opts.workDir != "" {
			cwd = filepath.Join(r.opts.workDir, cwd)
		}
	}
	spec := sandbox.ExecSpec{
		Binary: r.opts.spec.Command,
		Args:   append([]string(nil), r.opts.spec.Args...),
		Dir:    cwd,
		Env:    r.env(),
	}
	if r.opts.sandboxPolicy.Enabled {
		sandboxRunner := r.opts.sandboxRunner
		if sandboxRunner == nil {
			sandboxRunner = sandbox.DefaultRunner{}
		}
		prepared, err := sandboxRunner.Prepare(callCtx, sandbox.Request{
			Policy:         r.opts.sandboxPolicy,
			WorkspaceRoots: []string{r.opts.workDir},
			Spec:           spec,
		})
		if err != nil {
			return nil, err
		}
		spec = prepared
	}
	cmd := exec.CommandContext(runCtx, spec.Binary, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
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
	return r.batcher.Flush(reason)
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
			if acceptErr == nil {
				for _, unit := range units {
					flushed, addErr := r.batcher.Add(unit)
					if addErr == nil {
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
		if r.opts.deliver != nil {
			_ = r.opts.deliver(context.Background(), record)
		}
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

func (r *runner) env() []string {
	env := os.Environ()
	env = append(env, "WORKDIR="+r.opts.workDir, "JUEX_WORKDIR="+r.opts.workDir)
	for key, value := range r.opts.spec.Env {
		env = append(env, key+"="+ExpandVariables(value, r.opts.workDir))
	}
	return env
}
