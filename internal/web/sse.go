package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/juex-ai/juex/internal/events"
)

// writeSSEFrame writes one SSE frame to w using the documented shape:
//
//	id: <event.ID>
//	event: <type>
//	data: <json>
//
// Each frame ends with a blank line. The wire id is the event's bus id
// directly — clients send it back as Last-Event-ID (or ?since=) on
// reconnect so the server can replay missed events from events.jsonl.
// The data field is a single line of JSON; embedded newlines in
// payloads stay encoded as \n inside the JSON string so the wire format
// remains a single logical SSE record.
func writeSSEFrame(w io.Writer, e events.Event) error {
	body, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", e.ID, e.Type, body); err != nil {
		return err
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}
