package native

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/foundation/command"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/processidentity"
)

type CommandArguments struct {
	Extension        *execprotocol.ExtensionContext `json:"extension,omitempty"`
	Command          string                         `json:"command"`
	WorkingDirectory string                         `json:"working_directory"`
	TTY              bool                           `json:"tty"`
	TimeoutMS        int64                          `json:"timeout_ms"`
	Environment      map[string]string              `json:"environment"`
}

func (e *Engine) workingDirectory(directory string) (string, error) {
	if directory == "" {
		return e.config.WorkingDirectory, nil
	}
	if !filepath.IsAbs(directory) {
		return "", execprotocol.ErrInvalid
	}
	return filepath.Clean(directory), nil
}

// Only deliberate OS environment values are inherited. In particular, the
// connector's pairing credentials and platform service configuration stay out.
func processEnvironment(extra map[string]string) ([]string, error) {
	values := map[string]string{}
	for _, key := range []string{"PATH", "HOME", "LANG", "LC_ALL", "LC_CTYPE", "TMPDIR", "USER", "LOGNAME", "SHELL"} {
		if value, ok := os.LookupEnv(key); ok {
			values[key] = value
		}
	}
	values["TERM"] = "xterm-256color"
	for key, value := range extra {
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, 0) {
			return nil, execprotocol.ErrInvalid
		}
		values[key] = value
	}
	result := make([]string, 0, len(values))
	for key, value := range values {
		result = append(result, key+"="+value)
	}
	return result, nil
}

func (e *Engine) command(ctx context.Context, operation *operation) (*int, error) {
	var args CommandArguments
	if err := decodeArguments(operation.record.Request.Arguments, &args); err != nil {
		return nil, err
	}
	if args.Command == "" || len(args.Command) > 128<<10 || args.TimeoutMS < 0 || args.TimeoutMS > int64((24*time.Hour)/time.Millisecond) {
		return nil, execprotocol.ErrInvalid
	}
	if args.TimeoutMS == 0 {
		args.TimeoutMS = int64(time.Hour / time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(args.TimeoutMS)*time.Millisecond)
	defer cancel()
	directory, err := e.workingDirectory(args.WorkingDirectory)
	if err != nil {
		return nil, err
	}
	environment, err := e.processEnvironment(args.Environment)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", args.Command)
	cmd.Dir, cmd.Env, cmd.WaitDelay = directory, environment, 2*time.Second
	if err := e.extensionCommand(cmd, operation.record.Request.AgentID, args.Extension); err != nil {
		return nil, err
	}
	if err := configureProcessUser(cmd, e.config.ProcessUser); err != nil {
		return nil, err
	}
	writer := outputWriter{engine: e, operation: operation}
	var input io.WriteCloser
	var outputDone <-chan struct{}
	if args.TTY {
		command.ConfigureTerminalContext(cmd)
		input, outputDone, err = startTerminal(cmd, writer)
	} else {
		command.ConfigureContext(cmd)
		cmd.Stdout, cmd.Stderr = writer, writer
		input, err = cmd.StdinPipe()
		if err == nil {
			err = cmd.Start()
		}
	}
	if err != nil {
		if input != nil {
			_ = input.Close()
		}
		return nil, err
	}
	defer input.Close()
	e.mu.Lock()
	operation.stdin, operation.record.PID = input, cmd.Process.Pid
	operation.record.ProcessIdentity, _ = processidentity.Fingerprint(cmd.Process.Pid)
	err = e.save(&operation.record)
	e.mu.Unlock()
	if err != nil {
		cancel()
	}
	waitErr := cmd.Wait()
	if outputDone != nil {
		// A child can inherit the terminal after the shell exits. Closing the
		// owned PTY bounds shutdown instead of waiting on an unrelated daemon.
		select {
		case <-outputDone:
		case <-time.After(time.Second):
			_ = input.Close()
			<-outputDone
		}
	}
	code := cmd.ProcessState.ExitCode()
	if err != nil {
		return &code, err
	}
	if ctx.Err() != nil {
		return &code, ctx.Err()
	}
	return &code, waitErr
}

type StdinArguments struct {
	OperationID    string `json:"operation_id"`
	Chars          string `json:"chars"`
	After          int64  `json:"after"`
	YieldTimeMS    int    `json:"yield_time_ms"`
	MaxOutputBytes int    `json:"max_output_bytes"`
}

func (e *Engine) writeStdin(ctx context.Context, operation *operation) error {
	var args StdinArguments
	if err := decodeArguments(operation.record.Request.Arguments, &args); err != nil {
		return err
	}
	if len(args.Chars) > 64<<10 || args.YieldTimeMS < 0 || args.YieldTimeMS > 10000 {
		return execprotocol.ErrInvalid
	}
	if args.MaxOutputBytes == 0 {
		args.MaxOutputBytes = 64 << 10
	}
	e.mu.Lock()
	target := e.operations[args.OperationID]
	if target == nil {
		e.mu.Unlock()
		return execprotocol.ErrNotFound
	}
	if target.record.Request.AgentID != operation.record.Request.AgentID {
		e.mu.Unlock()
		return execprotocol.ErrDenied
	}
	input := target.stdin
	e.mu.Unlock()
	if args.Chars != "" {
		if input == nil {
			return errors.New("process stdin is not available")
		}
		written := make(chan error, 1)
		go func() {
			target.stdinMu.Lock()
			defer target.stdinMu.Unlock()
			n, err := io.WriteString(input, args.Chars)
			if err == nil && n != len(args.Chars) {
				err = io.ErrShortWrite
			}
			written <- err
		}()
		select {
		case err := <-written:
			if err != nil {
				return fmt.Errorf("stdin delivery incomplete: %w", err)
			}
		case <-ctx.Done():
			_ = input.Close()
			<-written
			return execprotocol.ErrOutcomeUnknown
		case <-time.After(5 * time.Second):
			_ = input.Close()
			<-written
			return execprotocol.ErrOutcomeUnknown
		}
	}
	if args.YieldTimeMS > 0 {
		timer := time.NewTimer(time.Duration(args.YieldTimeMS) * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	snapshot, err := e.Snapshot(operation.record.Request.AgentID, args.OperationID, args.After, args.MaxOutputBytes)
	if err != nil {
		return err
	}
	data, err := json.Marshal(struct {
		execprotocol.Snapshot
		Output string `json:"output"`
	}{Snapshot: snapshot, Output: snapshot.Text()})
	if err != nil {
		return err
	}
	_, err = (outputWriter{engine: e, operation: operation}).Write(data)
	return err
}
