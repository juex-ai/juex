package thread

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
)

const (
	JournalVersion    = 1
	ProjectionVersion = 1
	InitialGeneration = "g000001"
)

const (
	FactThreadCreated      = "thread.created"
	FactThreadRenamed      = "thread.renamed"
	FactThreadArchived     = "thread.archived"
	FactThreadUnarchived   = "thread.unarchived"
	FactMessageAppended    = "message.appended"
	FactEventRecorded      = "event.recorded"
	FactInputAccepted      = "input.accepted"
	FactInputAttemptStart  = "input.attempt.started"
	FactInputAttemptDone   = "input.attempt.succeeded"
	FactInputAttemptFailed = "input.attempt.failed"
	FactInputAttemptCancel = "input.attempt.cancelled"
	FactInputAttemptStop   = "input.attempt.interrupted"
	FactInputRequeued      = "input.requeued"
	FactInputCompleted     = "input.completed"
	FactInputDeadLettered  = "input.dead_lettered"
	FactInputCancelled     = "input.cancelled"
	FactInputExpired       = "input.expired"
	FactInputRecorded      = "input.recorded"
	FactTurnStarted        = "turn.started"
	FactTurnCompleted      = "turn.completed"
	FactTurnFailed         = "turn.failed"
	FactTurnCancelled      = "turn.cancelled"
	FactThreadSettled      = "thread.settled"
	FactContextRenewed     = "context.renewed"
	FactContextCompacted   = "context.compacted"
	FactGoalUpdated        = "goal.updated"
	FactGoalCleared        = "goal.cleared"
	FactNotesUpdated       = "notes.updated"
	FactNotesCleared       = "notes.cleared"
	FactUsageRecorded      = "usage.recorded"
	FactProjectionCheck    = "projection.checkpoint"
)

const (
	StateIdle     = "idle"
	StateWorking  = "working"
	StateFailed   = "failed"
	StateArchived = "archived"
)

var (
	ErrCorruptJournal    = errors.New("thread: corrupt journal")
	ErrInvalidFact       = errors.New("thread: invalid fact")
	ErrInvalidTransition = errors.New("thread: invalid transition")
	ErrProjectionStale   = errors.New("thread: projection persistence failed after journal commit")
)

type Commit struct {
	Version int       `json:"v"`
	Seq     uint64    `json:"seq"`
	At      Timestamp `json:"at"`
	Facts   []Fact    `json:"facts"`
}

type Fact struct {
	Type             string            `json:"type"`
	ThreadID         string            `json:"thread_id,omitempty"`
	Alias            string            `json:"alias,omitempty"`
	ParentThreadID   string            `json:"parent_thread_id,omitempty"`
	GenerationID     string            `json:"generation_id,omitempty"`
	FromGenerationID string            `json:"from_generation_id,omitempty"`
	ToGenerationID   string            `json:"to_generation_id,omitempty"`
	InputID          string            `json:"input_id,omitempty"`
	InputRecord      json.RawMessage   `json:"input_record,omitempty"`
	AttemptID        string            `json:"attempt_id,omitempty"`
	TurnID           string            `json:"turn_id,omitempty"`
	Message          *llm.Message      `json:"message,omitempty"`
	Event            *events.Event     `json:"event,omitempty"`
	Summary          *llm.Message      `json:"summary,omitempty"`
	Automatic        bool              `json:"automatic,omitempty"`
	Error            string            `json:"error,omitempty"`
	Goal             json.RawMessage   `json:"goal,omitempty"`
	Notes            *string           `json:"notes,omitempty"`
	NotesUpdatedAt   *Timestamp        `json:"notes_updated_at,omitempty"`
	Usage            *llm.Usage        `json:"usage,omitempty"`
	ContextUsage     *llm.ContextUsage `json:"context_usage,omitempty"`
	Checkpoint       *ReplayCheckpoint `json:"checkpoint,omitempty"`
}

// ReplayCheckpoint is a bounded recovery projection. It carries the current
// provider-visible context and nonterminal Inputs, never the full presentation
// transcript or terminal Input history.
type ReplayCheckpoint struct {
	Version          int                        `json:"v"`
	Projection       Projection                 `json:"projection"`
	ProviderMessages []llm.Message              `json:"provider_messages,omitempty"`
	LatestActivity   *Activity                  `json:"latest_activity,omitempty"`
	Inputs           map[string]InputProjection `json:"inputs,omitempty"`
	InputOrder       []string                   `json:"input_order,omitempty"`
	InputRecords     map[string]json.RawMessage `json:"input_records,omitempty"`
	ContextUsage     *llm.ContextUsage          `json:"context_usage,omitempty"`
}

type GenerationProjection struct {
	ID          string `json:"generation_id"`
	Ordinal     int    `json:"ordinal"`
	StartSeq    uint64 `json:"start_seq"`
	StartOffset int64  `json:"start_offset"`
}

type Counts struct {
	GenerationCount   int `json:"generation_count"`
	TurnCount         int `json:"turn_count"`
	PendingInputCount int `json:"pending_input_count"`
}

type ContextProjection struct {
	ContextWindow int        `json:"context_window"`
	CurrentTokens int        `json:"current_tokens"`
	Percentage    float64    `json:"percentage"`
	CalibratedAt  *Timestamp `json:"calibrated_at,omitempty"`
}

type JournalProjection struct {
	ProjectedSeq         uint64 `json:"projected_seq"`
	ProjectedOffset      int64  `json:"projected_offset"`
	LastCheckpointSeq    uint64 `json:"last_checkpoint_seq,omitempty"`
	LastCheckpointOffset int64  `json:"last_checkpoint_offset,omitempty"`
}

