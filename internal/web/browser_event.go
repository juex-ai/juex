package web

import (
	"encoding/json"
	"log"
	"time"

	"github.com/juex-ai/juex/internal/eventcatalog"
	"github.com/juex-ai/juex/internal/events"
	juexruntime "github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/statusapi"
	"github.com/juex-ai/juex/internal/toolevents"
)

// BrowserEvent is the stable event DTO sent over the Thread SSE stream.
// Runtime may persist more event facts than the browser consumes; this DTO is
// the web transport contract for browser-visible read-model updates.
type BrowserEvent struct {
	ID            string             `json:"id"`
	Type          string             `json:"type"`
	SchemaVersion int                `json:"schema_version"`
	Timestamp     time.Time          `json:"ts"`
	TurnID        string             `json:"turn_id,omitempty"`
	Payload       json.RawMessage    `json:"payload,omitempty"`
	Status        statusapi.Snapshot `json:"status"`
	transient     bool
	sequence      uint64
}

func browserEventTypes() []string {
	return eventcatalog.Default().BrowserTypes()
}

func browserEventFromRuntime(
	e events.Event,
	status statusapi.Snapshot,
) (BrowserEvent, bool, error) {
	prepared, payload, visible, err := eventcatalog.Default().BrowserPayload(e)
	if err != nil {
		return BrowserEvent{}, true, err
	}
	if !visible {
		return BrowserEvent{}, false, nil
	}
	if prepared.Type == toolevents.OutputDeltaType &&
		!browserToolOutputDeltaVisible(prepared.TurnID, payload, status) {
		return BrowserEvent{}, false, nil
	}
	return BrowserEvent{
		ID:            prepared.ID,
		Type:          prepared.Type,
		SchemaVersion: prepared.SchemaVersion,
		Timestamp:     prepared.Timestamp,
		TurnID:        prepared.TurnID,
		Payload:       payload,
		Status:        status,
		transient:     prepared.Transient,
	}, true, nil
}

func browserToolOutputDeltaVisible(turnID string, payload json.RawMessage, status statusapi.Snapshot) bool {
	if status.Turn == nil || status.Turn.ID != turnID {
		return false
	}
	var delta toolevents.OutputDeltaPayload
	if err := json.Unmarshal(payload, &delta); err != nil {
		return false
	}
	for _, tool := range status.Tools {
		if tool.ToolUseID == delta.ToolUseID {
			return tool.State == statusapi.ToolCallStreaming
		}
	}
	return false
}

type browserEventProjection struct {
	status *juexruntime.StatusStore
	stream *broadcaster
}

func (p browserEventProjection) Publish(event events.Event) {
	if p.status == nil || p.stream == nil {
		return
	}
	projected, visible, err := browserEventFromRuntime(
		event,
		statusapi.FromRuntime(p.status.Snapshot()),
	)
	if err != nil {
		log.Printf("web: project browser event %q: %v", event.Type, err)
		return
	}
	if visible {
		p.stream.enqueue(projected)
	}
}

func projectBrowserEvents(
	seed juexruntime.StatusSeed,
	journal []events.Event,
	after string,
	authoritative *juexruntime.StatusSnapshot,
) ([]BrowserEvent, error) {
	status := juexruntime.NewStatusStore(seed)
	projected := make([]BrowserEvent, 0, len(journal))
	emit := after == ""
	for _, event := range journal {
		status.Publish(event)
		if !emit {
			if event.ID == after {
				emit = true
			}
			continue
		}
		browserEvent, visible, err := browserEventFromRuntime(
			event,
			statusapi.FromRuntime(status.Snapshot()),
		)
		if err != nil {
			return projected, err
		}
		if visible {
			projected = append(projected, browserEvent)
		}
	}
	if authoritative != nil &&
		len(journal) > 0 &&
		len(projected) > 0 &&
		authoritative.Cursor == journal[len(journal)-1].ID {
		projected[len(projected)-1].Status = statusapi.FromRuntime(*authoritative)
	}
	return projected, nil
}
