package runtime

import "github.com/juex-ai/juex/internal/llm"

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
	Error string `json:"error"`
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

type LLMRequestedPayload struct {
	Iter       int `json:"iter"`
	HistoryLen int `json:"history_len"`
	ToolCount  int `json:"tool_count"`
}

type LLMRespondedPayload struct {
	Iter         int               `json:"iter,omitempty"`
	StopReason   llm.StopReason    `json:"stop_reason"`
	Usage        llm.Usage         `json:"usage"`
	TokenUsage   llm.Usage         `json:"token_usage"`
	Blocks       []llm.Block       `json:"blocks"`
	Text         string            `json:"text"`
	Thinking     string            `json:"thinking"`
	ToolCalls    []ToolCallPayload `json:"tool_calls"`
	Model        string            `json:"model"`
	ContextUsage *llm.ContextUsage `json:"context_usage,omitempty"`
}

type FinishAttemptedPayload struct {
	StopReason llm.StopReason `json:"stop_reason"`
	OutputLen  int            `json:"output_len"`
}

type ToolCallPayload struct {
	ToolUseID      string         `json:"tool_use_id"`
	Name           string         `json:"name"`
	Input          map[string]any `json:"input"`
	TimeoutSeconds int            `json:"timeout_seconds"`
}

type ToolRequestedPayload struct {
	Name           string         `json:"name"`
	Input          map[string]any `json:"input"`
	ToolUseID      string         `json:"tool_use_id"`
	TimeoutSeconds int            `json:"timeout_seconds"`
}

type ToolCompletedPayload struct {
	Name           string `json:"name"`
	ToolUseID      string `json:"tool_use_id"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	Len            int    `json:"len"`
	Preview        string `json:"preview"`
}

type ToolOutputDeltaPayload struct {
	Name      string `json:"name"`
	ToolUseID string `json:"tool_use_id"`
	SessionID string `json:"session_id"`
	ChunkID   int    `json:"chunk_id"`
	Stream    string `json:"stream"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated,omitempty"`
}

type ToolErroredPayload struct {
	Name           string `json:"name"`
	ToolUseID      string `json:"tool_use_id"`
	Error          string `json:"error"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	Len            int    `json:"len,omitempty"`
	Preview        string `json:"preview,omitempty"`
	TimedOut       bool   `json:"timed_out,omitempty"`
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
