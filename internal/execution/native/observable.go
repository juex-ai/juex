package native

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"slices"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/juex-ai/juex/internal/foundation/command"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/processidentity"
)

func (e *Engine) observeCommand(ctx context.Context, op *operation) (*int, error) {
	select {
	case e.observers <- struct{}{}:
		defer func() { <-e.observers }()
	default:
		return nil, execprotocol.ErrQuota
	}
	var args execprotocol.ObservableCommand
	if err := decodeArguments(op.record.Request.Arguments, &args); err != nil {
		return nil, err
	}
	if err := (execprotocol.HookCommand{Command: args.Command, Input: json.RawMessage(`{}`), Environment: args.Environment, Extension: args.Extension, TimeoutMS: 1, MaxOutputBytes: 1}).Validate(); err != nil {
		return nil, err
	}
	if err := args.Options.Validate(); err != nil {
		return nil, err
	}
	directory, err := e.workingDirectory(args.WorkingDirectory)
	if err != nil {
		return nil, err
	}
	environment, err := e.processEnvironment(args.Environment)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, args.Command[0], args.Command[1:]...)
	cmd.Dir, cmd.Env, cmd.WaitDelay = directory, environment, 2*time.Second
	if err := e.extensionCommand(cmd, op.record.Request.AgentID, args.Extension); err != nil {
		return nil, err
	}
	if err := configureProcessUser(cmd, e.config.ProcessUser); err != nil {
		return nil, err
	}
	command.ConfigureContext(cmd)
	output := outputWriter{engine: e, operation: op}
	streams := args.Options.Streams
	if len(streams) == 0 {
		streams = []string{"stdout", "stderr"}
	}
	stdout := &observableWriter{writer: output, stream: "stdout", enabled: slices.Contains(streams, "stdout"), jsonLines: args.Options.Parser.Type == "jsonl", cancel: cancel}
	stderr := &observableWriter{writer: output, stream: "stderr", enabled: slices.Contains(streams, "stderr"), jsonLines: args.Options.Parser.Type == "jsonl", cancel: cancel}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	e.mu.Lock()
	op.record.PID = cmd.Process.Pid
	op.record.ProcessIdentity, _ = processidentity.Fingerprint(cmd.Process.Pid)
	err = e.save(&op.record)
	e.mu.Unlock()
	if err != nil {
		cancel()
	}
	waitErr := cmd.Wait()
	streamErr := errors.Join(stdout.finish(), stderr.finish())
	code := cmd.ProcessState.ExitCode()
	if err != nil {
		return &code, err
	}
	if streamErr != nil {
		return &code, streamErr
	}
	if ctx.Err() != nil {
		return &code, ctx.Err()
	}
	return &code, waitErr
}

type observableWriter struct {
	mu        sync.Mutex
	writer    outputWriter
	stream    string
	enabled   bool
	jsonLines bool
	pending   []byte
	err       error
	cancel    context.CancelFunc
}

func (w *observableWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.enabled {
		return len(data), nil
	}
	if w.err != nil {
		return 0, w.err
	}
	count := len(data)
	if !w.jsonLines {
		w.pending = append(w.pending, data...)
		for len(w.pending) > 0 {
			n := 0
			for n < len(w.pending) && n < 16<<10 {
				if !utf8.FullRune(w.pending[n:]) {
					break
				}
				r, size := utf8.DecodeRune(w.pending[n:])
				if r == utf8.RuneError && size == 1 {
					w.err = errors.New("observable text is not UTF-8")
					w.cancel()
					return 0, w.err
				}
				n += size
			}
			if n == 0 {
				break
			}
			remainder := bytes.Clone(w.pending[n:])
			w.pending = w.pending[:n]
			if err := w.emit(); err != nil {
				return 0, err
			}
			w.pending = remainder
		}
		return count, nil
	}
	for len(data) > 0 {
		end := bytes.IndexByte(data, '\n')
		n := len(data)
		if end >= 0 {
			n = end + 1
		}
		if len(w.pending)+n > 64<<10 {
			w.err = errors.New("observable line exceeds 64 KiB")
			w.cancel()
			return 0, w.err
		}
		w.pending = append(w.pending, data[:n]...)
		data = data[n:]
		if end >= 0 {
			if err := w.emit(); err != nil {
				return 0, err
			}
		}
	}
	return count, nil
}
func (w *observableWriter) emit() error {
	encoded, _ := json.Marshal(map[string]any{"type": "observation", "stream": w.stream, "text": string(w.pending), "time": time.Now().UTC()})
	if _, err := w.writer.write(append(encoded, '\n'), true); err != nil {
		w.err = err
		w.cancel()
		return err
	}
	w.pending = nil
	return nil
}
func (w *observableWriter) finish() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return w.err
	}
	if len(w.pending) > 0 {
		if !utf8.Valid(w.pending) {
			return errors.New("observable output ended with invalid UTF-8")
		}
		return w.emit()
	}
	return nil
}
