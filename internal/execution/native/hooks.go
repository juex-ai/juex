package native

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"time"

	"github.com/juex-ai/juex/internal/foundation/command"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/processidentity"
)

func (e *Engine) runHook(parent context.Context, op *operation) (*int, error) {
	var args execprotocol.HookCommand
	if err := decodeArguments(op.record.Request.Arguments, &args); err != nil {
		return nil, err
	}
	if err := args.Validate(); err != nil {
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
	ctx, cancel := context.WithTimeout(parent, time.Duration(args.TimeoutMS)*time.Millisecond)
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
	stdout := &hookBuffer{limit: args.MaxOutputBytes, cancel: cancel}
	stderr := &hookBuffer{limit: args.MaxOutputBytes, cancel: cancel}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = bytes.NewReader(args.Input), stdout, stderr
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
	// Freeze the observed process outcome before storage can outlive its
	// deadline. A later timeout cannot undo a completed policy decision.
	if waitErr != nil && ctx.Err() != nil {
		waitErr = ctx.Err()
	}
	cancel()
	code := cmd.ProcessState.ExitCode()
	output, marshalErr := json.Marshal(execprotocol.HookOutput{Stdout: stdout.String(), Stderr: stderr.String(), Overflow: stdout.overflow || stderr.overflow})
	if marshalErr != nil {
		return &code, marshalErr
	}
	if _, writeErr := (outputWriter{engine: e, operation: op}).Write(output); writeErr != nil {
		return &code, writeErr
	}
	if err != nil {
		return &code, err
	}
	if stdout.overflow || stderr.overflow {
		return &code, errors.New("hook output exceeded its configured limit")
	}
	// Exit 2 is an explicit policy decision interpreted by Runtime.
	var exitErr *exec.ExitError
	if code == 2 && errors.As(waitErr, &exitErr) {
		return &code, nil
	}
	return &code, waitErr
}

type hookBuffer struct {
	buffer   bytes.Buffer
	limit    int
	overflow bool
	cancel   context.CancelFunc
}

func (b *hookBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - b.buffer.Len()
	_, _ = b.buffer.Write(p[:min(len(p), remaining)])
	if len(p) > remaining {
		b.overflow = true
		b.cancel()
	}
	return len(p), nil
}

func (b *hookBuffer) String() string { return b.buffer.String() }
