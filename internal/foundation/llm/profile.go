package llm

type Protocol string

const (
	ProtocolAnthropicMessages    Protocol = "anthropic/messages"
	ProtocolOpenAIResponses      Protocol = "openai/responses"
	ProtocolOpenAICodexResponses Protocol = "openai-codex/responses"
	ProtocolOpenAIChat           Protocol = "openai/chat"
)

type ProviderCapabilities struct {
	Tools           bool `json:"tools"`
	Vision          bool `json:"vision"`
	Streaming       bool `json:"streaming"`
	ReasoningEffort bool `json:"reasoning_effort"`
	ReasoningReplay bool `json:"reasoning_replay"`
	MaxOutputTokens bool `json:"max_output_tokens"`
}

type CapabilityOverrides struct {
	Tools           *bool
	Vision          *bool
	Streaming       *bool
	ReasoningEffort *bool
	ReasoningReplay *bool
	MaxOutputTokens *bool
}

type CompatOptions struct {
	ReasoningReplayFields []string
	CodexTransport        string
}

type ProviderProfile struct {
	ID             string
	Protocol       Protocol
	BaseURL        string
	APIKey         string
	Model          string
	ThinkingEffort string
	Headers        map[string]string
	Query          map[string]string
	Capabilities   ProviderCapabilities
	Compat         CompatOptions
	MediaDir       string
}
