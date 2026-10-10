package migration

import (
	"encoding/json"
	"testing"

	"github.com/juex-ai/juex/internal/migration/legacy"
)

func TestSourceBufferedWriteInventoryOwnedFailures(t *testing.T) {
	for _, kind := range []string{"begin", "chunk", "commit", "abort"} {
		t.Run(kind, func(t *testing.T) {
			failed := sourceWritePair(kind)
			failed.Facts[1].Message.Blocks[0].ResultFact.Data = nil
			thread := legacy.Thread{Commits: []legacy.Commit{failed}}
			if active, err := InspectSourceBufferedWrites(thread); err != nil || len(active) != 0 {
				t.Fatalf("failed operation created lifecycle state: %v %v", active, err)
			}
			thread.Commits = []legacy.Commit{sourceWritePair("begin"), failed}
			if active, err := InspectSourceBufferedWrites(thread); err != nil || len(active) != 1 {
				t.Fatalf("failed operation lost active session: %v %v", active, err)
			}
			thread.Commits = append(thread.Commits, sourceWritePair("commit"))
			if err := requireSettledSourceWrites(thread); err != nil {
				t.Fatalf("reused call ID did not pair with later successful commit: %v", err)
			}
		})
	}
	for _, name := range []string{"unpaired", "success_without_fact", "invalid_data"} {
		t.Run(name, func(t *testing.T) {
			failed := sourceWritePair("begin")
			result := &failed.Facts[1].Message.Blocks[0]
			result.ResultFact.Data = nil
			switch name {
			case "unpaired":
				failed.Facts = failed.Facts[1:]
			case "success_without_fact":
				result.IsError = false
			case "invalid_data":
				result.ResultFact.Data = json.RawMessage(`{}`)
			}
			if _, err := InspectSourceBufferedWrites(legacy.Thread{Commits: []legacy.Commit{failed}}); err == nil {
				t.Fatal("unproven lifecycle fact accepted")
			}
		})
	}
}
