// Package llm contains the canonical message representation used inside the
// runtime, plus the Provider interface implemented by each backend.
//
// Provider implementations translate between this canonical form and whatever
// the backend wire format requires. Higher layers (turn loop, prompt builder,
// session) only ever see the types defined here.
package llm

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
)

type BlockType string

const (
	BlockText       BlockType = "text"
	BlockToolUse    BlockType = "tool_use"
	BlockToolResult BlockType = "tool_result"
	// BlockReasoning carries provider-specific reasoning/thinking content
	// that some providers (e.g. DeepSeek's thinking models) require to be
	// echoed back on the next call.
	BlockReasoning BlockType = "reasoning"
)

const (
	// MessageKindMCPEvent marks user-visible MCP notification turns.
	MessageKindMCPEvent = "mcp_event"
	// MessageKindObservation marks user-visible Observable observations.
	MessageKindObservation = "observation"
	// MessageKindHookEvent marks user-visible command hook traces. These are
	// UI-only runtime diagnostics and must not be sent back to providers.
	MessageKindHookEvent = "hook_event"
	// MessageKindCompact marks an automatic context compaction summary. The
	// persisted transcript keeps the original messages; provider calls only
	// include the latest compact summary plus later messages.
	MessageKindCompact = "compact"
)

type Block struct {
	Type      BlockType      `json:"type"`
	Text      string         `json:"text,omitempty"`
	ToolUseID string         `json:"tool_use_id,omitempty"`
	ToolName  string         `json:"tool_name,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
	// TimeoutSeconds records the runtime timeout applied to a tool_use block.
	// Provider adapters ignore it when replaying history; UI surfaces use it
	// to explain how long an in-flight tool may run.
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	Content        string `json:"content,omitempty"`
	IsError        bool   `json:"is_error,omitempty"`
	// Signature is provider-specific opaque metadata that some providers
	// require alongside reasoning content (Anthropic thinking blocks ship a
	// signature that must be echoed back unchanged).
	Signature string `json:"signature,omitempty"`
	// Redacted, if non-empty, marks the block as a provider-redacted
	// variant whose Content is the encrypted payload to round-trip.
	Redacted bool `json:"redacted,omitempty"`
	// Artifact records full content that was moved out of provider context
	// while preserving a stable provider-visible preview in Text or Content.
	Artifact *ContextArtifactProjection `json:"artifact,omitempty"`
}

type ContextArtifactProjection struct {
	SourceKind    string `json:"source_kind"`
	MessageID     string `json:"message_id,omitempty"`
	ToolUseID     string `json:"tool_use_id,omitempty"`
	ToolName      string `json:"tool_name,omitempty"`
	OriginalBytes int    `json:"original_bytes"`
	StoredPath    string `json:"stored_path"`
	SHA256        string `json:"sha256"`
	HeadBytes     int    `json:"head_bytes"`
	TailBytes     int    `json:"tail_bytes"`
	Truncated     bool   `json:"truncated"`
}

type Message struct {
	ID     string  `json:"id,omitempty"`
	Role   Role    `json:"role"`
	Blocks []Block `json:"blocks"`
	// Kind marks app-level message categories that still travel through
	// providers as ordinary role/block messages. Empty means normal chat.
	Kind string `json:"kind,omitempty"`
	// Model is the provider:model name responsible for producing this
	// message. Only set on assistant messages (provider-stamped at
	// generation time so resuming a session under a different config
	// preserves the original attribution).
	Model      string              `json:"model,omitempty"`
	Compaction *CompactionMetadata `json:"compaction,omitempty"`
}

type CompactionMetadata struct {
	Auto               bool   `json:"auto"`
	Reason             string `json:"reason"`
	PreviousSummaryID  string `json:"previous_summary_id,omitempty"`
	FirstKeptMessageID string `json:"first_kept_message_id,omitempty"`
	TailStartMessageID string `json:"tail_start_message_id,omitempty"`
	TokensBefore       int    `json:"tokens_before"`
	TokensAfter        int    `json:"tokens_after"`
	SummaryChars       int    `json:"summary_chars"`
	SummaryModel       string `json:"summary_model,omitempty"`
}

// TextMessage is a convenience constructor for a single-text-block message.
func TextMessage(role Role, text string) Message {
	return Message{Role: role, Blocks: []Block{{Type: BlockText, Text: text}}}
}

// FirstText returns the first text block in the message, or "".
func (m Message) FirstText() string {
	for _, b := range m.Blocks {
		if b.Type == BlockText {
			return b.Text
		}
	}
	return ""
}

// ToolCalls returns all tool_use blocks.
func (m Message) ToolCalls() []Block {
	var out []Block
	for _, b := range m.Blocks {
		if b.Type == BlockToolUse {
			out = append(out, b)
		}
	}
	return out
}

type ToolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Schema      map[string]any `json:"schema"`
}

type Usage struct {
	InputTokens       int `json:"input_tokens"`
	OutputTokens      int `json:"output_tokens"`
	CachedInputTokens int `json:"cached_input_tokens,omitempty"`
}

type ContextUsagePart struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Tokens int    `json:"tokens"`
}

type ContextUsage struct {
	Model             string             `json:"model,omitempty"`
	ContextWindow     int                `json:"context_window,omitempty"`
	InputTokens       int                `json:"input_tokens"`
	OutputTokens      int                `json:"output_tokens"`
	CachedInputTokens int                `json:"cached_input_tokens,omitempty"`
	TotalTokens       int                `json:"total_tokens"`
	Breakdown         []ContextUsagePart `json:"breakdown,omitempty"`
}

func (u Usage) TotalTokens() int {
	return u.InputTokens + u.OutputTokens
}

func (u Usage) IsZero() bool {
	return u.InputTokens == 0 && u.OutputTokens == 0
}

func (u *Usage) Add(v Usage) {
	if u == nil {
		return
	}
	u.InputTokens += v.InputTokens
	u.OutputTokens += v.OutputTokens
	u.CachedInputTokens += v.CachedInputTokens
}

type StopReason string

const (
	StopEndTurn   StopReason = "end_turn"
	StopToolUse   StopReason = "tool_use"
	StopMaxTokens StopReason = "max_tokens"
	StopOther     StopReason = "other"
)

type Response struct {
	Message    Message    `json:"message"`
	StopReason StopReason `json:"stop_reason"`
	Usage      Usage      `json:"usage"`
}
