package native

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

const maxWriteBytes = 16 << 20
const maxWriteChunks = 4096
const writeIdleTTL = 2 * time.Hour

type writeBeginArguments struct {
	Path             string `json:"path"`
	Mode             string `json:"mode,omitempty"`
	WorkingDirectory string `json:"working_directory,omitempty"`
}
type writeChunkArguments struct {
	WriteID string  `json:"write_id"`
	Index   *int    `json:"index"`
	Content *string `json:"content"`
	SHA256  string  `json:"sha256,omitempty"`
}
type writeCommitArguments struct {
	WriteID        string `json:"write_id"`
	ExpectedChunks *int   `json:"expected_chunks,omitempty"`
	SHA256         string `json:"sha256,omitempty"`
}
type writeSession struct {
	begin    record
	chunks   map[int]string
	bytes    int64
	updated  time.Time
	terminal bool
	unknown  bool
}

// Requests and typed receipts already have durable, quota-bound identities.
// They are the single source of acknowledged chunks; output retention never
// removes them and restarting cannot replay a possibly published commit.
func (e *Engine) writeSessions() map[string]*writeSession {
	e.mu.Lock()
	defer e.mu.Unlock()
	sessions := map[string]*writeSession{}
	for _, op := range e.operations {
		r := op.record
		if r.Request.Kind == "write_begin" && r.Write != nil && r.WriteTarget != nil {
			sessions[r.Request.ID] = &writeSession{begin: r, chunks: map[int]string{}, updated: r.Write.At}
		}
	}
	for _, op := range e.operations {
		r := op.record
		if !execprotocol.IsChunkedWrite(r.Request.Kind) || r.Request.Kind == "write_begin" {
			continue
		}
		var handle struct {
			WriteID string `json:"write_id"`
		}
		if json.Unmarshal(r.Request.Arguments, &handle) != nil {
			continue
		}
		s := sessions[handle.WriteID]
		if s == nil || !sameWriteOwner(r.Request, s.begin.Request) {
			continue
		}
		if r.WriteAttempted && r.Write == nil {
			s.unknown = true
		}
		if r.Write == nil {
			continue
		}
		if r.Write.At.After(s.updated) {
			s.updated = r.Write.At
		}
		switch r.Write.Action {
		case "committed", "aborted":
			s.terminal = true
		case "chunk":
			var chunk writeChunkArguments
			if json.Unmarshal(r.Request.Arguments, &chunk) == nil && chunk.Content != nil && chunk.Index != nil {
				if _, exists := s.chunks[*chunk.Index]; !exists {
					s.chunks[*chunk.Index] = *chunk.Content
					s.bytes += int64(len(*chunk.Content))
				}
			}
		}
	}
	return sessions
}

func sameWriteOwner(a, b execprotocol.Request) bool {
	return a.AgentID == b.AgentID && a.AuthorizationVersion == b.AuthorizationVersion && a.WriteContext != nil && b.WriteContext != nil && *a.WriteContext == *b.WriteContext
}

