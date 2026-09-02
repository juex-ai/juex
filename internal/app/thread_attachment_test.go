package app

import (
	"path/filepath"
	"testing"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/thread"
)

func TestAttachWorkspaceThreadCanonicalizesAliasBeforeWorkerCreation(t *testing.T) {
	workDir := t.TempDir()
	stateDir := filepath.Join(workDir, "agent-state")
	cfg := config.Config{WorkDir: workDir, AgentStateDir: stateDir}
	if err := EnsureMainThread(cfg); err != nil {
		t.Fatal(err)
	}

	attachment, err := AttachWorkspaceThread(cfg, ThreadAttachmentRequest{
		ParentThreadID: thread.MainID,
		Alias:          " \t ",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = attachment.Thread.Close() }()
	if !attachment.Created || attachment.Thread.Alias != thread.DefaultWorkerAlias(attachment.Thread.ID) {
		t.Fatalf("attachment = %+v, Thread = %+v", attachment, attachment.Thread.Info())
	}

	entries, err := attachment.Store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("Thread count = %d, want Main and one Worker", len(entries))
	}
}
