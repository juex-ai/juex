package workmem

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRecoverContextRenewalFilesDistinguishesGenerationCommit(t *testing.T) {
	for _, test := range []struct {
		name              string
		currentGeneration string
		wantRestored      bool
	}{
		{name: "before commit", currentGeneration: "g000001", wantRestored: true},
		{name: "after commit", currentGeneration: "g000002", wantRestored: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			goal := NewGoalStateStore(dir, GoalStateOptions{})
			notes := NewNotesStore(dir)
			if _, err := goal.Create("preserve transaction state", "recover after restart"); err != nil {
				t.Fatal(err)
			}
			if _, err := notes.Update("- [ ] preserve Notes"); err != nil {
				t.Fatal(err)
			}
			goalBefore, err := os.ReadFile(goal.Path)
			if err != nil {
				t.Fatal(err)
			}
			notesPath := filepath.Join(dir, NotesFileName)
			notesBefore, err := os.ReadFile(notesPath)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := goal.StageClearForContextRenewal("g000001"); err != nil {
				t.Fatal(err)
			}
			if _, _, err := notes.StageClearForContextRenewal("g000001"); err != nil {
				t.Fatal(err)
			}
			if err := RecoverContextRenewalFiles(dir, test.currentGeneration); err != nil {
				t.Fatal(err)
			}
			for path, before := range map[string][]byte{goal.Path: goalBefore, notesPath: notesBefore} {
				got, err := os.ReadFile(path)
				if test.wantRestored {
					if err != nil || string(got) != string(before) {
						t.Fatalf("restored %s = %q, %v; want %q", filepath.Base(path), got, err, before)
					}
				} else if !os.IsNotExist(err) {
					t.Fatalf("committed clear retained %s: %v", filepath.Base(path), err)
				}
			}
			backups, err := filepath.Glob(filepath.Join(dir, "*.context-renewal-*"))
			if err != nil {
				t.Fatal(err)
			}
			if len(backups) != 0 {
				t.Fatalf("recovery left backups: %v", backups)
			}
		})
	}
}