func (e *Engine) chunkedWrite(ctx context.Context, op *operation) error {
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	r := op.record.Request
	sessions := e.writeSessions()
	now := time.Now().UTC()
	if r.Kind == "write_begin" {
		var args writeBeginArguments
		if err := decodeArguments(r.Arguments, &args); err != nil {
			return err
		}
		if args.Mode == "" {
			args.Mode = "overwrite"
		}
		directory, err := e.workingDirectory(args.WorkingDirectory)
		if err != nil {
			return err
		}
		reply, err := e.writeWorker(ctx, WriteWorkerInput{Action: "prepare", Directory: directory, Path: args.Path, Mode: args.Mode}, nil)
		if err != nil {
			return err
		}
		if reply.Target == nil {
			return execprotocol.ErrUnavailable
		}
		active := 0
		for _, s := range sessions {
			if s.terminal || (!s.unknown && now.Sub(s.updated) >= writeIdleTTL) {
				continue
			}
			active++
			prior := s.begin.WriteTarget
			sameTarget := prior.Identity == reply.Target.Identity || prior.FileIdentity != "" && prior.FileIdentity == reply.Target.FileIdentity
			if sameWriteOwner(r, s.begin.Request) && sameTarget {
				return errors.New("a write session already owns this target")
			}
		}
		if active >= 128 {
			return execprotocol.ErrQuota
		}
		return e.writeReceipt(op, &execprotocol.WriteReceipt{WriteID: r.ID, Action: "began", Path: reply.Target.Path, Mode: args.Mode, At: now}, reply.Target)
	}
	var handle struct {
		WriteID string `json:"write_id"`
	}
	if json.Unmarshal(r.Arguments, &handle) != nil || handle.WriteID == "" {
		return execprotocol.ErrInvalid
	}
	s := sessions[handle.WriteID]
	if s == nil {
		return execprotocol.ErrNotFound
	}
	if !sameWriteOwner(r, s.begin.Request) {
		return execprotocol.ErrDenied
	}
	if s.unknown {
		return execprotocol.ErrOutcomeUnknown
	}
	if s.terminal {
		return errors.New("write session has already finished")
	}
	if now.Sub(s.updated) >= writeIdleTTL {
		return errors.New("write session expired after two hours without a new chunk")
	}
	receipt := &execprotocol.WriteReceipt{WriteID: handle.WriteID, Path: s.begin.WriteTarget.Path, Mode: s.begin.WriteTarget.Mode, At: now}
	switch r.Kind {
	case "write_chunk":
		var args writeChunkArguments
		if err := decodeArguments(r.Arguments, &args); err != nil {
			return err
		}
		if args.Content == nil || args.Index == nil || *args.Index < 0 || *args.Index >= maxWriteChunks || !utf8.ValidString(*args.Content) || len(*args.Content) > 4000 || utf8.RuneCountInString(*args.Content) > 2000 {
			return execprotocol.ErrInvalid
		}
		hash := digest([]byte(*args.Content))
		if args.SHA256 != "" && !strings.EqualFold(args.SHA256, hash) {
			return execprotocol.ErrConflict
		}
		if content, exists := s.chunks[*args.Index]; exists {
			if content != *args.Content {
				return execprotocol.ErrConflict
			}
			// An idempotent duplicate must not keep an abandoned session alive.
			receipt.At = s.updated
		} else if s.bytes+int64(len(*args.Content)) > maxWriteBytes {
			return execprotocol.ErrQuota
		}
		receipt.Action, receipt.Index, receipt.Bytes, receipt.SHA256 = "chunk", *args.Index, int64(len(*args.Content)), hash
	case "write_abort":
		if err := decodeArguments(r.Arguments, &handle); err != nil {
			return err
		}
		receipt.Action = "aborted"
	case "write_commit":
		var args writeCommitArguments
		if err := decodeArguments(r.Arguments, &args); err != nil {
			return err
		}
		if len(s.chunks) == 0 || (args.ExpectedChunks != nil && *args.ExpectedChunks != len(s.chunks)) {
			return errors.New("write commit requires a nonempty, complete chunk sequence")
		}
		var content strings.Builder
		content.Grow(int(s.bytes))
		for index := 0; index < len(s.chunks); index++ {
			chunk, ok := s.chunks[index]
			if !ok {
				return errors.New("write commit has missing chunks")
			}
			content.WriteString(chunk)
		}
		sha := digest([]byte(content.String()))
		if args.SHA256 != "" && !strings.EqualFold(args.SHA256, sha) {
			return execprotocol.ErrConflict
		}
		if err := e.markWriteAttempt(op, true); err != nil {
			return err
		}
		_, err := e.writeWorker(ctx, WriteWorkerInput{Action: "commit", Target: s.begin.WriteTarget, Manifest: execprotocol.FileManifest{Size: s.bytes, SHA256: sha}}, strings.NewReader(content.String()))
		if err != nil {
			if !errors.Is(err, execprotocol.ErrOutcomeUnknown) {
				if saveErr := e.markWriteAttempt(op, false); saveErr != nil {
					return saveErr
				}
			}
			return err
		}
		receipt.Action, receipt.Chunks, receipt.Bytes, receipt.SHA256 = "committed", len(s.chunks), s.bytes, sha
	default:
		return execprotocol.ErrInvalid
	}
	return e.writeReceipt(op, receipt, nil)
}

func (e *Engine) markWriteAttempt(op *operation, attempted bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	op.record.WriteAttempted = attempted
	return e.save(&op.record)
}

func (e *Engine) writeReceipt(op *operation, receipt *execprotocol.WriteReceipt, target *WriteTarget) error {
	e.mu.Lock()
	op.record.Write, op.record.WriteTarget = receipt, target
	err := e.save(&op.record)
	e.mu.Unlock()
	if err != nil {
		return errors.Join(execprotocol.ErrOutcomeUnknown, err)
	}
	encoded, _ := json.Marshal(receipt)
	_, err = (outputWriter{engine: e, operation: op}).Write(encoded)
	if err != nil {
		return errors.Join(execprotocol.ErrOutcomeUnknown, err)
	}
	return nil
}

func (e *Engine) writeWorker(ctx context.Context, request WriteWorkerInput, in io.Reader) (WriteWorkerReply, error) {
	if in == nil {
		in = strings.NewReader("")
	}
	var reply WriteWorkerReply
	if e.config.ProcessUser == nil {
		reply = RunWriteWorker(ctx, request, in)
	} else {
		environment, err := e.processEnvironment(nil)
		if err != nil {
			return reply, err
		}
		cmd, err := fileHelperCommand(ctx, e.config.ProcessUser.Helper, e.config.WorkingDirectory, environment, e.config.ProcessUser, "write-tool")
		if err != nil {
			return reply, err
		}
		header, _ := json.Marshal(request)
		cmd.Stdin = io.MultiReader(bytes.NewReader(append(header, '\n')), in)
		var output workerError
		var stderr workerError
		cmd.Stdout, cmd.Stderr = &output, &stderr
		if err := cmd.Run(); err != nil {
			if request.Action == "commit" {
				return reply, errors.Join(execprotocol.ErrOutcomeUnknown, err)
			}
			return reply, fmt.Errorf("write prepare: %w: %s", err, stderr.String())
		}
		if err := decodeArguments(output.Bytes(), &reply); err != nil {
			if request.Action == "commit" {
				return reply, errors.Join(execprotocol.ErrOutcomeUnknown, err)
			}
			return reply, err
		}
	}
	if reply.Unknown {
		return reply, errors.Join(execprotocol.ErrOutcomeUnknown, errors.New(reply.Error))
	}
	if reply.Error != "" {
		return reply, errors.New(reply.Error)
	}
	return reply, nil
}
