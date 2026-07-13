package runtime

import (
	"time"

	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/toolevents"
)

type TurnStartedPayload struct {
	Input string `json:"input"`
	Kind  string `json:"kind,omitempty"`
}

type TurnCompletedPayload struct {
	DurationMS int64     `json:"duration_ms"`
	OutputLen  int       `json:"output_len"`
	TokenUsage llm.Usage `json:"token_usage"`
}

type TurnErroredPayload struct {
	Error        string `json:"error"`
	ErrorKind    string `json:"error_kind,omitempty"`
	TimedOut     bool   `json:"timed_out,omitempty"`
	RawCause     string `json:"raw_cause,omitempty"`
	Signal       string `json:"signal,omitempty"`
	SignalNumber int    `json:"signal_number,omitempty"`
	Interrupted  bool   `json:"interrupted,omitempty"`
}

type HookStartedPayload struct {
	Name      string `json:"name"`
	Source    string `json:"source,omitempty"`
	EventName string `json:"event_name"`
	ToolName  string `json:"tool_name,omitempty"`
}

type HookCompletedPayload struct {
	Name                 string `json:"name"`
	Source               string `json:"source,omitempty"`
	EventName            string `json:"event_name"`
	ToolName             string `json:"tool_name,omitempty"`
	DurationMS           int64  `json:"duration_ms"`
	Decision             string `json:"decision,omitempty"`
	AdditionalContextLen int    `json:"additional_context_len,omitempty"`
	BlockStop            bool   `json:"block_stop,omitempty"`
	ContinuePromptLen    int    `json:"continue_prompt_len,omitempty"`
	StdoutLen            int    `json:"stdout_len,omitempty"`
	StderrLen            int    `json:"stderr_len,omitempty"`
	StdoutPreview        string `json:"stdout_preview,omitempty"`
	StderrPreview        string `json:"stderr_preview,omitempty"`
}

type HookErroredPayload struct {
	Name          string `json:"name"`
	Source        string `json:"source,omitempty"`
	EventName     string `json:"event_name"`
	ToolName      string `json:"tool_name,omitempty"`
	DurationMS    int64  `json:"duration_ms"`
	Error         string `json:"error"`
	StdoutLen     int    `json:"stdout_len,omitempty"`
	StderrLen     int    `json:"stderr_len,omitempty"`
	StdoutPreview string `json:"stdout_preview,omitempty"`
	StderrPreview string `json:"stderr_preview,omitempty"`
}

type HookTracePayload struct {
	Text string `json:"text"`
}

type LLMRequestedPayload struct {
	Iter       int `json:"iter"`
	HistoryLen int `json:"history_len"`
	ToolCount  int `json:"tool_count"`
}

type LLMRespondedPayload struct {
	Iter         int                          `json:"iter,omitempty"`
	StopReason   llm.StopReason               `json:"stop_reason"`
	Usage        llm.Usage                    `json:"usage"`
	TokenUsage   llm.Usage                    `json:"token_usage"`
	Blocks       []llm.Block                  `json:"blocks"`
	Text         string                       `json:"text"`
	Thinking     string                       `json:"thinking"`
	ToolCalls    []toolevents.ToolCallPayload `json:"tool_calls"`
	Model        string                       `json:"model"`
	ContextUsage *llm.ContextUsage            `json:"context_usage,omitempty"`
}

type LLMOutputDeltaPayload struct {
	Iter  int    `json:"iter"`
	Model string `json:"model,omitempty"`
	Kind  string `json:"kind"`
	Index int    `json:"index"`
	Text  string `json:"text"`
}

type LLMRetryPayload struct {
	llm.ProviderRetryDiagnostic
	Purpose string `json:"purpose,omitempty"`
	Iter    *int   `json:"iter,omitempty"`
}

type FinishAttemptedPayload struct {
	StopReason llm.StopReason `json:"stop_reason"`
	OutputLen  int            `json:"output_len"`
}

type ToolFailureClassification string

const (
	ToolFailureRecoverable            ToolFailureClassification = "recoverable"
	ToolFailureExternalBlocked        ToolFailureClassification = "external_blocked"
	ToolFailureRuntimeFatal           ToolFailureClassification = "runtime_fatal"
	ToolFailureRepeatedStuck          ToolFailureClassification = "repeated_stuck"
	ToolFailureNonblockingExploratory ToolFailureClassification = "nonblocking_exploratory"
)

type ToolFailureStatus string

const (
	ToolFailureStatusUnresolved ToolFailureStatus = "unresolved"
	ToolFailureStatusResolved   ToolFailureStatus = "resolved"
	ToolFailureStatusStale      ToolFailureStatus = "stale"
	ToolFailureStatusSuperseded ToolFailureStatus = "superseded"
)

