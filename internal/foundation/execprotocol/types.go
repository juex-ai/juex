// Package execprotocol defines the versioned contract shared by hosted and
// native execution environments. It contains no platform or transport policy.
package execprotocol

import (
	"encoding/json"
	"errors"
	"time"
)

// MaxMCPEventBytes bounds a complete JSONL record, including its newline.
const MaxMCPEventBytes = 1 << 20

const Version = 1

// AuthorityFence binds an external operation to the human/Agent authority of
// its original Turn. It is supplied by Runtime, never by a model argument.
type AuthorityFence struct {
	ActorEpoch      int64 `json:"actor_epoch"`
	MembershipEpoch int64 `json:"membership_epoch"`
	AgentEpoch      int64 `json:"agent_epoch"`
}

var (
	ErrVersion        = errors.New("execution protocol incompatible; upgrade required")
	ErrInvalid        = errors.New("invalid execution request")
	ErrDenied         = errors.New("execution permission denied")
	ErrConflict       = errors.New("operation identity reused with different request")
	ErrNotFound       = errors.New("operation not found")
	ErrQuota          = errors.New("execution result storage full; acknowledge or free retained results")
	ErrUnavailable    = errors.New("execution environment unavailable")
	ErrOutcomeUnknown = errors.New("external operation outcome unknown; do not repeat")
)

type Capability string

const (
	Files Capability = "files"
	Shell Capability = "shell"
	MCP   Capability = "mcp"
)

func RequiredCapability(kind string) Capability {
	switch kind {
	case "browse_workspace", "read_agent_instructions", "inspect_extension", "read", "write", "edit", "apply_patch", "write_begin", "write_chunk", "write_commit", "write_abort", "glob", "grep", "export_file", "import_file":
		return Files
	case "observe_command", "exec_command", "write_stdin", "run_hook":
		return Shell
	case "mcp_connect", "mcp_call", "mcp_list", "mcp_close":
		return MCP
	default:
		return ""
	}
}

type Request struct {
	WriteContext         *WriteContext   `json:"write_context,omitempty"`
	AuthorizationVersion int64           `json:"authorization_version,omitempty"`
	Version              int             `json:"version"`
	ID                   string          `json:"id"`
	AgentID              string          `json:"agent_id"`
	Kind                 string          `json:"kind"`
	Arguments            json.RawMessage `json:"arguments"`
}

func (r Request) Validate() error {
	if r.Version != Version {
		return ErrVersion
	}
	if r.AuthorizationVersion < 0 || r.ID == "" || len(r.ID) > 256 || r.AgentID == "" || len(r.AgentID) > 128 || RequiredCapability(r.Kind) == "" || len(r.Arguments) > 2<<20 || !json.Valid(r.Arguments) {
		return ErrInvalid
	}
	if IsChunkedWrite(r.Kind) {
		if r.WriteContext == nil || r.WriteContext.ThreadID == "" || r.WriteContext.ResetID == "" || len(r.WriteContext.ThreadID) > 128 || len(r.WriteContext.ResetID) > 128 {
			return ErrInvalid
		}
	} else if r.WriteContext != nil {
		return ErrInvalid
	}
	return nil
}

type State string

const (
	Accepted  State = "accepted"
	Running   State = "running"
	Completed State = "completed"
	Failed    State = "failed"
	Cancelled State = "cancelled"
	Unknown   State = "unknown"
)

func (s State) Terminal() bool {
	return s == Completed || s == Failed || s == Cancelled || s == Unknown
}

type Snapshot struct {
	Write         *WriteReceipt  `json:"write,omitempty"`
	MCP           *MCPConnection `json:"mcp,omitempty"`
	Version       int            `json:"version"`
	EnvironmentID string         `json:"environment_id"`
	ID            string         `json:"id"`
	AgentID       string         `json:"agent_id"`
	Kind          string         `json:"kind"`
	State         State          `json:"state"`
	Output        []byte         `json:"output"`
	NextCursor    int64          `json:"next_cursor"`
	OutputBytes   int64          `json:"output_bytes"`
	Truncated     bool           `json:"truncated"`
	OutputExpired bool           `json:"output_expired"`
	ExitCode      *int           `json:"exit_code"`
	Error         string         `json:"error"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	File          *FileStatus    `json:"file,omitempty"`
	FileExpired   bool           `json:"file_expired,omitempty"`
}

func (s Snapshot) Text() string { return string(s.Output) }

// Environment is descriptive context, not authority supplied by the model.
type Environment struct {
	Default              bool         `json:"default"`
	ID                   string       `json:"id"`
	JournalID            string       `json:"journal_id"`
	Name                 string       `json:"name"`
	Kind                 string       `json:"kind"`
	OS                   string       `json:"os"`
	Online               bool         `json:"online"`
	Capabilities         []Capability `json:"capabilities"`
	WorkingDirectory     string       `json:"working_directory"`
	WorkingFilesRoot     string       `json:"working_files_root,omitempty"`
	PermissionMode       string       `json:"permission_mode"`
	AuthorizationVersion int64        `json:"authorization_version"`
	Availability         string       `json:"availability,omitempty"`
	Error                string       `json:"error,omitempty"`
}
