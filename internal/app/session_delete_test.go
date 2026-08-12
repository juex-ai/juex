package app

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/juex-ai/juex/internal/artifact"
	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/session"
)

func TestDeleteSessionRemovesOnlySessionArtifactNamespace(t *testing.T) {
	cfg := testSessionDeleteConfig(t)
	sess, err := session.New(cfg.SessionsDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := artifact.NewStore(cfg.ArtifactDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put("sessions/"+sess.ID+"/media/image.png", []byte("image")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put("event-media/event.png", []byte("event")); err != nil {
		t.Fatal(err)
	}

	if err := DeleteSession(cfg, sess.ID, SessionDeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sess.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session stat = %v, want not exist", err)
	}
	if exists, err := store.HasNamespace("sessions/" + sess.ID); err != nil || exists {
		t.Fatalf("session Artifact namespace = %t, %v", exists, err)
	}
	if exists, err := store.HasNamespace("event-media"); err != nil || !exists {
		t.Fatalf("Agent Artifact namespace = %t, %v", exists, err)
	}
}

func TestPrepareSessionDeletePreflightsBothStores(t *testing.T) {
	cfg := testSessionDeleteConfig(t)
	id := "20260812T120000-incomplete"
	if err := os.MkdirAll(filepath.Join(cfg.SessionsDir(), id), 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := artifact.NewStore(cfg.ArtifactDir())
	if err != nil {
		t.Fatal(err)
	}
	ref, err := store.Put("sessions/"+id+"/media/image.png", []byte("image"))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := PrepareSessionDelete(cfg, id, SessionDeleteOptions{}); err == nil {
		t.Fatal("PrepareSessionDelete accepted an incomplete Session")
	}
	if _, err := store.Read(ref); err != nil {
		t.Fatalf("failed preflight mutated Artifact: %v", err)
	}
}

func TestDeleteSessionRetriesOrphanArtifactCleanupAfterPartialFailure(t *testing.T) {
	cfg := testSessionDeleteConfig(t)
	sess, err := session.New(cfg.SessionsDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := artifact.NewStore(cfg.ArtifactDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put("sessions/"+sess.ID+"/media/image.png", []byte("image")); err != nil {
		t.Fatal(err)
	}
	plan, err := PrepareSessionDelete(cfg, sess.ID, SessionDeleteOptions{})
	if err != nil {
		t.Fatal(err)
	}

	original := cfg.ArtifactDir() + "-original"
	if err := os.Rename(cfg.ArtifactDir(), original); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, cfg.ArtifactDir()); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	err = plan.Commit()
	var partial *PartialSessionDeleteError
	if !errors.As(err, &partial) || partial.SessionID != sess.ID {
		t.Fatalf("Commit error = %T %v, want PartialSessionDeleteError", err, err)
	}
	if _, err := os.Stat(sess.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session stat = %v, want removed before partial error", err)
	}
	if err := os.Remove(cfg.ArtifactDir()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(original, cfg.ArtifactDir()); err != nil {
		t.Fatal(err)
	}

	if err := DeleteSession(cfg, sess.ID, SessionDeleteOptions{}); err != nil {
		t.Fatalf("orphan cleanup retry: %v", err)
	}
	if exists, err := store.HasNamespace("sessions/" + sess.ID); err != nil || exists {
		t.Fatalf("orphan namespace after retry = %t, %v", exists, err)
	}
}

func TestDeleteSessionRetriesHistoryOnlyCleanupAfterSessionDirRemoved(t *testing.T) {
	cfg := testSessionDeleteConfig(t)
	sess, err := session.New(cfg.SessionsDir())
	if err != nil {
		t.Fatal(err)
	}
	info := sess.Info()
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	if err := session.SetActive(cfg.HistoryPath(), info); err != nil {
		t.Fatal(err)
	}
	store, err := artifact.NewStore(cfg.ArtifactDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put("sessions/"+sess.ID+"/media/image.png", []byte("image")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(sess.Dir); err != nil {
		t.Fatal(err)
	}

	if err := DeleteSession(cfg, sess.ID, SessionDeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	history, err := session.LoadHistory(cfg.HistoryPath())
	if err != nil {
		t.Fatal(err)
	}
	if history.Active != nil {
		t.Fatalf("active history = %+v, want nil", history.Active)
	}
	for _, got := range history.Sessions {
		if got.ID == sess.ID {
			t.Fatalf("history still contains deleted Session: %+v", history)
		}
	}
	if exists, err := store.HasNamespace("sessions/" + sess.ID); err != nil || exists {
		t.Fatalf("history-only retry Artifact namespace = %t, %v", exists, err)
	}
}

func TestSessionDeleteCommitRemovesNamespaceCreatedAfterPreflight(t *testing.T) {
	cfg := testSessionDeleteConfig(t)
	sess, err := session.New(cfg.SessionsDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	plan, err := PrepareSessionDelete(cfg, sess.ID, SessionDeleteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	store, err := artifact.NewStore(cfg.ArtifactDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put("sessions/"+sess.ID+"/media/late.png", []byte("late")); err != nil {
		t.Fatal(err)
	}

	if err := plan.Commit(); err != nil {
		t.Fatal(err)
	}
	if exists, err := store.HasNamespace("sessions/" + sess.ID); err != nil || exists {
		t.Fatalf("late Artifact namespace = %t, %v", exists, err)
	}
}

func testSessionDeleteConfig(t *testing.T) config.Config {
	t.Helper()
	stateDir := filepath.Join(t.TempDir(), "agent")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return config.Config{WorkDir: t.TempDir(), AgentStateDir: stateDir}
}
