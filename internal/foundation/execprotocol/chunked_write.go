package execprotocol

import (
	"encoding/hex"
	"encoding/json"
	"time"
)

// WriteContext is supplied by Runtime, outside model arguments. ResetID is the
// Thread's reset lifecycle, which survives compaction but changes on /new.
type WriteContext struct {
	ThreadID string `json:"thread_id"`
	ResetID  string `json:"reset_id"`
}

type WriteReceipt struct {
	WriteID string    `json:"write_id"`
	Action  string    `json:"action"`
	Path    string    `json:"path,omitempty"`
	Mode    string    `json:"mode,omitempty"`
	Index   int       `json:"index,omitempty"`
	Chunks  int       `json:"chunks,omitempty"`
	Bytes   int64     `json:"bytes"`
	SHA256  string    `json:"sha256,omitempty"`
	At      time.Time `json:"at"`
}

func IsChunkedWrite(kind string) bool {
	switch kind {
	case "write_begin", "write_chunk", "write_commit", "write_abort":
		return true
	default:
		return false
	}
}

func (w WriteReceipt) Validate(request Request) error {
	actions := map[string]string{"write_begin": "began", "write_chunk": "chunk", "write_commit": "committed", "write_abort": "aborted"}
	if w.WriteID == "" || w.At.IsZero() || w.Action != actions[request.Kind] || len(w.Path) > 4096 || (w.Mode != "overwrite" && w.Mode != "create") || w.Bytes < 0 || w.Bytes > 16<<20 || w.Index < 0 || w.Index >= 4096 || w.Chunks < 0 || w.Chunks > 4096 {
		return ErrInvalid
	}
	if request.Kind == "write_begin" {
		if w.WriteID != request.ID {
			return ErrInvalid
		}
	} else {
		var args struct {
			WriteID string `json:"write_id"`
		}
		if json.Unmarshal(request.Arguments, &args) != nil || w.WriteID != args.WriteID {
			return ErrInvalid
		}
	}
	if w.Action == "chunk" || w.Action == "committed" {
		hash, err := hex.DecodeString(w.SHA256)
		if err != nil || len(hash) != 32 {
			return ErrInvalid
		}
	}
	return nil
}
