package app

import (
	"fmt"
	"path"

	"github.com/juex-ai/juex/internal/artifact"
	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/thread"
)

// PartialThreadDeleteError means durable Thread state was removed but its
// optional Agent Artifact namespace still needs cleanup.
type PartialThreadDeleteError struct {
	ThreadID string
	Err      error
}

func (e *PartialThreadDeleteError) Error() string {
	return fmt.Sprintf("Thread %q was deleted but Artifact cleanup failed: %v", e.ThreadID, e.Err)
}

func (e *PartialThreadDeleteError) Unwrap() error { return e.Err }

// DeleteThread permanently removes an archived Worker. Store enforces that
// Main, active Workers, and Workers with children cannot be deleted.
func DeleteThread(cfg config.Config, id string) error {
	store := thread.NewStore(cfg.RuntimePaths().StateDir)
	if err := store.DeleteArchived(id); err != nil {
		return err
	}
	if artifactDir := cfg.MediaDir(); artifactDir != "" {
		artifactStore, err := artifact.NewStore(artifactDir)
		if err != nil {
			return &PartialThreadDeleteError{ThreadID: id, Err: err}
		}
		if err := artifactStore.RemoveNamespace(path.Join("threads", id)); err != nil {
			return &PartialThreadDeleteError{ThreadID: id, Err: err}
		}
	}
	return nil
}
