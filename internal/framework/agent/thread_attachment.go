package agent

import (
	"fmt"

	"github.com/juex-ai/juex/internal/framework/thread"
)

// ThreadAttachmentRequest selects an existing active Thread or creates a
// Worker below ParentThreadID. An empty ThreadID selects Main.
type ThreadAttachmentRequest struct {
	ThreadID       string
	ParentThreadID string
	Alias          string
}

type ThreadAttachment struct {
	Thread  *thread.Thread
	Store   *thread.Store
	Created bool
}

// AttachWorkspaceThread applies the Agent-owned Thread identity rules. Main
// has the stable id "0"; Worker identity and parentage are owned by Store.
func AttachThread(stateDir string, request ThreadAttachmentRequest) (ThreadAttachment, error) {
	if stateDir == "" {
		return ThreadAttachment{}, fmt.Errorf("app: Agent state directory is required")
	}
	store := thread.NewStore(stateDir)
	if err := store.RecoverLayout(); err != nil {
		return ThreadAttachment{}, err
	}

	var target *thread.Thread
	created := false
	var err error
	switch {
	case request.ParentThreadID != "":
		target, err = store.CreateWorker(request.ParentThreadID, request.Alias)
		created = err == nil
	case request.ThreadID == "" || request.ThreadID == thread.MainID:
		target, err = store.EnsureMain()
	default:
		target, err = store.OpenActive(request.ThreadID)
	}
	if err != nil {
		return ThreadAttachment{}, err
	}
	if request.Alias != "" && target.Alias != request.Alias {
		if err := target.ApplyAlias(request.Alias); err != nil {
			_ = target.Close()
			return ThreadAttachment{}, err
		}
	}
	return ThreadAttachment{Thread: target, Store: store, Created: created}, nil
}

func EnsureMainThread(stateDir string) error {
	attachment, err := AttachThread(stateDir, ThreadAttachmentRequest{})
	if err != nil {
		return err
	}
	return attachment.Thread.Close()
}
