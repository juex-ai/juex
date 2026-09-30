package native

import (
	"context"
	"encoding/json"
	"io"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

func fileAdmission(request execprotocol.Request) (*execprotocol.FileStatus, int64, error) {
	if request.Kind != "export_file" && request.Kind != "import_file" {
		return nil, 0, nil
	}
	var args execprotocol.FileTransferArguments
	if err := decodeArguments(request.Arguments, &args); err != nil {
		return nil, 0, err
	}
	if _, err := transferPath("/", args.Path); err != nil {
		return nil, 0, err
	}
	if request.Kind == "export_file" {
		if args.Manifest != nil {
			return nil, 0, execprotocol.ErrInvalid
		}
		return nil, execprotocol.MaxFileBytes + 4096, nil
	}
	if args.Manifest == nil || args.Manifest.Validate() != nil {
		return nil, 0, execprotocol.ErrInvalid
	}
	return &execprotocol.FileStatus{Manifest: *args.Manifest}, args.Manifest.Size + 4096, nil
}

func (e *Engine) fileID(id string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(e.config.EnvironmentID+"\x00"+id)).String()
}

func (e *Engine) reservedFileBytes() int64 {
	var size int64
	for _, op := range e.operations {
		size += op.record.FileReserved
	}
	return size
}

func (e *Engine) fileOperationLocked(agent, id, kind string) (*operation, error) {
	if e.ctx.Err() != nil || e.fault != nil {
		return nil, execprotocol.ErrUnavailable
	}
	op := e.operations[id]
	if op == nil {
		return nil, execprotocol.ErrNotFound
	}
	if op.record.Request.AgentID != agent || op.record.Request.Kind != kind || !slices.Contains(e.allowed[agent], execprotocol.Files) {
		return nil, execprotocol.ErrDenied
	}
	if op.record.FileExpired {
		return nil, execprotocol.ErrNotFound
	}
	return op, nil
}

func (e *Engine) ReadFile(agent, id string, offset int64, limit int) (execprotocol.FileChunk, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	op, err := e.fileOperationLocked(agent, id, "export_file")
	if err != nil {
		return execprotocol.FileChunk{}, err
	}
	if op.record.State != execprotocol.Completed || op.record.File == nil || !op.record.File.Ready {
		return execprotocol.FileChunk{}, execprotocol.ErrConflict
	}
	return e.files.Read(e.fileID(id), offset, limit)
}

func (e *Engine) WriteFile(agent, id string, chunk execprotocol.FileChunk) (execprotocol.FileStatus, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	op, err := e.fileOperationLocked(agent, id, "import_file")
	if err != nil {
		return execprotocol.FileStatus{}, err
	}
	if op.record.State != execprotocol.Accepted || op.record.CancelRequested || op.record.File == nil {
		return execprotocol.FileStatus{}, execprotocol.ErrConflict
	}
	status, err := e.files.Write(e.fileID(id), chunk)
	if err != nil {
		return execprotocol.FileStatus{}, err
	}
	op.record.File = &status
	return status, e.save(&op.record)
}

func (e *Engine) CommitFile(agent, id string) (execprotocol.FileStatus, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	op, err := e.fileOperationLocked(agent, id, "import_file")
	if err != nil {
		return execprotocol.FileStatus{}, err
	}
	if op.record.File == nil || op.record.CancelRequested || op.record.State != execprotocol.Accepted && op.record.State != execprotocol.Running && op.record.State != execprotocol.Completed {
		return execprotocol.FileStatus{}, execprotocol.ErrConflict
	}
	if op.record.File.Ready {
		return *op.record.File, nil
	}
	status, err := e.files.Commit(e.fileID(id))
	if err != nil {
		return execprotocol.FileStatus{}, err
	}
	op.record.File = &status
	if err := e.save(&op.record); err != nil {
		return execprotocol.FileStatus{}, err
	}
	close(op.fileReady)
	return status, nil
}

// File acknowledgment is separate from the small operation result. A source
// snapshot must survive until the platform has durably received its bytes.
func (e *Engine) AcknowledgeFile(agent, id string, manifest execprotocol.FileManifest) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ctx.Err() != nil || e.fault != nil {
		return execprotocol.ErrUnavailable
	}
	op := e.operations[id]
	if op == nil {
		return execprotocol.ErrNotFound
	}
	if op.record.Request.AgentID != agent || op.record.Request.Kind != "export_file" {
		return execprotocol.ErrDenied
	}
	if op.record.State != execprotocol.Completed || op.record.File == nil || !op.record.File.Ready || op.record.File.Manifest != manifest {
		return execprotocol.ErrConflict
	}
	if op.record.FileAcknowledgedAt != nil {
		return nil
	}
	now := time.Now().UTC()
	op.record.FileAcknowledgedAt = &now
	return e.save(&op.record)
}

func (e *Engine) transferFile(ctx context.Context, op *operation) error {
	var args execprotocol.FileTransferArguments
	if err := decodeArguments(op.record.Request.Arguments, &args); err != nil {
		return err
	}
	directory, err := e.workingDirectory(args.WorkingDirectory)
	if err != nil {
		return err
	}
	id := e.fileID(op.record.Request.ID)
	if op.record.Request.Kind == "export_file" {
		status, err := e.files.Capture(id, func(out io.Writer) error {
			if e.config.ProcessUser != nil {
				return e.exportWorker(ctx, directory, args.Path, out)
			}
			return RunFileExport(ctx, directory, args.Path, out)
		})
		if err != nil {
			if removeErr := e.files.Remove(id); removeErr != nil {
				return execprotocol.ErrUnavailable
			}
			e.mu.Lock()
			op.record.FileReserved = 0
			saveErr := e.save(&op.record)
			e.mu.Unlock()
			if saveErr != nil {
				return saveErr
			}
			return err
		}
		e.mu.Lock()
		op.record.File = &status
		op.record.FileReserved = status.Manifest.Size + 4096
		err = e.save(&op.record)
		e.mu.Unlock()
		if err != nil {
			return err
		}
		return json.NewEncoder(outputWriter{engine: e, operation: op}).Encode(status.Manifest)
	}
	reader, err := e.files.OpenReader(id)
	if err != nil {
		return err
	}
	defer reader.Close()
	if e.config.ProcessUser != nil {
		err = e.importWorker(ctx, directory, args.Path, *args.Manifest, reader)
	} else {
		err = RunFileImport(ctx, directory, args.Path, *args.Manifest, reader)
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(outputWriter{engine: e, operation: op}).Encode(args.Manifest)
}
