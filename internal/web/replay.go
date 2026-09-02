package web

import (
	"bufio"
	"encoding/json"
	"io"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/thread"
)

type committedEventReplay struct {
	journal       *thread.EventJournalSnapshot
	seed          runtime.StatusSeed
	authoritative *runtime.StatusSnapshot
}

func captureCommittedEventReplay(runtimeApp *app.App, threadID string) (*committedEventReplay, error) {
	var replay *committedEventReplay
	err := runtimeApp.ReadThreadID(threadID, func(target *thread.Thread) error {
		return runtimeApp.ReadCommittedEvents(func() error {
			journal, err := target.CaptureEventJournal()
			if err != nil {
				return err
			}
			var authoritative *runtime.StatusSnapshot
			if runtimeApp.Status != nil {
				snapshot := runtimeApp.Status.Snapshot()
				authoritative = &snapshot
			}
			replay = &committedEventReplay{
				journal: journal,
				seed: runtime.StatusSeed{
					ThreadID: target.ID, ThreadAlias: target.Alias,
					MaxPendingInputs: runtime.DefaultMaxPendingInput,
				},
				authoritative: authoritative,
			}
			return nil
		})
	})
	return replay, err
}

func (r *committedEventReplay) readJournal() ([]events.Event, error) {
	if r == nil || r.journal == nil {
		return nil, nil
	}
	return r.journal.Events()
}

func (r *committedEventReplay) Close() error {
	if r == nil || r.journal == nil {
		return nil
	}
	err := r.journal.Close()
	r.journal = nil
	return err
}

type browserReplayDeduplicator struct {
	durableIDs     map[string]struct{}
	replayBoundary uint64
}

func newBrowserReplayDeduplicator(replayed []BrowserEvent, replayBoundary uint64) *browserReplayDeduplicator {
	if len(replayed) == 0 {
		return nil
	}
	// A subscribed browser can queue at most broadcasterBufferSize events
	// while its durable replay is being projected. Only that bounded suffix can
	// overlap the live stream.
	start := max(0, len(replayed)-broadcasterBufferSize)
	ids := make(map[string]struct{}, len(replayed)-start)
	for _, event := range replayed[start:] {
		if !event.transient && event.ID != "" {
			ids[event.ID] = struct{}{}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	return &browserReplayDeduplicator{durableIDs: ids, replayBoundary: replayBoundary}
}

func (d *browserReplayDeduplicator) skip(event BrowserEvent) bool {
	if d == nil || len(d.durableIDs) == 0 {
		return false
	}
	if event.transient {
		return event.sequence <= d.replayBoundary
	}
	if _, duplicate := d.durableIDs[event.ID]; duplicate {
		delete(d.durableIDs, event.ID)
		return true
	}
	// Broadcaster delivery is ordered. The first unseen durable event is the
	// start of the live stream, so no older duplicate can follow it.
	d.durableIDs = nil
	return false
}

// replaySince remains a transport helper for imported event streams. Thread
// persistence replays event facts directly from journal.jsonl.
func replaySince(reader io.Reader, since string) ([]events.Event, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var result []events.Event
	emit := since == ""
	for scanner.Scan() {
		var event events.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		if !emit {
			if event.ID == since {
				emit = true
			}
			continue
		}
		result = append(result, event)
	}
	return result, scanner.Err()
}
