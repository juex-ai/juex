package profile

import (
	"fmt"
	"sort"
	"strings"

	"github.com/juex-ai/juex/internal/foundation/llm"
)

func ResolveProfile(cfg Config) (llm.ProviderProfile, error) {
	profile, err := baseProfile(cfg)
	if err != nil {
		return llm.ProviderProfile{}, err
	}
	profile.BaseURL = firstProfileValue(cfg.BaseURL, profile.BaseURL)
	profile.APIKey = firstProfileValue(cfg.APIKey, profile.APIKey)
	profile.Model = firstProfileValue(cfg.Model, profile.Model)
	profile.ThinkingEffort = firstProfileValue(cfg.ThinkingEffort, profile.ThinkingEffort)
	profile.Headers = mergeStringMap(profile.Headers, cfg.Headers)
	profile.Query = mergeStringMap(profile.Query, cfg.Query)
	profile.MediaDir = cfg.MediaDir
	profile.Capabilities = applyCapabilityOverrides(profile.Capabilities, cfg.Capabilities)
	if len(cfg.Compat.ReasoningReplayFields) > 0 {
		profile.Compat.ReasoningReplayFields = append([]string(nil), cfg.Compat.ReasoningReplayFields...)
	}
	if cfg.Compat.CodexTransport != "" {
		transport, err := NormalizeCodexTransport(cfg.Compat.CodexTransport)
		if err != nil {
			return llm.ProviderProfile{}, err
		}
		profile.Compat.CodexTransport = transport
	}
	if len(profile.Compat.ReasoningReplayFields) == 0 && profile.Capabilities.ReasoningReplay {
		profile.Compat.ReasoningReplayFields = []string{"reasoning_content", "reasoning", "thinking"}
	}
	return profile, nil
}

