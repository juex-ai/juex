package llm

import (
	"fmt"
	"sort"
	"strings"
)

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
	WorkDir        string
}

func ResolveProfile(cfg Config) (ProviderProfile, error) {
	profile, err := baseProfile(cfg)
	if err != nil {
		return ProviderProfile{}, err
	}
	profile.BaseURL = firstNonEmpty(cfg.BaseURL, profile.BaseURL)
	profile.APIKey = firstNonEmpty(cfg.APIKey, profile.APIKey)
	profile.Model = firstNonEmpty(cfg.Model, profile.Model)
	profile.ThinkingEffort = firstNonEmpty(cfg.ThinkingEffort, profile.ThinkingEffort)
	profile.Headers = mergeStringMap(profile.Headers, cfg.Headers)
	profile.Query = mergeStringMap(profile.Query, cfg.Query)
	profile.WorkDir = cfg.WorkDir
	profile.Capabilities = applyCapabilityOverrides(profile.Capabilities, cfg.Capabilities)
	if len(cfg.Compat.ReasoningReplayFields) > 0 {
		profile.Compat.ReasoningReplayFields = append([]string(nil), cfg.Compat.ReasoningReplayFields...)
	}
	if cfg.Compat.CodexTransport != "" {
		transport, err := NormalizeCodexTransport(cfg.Compat.CodexTransport)
		if err != nil {
			return ProviderProfile{}, err
		}
		profile.Compat.CodexTransport = transport
	}
	if len(profile.Compat.ReasoningReplayFields) == 0 && profile.Capabilities.ReasoningReplay {
		profile.Compat.ReasoningReplayFields = []string{"reasoning_content", "reasoning", "thinking"}
	}
	return profile, nil
}

func cloneProviderProfile(p ProviderProfile) ProviderProfile {
	p.Headers = cloneStringMap(p.Headers)
	p.Query = cloneStringMap(p.Query)
	p.Compat.ReasoningReplayFields = append([]string(nil), p.Compat.ReasoningReplayFields...)
	return p
}

const (
	CodexTransportSSE             = "sse"
	CodexTransportAuto            = "auto"
	CodexTransportWebSocket       = "websocket"
	CodexTransportWebSocketCached = "websocket-cached"
)

func NormalizeCodexTransport(raw string) (string, error) {
	switch strings.TrimSpace(raw) {
	case "":
		return "", nil
	case CodexTransportSSE:
		return CodexTransportSSE, nil
	case CodexTransportAuto:
		return CodexTransportAuto, nil
	case CodexTransportWebSocket:
		return CodexTransportWebSocket, nil
	case CodexTransportWebSocketCached:
		return CodexTransportWebSocketCached, nil
	default:
		return "", fmt.Errorf("llm: unsupported codex transport %q", raw)
	}
}

func baseProfile(cfg Config) (ProviderProfile, error) {
	id := strings.TrimSpace(cfg.ID)
	rawProtocol := strings.TrimSpace(cfg.Protocol)

	if id != "" {
		profile := presetProfile(id)
		if profile.ID != "" {
			if rawProtocol != "" {
				proto, err := parseProtocol(rawProtocol)
				if err != nil {
					return ProviderProfile{}, err
				}
				if proto != profile.Protocol {
					return ProviderProfile{}, fmt.Errorf("llm: provider id %q uses fixed protocol %q; omit providers[].protocol or use a custom providers[].id", id, profile.Protocol)
				}
			}
			return profile, nil
		}
		if rawProtocol == "" {
			return ProviderProfile{}, fmt.Errorf("llm: unknown provider id %q requires providers[].protocol", id)
		}
		return customProfileForProtocol(id, rawProtocol)
	}

	if rawProtocol != "" {
		return customProfileForProtocol("custom", rawProtocol)
	}

	return ProviderProfile{}, fmt.Errorf("llm: provider id or protocol is empty")
}

