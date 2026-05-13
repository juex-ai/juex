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

type Block struct {
	Type      BlockType      `json:"type"`
	Text      string         `json:"text,omitempty"`
	ToolUseID string         `json:"tool_use_id,omitempty"`
	ToolName  string         `json:"tool_name,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
	Content   string         `json:"content,omitempty"`
	IsError   bool           `json:"is_error,omitempty"`
	// Signature is provider-specific opaque metadata that some providers
	// require alongside reasoning content (Anthropic thinking blocks ship a
	// signature that must be echoed back unchanged).
	Signature string `json:"signature,omitempty"`
	// Redacted, if non-empty, marks the block as a provider-redacted
	// variant whose Content is the encrypted payload to round-trip.
	Redacted bool `json:"redacted,omitempty"`
}

type Message struct {
	Role   Role    `json:"role"`
	Blocks []Block `json:"blocks"`
	// Model is the provider:model name responsible for producing this
	// message. Only set on assistant messages (provider-stamped at
	// generation time so resuming a session under a different config
	// preserves the original attribution).
	Model string `json:"model,omitempty"`
	// Usage is provider-reported token usage for this assistant message.
	// It is nil for user messages and older transcripts without usage data.
	Usage *Usage `json:"usage,omitempty"`
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
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
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
}

func SumUsage(messages []Message) Usage {
	var total Usage
	for _, msg := range messages {
		if msg.Usage != nil {
			total.Add(*msg.Usage)
		}
	}
	return total
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