type ToolFailureRecordedPayload struct {
	Fingerprint     string                    `json:"fingerprint"`
	Name            string                    `json:"name"`
	ToolUseID       string                    `json:"tool_use_id"`
	Classification  ToolFailureClassification `json:"classification"`
	Status          ToolFailureStatus         `json:"status"`
	Blocking        bool                      `json:"blocking"`
	Occurrences     int                       `json:"occurrences"`
	Error           string                    `json:"error,omitempty"`
	ExitCode        *int                      `json:"exit_code,omitempty"`
	OutputLen       int                       `json:"output_len,omitempty"`
	OutputPreview   string                    `json:"output_preview,omitempty"`
	RelatedPaths    []string                  `json:"related_paths,omitempty"`
	LatestModUnixMS int64                     `json:"latest_mod_unix_ms,omitempty"`
}

type ToolFailureResolvedPayload struct {
	Fingerprint   string            `json:"fingerprint"`
	Name          string            `json:"name"`
	ToolUseID     string            `json:"tool_use_id"`
	Status        ToolFailureStatus `json:"status"`
	Reason        string            `json:"reason"`
	ResolverName  string            `json:"resolver_name"`
	ResolverUseID string            `json:"resolver_tool_use_id"`
}

type ToolFailureStalePayload struct {
	Fingerprint     string            `json:"fingerprint"`
	Name            string            `json:"name"`
	ToolUseID       string            `json:"tool_use_id"`
	Status          ToolFailureStatus `json:"status"`
	Reason          string            `json:"reason"`
	ResolverName    string            `json:"resolver_name"`
	ResolverUseID   string            `json:"resolver_tool_use_id"`
	RelatedPaths    []string          `json:"related_paths,omitempty"`
	LatestModUnixMS int64             `json:"latest_mod_unix_ms,omitempty"`
}

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

type GoalContinuedPayload struct {
	Status                GoalStatus `json:"status"`
	Reason                string     `json:"reason,omitempty"`
	ContinuationCount     int        `json:"continuation_count"`
	ContinuationPromptLen int        `json:"continuation_prompt_len"`
}

type PendingInputQueuedPayload struct {
	Input            string `json:"input"`
	Kind             string `json:"kind"`
	PendingCount     int    `json:"pending_count"`
	MaxPendingInputs int    `json:"max_pending_inputs"`
}

type PendingInputDrainedPayload struct {
	Count            int `json:"count"`
	PendingCount     int `json:"pending_count"`
	MaxPendingInputs int `json:"max_pending_inputs"`
}

type PendingInputDroppedPayload struct {
	Count            int `json:"count"`
	PendingCount     int `json:"pending_count"`
	MaxPendingInputs int `json:"max_pending_inputs"`
}

type PendingInputRejectedPayload struct {
	Input            string `json:"input"`
	Kind             string `json:"kind"`
	PendingCount     int    `json:"pending_count"`
	MaxPendingInputs int    `json:"max_pending_inputs"`
	Reason           string `json:"reason"`
}

type ContextCompactSkippedPayload struct {
	Reason              string `json:"reason"`
	Auto                bool   `json:"auto"`
	ConsecutiveFailures int    `json:"consecutive_failures"`
	MaxAutoFailures     int    `json:"max_auto_failures"`
	Error               string `json:"error"`
}

type ContextCompactStartedPayload struct {
	Reason           string `json:"reason"`
	Auto             bool   `json:"auto"`
	EstimatedTokens  int    `json:"estimated_tokens"`
	TokensBefore     int    `json:"tokens_before"`
	ContextWindow    int    `json:"context_window"`
	ReserveTokens    int    `json:"reserve_tokens"`
	KeepRecentTokens int    `json:"keep_recent_tokens"`
	TailTurns        int    `json:"tail_turns"`
}

type ContextCompactErroredPayload struct {
	Reason string `json:"reason"`
	Auto   bool   `json:"auto"`
	Error  string `json:"error"`
}

type ContextCompactSummaryFallbackPayload struct {
	ConfiguredModel string `json:"configured_model,omitempty"`
	FallbackModel   string `json:"fallback_model,omitempty"`
	Error           string `json:"error"`
}

type ContextCompactSummaryRetryPayload struct {
	Attempt                 int            `json:"attempt"`
	Reason                  string         `json:"reason"`
	StopReason              llm.StopReason `json:"stop_reason,omitempty"`
	ReasoningOnly           bool           `json:"reasoning_only,omitempty"`
	PreviousMaxOutputTokens int            `json:"previous_max_output_tokens"`
	MaxOutputTokens         int            `json:"max_output_tokens"`
}

type ContextCompactCompletedPayload struct {
	MessageID          string `json:"message_id"`
	Reason             string `json:"reason"`
	Auto               bool   `json:"auto"`
	EstimatedTokens    int    `json:"estimated_tokens"`
	TokensBefore       int    `json:"tokens_before"`
	TokensAfter        int    `json:"tokens_after"`
	SummaryChars       int    `json:"summary_chars"`
	SummaryModel       string `json:"summary_model"`
	TailStartMessageID string `json:"tail_start_message_id"`
	ContextWindow      int    `json:"context_window"`
	ReserveTokens      int    `json:"reserve_tokens"`
	KeepRecentTokens   int    `json:"keep_recent_tokens"`
}