func CloneProviderProfile(p llm.ProviderProfile) llm.ProviderProfile {
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

func baseProfile(cfg Config) (llm.ProviderProfile, error) {
	id := strings.TrimSpace(cfg.ID)
	rawProtocol := strings.TrimSpace(cfg.Protocol)

	if id != "" {
		profile := presetProfile(id)
		if profile.ID != "" {
			if rawProtocol != "" {
				proto, err := parseProtocol(rawProtocol)
				if err != nil {
					return llm.ProviderProfile{}, err
				}
				if proto != profile.Protocol {
					return llm.ProviderProfile{}, fmt.Errorf("llm: provider id %q uses fixed protocol %q; omit providers[].protocol or use a custom providers[].id", id, profile.Protocol)
				}
			}
			return profile, nil
		}
		if rawProtocol == "" {
			return llm.ProviderProfile{}, fmt.Errorf("llm: unknown provider id %q requires providers[].protocol", id)
		}
		return customProfileForProtocol(id, rawProtocol)
	}

	if rawProtocol != "" {
		return customProfileForProtocol("custom", rawProtocol)
	}

	return llm.ProviderProfile{}, fmt.Errorf("llm: provider id or protocol is empty")
}

func KnownProviderIDs() []string {
	ids := make([]string, 0, len(providerPresets))
	for id := range providerPresets {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

var providerPresets = map[string]llm.ProviderProfile{
	"anthropic": {
		ID:       "anthropic",
		Protocol: llm.ProtocolAnthropicMessages,
		Capabilities: llm.ProviderCapabilities{
			Tools:           true,
			Streaming:       true,
			ReasoningEffort: true,
			ReasoningReplay: true,
			MaxOutputTokens: true,
		},
		Compat: llm.CompatOptions{ReasoningReplayFields: []string{"thinking", "redacted_thinking"}},
	},
	"openai": {
		ID:       "openai",
		Protocol: llm.ProtocolOpenAIResponses,
		Capabilities: llm.ProviderCapabilities{
			Tools:           true,
			Streaming:       true,
			ReasoningEffort: true,
			ReasoningReplay: true,
			MaxOutputTokens: true,
		},
		Compat: llm.CompatOptions{ReasoningReplayFields: []string{"reasoning_content", "reasoning", "thinking"}},
	},
	"openai-codex": {
		ID:       "openai-codex",
		Protocol: llm.ProtocolOpenAICodexResponses,
		Capabilities: llm.ProviderCapabilities{
			Tools:           true,
			Streaming:       true,
			ReasoningEffort: true,
			ReasoningReplay: true,
		},
		Compat: llm.CompatOptions{ReasoningReplayFields: []string{"reasoning_content", "reasoning", "thinking"}},
	},
	"deepseek": {
		ID:       "deepseek",
		Protocol: llm.ProtocolOpenAIChat,
		BaseURL:  "https://api.deepseek.com",
		Capabilities: llm.ProviderCapabilities{
			Tools:           true,
			Streaming:       true,
			ReasoningEffort: true,
			ReasoningReplay: true,
			MaxOutputTokens: true,
		},
		Compat: llm.CompatOptions{ReasoningReplayFields: []string{"reasoning_content"}},
	},
}

func customProfileForProtocol(id, rawProtocol string) (llm.ProviderProfile, error) {
	proto, err := parseProtocol(rawProtocol)
	if err != nil {
		return llm.ProviderProfile{}, err
	}
	switch proto {
	case llm.ProtocolAnthropicMessages:
		return llm.ProviderProfile{
			ID:       id,
			Protocol: proto,
			Capabilities: llm.ProviderCapabilities{
				Tools:           true,
				Streaming:       true,
				ReasoningEffort: true,
				ReasoningReplay: true,
				MaxOutputTokens: true,
			},
			Compat: llm.CompatOptions{ReasoningReplayFields: []string{"thinking", "redacted_thinking"}},
		}, nil
	case llm.ProtocolOpenAIResponses:
		return llm.ProviderProfile{
			ID:       id,
			Protocol: proto,
			Capabilities: llm.ProviderCapabilities{
				Tools:           true,
				Streaming:       true,
				ReasoningEffort: true,
				ReasoningReplay: true,
				MaxOutputTokens: true,
			},
			Compat: llm.CompatOptions{ReasoningReplayFields: []string{"reasoning_content", "reasoning", "thinking"}},
		}, nil
	case llm.ProtocolOpenAIChat:
		return customOpenAIChatProfile(id, proto), nil
	case llm.ProtocolOpenAICodexResponses:
		return llm.ProviderProfile{}, fmt.Errorf("llm: protocol %q is reserved for provider id %q", proto, "openai-codex")
	default:
		return llm.ProviderProfile{}, fmt.Errorf("llm: unsupported provider protocol %q", proto)
	}
}

func customOpenAIChatProfile(id string, proto llm.Protocol) llm.ProviderProfile {
	return llm.ProviderProfile{
		ID:       id,
		Protocol: proto,
		Capabilities: llm.ProviderCapabilities{
			Tools:           true,
			Streaming:       true,
			ReasoningEffort: true,
			ReasoningReplay: true,
			MaxOutputTokens: true,
		},
		Compat: llm.CompatOptions{ReasoningReplayFields: []string{"reasoning_content", "reasoning", "thinking"}},
	}
}

func presetProfile(id string) llm.ProviderProfile {
	if p, ok := providerPresets[id]; ok {
		p.Headers = cloneStringMap(p.Headers)
		p.Query = cloneStringMap(p.Query)
		p.Compat.ReasoningReplayFields = append([]string(nil), p.Compat.ReasoningReplayFields...)
		return p
	}
	return llm.ProviderProfile{}
}

func parseProtocol(in string) (llm.Protocol, error) {
	switch llm.Protocol(in) {
	case llm.ProtocolAnthropicMessages, llm.ProtocolOpenAIResponses, llm.ProtocolOpenAICodexResponses, llm.ProtocolOpenAIChat:
		return llm.Protocol(in), nil
	default:
		return "", fmt.Errorf("llm: unknown provider protocol %q", in)
	}
}

func applyCapabilityOverrides(c llm.ProviderCapabilities, o llm.CapabilityOverrides) llm.ProviderCapabilities {
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

func firstProfileValue(values ...string) string {
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
