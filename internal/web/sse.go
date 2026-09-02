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

// sseReplayParam requests a replay position that is not a durable event ID. It
// is deliberately a separate parameter rather than a reserved ?since= value:
// event IDs are opaque and events.Normalize preserves any non-empty
// caller-supplied ID, so a reserved cursor value could be produced by an
// extension and turn into the full-journal replay this contract prevents.
const (
	sseReplayParam        = "replay"
	sseReplayJournalStart = "journal-start"
)

// sseResumeCursorWithPresence reports the durable event the client has already
// applied, and whether it asked to resume at all. A blank Last-Event-ID or a
// blank ?since= carries no resume position and reads as absent: there is
// nothing to replay. A client whose transcript snapshot was taken while the
// journal was still empty has no event to resume after but still needs
// everything committed since, and asks for that with ?replay=journal-start.
// Keeping the two separate means "I lost my cursor" can never be mistaken for
// "replay everything".
func sseResumeCursorWithPresence(r *http.Request) (string, bool) {
	if r == nil {
		return "", false
	}
	if cursor := strings.TrimSpace(r.Header.Get("Last-Event-ID")); cursor != "" {
		return cursor, true
	}
	if values, ok := r.URL.Query()["since"]; ok && len(values) > 0 {
		if cursor := strings.TrimSpace(values[0]); cursor != "" {
			return cursor, true
		}
	}
	if strings.TrimSpace(r.URL.Query().Get(sseReplayParam)) == sseReplayJournalStart {
		return "", true
	}
	return "", false
}

// writeSSEFrame writes one SSE frame to w using the documented shape:
//
//	id: <event.ID>    (durable events only)
//	data: <json>
//
// Each frame ends with a blank line. Durable events use the event's bus id
// directly; clients send it back as Last-Event-ID (or ?since=) on reconnect
// so the server can replay missed facts from the Thread Journal. Transient events
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
