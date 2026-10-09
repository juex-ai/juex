// Package legacy reads the fixed 281889e5 on-disk format without opening the
// recovery-capable legacy stores. It is only used by the offline migration.
package legacy

import (
	"encoding/json"
	"time"

	"github.com/juex-ai/juex/internal/foundation/llm"
)

type Generation struct {
	ID          string `json:"generation_id"`
	Ordinal     int    `json:"ordinal"`
	BoundarySeq uint64 `json:"boundary_seq"`
}

type Cursor struct {
	GenerationID string `json:"generation_id"`
	Seq          uint64 `json:"seq"`
	Offset       int64  `json:"offset"`
}

type ThreadMetadata struct {
	Version                int                `json:"v"`
	ThreadID               string             `json:"thread_id"`
	Alias                  string             `json:"alias"`
	ParentThreadID         string             `json:"parent_thread_id,omitempty"`
	CreatedAt              string             `json:"created_at"`
	UpdatedAt              string             `json:"updated_at"`
	ArchivedAt             *string            `json:"archived_at,omitempty"`
	RetentionState         string             `json:"retention_state"`
	ExecutionState         string             `json:"execution_state,omitempty"`
	Revision               uint64             `json:"revision"`
	CurrentGeneration      Generation         `json:"current_generation"`
	Generations            []Generation       `json:"generations"`
	Counts                 Counts             `json:"counts"`
	TokenUsage             UsageAggregate     `json:"token_usage"`
	ContextUsage           *ContextProjection `json:"context_usage,omitempty"`
	LastActivityAt         string             `json:"last_activity_at"`
	EventCursor            Cursor             `json:"event_cursor"`
	UsageAggregatedThrough Cursor             `json:"usage_aggregated_through"`
}

type UsageAggregate struct {
	Total   llm.Usage            `json:"total"`
	ByModel map[string]llm.Usage `json:"by_model"`
}

type ContextProjection struct {
	ContextWindow int     `json:"context_window"`
	CurrentTokens int     `json:"current_tokens"`
	Percentage    float64 `json:"percentage"`
	CalibratedAt  *string `json:"calibrated_at,omitempty"`
}

type Counts struct {
	GenerationCount   int `json:"generation_count"`
	TurnCount         int `json:"turn_count"`
	PendingInputCount int `json:"pending_input_count"`
}

type Commit struct {
	Version      int    `json:"v"`
	Seq          uint64 `json:"seq"`
	At           string `json:"at"`
	Facts        []Fact `json:"facts"`
	GenerationID string `json:"-"`
	EndOffset    int64  `json:"-"`
}

type Fact struct {
	Type             string            `json:"type"`
	ThreadID         string            `json:"thread_id,omitempty"`
	Alias            string            `json:"alias,omitempty"`
	ParentThreadID   string            `json:"parent_thread_id,omitempty"`
	GenerationID     string            `json:"generation_id,omitempty"`
	FromGenerationID string            `json:"from_generation_id,omitempty"`
	ToGenerationID   string            `json:"to_generation_id,omitempty"`
	TurnID           string            `json:"turn_id,omitempty"`
	Message          *llm.Message      `json:"message,omitempty"`
	Event            json.RawMessage   `json:"event,omitempty"`
	Summary          *llm.Message      `json:"summary,omitempty"`
	Automatic        bool              `json:"automatic,omitempty"`
	Error            string            `json:"error,omitempty"`
	ModelRef         string            `json:"model_ref,omitempty"`
	Usage            *llm.Usage        `json:"usage,omitempty"`
	ContextUsage     *llm.ContextUsage `json:"context_usage,omitempty"`
	Seed             *GenerationSeed   `json:"seed,omitempty"`
}

type GenerationSeed struct {
	Version          int               `json:"v"`
	ContextScopeID   string            `json:"context_scope_id"`
	ProviderMessages []llm.Message     `json:"provider_messages,omitempty"`
	RecoveryEvents   []json.RawMessage `json:"recovery_events,omitempty"`
	CompactionCount  int               `json:"compaction_count,omitempty"`
	ContextUsage     *llm.ContextUsage `json:"context_usage,omitempty"`
}

type Input struct {
	ScopeID       string       `json:"scope_id,omitempty"`
	CheckedAt     *time.Time   `json:"checked_at,omitempty"`
	ModelMessage  *llm.Message `json:"model_message,omitempty"`
	ID            string       `json:"id"`
	TurnID        string       `json:"turn_id,omitempty"`
	MessageID     string       `json:"message_id"`
	Message       llm.Message  `json:"message"`
	Summary       string       `json:"summary,omitempty"`
	Origin        string       `json:"origin,omitempty"`
	State         string       `json:"state"`
	CreatedAt     time.Time    `json:"created_at"`
	ExpiresAt     time.Time    `json:"expires_at,omitempty"`
	Attempts      int          `json:"attempts"`
	ProcessedAt   *time.Time   `json:"processed_at,omitempty"`
	LastError     string       `json:"last_error,omitempty"`
	LastErrorKind string       `json:"last_error_kind,omitempty"`
}

type SourceFile struct {
	ModifiedAt time.Time `json:"modified_at,omitempty"`
	Path       string    `json:"path"`
	SHA256     string    `json:"sha256"`
	Size       int64     `json:"size"`
	Mode       uint32    `json:"mode"`
	// Data retains the exact original bytes for the private migration archive.
	Data []byte `json:"-"`
}

type Thread struct {
	Metadata       ThreadMetadata
	Commits        []Commit
	Context        []llm.Message
	ContextScopeID string
	Inputs         []Input
	Files          []SourceFile
	AbsentFiles    []string
	executionState string
	turnCount      int
}
