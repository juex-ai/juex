// Package native executes explicitly authorized work as the current OS user.
// Its working directory is a default path, not a security boundary.
package native

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

type record struct {
	Request            execprotocol.Request     `json:"request"`
	Hash               string                   `json:"hash"`
	State              execprotocol.State       `json:"state"`
	OutputBytes        int64                    `json:"output_bytes"`
	Truncated          bool                     `json:"truncated"`
	OutputExpired      bool                     `json:"output_expired"`
	ExitCode           *int                     `json:"exit_code"`
	Error              string                   `json:"error"`
	PID                int                      `json:"pid"`
	ProcessIdentity    string                   `json:"process_identity"`
	CreatedAt          time.Time                `json:"created_at"`
	UpdatedAt          time.Time                `json:"updated_at"`
	AcknowledgedAt     *time.Time               `json:"acknowledged_at"`
	CancelRequested    bool                     `json:"cancel_requested"`
	File               *execprotocol.FileStatus `json:"file,omitempty"`
	FileReserved       int64                    `json:"file_reserved,omitempty"`
	FileExpired        bool                     `json:"file_expired,omitempty"`
	FileAcknowledgedAt *time.Time               `json:"file_acknowledged_at,omitempty"`
}

type stateIdentity struct {
	Version       int    `json:"version"`
	EnvironmentID string `json:"environment_id"`
	JournalID     string `json:"journal_id"`
}

func (e *Engine) checkIdentity() error {
	path := filepath.Join(e.config.StateDirectory, "identity.json")
	want := stateIdentity{Version: execprotocol.Version, EnvironmentID: e.config.EnvironmentID}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		entries, readErr := os.ReadDir(filepath.Join(e.config.StateDirectory, "operations"))
		if readErr != nil {
			return readErr
		}
		if len(entries) != 0 {
			return errors.New("execution journal has lost its enrollment identity")
		}
		want.JournalID = rand.Text()
		e.identity = want
		return saveJSON(path, want)
	}
	if err != nil {
		return err
	}
	var got stateIdentity
	if json.Unmarshal(data, &got) != nil || got.Version != want.Version || got.EnvironmentID != want.EnvironmentID || got.JournalID == "" {
		return errors.New("execution state belongs to a different environment or protocol version")
	}
	e.identity = got
	return nil
}

func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func (e *Engine) path(id, extension string) string {
	return filepath.Join(e.config.StateDirectory, "operations", digest([]byte(id))+extension)
}

func saveJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".record-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer func() { _ = file.Close(); _ = os.Remove(name) }()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func (e *Engine) save(r *record) error {
	r.UpdatedAt = time.Now().UTC()
	if err := saveJSON(e.path(r.Request.ID, ".json"), r); err != nil {
		e.fault = err
		return execprotocol.ErrUnavailable
	}
	return nil
}

func (e *Engine) load() error {
	entries, err := os.ReadDir(filepath.Join(e.config.StateDirectory, "operations"))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(e.config.StateDirectory, "operations", entry.Name())
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		data, readErr := io.ReadAll(io.LimitReader(file, 3<<20))
		_ = file.Close()
		if readErr != nil {
			return readErr
		}
		var r record
		if json.Unmarshal(data, &r) != nil || r.Request.Validate() != nil || e.path(r.Request.ID, ".json") != path || r.OutputBytes < 0 {
			return errors.New("invalid durable execution record")
		}
		encoded, err := json.Marshal(r.Request)
		if err != nil || digest(encoded) != r.Hash {
			return errors.New("execution request identity checksum mismatch")
		}
		if !r.OutputExpired {
			output, err := os.Stat(e.path(r.Request.ID, ".output"))
			if err != nil || output.Size() < r.OutputBytes {
				return errors.New("durable execution output missing or incomplete")
			}
			// A crash may have persisted output before its metadata checkpoint.
			r.OutputBytes = output.Size()
		}
		if !r.State.Terminal() {
			r.State, r.Error = execprotocol.Unknown, "executor_restarted_outcome_unknown"
			if err := e.save(&r); err != nil {
				return err
			}
		}
		if r.FileReserved < 0 || r.FileReserved > execprotocol.MaxFileBytes+4096 || r.File != nil && (r.File.Manifest.Validate() != nil || r.File.Cursor < 0 || r.File.Cursor > r.File.Manifest.Size) {
			return errors.New("invalid durable file transfer record")
		}
		if r.State == execprotocol.Completed && r.File != nil && !r.FileExpired {
			status, err := e.files.Status(e.fileID(r.Request.ID))
			if err != nil || status.Manifest != r.File.Manifest || !status.Ready {
				return errors.New("durable transferred file missing or incomplete")
			}
		}
		e.operations[r.Request.ID] = &operation{record: r}
	}
	return nil
}

func (e *Engine) reservedBytes() int64 {
	var bytes int64
	for _, operation := range e.operations {
		encoded, _ := json.Marshal(operation.record)
		bytes += int64(len(encoded)) + 4096
		if !operation.record.State.Terminal() {
			bytes += e.config.OutputLimit
		} else if !operation.record.OutputExpired {
			bytes += operation.record.OutputBytes
		}
	}
	return bytes
}

// Prune removes only explicitly acknowledged, settled output. Identity records
// survive so a late replay of an old operation can never execute it again.
func (e *Engine) Prune(now time.Time) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, operation := range e.operations {
		r := &operation.record
		if r.State == execprotocol.Unknown || !r.State.Terminal() || r.AcknowledgedAt == nil || now.Sub(*r.AcknowledgedAt) < e.config.Retention {
			continue
		}
		if !r.OutputExpired {
			r.OutputExpired = true
			if err := e.save(r); err != nil {
				return err
			}
		}
		if err := os.Remove(e.path(r.Request.ID, ".output")); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if r.FileAcknowledgedAt != nil && now.Sub(*r.FileAcknowledgedAt) >= e.config.Retention && r.FileReserved > 0 {
			r.FileExpired = true
			if err := e.save(r); err != nil {
				return err
			}
			if err := e.files.Remove(e.fileID(r.Request.ID)); err != nil {
				return err
			}
			r.FileReserved = 0
			if err := e.save(r); err != nil {
				return err
			}
		}
	}
	return nil
}
