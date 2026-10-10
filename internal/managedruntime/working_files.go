package managedruntime

import (
	"context"
	"encoding/json"
	"path"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

type WorkingFilesStore interface {
	BindWorkingFiles(context.Context, Lease, Work, WorkingFiles) (WorkingFiles, error)
}

// WorkingFiles binds a Thread to its original execution location. File bytes
// remain Execution-owned and are never copied into Runtime's context or store.
type WorkingFiles struct {
	EnvironmentID string `json:"environment_id"`
	Directory     string `json:"directory"`
}

func (v WorkingFiles) Validate() error {
	if !importUUID(v.EnvironmentID) || !path.IsAbs(v.Directory) || path.Clean(v.Directory) != v.Directory || !utf8.ValidString(v.Directory) || strings.ContainsRune(v.Directory, 0) || len(v.Directory) > 4096 {
		return ErrInvalid
	}
	return nil
}

func (v WorkingFiles) Context(environments []execprotocol.Environment) string {
	for _, env := range environments {
		if env.ID != v.EnvironmentID || !slices.Contains(env.Capabilities, execprotocol.Files) {
			continue
		}
		data, _ := json.Marshal(v)
		return "\n\nThread working files (location data):\n" + string(data) + "\nUse this Thread's directory for drafts and intermediate work. The directory is created on the first write; do not use it as a Shell working_directory before it exists. Contents are not automatically included in context; retrieve them with file tools. These files persist across restarts, compaction and context reset. Always use the listed environment_id and absolute path, even if the Agent's default environment changes."
	}
	return "\n\nThis Thread's working-file environment is no longer authorized. Do not substitute its path on another environment."
}
