// Package chunkedwrite owns the canonical lifecycle facts for chunked file
// writes. Tool adapters emit these facts; replay and restore code derive
// state from them instead of scraping provider-facing text.
package chunkedwrite

import "sort"

type EventKind string

const (
	EventBegin  EventKind = "begin"
	EventChunk  EventKind = "chunk"
	EventCommit EventKind = "commit"
	EventAbort  EventKind = "abort"
)

const (
	ModeCreate    = "create"
	ModeOverwrite = "overwrite"
)

type Status string

const (
	StatusActive    Status = "active"
	StatusCommitted Status = "committed"
	StatusAborted   Status = "aborted"
)

type Event struct {
	Kind      EventKind `json:"kind"`
	WriteID   string    `json:"write_id"`
	Path      string    `json:"path,omitempty"`
	Mode      string    `json:"mode,omitempty"`
	Index     int       `json:"index,omitempty"`
	Bytes     int       `json:"bytes,omitempty"`
	Chars     int       `json:"chars,omitempty"`
	Chunks    int       `json:"chunks,omitempty"`
	SHA256    string    `json:"sha256,omitempty"`
	Duplicate bool      `json:"duplicate,omitempty"`
	FileMode  uint32    `json:"file_mode,omitempty"`
}

type Chunk struct {
	Index  int
	Bytes  int
	Chars  int
	SHA256 string
}

type State struct {
	WriteID  string
	Status   Status
	Path     string
	Mode     string
	FileMode uint32
	Chunks   []Chunk
	Commit   *Event
	Abort    *Event
}

func EventFromStructured(value any) (Event, bool) {
	switch v := value.(type) {
	case Event:
		return v, v.WriteID != "" && v.Kind != ""
	case *Event:
		if v == nil {
			return Event{}, false
		}
		return *v, v.WriteID != "" && v.Kind != ""
	default:
		return Event{}, false
	}
}

func BuildStates(events []Event) map[string]State {
	states := map[string]State{}
	for _, event := range events {
		if event.WriteID == "" || event.Kind == "" {
			continue
		}
		state := states[event.WriteID]
		if state.WriteID == "" {
			state.WriteID = event.WriteID
		}
		switch event.Kind {
		case EventBegin:
			state.Status = StatusActive
			state.Path = firstNonEmpty(event.Path, state.Path)
			state.Mode = firstNonEmpty(event.Mode, state.Mode)
			if event.FileMode != 0 {
				state.FileMode = event.FileMode
			}
			state.Commit = nil
			state.Abort = nil
		case EventChunk:
			state.Chunks = upsertChunk(state.Chunks, Chunk{
				Index:  event.Index,
				Bytes:  event.Bytes,
				Chars:  event.Chars,
				SHA256: event.SHA256,
			})
			if state.Status == "" {
				state.Status = StatusActive
			}
		case EventCommit:
			state.Status = StatusCommitted
			state.Path = firstNonEmpty(event.Path, state.Path)
			commit := event
			state.Commit = &commit
			state.Abort = nil
		case EventAbort:
			state.Status = StatusAborted
			abort := event
			state.Abort = &abort
			state.Commit = nil
		}
		states[event.WriteID] = state
	}
	for id, state := range states {
		sort.Slice(state.Chunks, func(i, j int) bool {
			return state.Chunks[i].Index < state.Chunks[j].Index
		})
		states[id] = state
	}
	return states
}

func upsertChunk(chunks []Chunk, chunk Chunk) []Chunk {
	for i := range chunks {
		if chunks[i].Index == chunk.Index {
			chunks[i] = chunk
			return chunks
		}
	}
	return append(chunks, chunk)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
