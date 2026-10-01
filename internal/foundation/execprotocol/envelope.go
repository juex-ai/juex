package execprotocol

import (
	"errors"
	"github.com/juex-ai/juex/internal/foundation/maintenance"
)

// Envelope request IDs correlate transport replies. Operation IDs remain stable
// across transport reconnects and are deduplicated by the execution journal.
type Envelope struct {
	Version      int                     `json:"version"`
	ID           string                  `json:"id,omitempty"`
	Type         string                  `json:"type"`
	AgentID      string                  `json:"agent_id,omitempty"`
	OperationID  string                  `json:"operation_id,omitempty"`
	Cursor       int64                   `json:"cursor,omitempty"`
	Limit        int                     `json:"limit,omitempty"`
	Request      *Request                `json:"request,omitempty"`
	Snapshot     *Snapshot               `json:"snapshot,omitempty"`
	Environment  *Environment            `json:"environment,omitempty"`
	Grants       map[string][]Capability `json:"grants,omitempty"`
	Revoked      bool                    `json:"revoked,omitempty"`
	Error        string                  `json:"error,omitempty"`
	FileChunk    *FileChunk              `json:"file_chunk,omitempty"`
	FileStatus   *FileStatus             `json:"file_status,omitempty"`
	FileManifest *FileManifest           `json:"file_manifest,omitempty"`
}

func ErrorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, maintenance.ErrDraining):
		return "maintenance"
	case errors.Is(err, ErrVersion):
		return "protocol_version"
	case errors.Is(err, ErrInvalid):
		return "invalid"
	case errors.Is(err, ErrDenied):
		return "denied"
	case errors.Is(err, ErrConflict):
		return "conflict"
	case errors.Is(err, ErrNotFound):
		return "not_found"
	case errors.Is(err, ErrQuota):
		return "result_quota"
	case errors.Is(err, ErrOutcomeUnknown):
		return "outcome_unknown"
	default:
		return "unavailable"
	}
}

func FromErrorCode(code string) error {
	switch code {
	case "":
		return nil
	case "maintenance":
		return maintenance.ErrDraining
	case "protocol_version":
		return ErrVersion
	case "invalid":
		return ErrInvalid
	case "denied":
		return ErrDenied
	case "conflict":
		return ErrConflict
	case "not_found":
		return ErrNotFound
	case "result_quota":
		return ErrQuota
	case "outcome_unknown":
		return ErrOutcomeUnknown
	default:
		return ErrUnavailable
	}
}
