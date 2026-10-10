package migration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path"

	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/migration/legacy"
)

// Only current module files are authoritative. An earlier notes.updated event
// may precede a clear/reset and cannot be used to reconstruct current state.
func currentThreadState(thread legacy.Thread, files []legacy.SourceFile) (managedruntime.ThreadState, error) {
	state := managedruntime.ThreadState{Tasks: []managedruntime.ThreadTask{}}
	prefix := path.Join("threads", thread.Metadata.ThreadID, "modules")
	if thread.Metadata.RetentionState == "archived" {
		prefix = "archive/" + prefix
	}
	for _, file := range files {
		switch file.Path {
		case prefix + "/notes/notes.md":
			state.Notes = managedruntime.ThreadNotes{Content: string(file.Data), UpdatedAt: file.ModifiedAt}
		case prefix + "/tasks/tasks.json":
			var snapshot struct {
				Version int                         `json:"version"`
				Tasks   []managedruntime.ThreadTask `json:"tasks"`
			}
			decoder := json.NewDecoder(bytes.NewReader(file.Data))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&snapshot); err != nil {
				return state, fmt.Errorf("current tasks state: %w", err)
			}
			if snapshot.Version != 1 {
				return state, fmt.Errorf("unsupported current tasks version %d", snapshot.Version)
			}
			if err := decoder.Decode(new(any)); err != io.EOF {
				return state, fmt.Errorf("current tasks contain trailing data")
			}
			state.Tasks = append(state.Tasks, snapshot.Tasks...)
		}
	}
	return state, state.Validate()
}
