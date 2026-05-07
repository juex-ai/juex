package web

import (
	"bufio"
	"encoding/json"
	"io"

	"github.com/juex-ai/juex/internal/events"
)

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
