package thread

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"testing"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
)

func TestIncrementalProjectionMatchesCurrentGenerationReplayAcrossGeneratedHistory(t *testing.T) {
	for seed := int64(1); seed <= 8; seed++ {
		t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) {
			store := NewStore(t.TempDir())
			target, err := store.EnsureMain()
			if err != nil {
				t.Fatal(err)
			}
			random := rand.New(rand.NewSource(seed))
			turnOrdinal := 0
			for operation := 0; operation < 24; operation++ {
				switch random.Intn(4) {
				case 0:
					if err := target.Append(llm.TextMessage(llm.RoleUser, fmt.Sprintf("message-%d", operation))); err != nil {
						t.Fatal(err)
					}
				case 1:
					if _, err := target.BeginNewGeneration(); err != nil {
						t.Fatal(err)
					}
				case 2:
					if _, err := target.BeginCompactedGeneration(llm.TextMessage(llm.RoleUser, fmt.Sprintf("summary-%d", operation)), operation%2 == 0, nil); err != nil {
						t.Fatal(err)
					}
				case 3:
					turnOrdinal++
					turnID := fmt.Sprintf("turn-%d", turnOrdinal)
					if err := target.AppendEvent(events.Event{Type: "turn.started", TurnID: turnID}); err != nil {
						t.Fatal(err)
					}
					if err := target.Append(llm.TextMessage(llm.RoleUser, fmt.Sprintf("turn-message-%d", turnOrdinal))); err != nil {
						t.Fatal(err)
					}
					if err := target.AppendEvent(events.Event{Type: "turn.completed", TurnID: turnID}); err != nil {
						t.Fatal(err)
					}
				}
			}

			want := target.ReplaySnapshot()
			if err := target.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := store.OpenActive(MainID)
			if err != nil {
				t.Fatal(err)
			}
			got := reopened.ReplaySnapshot()
			assertReplayStateJSONEqual(t, got, want)
			if err := reopened.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func assertReplayStateJSONEqual(t *testing.T, got, want ReplayState) {
	t.Helper()
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("incremental projection differs from full replay\ngot:  %s\nwant: %s", gotJSON, wantJSON)
	}
}
