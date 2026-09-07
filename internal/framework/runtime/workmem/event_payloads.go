package workmem

import "time"

type GoalUpdatedPayload struct {
	Description       string     `json:"description,omitempty"`
	Acceptance        string     `json:"acceptance,omitempty"`
	ContinuationCount int        `json:"continuation_count,omitempty"`
	Status            GoalStatus `json:"status,omitempty"`
	StatusReason      string     `json:"status_reason,omitempty"`
	UpdatedAt         time.Time  `json:"updated_at,omitempty"`
}

type NotesUpdatedPayload struct {
	Content   string    `json:"content"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type NotesErroredPayload struct {
	Error string `json:"error"`
	Path  string `json:"path"`
}

type GoalContinuedPayload struct {
	Status                GoalStatus `json:"status"`
	Reason                string     `json:"reason,omitempty"`
	ContinuationCount     int        `json:"continuation_count"`
	ContinuationPromptLen int        `json:"continuation_prompt_len"`
}