type Projection struct {
	Version           int                  `json:"v"`
	ThreadID          string               `json:"thread_id"`
	Alias             string               `json:"alias"`
	ParentThreadID    string               `json:"parent_thread_id,omitempty"`
	CreatedAt         Timestamp            `json:"created_at"`
	ArchivedAt        *Timestamp           `json:"archived_at,omitempty"`
	State             string               `json:"state"`
	Revision          uint64               `json:"revision"`
	CurrentGeneration GenerationProjection `json:"current_generation"`
	Counts            Counts               `json:"counts"`
	Goal              json.RawMessage      `json:"goal,omitempty"`
	Notes             string               `json:"notes,omitempty"`
	NotesUpdatedAt    *Timestamp           `json:"notes_updated_at,omitempty"`
	TokenUsage        llm.Usage            `json:"token_usage"`
	ContextUsage      *ContextProjection   `json:"context_usage,omitempty"`
	LastActivityAt    Timestamp            `json:"last_activity_at"`
	Journal           JournalProjection    `json:"journal"`
}

type IndexEntry struct {
	ThreadID             string     `json:"thread_id"`
	Alias                string     `json:"alias"`
	ParentThreadID       string     `json:"parent_thread_id,omitempty"`
	ArchivedAt           *Timestamp `json:"archived_at,omitempty"`
	CreatedAt            Timestamp  `json:"created_at"`
	LastActivityAt       Timestamp  `json:"last_activity_at"`
	State                string     `json:"state"`
	PendingInputCount    int        `json:"pending_input_count"`
	TurnCount            int        `json:"turn_count"`
	GenerationCount      int        `json:"generation_count"`
	CurrentGenerationID  string     `json:"current_generation_id"`
	CurrentContextTokens int        `json:"current_context_tokens"`
	TokenUsage           llm.Usage  `json:"token_usage"`
	ThreadRevision       uint64     `json:"thread_revision"`
}

type Index struct {
	Version   int          `json:"v"`
	Revision  uint64       `json:"revision"`
	UpdatedAt Timestamp    `json:"updated_at"`
	Threads   []IndexEntry `json:"threads"`
}

// Info is a lightweight Thread snapshot for adapters that already hold a
// Thread handle. Agent-wide lists use IndexEntry instead.
type Info struct {
	ID             string            `json:"thread_id"`
	Alias          string            `json:"alias"`
	ParentThreadID string            `json:"parent_thread_id,omitempty"`
	Dir            string            `json:"dir"`
	CreatedAt      Timestamp         `json:"created_at"`
	LastActivityAt Timestamp         `json:"last_activity_at"`
	ArchivedAt     *Timestamp        `json:"archived_at,omitempty"`
	State          string            `json:"state"`
	Revision       uint64            `json:"revision"`
	GenerationID   string            `json:"generation_id"`
	TurnCount      int               `json:"turn_count"`
	PendingInputs  int               `json:"pending_input_count"`
	TokenUsage     llm.Usage         `json:"token_usage"`
	ContextUsage   *llm.ContextUsage `json:"context_usage,omitempty"`
}

type InputState string

const (
	InputAccepted     InputState = "accepted"
	InputRunning      InputState = "running"
	InputRetryable    InputState = "retryable"
	InputCompleted    InputState = "completed"
	InputDeadLettered InputState = "dead_lettered"
	InputCancelled    InputState = "cancelled"
	InputExpired      InputState = "expired"
)

func (state InputState) Terminal() bool {
	switch state {
	case InputCompleted, InputDeadLettered, InputCancelled, InputExpired:
		return true
	default:
		return false
	}
}

type AttemptProjection struct {
	ID           string `json:"attempt_id"`
	GenerationID string `json:"generation_id"`
	TurnID       string `json:"turn_id"`
	State        string `json:"state"`
	Error        string `json:"error,omitempty"`
}

type InputProjection struct {
	ID       string              `json:"input_id"`
	State    InputState          `json:"state"`
	Attempts []AttemptProjection `json:"attempts,omitempty"`
}

type Activity struct {
	Type             string       `json:"type"`
	At               Timestamp    `json:"at"`
	FromGenerationID string       `json:"from_generation_id,omitempty"`
	ToGenerationID   string       `json:"to_generation_id,omitempty"`
	Summary          *llm.Message `json:"summary,omitempty"`
	Automatic        bool         `json:"automatic,omitempty"`
}

type ReplayState struct {
	Projection       Projection
	Messages         []llm.Message
	ProviderMessages []llm.Message
	Events           []events.Event
	Activities       []Activity
	Inputs           map[string]*InputProjection
	InputOrder       []string
	InputRecords     map[string]json.RawMessage
	ContextUsage     *llm.ContextUsage
}

type TimelineItem struct {
	Type     string       `json:"type"`
	Seq      uint64       `json:"seq"`
	At       Timestamp    `json:"at"`
	Message  *llm.Message `json:"message,omitempty"`
	Activity *Activity    `json:"activity,omitempty"`
}

type TimelinePage struct {
	Items          []TimelineItem `json:"items"`
	HasMoreBefore  bool           `json:"has_more_before"`
	PreviousCursor string         `json:"previous_cursor,omitempty"`
}

func generationID(ordinal int) string {
	return "g" + fmt.Sprintf("%06d", ordinal)
}

func parseGenerationID(id string) (int, error) {
	if len(id) != 7 || id[0] != 'g' {
		return 0, fmt.Errorf("%w: invalid generation id %q", ErrInvalidFact, id)
	}
	ordinal, err := strconv.Atoi(id[1:])
	if err != nil || ordinal <= 0 || generationID(ordinal) != id {
		return 0, fmt.Errorf("%w: invalid generation id %q", ErrInvalidFact, id)
	}
	return ordinal, nil
}
