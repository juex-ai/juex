package native

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

type FileTransferResult struct {
	Error string `json:"error"`
}

func (e *Engine) transferWorker(ctx context.Context, directory, path, direction string) (*exec.Cmd, error) {
	environment, err := e.processEnvironment(nil)
	if err != nil {
		return nil, err
	}
	return fileHelperCommand(ctx, e.config.ProcessUser.Helper, directory, environment, e.config.ProcessUser, "transfer-tool", "--direction="+direction, "--working-directory="+directory, "--path="+path)
}

func (e *Engine) exportWorker(ctx context.Context, directory, path string, out io.Writer) error {
	cmd, err := e.transferWorker(ctx, directory, path, "export")
	if err != nil {
		return err
	}
	stderr := &workerError{}
	cmd.Stdout, cmd.Stderr = out, stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("file capture: %w: %s", err, stderr.String())
	}
	return nil
}

func (e *Engine) importWorker(ctx context.Context, directory, path string, manifest execprotocol.FileManifest, in io.Reader) error {
	cmd, err := e.transferWorker(ctx, directory, path, "import")
	if err != nil {
		return err
	}
	cmd.Args = append(cmd.Args, "--size="+strconv.FormatInt(manifest.Size, 10), "--sha256="+manifest.SHA256)
	result := &workerError{}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, result, &workerError{}
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := cmd.Wait(); err != nil {
		return execprotocol.ErrOutcomeUnknown
	}
	var outcome FileTransferResult
	if json.Unmarshal(result.Bytes(), &outcome) != nil {
		return execprotocol.ErrOutcomeUnknown
	}
	if outcome.Error == "cancelled" {
		return context.Canceled
	}
	return execprotocol.FromErrorCode(outcome.Error)
}
