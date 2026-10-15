package runtime

import (
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/framework/provenance"
	"github.com/juex-ai/juex/internal/framework/thread"
)

// RecitationSnapshot records what a normal request prepared, not whether the
// provider received it or what a future request will contain.
type RecitationSnapshot struct {
	EpochID      string               `json:"epoch_id"`
	TurnID       string               `json:"turn_id"`
	RecordedAt   time.Time            `json:"recorded_at"`
	GenerationID string               `json:"generation_id"`
	Iter         int                  `json:"iter"`
	Fragments    []RecitationFragment `json:"fragments"`
}

type RecitationFragment struct {
	MessageID string `json:"message_id"`
	Text      string `json:"text"`
}

type recitationIndex struct {
	cursor     thread.EventCursor
	generation string
	bodies     map[string]RecitationFragment
	latest     *RecitationSnapshot
}

// RecitationReader incrementally inspects existing journals. It never opens a
// mutable Thread, collects module context, or repairs runtime state.
type RecitationReader struct {
	mu      sync.Mutex
	indexes map[string]*recitationIndex
	order   []string
}

func (r *RecitationReader) Read(dir string) (*RecitationSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	snapshot, err := thread.CaptureEventStoreSnapshot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = snapshot.Close() }()
	if r.indexes == nil {
		r.indexes = map[string]*recitationIndex{}
	}
	index := r.indexes[dir]
	if index == nil {
		index = &recitationIndex{generation: thread.InitialGeneration, bodies: map[string]RecitationFragment{}}
		r.indexes[dir] = index
	}
	r.order = slices.DeleteFunc(r.order, func(value string) bool { return value == dir })
	r.order = append(r.order, dir)
	if len(r.order) > 16 {
		delete(r.indexes, r.order[0])
		r.order = r.order[1:]
	}
	cursor, err := snapshot.VisitAfter(index.cursor, func(commit thread.Commit) error {
		for _, fact := range commit.Facts {
			if fact.ToGenerationID != "" {
				index.generation = fact.ToGenerationID
			}
			if fact.Type != thread.FactEventRecorded || fact.Event == nil {
				continue
			}
			event := fact.Event
			switch event.Type {
			case "turn.completed", "turn.errored", "turn.cancelled":
				clear(index.bodies)
			case provenance.RequestEpochType:
				raw, err := json.Marshal(event.Payload)
				if err != nil {
					return err
				}
				var payload provenance.RequestEpochPayload
				if err := json.Unmarshal(raw, &payload); err != nil {
					return err
				}
				if err := provenance.ValidateRequestEpoch(payload); err != nil {
					return err
				}
				epoch := payload.Epoch
				fragments := []RecitationFragment{}
				for _, ref := range epoch.Messages {
					if ref.Source != "runtime_context" {
						continue
					}
					if len(ref.Snapshot.Content) > 0 {
						var message llm.Message
						if err := json.Unmarshal(ref.Snapshot.Content, &message); err != nil {
							return err
						}
						index.bodies[ref.ContentDigest] = RecitationFragment{MessageID: message.ID, Text: llm.BlocksPlainText(message.Blocks)}
					}
					fragment, ok := index.bodies[ref.ContentDigest]
					if !ok {
						return fmt.Errorf("recitation: missing snapshot for %s", ref.ID)
					}
					fragments = append(fragments, fragment)
				}
				if epoch.Purpose == "turn" {
					index.latest = &RecitationSnapshot{EpochID: epoch.EpochID, TurnID: event.TurnID, RecordedAt: event.Timestamp, GenerationID: index.generation, Iter: epoch.Iter, Fragments: fragments}
				}
			}
		}
		return nil
	})
	if err != nil {
		delete(r.indexes, dir)
		return nil, err
	}
	index.cursor = cursor
	if index.latest == nil {
		return nil, nil
	}
	result := *index.latest
	result.Fragments = append([]RecitationFragment{}, result.Fragments...)
	return &result, nil
}
