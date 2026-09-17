package tasks

import (
	"os"
	"strings"
	"testing"
)

func TestCompactionFreezesOnlyUnfinishedContext(t *testing.T) {
	for _, unfinished := range []bool{false, true} {
		store := NewStore(t.TempDir(), Options{})
		if _, err := store.Create(Create{Title: "Completed", Description: "done contract", Status: Done}); err != nil {
			t.Fatal(err)
		}
		if unfinished {
			if _, err := store.Create(Create{Title: "Waiting", Description: "unfinished contract", Status: Pending}); err != nil {
				t.Fatal(err)
			}
		}
		before, err := os.ReadFile(store.Path)
		if err != nil {
			t.Fatal(err)
		}
		part, err := New(store).CompactionContribution(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		text, present := part.ContextReplacements["thread_tasks"]
		if !present || strings.Contains(text, "done contract") || strings.Contains(text, "unfinished contract") != unfinished || (!unfinished && text != "") {
			t.Fatalf("incorrect context: %+v", part)
		}
		after, err := os.ReadFile(store.Path)
		if err != nil || string(before) != string(after) {
			t.Fatalf("snapshot changed state: %v", err)
		}
		if _, err := store.Create(Create{Title: "Later", Description: "later contract"}); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(part.State+part.ContextReplacements["thread_tasks"], "later contract") {
			t.Fatal("snapshot followed later mutations")
		}
	}
}
