// Package hookpolicy contains declarative lifecycle policy shared by Management
// and Runtime. It never executes user code or resolves filesystem resources.
package hookpolicy

import (
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

type Event string

const (
	ThreadStart      Event = "ThreadStart"
	UserPromptSubmit Event = "UserPromptSubmit"
	PreToolUse       Event = "PreToolUse"
	PostToolUse      Event = "PostToolUse"
	PreCompact       Event = "PreCompact"
	PostCompact      Event = "PostCompact"
	Stop             Event = "Stop"
)

var ErrInvalid = errors.New("invalid hook declaration")
var namePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`)

type Declaration struct {
	ID               string   `json:"id"`
	Enabled          bool     `json:"enabled"`
	Events           []Event  `json:"events"`
	Tools            []string `json:"tools,omitempty"`
	EnvironmentID    string   `json:"environment_id,omitempty"`
	Command          []string `json:"command"`
	WorkingDirectory string   `json:"working_directory,omitempty"`
	TimeoutSeconds   int      `json:"timeout_seconds,omitempty"`
	MaxOutputBytes   int      `json:"max_output_bytes,omitempty"`
	Required         bool     `json:"required"`
	Source           string   `json:"source,omitempty"`
}

func (h Declaration) Matches(event Event, tool string) bool {
	return h.Enabled && slices.Contains(h.Events, event) && (len(h.Tools) == 0 || slices.Contains(h.Tools, tool))
}

func (h Declaration) Operation(input json.RawMessage) execprotocol.HookCommand {
	timeout, output := h.TimeoutSeconds, h.MaxOutputBytes
	if timeout == 0 {
		timeout = 10
	}
	if output == 0 {
		output = 8192
	}
	return execprotocol.HookCommand{Command: slices.Clone(h.Command), Input: input, WorkingDirectory: h.WorkingDirectory, TimeoutMS: timeout * 1000, MaxOutputBytes: output}
}

func Validate(values []Declaration) error {
	if len(values) > 16 {
		return ErrInvalid
	}
	encoded, err := json.Marshal(values)
	if err != nil || len(encoded) > 256<<10 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, h := range values {
		if !namePattern.MatchString(h.ID) || seen[h.ID] || len(h.Events) < 1 || len(h.Events) > 7 || len(h.Tools) > 32 || len(h.Source) > 256 || len(h.WorkingDirectory) > 4096 || strings.ContainsRune(h.WorkingDirectory, 0) || h.TimeoutSeconds < 0 || h.TimeoutSeconds > 300 || h.MaxOutputBytes < 0 || h.MaxOutputBytes > 64<<10 {
			return ErrInvalid
		}
		seen[h.ID] = true
		if h.EnvironmentID != "" {
			id, err := uuid.Parse(h.EnvironmentID)
			if err != nil || id == uuid.Nil || id.String() != h.EnvironmentID {
				return ErrInvalid
			}
		}
		for i, event := range h.Events {
			if !slices.Contains([]Event{ThreadStart, UserPromptSubmit, PreToolUse, PostToolUse, PreCompact, PostCompact, Stop}, event) || slices.Contains(h.Events[:i], event) {
				return ErrInvalid
			}
		}
		for _, tool := range h.Tools {
			if tool == "" || len(tool) > 128 {
				return ErrInvalid
			}
		}
		if err := h.Operation(json.RawMessage(`{}`)).Validate(); err != nil {
			return ErrInvalid
		}
	}
	return nil
}

// Removing or disabling a declaration revokes already frozen work. Other edits
// take effect with the next Turn's configuration snapshot.
func Revokes(before, after []Declaration) bool {
	for _, old := range before {
		if !old.Enabled {
			continue
		}
		if !slices.ContainsFunc(after, func(next Declaration) bool { return old.ID == next.ID && next.Enabled }) {
			return true
		}
	}
	return false
}
