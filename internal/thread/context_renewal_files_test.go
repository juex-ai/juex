package thread

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestStoreOpenRecoversContextRenewalFilesFromJournalGeneration(t *testing.T) {
	for _, test := range []struct {
		name         string
		commit       bool
		wantRestored bool
	}{
		{name: "before commit", wantRestored: true},
		{name: "after commit", commit: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := NewStore(t.TempDir())
			target, err := store.EnsureMain()
			if err != nil {
				t.Fatal(err)
			}
			statePath := filepath.Join(target.Dir, "module.state")
			before := []byte("module-owned state")
			if err := os.WriteFile(statePath, before, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := StageContextRenewalFileClear(statePath, target.Projection().CurrentGeneration.ID); err != nil {
				t.Fatal(err)
			}
			if test.commit {
				if _, err := target.BeginNewGeneration(); err != nil {
					t.Fatal(err)
				}
			}
			if err := target.Close(); err != nil {
				t.Fatal(err)
			}

			reopened, err := store.OpenActive(MainID)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = reopened.Close() }()
			got, err := os.ReadFile(statePath)
			if test.wantRestored {
				if err != nil || string(got) != string(before) {
					t.Fatalf("restored state = %q, %v; want %q", got, err, before)
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("committed clear retained state: %v", err)
			}
			backups, err := filepath.Glob(filepath.Join(target.Dir, "*.context-renewal-*"))
			if err != nil {
				t.Fatal(err)
			}
			if len(backups) != 0 {
				t.Fatalf("recovery left backups: %v", backups)
			}
		})
	}
}

func TestRecoverLayoutRestoresInactiveWorkerContextRenewalFiles(t *testing.T) {
	stateDir := t.TempDir()
	store := NewStore(stateDir)
	main, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = main.Close() }()
	worker, err := store.CreateWorker(MainID, "interrupted-worker")
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(worker.Dir, "module.state")
	if err := os.WriteFile(statePath, []byte("restore me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := StageContextRenewalFileClear(statePath, worker.Projection().CurrentGeneration.ID); err != nil {
		t.Fatal(err)
	}
	if err := worker.Close(); err != nil {
		t.Fatal(err)
	}

	if err := NewStore(stateDir).RecoverLayout(); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(statePath); err != nil || string(got) != "restore me" {
		t.Fatalf("recovered inactive Worker state = %q, %v", got, err)
	}
}

func TestRecoverLayoutUsesJournalGenerationForContextRenewalFiles(t *testing.T) {
	stateDir := t.TempDir()
	store := NewStore(stateDir)
	main, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = main.Close() }()
	worker, err := store.CreateWorker(MainID, "committed-worker")
	if err != nil {
		t.Fatal(err)
	}
	metadataPath := filepath.Join(worker.Dir, projectionFile)
	metadataBefore, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(worker.Dir, "module.state")
	if err := os.WriteFile(statePath, []byte("do not restore"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := StageContextRenewalFileClear(statePath, worker.Projection().CurrentGeneration.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.BeginNewGeneration(); err != nil {
		t.Fatal(err)
	}
	if err := worker.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metadataPath, metadataBefore, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := NewStore(stateDir).RecoverLayout(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("recovery restored state cleared by a committed Journal boundary: %v", err)
	}
	backups, err := filepath.Glob(filepath.Join(worker.Dir, "*.context-renewal-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 0 {
		t.Fatalf("recovery retained committed backups: %v", backups)
	}
}

func TestDeleteArchivedStopsWhenContextRenewalRecoveryIsInvalid(t *testing.T) {
	store := NewStore(t.TempDir())
	main, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = main.Close() }()
	worker, err := store.CreateWorker(MainID, "invalid-recovery")
	if err != nil {
		t.Fatal(err)
	}
	workerID := worker.ID
	if err := store.Archive(worker); err != nil {
		t.Fatal(err)
	}
	archivedDir := filepath.Join(store.ArchiveDir(), workerID)
	if err := os.WriteFile(filepath.Join(archivedDir, "module.state.context-renewal-invalid"), []byte("state"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := store.DeleteArchived(workerID); err == nil {
		t.Fatal("DeleteArchived() succeeded without resolving an invalid Context renewal backup")
	}
	if _, err := os.Stat(archivedDir); err != nil {
		t.Fatalf("failed recovery deleted archived Thread: %v", err)
	}
}

func TestDeleteArchivedUsesJournalGenerationForContextRenewalFiles(t *testing.T) {
	store := NewStore(t.TempDir())
	main, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = main.Close() }()
	worker, err := store.CreateWorker(MainID, "committed-delete")
	if err != nil {
		t.Fatal(err)
	}
	before := worker.Projection()
	statePath := filepath.Join(worker.Dir, "module.state")
	if err := os.WriteFile(statePath, []byte("do not restore"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := StageContextRenewalFileClear(statePath, before.CurrentGeneration.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.BeginNewGeneration(); err != nil {
		t.Fatal(err)
	}
	workerID := worker.ID
	if err := store.Archive(worker); err != nil {
		t.Fatal(err)
	}
	archivedDir := filepath.Join(store.ArchiveDir(), workerID)
	metadata, err := readProjectionFile(archivedDir, workerID)
	if err != nil {
		t.Fatal(err)
	}
	metadata.CurrentGeneration = before.CurrentGeneration
	metadata.Generations = before.Generations
	metadata.Counts = before.Counts
	metadata.TokenUsage = before.TokenUsage
	metadata.ContextUsage = before.ContextUsage
	metadata.LastActivityAt = before.LastActivityAt
	metadata.Journal = before.Journal
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(archivedDir, projectionFile), append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(store.TrashDir()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.TrashDir(), []byte("block rename after recovery"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := store.DeleteArchived(workerID); err == nil {
		t.Fatal("DeleteArchived() succeeded despite blocked trash directory")
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("delete recovery restored state cleared by a committed Journal boundary: %v", err)
	}
	backups, err := filepath.Glob(filepath.Join(archivedDir, "*.context-renewal-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 0 {
		t.Fatalf("delete recovery retained committed backups: %v", backups)
	}
}
