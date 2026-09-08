package notes

import (
	"time"
)

type NotesUpdatedPayload struct {
	Content   string    `json:"content"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type NotesErroredPayload struct {
	Error string `json:"error"`
	Path  string `json:"path"`
}
