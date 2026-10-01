package management

import "time"

type NotificationPreferences struct {
	Version     int64 `json:"version"`
	Completions bool  `json:"completions"`
	Email       bool  `json:"email"`
}

type Notification struct {
	ID          string    `json:"id"`
	Sequence    int64     `json:"sequence"`
	Application string    `json:"application"`
	ResourceID  string    `json:"resource_id"`
	AgentID     string    `json:"agent_id,omitempty"`
	Kind        string    `json:"kind"`
	Title       string    `json:"title"`
	Summary     string    `json:"summary"`
	CreatedAt   time.Time `json:"created_at"`
	Read        bool      `json:"read"`
}

type NotificationPage struct {
	Items  []Notification `json:"items"`
	Next   int64          `json:"next"`
	Unread int            `json:"unread"`
}
