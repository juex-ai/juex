package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/juex-ai/juex/internal/statusapi"
)

func sseResumeCursor(r *http.Request) string {
	cursor, _ := sseResumeCursorWithPresence(r)
	return cursor
}

// sseResumeCursorWithPresence reports the durable event the client has already
// applied, and whether it supplied one at all. An empty value carries no resume
// position, so a blank Last-Event-ID or a blank ?since= is treated exactly like
// an absent one: there is nothing to replay. Callers must not read a blank
// cursor as "start of journal", or a client that lost its cursor would pull the
// whole transcript back on every reconnect.
func sseResumeCursorWithPresence(r *http.Request) (string, bool) {
	if r == nil {
		return "", false
	}
	if cursor := strings.TrimSpace(r.Header.Get("Last-Event-ID")); cursor != "" {
		return cursor, true
	}
	values, ok := r.URL.Query()["since"]
	if !ok || len(values) == 0 {
		return "", false
	}
	cursor := strings.TrimSpace(values[0])
	if cursor == "" {
		return "", false
	}
	return cursor, true
}

// writeSSEFrame writes one SSE frame to w using the documented shape:
//
//	id: <event.ID>    (durable events only)
//	data: <json>
//
// Each frame ends with a blank line. Durable events use the event's bus id
// directly; clients send it back as Last-Event-ID (or ?since=) on reconnect
// so the server can replay missed events from events.jsonl. Transient events
// omit the id field so they cannot replace the browser's durable replay cursor.
// The data field is a single line of JSON; embedded newlines in
// payloads stay encoded as \n inside the JSON string so the wire format
// remains a single logical SSE record.
//
// We deliberately omit the "event:" line so EventSource routes every
// frame to the default "message" listener — the type is in the JSON
// payload (e.type) and the client switches on that.
func writeBrowserSSEFrame(w io.Writer, event BrowserEvent) error {
	body, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if event.transient {
		if _, err := fmt.Fprintf(w, "data: %s\n\n", body); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintf(w, "id: %s\ndata: %s\n\n", event.ID, body); err != nil {
			return err
		}
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}

func writeStatusSSE(w io.Writer, snapshot statusapi.Snapshot) error {
	body, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	if snapshot.Cursor != "" {
		if _, err := fmt.Fprintf(w, "id: %s\n", snapshot.Cursor); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", body); err != nil {
		return err
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}
