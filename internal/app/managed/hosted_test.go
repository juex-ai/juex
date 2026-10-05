package managed

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/execution/hosted"
)

type purgeHostedEngine struct {
	running bool
	removed bool
	stopErr error
}

func (p *purgeHostedEngine) Ensure(context.Context, hosted.Spec) (hosted.Instance, error) {
	return hosted.Instance{}, errors.New("unexpected start during purge")
}

func (p *purgeHostedEngine) Stop(context.Context, hosted.Spec) error {
	if p.stopErr != nil {
		return p.stopErr
	}
	p.running = false
	return nil
}

func (p *purgeHostedEngine) Purge(context.Context, hosted.Spec) error {
	if p.running {
		return errors.New("container must be stopped before removal")
	}
	p.removed = true
	return nil
}

func TestManagedHostedPurgeStopsRunningContainerBeforeRemoval(t *testing.T) {
	for _, stopFails := range []bool{false, true} {
		engine := &purgeHostedEngine{running: true}
		if stopFails {
			engine.stopErr = errors.New("stop failed")
		}
		backend := hostedBackend{docker: engine}
		err := backend.Purge(context.Background(), execution.ManagedResource{})
		if stopFails {
			if !errors.Is(err, engine.stopErr) || engine.removed {
				t.Fatal("purge continued after failed stop", err, engine.removed)
			}
		} else if err != nil || !engine.removed {
			t.Fatal("running container could not be purged", err, engine.removed)
		}
	}
}

func TestBlobStorageCannotBeInsideUserWritableHostedStorage(t *testing.T) {
	root := t.TempDir()
	blobs, workspace := filepath.Join(root, "blobs"), filepath.Join(root, "workspace")
	for _, path := range []string{blobs, workspace, filepath.Join(workspace, "agent", "files")} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := separateBlobWorkspace(blobs, workspace); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(filepath.Join(workspace, "agent", "files"), alias); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{workspace, workspace}, {alias, workspace}, {root, workspace}} {
		if err := separateBlobWorkspace(pair[0], pair[1]); err == nil {
			t.Fatal("overlapping storage accepted", pair)
		}
	}
}
