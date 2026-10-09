package native

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"

	"github.com/juex-ai/juex/internal/foundation/command"
)

// ProcessUser is trusted hosted configuration, never a tool argument. Native
// devices leave it nil to use their explicitly approved OS account.
type ProcessUser struct {
	UID, GID     uint32
	Home, Helper string
}

func (e *Engine) processEnvironment(extra map[string]string) ([]string, error) {
	if e.config.ProcessUser == nil {
		if home := e.config.HomeDirectory; home != "" {
			values := map[string]string{"HOME": home, "NPM_CONFIG_PREFIX": home + "/.local", "PYTHONUSERBASE": home + "/.local", "PATH": home + "/.local/bin:" + os.Getenv("PATH")}
			for key, value := range extra {
				values[key] = value
			}
			return processEnvironment(values)
		}
		return processEnvironment(extra)
	}
	values := map[string]string{"HOME": e.config.ProcessUser.Home, "USER": "agent", "LOGNAME": "agent", "TMPDIR": "/tmp", "NPM_CONFIG_PREFIX": e.config.ProcessUser.Home + "/.local", "PYTHONUSERBASE": e.config.ProcessUser.Home + "/.local"}
	for k, v := range extra {
		values[k] = v
	}
	return processEnvironment(values)
}

func (e *Engine) fileWorker(ctx context.Context, op *operation) error {
	directory, err := e.workingDirectory("")
	if err != nil {
		return err
	}
	environment, err := e.processEnvironment(nil)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(op.record.Request)
	if err != nil {
		return err
	}
	cmd, err := fileHelperCommand(ctx, e.config.ProcessUser.Helper, directory, environment, e.config.ProcessUser, "file-tool", "--working-directory", directory)
	if err != nil {
		return err
	}
	cmd.Stdin = bytes.NewReader(payload)
	cmd.Stdout = outputWriter{engine: e, operation: op}
	stderr := &workerError{}
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("file operation: %w: %s", err, stderr.String())
	}
	return nil
}

func fileHelperCommand(ctx context.Context, executable, directory string, environment []string, user *ProcessUser, args ...string) (*exec.Cmd, error) {
	cmd := exec.CommandContext(ctx, executable, args...)
	// These fixed file helpers spawn no descendants. Wait for complete output,
	// including slow journal fsyncs or transfer receivers, after the helper exits.
	cmd.Dir, cmd.Env = directory, environment
	if err := configureProcessUser(cmd, user); err != nil {
		return nil, err
	}
	command.ConfigureContext(cmd)
	return cmd, nil
}

type workerError struct{ buffer bytes.Buffer }

func (w *workerError) Write(p []byte) (int, error) {
	_, _ = w.buffer.Write(p[:min(len(p), max(0, 8192-w.buffer.Len()))])
	return len(p), nil
}

func (w *workerError) String() string { return w.buffer.String() }

func (w *workerError) Bytes() []byte { return w.buffer.Bytes() }
