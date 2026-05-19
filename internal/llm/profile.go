package llm

import (
	"fmt"
	"sort"
)

type Protocol string

const (
	ProtocolAnthropicMessages    Protocol = "anthropic/messages"
	ProtocolOpenAIResponses      Protocol = "openai/responses"
	ProtocolOpenAIChat           Protocol = "openai/chat"
	ProtocolOpenAICompatibleChat Protocol = "openai-compatible/chat"
)

type ProviderCapabilities struct {
	Tools           bool `json:"tools"`
	Streaming       bool `json:"streaming"`
	ReasoningEffort bool `json:"reasoning_effort"`
	ReasoningReplay bool `json:"reasoning_replay"`
	MaxOutputTokens bool `json:"max_output_tokens"`
}

type CapabilityOverrides struct {
	Tools           *bool
	Streaming       *bool
	ReasoningEffort *bool
	ReasoningReplay *bool
	MaxOutputTokens *bool
}

type CompatOptions struct {
	ReasoningReplayFields []string
}

type ProviderProfile struct {
	ID             string
	Type           string
	Protocol       Protocol
	BaseURL        string
	APIKey         string
	Model          string
	ThinkingEffort string
	Headers        map[string]string
	Query          map[string]string
	Capabilities   ProviderCapabilities
	Compat         CompatOptions
}

func (p ProviderProfile) Config() Config {
	return Config{
		Type:           p.Type,
		ID:             p.ID,
		Protocol:       string(p.Protocol),
		BaseURL:        p.BaseURL,
		APIKey:         p.APIKey,
		Model:          p.Model,
		ThinkingEffort: p.ThinkingEffort,
		Headers:        cloneStringMap(p.Headers),
		Query:          cloneStringMap(p.Query),
		Capabilities: CapabilityOverrides{
			Tools:           boolPtr(p.Capabilities.Tools),
			Streaming:       boolPtr(p.Capabilities.Streaming),
			ReasoningEffort: boolPtr(p.Capabilities.ReasoningEffort),
			ReasoningReplay: boolPtr(p.Capabilities.ReasoningReplay),
			MaxOutputTokens: boolPtr(p.Capabilities.MaxOutputTokens),
		},
		Compat: CompatOptions{
			ReasoningReplayFields: append([]string(nil), p.Compat.ReasoningReplayFields...),
		},
	}
}

func ResolveProfile(cfg Config) (ProviderProfile, error) {
	id := firstNonEmpty(cfg.ID, cfg.Type, "custom")
	profile := presetProfile(id)
	if profile.ID == "" {
		profile = customProfile(id)
	}
	if cfg.Type != "" {
		if cfg.Protocol == "" && cfg.ID == "" && cfg.Type != "anthropic" && cfg.Type != "openai" {
			return ProviderProfile{}, fmt.Errorf("llm: unknown provider type %q", cfg.Type)
		}
		profile.Type = cfg.Type
	}
	if cfg.Protocol != "" {
		proto, err := parseProtocol(cfg.Protocol)
		if err != nil {
			return ProviderProfile{}, err
		}
		profile.Protocol = proto
		profile.Type = typeForProtocol(proto)
	} else if profile.Protocol == "" {
		profile.Protocol = protocolForType(profile.Type)
	}
	if profile.Type == "" {
		profile.Type = typeForProtocol(profile.Protocol)
	}
	if cfg.ID != "" {
		profile.ID = cfg.ID
	}
	if cfg.BaseURL != "" {
		profile.BaseURL = cfg.BaseURL
	}
	if cfg.APIKey != "" {
		profile.APIKey = cfg.APIKey
	}
	if cfg.Model != "" {
		profile.Model = cfg.Model
	}
	if cfg.ThinkingEffort != "" {
		profile.ThinkingEffort = cfg.ThinkingEffort
	}
	profile.Headers = mergeStringMap(profile.Headers, cfg.Headers)
	profile.Query = mergeStringMap(profile.Query, cfg.Query)
	profile.Capabilities = applyCapabilityOverrides(profile.Capabilities, cfg.Capabilities)
	if len(cfg.Compat.ReasoningReplayFields) > 0 {
		profile.Compat.ReasoningReplayFields = append([]string(nil), cfg.Compat.ReasoningReplayFields...)
	}
	if len(profile.Compat.ReasoningReplayFields) == 0 && profile.Capabilities.ReasoningReplay {
		profile.Compat.ReasoningReplayFields = []string{"reasoning_content", "reasoning", "thinking"}
	}
	return profile, nil
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
		Type:     "anthropic",
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
		Type:     "openai",
		Protocol: ProtocolOpenAIChat,
		Capabilities: ProviderCapabilities{
			Tools:           true,
			ReasoningEffort: true,
			ReasoningReplay: true,
			MaxOutputTokens: true,
		},
		Compat: CompatOptions{ReasoningReplayFields: []string{"reasoning_content", "reasoning", "thinking"}},
	},
	"openai-compatible": openAICompatiblePreset("openai-compatible"),
	"openrouter":        openAICompatiblePreset("openrouter"),
	"deepseek":          openAICompatiblePreset("deepseek"),
	"qwen":              openAICompatiblePreset("qwen"),
	"dashscope":         openAICompatiblePreset("dashscope"),
	"moonshot":          openAICompatiblePreset("moonshot"),
	"kimi":              openAICompatiblePreset("kimi"),
	"minimax": {
		ID:       "minimax",
		Type:     "anthropic",
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
	"volcengine": openAICompatiblePreset("volcengine"),
	"ark":        openAICompatiblePreset("ark"),
}

func openAICompatiblePreset(id string) ProviderProfile {
	return ProviderProfile{
		ID:       id,
		Type:     "openai",
		Protocol: ProtocolOpenAICompatibleChat,
		Capabilities: ProviderCapabilities{
			Tools:           true,
			ReasoningReplay: true,
			MaxOutputTokens: true,
		},
		Compat: CompatOptions{ReasoningReplayFields: []string{"reasoning_content", "reasoning", "thinking"}},
	}
}

func customProfile(id string) ProviderProfile {
	return ProviderProfile{
		ID:       id,
		Type:     "openai",
		Protocol: protocolForType(id),
		Capabilities: ProviderCapabilities{
			Tools:           true,
			MaxOutputTokens: true,
		},
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
	case ProtocolAnthropicMessages, ProtocolOpenAIResponses, ProtocolOpenAIChat, ProtocolOpenAICompatibleChat:
		return Protocol(in), nil
	default:
		return "", fmt.Errorf("llm: unknown provider protocol %q", in)
	}
}

func protocolForType(typ string) Protocol {
	switch typ {
	case "anthropic":
		return ProtocolAnthropicMessages
	case "openai":
		return ProtocolOpenAIChat
	default:
		return ProtocolOpenAICompatibleChat
	}
}

func typeForProtocol(proto Protocol) string {
	switch proto {
	case ProtocolAnthropicMessages:
		return "anthropic"
	case ProtocolOpenAIResponses, ProtocolOpenAIChat, ProtocolOpenAICompatibleChat:
		return "openai"
	default:
		return ""
	}
}

func applyCapabilityOverrides(c ProviderCapabilities, o CapabilityOverrides) ProviderCapabilities {
	if o.Tools != nil {
		c.Tools = *o.Tools
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
