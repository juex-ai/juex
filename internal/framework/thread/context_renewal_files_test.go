package thread

import (
	"encoding/json"
	"errors"
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
			stageContextRenewalCrashFixture(t, statePath, target.Projection().CurrentGeneration.ID)
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
	worker, err := store.CreateWorker(MainID, "interrupted-worker", 2)
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(worker.Dir, "module.state")
	if err := os.WriteFile(statePath, []byte("restore me"), 0o600); err != nil {
		t.Fatal(err)
	}
	stageContextRenewalCrashFixture(t, statePath, worker.Projection().CurrentGeneration.ID)
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

func TestRecoverLayoutUsesCommittedMetadataForContextRenewalFiles(t *testing.T) {
	stateDir := t.TempDir()
	store := NewStore(stateDir)
	main, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = main.Close() }()
	worker, err := store.CreateWorker(MainID, "committed-worker", 2)
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
	stageContextRenewalCrashFixture(t, statePath, worker.Projection().CurrentGeneration.ID)
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
	if got, err := os.ReadFile(statePath); err != nil || string(got) != "do not restore" {
		t.Fatalf("recovery did not restore state for uncommitted metadata boundary: %q, %v", got, err)
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
	worker, err := store.CreateWorker(MainID, "invalid-recovery", 2)
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

	if err := os.WriteFile(filepath.Join(archivedDir, contextRenewalManifestFile), []byte(`{"version":1,"files":[{"path":"module.state","generation_id":"invalid"}]}`), 0600); err != nil {
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
	worker, err := store.CreateWorker(MainID, "committed-delete", 2)
	if err != nil {
		t.Fatal(err)
	}
	before := worker.Projection()
	statePath := filepath.Join(worker.Dir, "module.state")
	if err := os.WriteFile(statePath, []byte("do not restore"), 0o600); err != nil {
		t.Fatal(err)
	}
	stageContextRenewalCrashFixture(t, statePath, before.CurrentGeneration.ID)
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
	metadata.EventCursor = before.EventCursor
	metadata.UsageAggregatedThrough = before.UsageAggregatedThrough
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

func TestStoreOpenDoesNotRecoverActiveContextRenewal(t *testing.T) {
	store := NewStore(t.TempDir())
	target, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(target.Dir, "module.state")
	if err := os.WriteFile(statePath, []byte("old state"), 0o600); err != nil {
		t.Fatal(err)
	}
	clear, err := StageContextRenewalFileClear(target.Dir, statePath, target.Projection().CurrentGeneration.ID)
	if err != nil {
		t.Fatal(err)
	}

	if opened, err := store.OpenActive(MainID); !errors.Is(err, errContextRenewalInProgress) {
		if opened != nil {
			_ = opened.Close()
		}
		t.Fatalf("OpenActive() error = %v, want Context renewal in progress", err)
	}
	if _, err := os.Lstat(statePath); !os.IsNotExist(err) {
		t.Fatalf("concurrent open restored staged state: %v", err)
	}
	if _, err := target.BeginNewGeneration(); err != nil {
		t.Fatal(err)
	}
	if err := clear.Finalize(); err != nil {
		t.Fatal(err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.OpenActive(MainID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("committed renewal retained old state: %v", err)
	}
}

func TestStageContextRenewalFileClearRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	targetPath := filepath.Join(dir, "target")
	if err := os.WriteFile(targetPath, []byte("state"), 0o600); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(dir, "module.state")
	if err := os.Symlink(targetPath, statePath); err != nil {
		t.Fatal(err)
	}

	if _, err := StageContextRenewalFileClear(dir, statePath, InitialGeneration); err == nil {
		t.Fatal("StageContextRenewalFileClear() accepted a symlink")
	}
	info, err := os.Lstat(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("state path mode = %v, want symlink", info.Mode())
	}
	if _, err := os.Stat(contextRenewalBackupPath(statePath, InitialGeneration)); !os.IsNotExist(err) {
		t.Fatalf("symlink staging created a backup: %v", err)
	}
}

func stageContextRenewalCrashFixture(t *testing.T, path, generationID string) {
	t.Helper()
	dir := filepath.Dir(path)
	manifest, err := readContextRenewalManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Files = append(manifest.Files, contextRenewalFile{Path: filepath.Base(path), GenerationID: generationID})
	if err := writeContextRenewalManifest(dir, manifest); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, contextRenewalBackupPath(path, generationID)); err != nil {
		t.Fatal(err)
	}
}

func TestContextRenewalRecoversRegisteredNestedFilesAcrossCrashWindows(t *testing.T) {
	for _, phase := range []string{"registered", "renamed", "restored", "retired"} {
		t.Run(phase, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "private", "module.state")
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("preserve"), 0600); err != nil {
				t.Fatal(err)
			}
			manifest := contextRenewalManifest{Version: 1, Files: []contextRenewalFile{{Path: "private/module.state", GenerationID: InitialGeneration}}}
			if err := writeContextRenewalManifest(dir, manifest); err != nil {
				t.Fatal(err)
			}
			if phase != "registered" {
				if err := os.Rename(path, contextRenewalBackupPath(path, InitialGeneration)); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "restored" {
				if err := os.Rename(contextRenewalBackupPath(path, InitialGeneration), path); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "retired" {
				if err := os.RemoveAll(filepath.Dir(path)); err != nil {
					t.Fatal(err)
				}
			}
			if err := recoverContextRenewalFiles(dir, InitialGeneration); err != nil {
				t.Fatal(err)
			}
			if phase == "retired" {
				if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
					t.Fatal("recreated retired directory", err)
				}
			} else {
				if body, err := os.ReadFile(path); err != nil || string(body) != "preserve" {
					t.Fatalf("state = %s %v", body, err)
				}
			}
			if present, err := contextRenewalFilesPresent(dir); err != nil || present {
				t.Fatalf("transaction remains: %v %v", present, err)
			}
		})
	}
}

func TestContextRenewalStagesNestedFilesWithoutRecoveringAnotherActiveFile(t *testing.T) {
	dir := t.TempDir()
	paths := []string{filepath.Join(dir, "first", "state"), filepath.Join(dir, "second", "state")}
	for _, path := range paths {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("current"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	first, err := StageContextRenewalFileClear(dir, paths[0], InitialGeneration)
	if err != nil {
		t.Fatal(err)
	}
	second, err := StageContextRenewalFileClear(dir, paths[1], InitialGeneration)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := StageContextRenewalFileClear(dir, paths[0], InitialGeneration); !errors.Is(err, errContextRenewalInProgress) {
		t.Fatalf("duplicate stage = %v", err)
	}
	for _, path := range paths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("staging restored active file", err)
		}
	}
	if err := recoverContextRenewalFiles(dir, InitialGeneration); !errors.Is(err, errContextRenewalInProgress) {
		t.Fatalf("recovered active transaction: %v", err)
	}
	if err := first.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := second.Finalize(); err != nil {
		t.Fatal(err)
	}
	if err := second.Finalize(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(paths[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(paths[1]); !os.IsNotExist(err) {
		t.Fatal("finalized file returned", err)
	}
}

func TestContextRenewalRejectsEscapingAndLinkedTransactionPaths(t *testing.T) {
	for _, relative := range []string{"../outside.state", "linked/state"} {
		t.Run(relative, func(t *testing.T) {
			dir, outside := t.TempDir(), t.TempDir()
			backup := filepath.Join(outside, "state") + contextRenewalBackupMarker + InitialGeneration
			if err := os.WriteFile(backup, []byte("outside"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(dir, "linked")); err != nil {
				t.Fatal(err)
			}
			if err := writeContextRenewalManifest(dir, contextRenewalManifest{Version: 1, Files: []contextRenewalFile{{Path: relative, GenerationID: InitialGeneration}}}); err != nil {
				t.Fatal(err)
			}
			if err := recoverContextRenewalFiles(dir, InitialGeneration); err == nil {
				t.Fatal("recovered outside the Thread boundary")
			}
			if body, err := os.ReadFile(backup); err != nil || string(body) != "outside" {
				t.Fatalf("outside backup modified: %q %v", body, err)
			}
		})
	}
}

func TestContextRenewalRecoveryPreservesConflictingCanonicalFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	stageContextRenewalCrashFixture(t, path, InitialGeneration)
	if err := os.WriteFile(path, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := recoverContextRenewalFiles(dir, InitialGeneration); err == nil {
		t.Fatal("overwrote new canonical state")
	}
	for _, item := range []struct{ path, content string }{{path, "new"}, {contextRenewalBackupPath(path, InitialGeneration), "old"}} {
		if body, err := os.ReadFile(item.path); err != nil || string(body) != item.content {
			t.Fatalf("state modified: %q %v", body, err)
		}
	}
}
