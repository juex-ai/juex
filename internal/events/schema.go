package events

import "encoding/json"

type ReplayPolicy string

const (
	ReplayRequired  ReplayPolicy = "required"
	ReplayIgnorable ReplayPolicy = "ignorable"
)

// SchemaCatalog owns stable cross-module event schemas without closing the
// generic Event envelope to local or Extension-defined event types.
type SchemaCatalog interface {
	Prepare(Event) (Event, error)
	Decode(Event) (Event, error)
	BrowserPayload(Event) (Event, json.RawMessage, bool, error)
	BrowserTypes() []string
}
