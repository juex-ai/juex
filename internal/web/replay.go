package web

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/session"
)

type committedEventReplay struct {
	source        *os.File
	size          int64
	seed          runtime.StatusSeed
	authoritative *runtime.StatusSnapshot
}

func captureCommittedEventReplay(runtimeApp *app.App, sessionID string) (*committedEventReplay, error) {
	var replay *committedEventReplay
	err := runtimeApp.ReadCommittedEvents(func() error {
		return runtimeApp.ReadSessionID(sessionID, func(sess *session.Session) error {
			source, err := os.Open(filepath.Join(sess.Dir, "events.jsonl"))
			if err != nil {
				return err
			}
			info, err := source.Stat()
			if err != nil {
				_ = source.Close()
				return err
			}
			var authoritative *runtime.StatusSnapshot
			if runtimeApp.Status != nil {
				snapshot := runtimeApp.Status.Snapshot()
				authoritative = &snapshot
			}
			replay = &committedEventReplay{
				source: source,
				size:   info.Size(),
				seed: runtime.StatusSeed{
					SessionID:        sess.ID,
					SessionAlias:     sess.Alias,
					MaxPendingInputs: runtime.DefaultMaxPendingInput,
				},
				authoritative: authoritative,
			}
			return nil
		})
	})
	if err != nil {
		if replay != nil {
			_ = replay.Close()
		}
		return nil, err
	}
	return replay, nil
}

func (r *committedEventReplay) readJournal() ([]events.Event, error) {
	return replaySince(io.NewSectionReader(r.source, 0, r.size), "")
}

func (r *committedEventReplay) Close() error {
	if r == nil || r.source == nil {
		return nil
	}
	return r.source.Close()
}

// replaySince reads NDJSON events from r and returns every event whose
// ID is *after* the given since marker. An empty since returns all events.
// Malformed lines are silently skipped (a corrupt session should still
// be browsable).
//
// The scanner buffer is sized for very large payloads (e.g. a tool
// result containing a multi-MB blob).
func replaySince(r io.Reader, since string) ([]events.Event, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var (
		out   []events.Event
		after = since == ""
	)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e events.Event
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		if !after {
			if e.ID == since {
				after = true
			}
			continue
		}
		out = append(out, e)
	}
	if err := sc.Err(); err != nil {
		return out, err
	}
	return out, nil
}