func KnownProviderIDs() []string {
	ids := make([]string, 0, len(providerPresets))
	for id := range providerPresets {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

var providerPresets = map[string]ProviderProfile{
	"anthropic": {
		ID:       "anthropic",
		Protocol: ProtocolAnthropicMessages,
		Capabilities: ProviderCapabilities{
			Tools:           true,
			Streaming:       true,
			ReasoningEffort: true,
			ReasoningReplay: true,
			MaxOutputTokens: true,
		},
		Compat: CompatOptions{ReasoningReplayFields: []string{"thinking", "redacted_thinking"}},
	},
	"openai": {
		ID:       "openai",
		Protocol: ProtocolOpenAIResponses,
		Capabilities: ProviderCapabilities{
			Tools:           true,
			ReasoningEffort: true,
			ReasoningReplay: true,
			MaxOutputTokens: true,
		},
		Compat: CompatOptions{ReasoningReplayFields: []string{"reasoning_content", "reasoning", "thinking"}},
	},
	"openai-codex": {
		ID:       "openai-codex",
		Protocol: ProtocolOpenAICodexResponses,
		Capabilities: ProviderCapabilities{
			Tools:           true,
			ReasoningEffort: true,
			ReasoningReplay: true,
		},
		Compat: CompatOptions{ReasoningReplayFields: []string{"reasoning_content", "reasoning", "thinking"}},
	},
	"deepseek": {
		ID:       "deepseek",
		Protocol: ProtocolOpenAIChat,
		BaseURL:  "https://api.deepseek.com",
		Capabilities: ProviderCapabilities{
			Tools:           true,
			ReasoningEffort: true,
			ReasoningReplay: true,
			MaxOutputTokens: true,
		},
		Compat: CompatOptions{ReasoningReplayFields: []string{"reasoning_content"}},
	},
}

func customProfileForProtocol(id, rawProtocol string) (ProviderProfile, error) {
	proto, err := parseProtocol(rawProtocol)
	if err != nil {
		return ProviderProfile{}, err
	}
	switch proto {
	case ProtocolAnthropicMessages:
		return ProviderProfile{
			ID:       id,
			Protocol: proto,
			Capabilities: ProviderCapabilities{
				Tools:           true,
				Streaming:       true,
				ReasoningEffort: true,
				ReasoningReplay: true,
				MaxOutputTokens: true,
			},
			Compat: CompatOptions{ReasoningReplayFields: []string{"thinking", "redacted_thinking"}},
		}, nil
	case ProtocolOpenAIResponses:
		return ProviderProfile{
			ID:       id,
			Protocol: proto,
			Capabilities: ProviderCapabilities{
				Tools:           true,
				ReasoningEffort: true,
				ReasoningReplay: true,
				MaxOutputTokens: true,
			},
			Compat: CompatOptions{ReasoningReplayFields: []string{"reasoning_content", "reasoning", "thinking"}},
		}, nil
	case ProtocolOpenAIChat:
		return customOpenAIChatProfile(id, proto), nil
	case ProtocolOpenAICodexResponses:
		return ProviderProfile{}, fmt.Errorf("llm: protocol %q is reserved for provider id %q", proto, "openai-codex")
	default:
		return ProviderProfile{}, fmt.Errorf("llm: unsupported provider protocol %q", proto)
	}
}

func customOpenAIChatProfile(id string, proto Protocol) ProviderProfile {
	return ProviderProfile{
		ID:       id,
		Protocol: proto,
		Capabilities: ProviderCapabilities{
			Tools:           true,
			ReasoningEffort: true,
			ReasoningReplay: true,
			MaxOutputTokens: true,
		},
		Compat: CompatOptions{ReasoningReplayFields: []string{"reasoning_content", "reasoning", "thinking"}},
	}
}

func presetProfile(id string) ProviderProfile {
	if p, ok := providerPresets[id]; ok {
		p.Headers = cloneStringMap(p.Headers)
		p.Query = cloneStringMap(p.Query)
		p.Compat.ReasoningReplayFields = append([]string(nil), p.Compat.ReasoningReplayFields...)
		return p
	}
	return ProviderProfile{}
}

func parseProtocol(in string) (Protocol, error) {
	switch Protocol(in) {
	case ProtocolAnthropicMessages, ProtocolOpenAIResponses, ProtocolOpenAICodexResponses, ProtocolOpenAIChat:
		return Protocol(in), nil
	default:
		return "", fmt.Errorf("llm: unknown provider protocol %q", in)
	}
}

func applyCapabilityOverrides(c ProviderCapabilities, o CapabilityOverrides) ProviderCapabilities {
	if o.Tools != nil {
		c.Tools = *o.Tools
	}
	if o.Vision != nil {
		c.Vision = *o.Vision
	}
	if o.Streaming != nil {
		c.Streaming = *o.Streaming
	}
	if o.ReasoningEffort != nil {
		c.ReasoningEffort = *o.ReasoningEffort
	}
	if o.ReasoningReplay != nil {
		c.ReasoningReplay = *o.ReasoningReplay
	}
	if o.MaxOutputTokens != nil {
		c.MaxOutputTokens = *o.MaxOutputTokens
	}
	return c
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func mergeStringMap(base, override map[string]string) map[string]string {
	out := cloneStringMap(base)
	for k, v := range override {
		if v == "" {
			delete(out, k)
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[k] = v
	}
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func boolPtr(v bool) *bool {
	return &v
}
