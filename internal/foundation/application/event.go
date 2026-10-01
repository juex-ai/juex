package application

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// Event is an application-owned business fact. It contains no recipient address,
// raw conversation evidence or executable instruction. Each delivery deduplicates
// the frozen identity and payload independently.
type Event struct {
	ID          string    `json:"id"`
	Application string    `json:"application"`
	ResourceID  string    `json:"resource_id"`
	Kind        string    `json:"kind"`
	Title       string    `json:"title"`
	Summary     string    `json:"summary,omitempty"`
	Scope       Scope     `json:"scope"`
	Epoch       int64     `json:"epoch"`
	Fence       uint64    `json:"fence,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

func (e Event) Valid() bool {
	_, err := uuid.Parse(e.ID)
	return err == nil && e.Scope.Valid() && (e.Application == "memory" || e.Application == "calendar" || e.Application == "runtime") && e.ResourceID != "" && len(e.ResourceID) <= 128 && (e.Kind == "completed" || e.Kind == "attention" || e.Kind == "reminder") && strings.TrimSpace(e.Title) != "" && len(e.Title) <= 256 && !strings.ContainsAny(e.Title, "\r\n") && len(e.Summary) <= 2048 && e.Epoch > 0 && !e.CreatedAt.IsZero()
}
