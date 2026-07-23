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

func sseResumeCursorWithPresence(r *http.Request) (string, bool) {
	if r == nil {
		return "", false
	}
	if cursor := strings.TrimSpace(r.Header.Get("Last-Event-ID")); cursor != "" {
		return cursor, true
	}
	values, ok := r.URL.Query()["since"]
	if !ok {
		return "", false
	}
	if len(values) == 0 {
		return "", true
	}
	return strings.TrimSpace(values[0]), true
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
