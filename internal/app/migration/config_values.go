package migration

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/providers/profile"
	"gopkg.in/yaml.v3"
)

type configDocument struct {
	Imports []struct {
		Source string `yaml:"source"`
	} `yaml:"imports"`
	Models        *[]string               `yaml:"models"`
	UserResources *configBool             `yaml:"enable_user_agents_resources"`
	Providers     []configProvider        `yaml:"providers"`
	Preset        *string                 `yaml:"preset"`
	Modules       map[string]configModule `yaml:"modules"`
	Extensions    struct {
		Allow *[]string `yaml:"allow"`
	} `yaml:"extensions"`
	FleetClient *struct {
		Profile string `yaml:"profile"`
	} `yaml:"fleet_client"`
	Fleet *struct {
		Addr          string      `yaml:"addr"`
		UnsafeBindAny *configBool `yaml:"unsafe_bind_any"`
	} `yaml:"fleet"`
	Compaction  yaml.Node `yaml:"compaction"`
	ToolOutput  yaml.Node `yaml:"tool_output"`
	Hooks       yaml.Node `yaml:"hooks"`
	Runtime     yaml.Node `yaml:"runtime"`
	Shell       yaml.Node `yaml:"shell"`
	Sandbox     yaml.Node `yaml:"sandbox"`
	Skills      yaml.Node `yaml:"skills"`
	Environment yaml.Node `yaml:"environment"`
}

type configBool bool

func (b *configBool) UnmarshalYAML(node *yaml.Node) error {
	switch strings.ToLower(strings.TrimSpace(node.Value)) {
	case "1", "true", "t", "yes", "y", "on":
		*b = true
	case "0", "false", "f", "no", "n", "off":
		*b = false
	default:
		return errors.New("invalid configuration boolean")
	}
	return nil
}

type configModule struct {
	Enabled  *configBool `yaml:"enabled"`
	MaxDepth yaml.Node   `yaml:"max_depth"`
	Service  *string     `yaml:"service"`
	Profile  *string     `yaml:"profile"`
}

type configProvider struct {
	ID       string                `yaml:"id"`
	Protocol string                `yaml:"protocol"`
	BaseURL  string                `yaml:"base_url"`
	APIKey   string                `yaml:"api_key"`
	Options  configOptions         `yaml:",inline"`
	Models   []configProviderModel `yaml:"models"`
}

type configProviderModel struct {
	ID             string        `yaml:"id"`
	ThinkingEffort string        `yaml:"thinking_effort"`
	ContextWindow  int           `yaml:"context_window"`
	Options        configOptions `yaml:",inline"`
}

type configOptions struct {
	Headers      map[string]string `yaml:"headers"`
	Query        map[string]string `yaml:"query"`
	Capabilities struct {
		Tools           *bool `yaml:"tools"`
		Vision          *bool `yaml:"vision"`
		Streaming       *bool `yaml:"streaming"`
		ReasoningEffort *bool `yaml:"reasoning_effort"`
		ReasoningReplay *bool `yaml:"reasoning_replay"`
		MaxOutputTokens *bool `yaml:"max_output_tokens"`
	} `yaml:"capabilities"`
	Compat struct {
		ReasoningReplayFields []string `yaml:"reasoning_replay_fields"`
		CodexTransport        string   `yaml:"codex_transport"`
		MaxTokensField        string   `yaml:"max_tokens_field"`
	} `yaml:"compat"`
}

type configSettings struct {
	value        ResolvedConfig
	models       []string
	providers    map[string]configProvider
	modules      map[string]bool
	fleetProfile string
}

// Fixed 281889e5 inventory: preset defaults must be expanded only after every
// sparse overlay, including an explicit startup-file replay, has been applied.
var sourceModules = map[string]bool{
	"basic-file-tools": true, "shell": true, "operating-context": true,
	"apply-patch": false, "chunked-write": false, "file-search": false,
	"agents-md": false, "skills": false, "scratchpad": false, "tasks": false,
	"notes": false, "input-tracking": false, "fleet-management": false,
	"memory": false, "context-control": false, "worker-threads": false,
	"observables": false, "mcp": false, "hooks": false, "extensions": false,
}

func (s *configSettings) apply(value configDocument, scope string, replay bool) error {
	for _, unsupported := range []struct {
		name string
		node yaml.Node
	}{
		{"compaction", value.Compaction}, {"tool_output", value.ToolOutput},
		{"hooks", value.Hooks}, {"runtime", value.Runtime}, {"shell", value.Shell},
		{"sandbox", value.Sandbox}, {"skills", value.Skills}, {"environment", value.Environment},
	} {
		if unsupported.node.Kind != 0 {
			return fmt.Errorf("configuration field %s requires explicit conversion", unsupported.name)
		}
	}
	if value.Fleet != nil && scope != "default-home" && scope != "instance-home" {
		return errors.New("source Fleet configuration is only valid in a source Home")
	}
	if value.Models != nil {
		s.models = slices.Clone(*value.Models)
	}
	if value.UserResources != nil {
		s.value.UserResources = bool(*value.UserResources)
	}
	if value.Preset != nil {
		if *value.Preset != "standard" && *value.Preset != "minimal" {
			return errors.New("unsupported source preset")
		}
		s.value.Preset = *value.Preset
	}
	if value.FleetClient != nil {
		if value.FleetClient.Profile != "agent" && value.FleetClient.Profile != "supervisor" {
			return errors.New("invalid source Fleet profile")
		}
		s.fleetProfile = value.FleetClient.Profile
	}
	for id, module := range value.Modules {
		if _, exists := sourceModules[id]; !exists {
			return errors.New("unknown source module")
		}
		if module.Service != nil {
			return errors.New("source Memory service selection requires an explicit target binding")
		}
		if module.Profile != nil {
			if id != "memory" || (*module.Profile != "agent" && *module.Profile != "supervisor") {
				return errors.New("invalid source Memory profile")
			}
			s.value.MemoryProfile = *module.Profile
		}
		if module.MaxDepth.Kind != 0 {
			var depth int
			if id != "worker-threads" || module.MaxDepth.Tag != "!!int" || module.MaxDepth.Decode(&depth) != nil || (depth != 1 && depth != 2) {
				return errors.New("invalid source Worker depth")
			}
			s.value.WorkerDepth = depth
		}
		if module.Enabled != nil {
			s.modules[id] = bool(*module.Enabled)
		}
	}
	if value.Extensions.Allow != nil && !replay {
		if scope == "explicit" {
			return errors.New("extension policy is not allowed in an explicit-only configuration")
		}
		s.value.ExtensionPolicyConfigured = true
		s.value.ExtensionAllow = []string{}
		for _, raw := range *value.Extensions.Allow {
			name := strings.TrimSpace(raw)
			if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
				return errors.New("invalid source extension name")
			}
			if !slices.Contains(s.value.ExtensionAllow, name) {
				s.value.ExtensionAllow = append(s.value.ExtensionAllow, name)
			}
		}
	}
	for _, provider := range value.Providers {
		provider.ID = strings.TrimSpace(provider.ID)
		if provider.ID == "" || strings.Contains(provider.ID, ":") {
			return errors.New("invalid source provider identity")
		}
		if err := provider.Options.validate(); err != nil {
			return err
		}
		prior := s.providers[provider.ID]
		prior.ID = provider.ID
		if strings.TrimSpace(provider.Protocol) != "" {
			prior.Protocol = strings.TrimSpace(provider.Protocol)
		}
		if provider.BaseURL != "" {
			prior.BaseURL = provider.BaseURL
		}
		if provider.APIKey != "" {
			prior.APIKey = provider.APIKey
		}
		prior.Options = mergeConfigOptions(prior.Options, provider.Options)
		for _, model := range provider.Models {
			model.ID = strings.TrimSpace(model.ID)
			model.ThinkingEffort = strings.TrimSpace(model.ThinkingEffort)
			if model.ID == "" || !slices.Contains([]string{"", "low", "medium", "high", "xhigh", "max"}, model.ThinkingEffort) {
				return errors.New("invalid source model identity or thinking effort")
			}
			if err := model.Options.validate(); err != nil {
				return err
			}
			i := slices.IndexFunc(prior.Models, func(m configProviderModel) bool { return m.ID == model.ID })
			if i < 0 {
				prior.Models = append(prior.Models, model)
			} else {
				current := &prior.Models[i]
				if model.ThinkingEffort != "" {
					current.ThinkingEffort = model.ThinkingEffort
				}
				if model.ContextWindow > 0 {
					current.ContextWindow = model.ContextWindow
				}
				current.Options = mergeConfigOptions(current.Options, model.Options)
			}
		}
		s.providers[provider.ID] = prior
	}
	return nil
}

func (s *configSettings) resolve() (ResolvedConfig, error) {
	s.value.Modules = map[string]bool{}
	for id, minimal := range sourceModules {
		enabled := s.value.Preset == "standard" || minimal
		if explicit, ok := s.modules[id]; ok {
			enabled = explicit
		}
		s.value.Modules[id] = enabled
	}
	if s.value.MemoryProfile == "" {
		s.value.MemoryProfile = s.fleetProfile
	}
	seen := map[string]bool{}
	for _, raw := range s.models {
		parts := strings.SplitN(strings.TrimSpace(raw), ":", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
			return ResolvedConfig{}, errors.New("invalid source model selector")
		}
		providerID, modelID := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		ref := providerID + ":" + modelID
		provider, ok := s.providers[providerID]
		i := slices.IndexFunc(provider.Models, func(m configProviderModel) bool { return m.ID == modelID })
		if !ok || i < 0 || seen[ref] {
			return ResolvedConfig{}, errors.New("source model selector is unknown or repeated")
		}
		seen[ref] = true
		model := provider.Models[i]
		options := mergeConfigOptions(provider.Options, model.Options)
		window := model.ContextWindow
		if window <= 0 {
			window = 256000
		}
		cfg := profile.Config{ID: providerID, Protocol: provider.Protocol, BaseURL: provider.BaseURL, APIKey: provider.APIKey,
			Model: modelID, ThinkingEffort: model.ThinkingEffort, Headers: options.Headers, Query: options.Query,
			Capabilities: llm.CapabilityOverrides{Tools: options.Capabilities.Tools, Vision: options.Capabilities.Vision, Streaming: options.Capabilities.Streaming, ReasoningEffort: options.Capabilities.ReasoningEffort, ReasoningReplay: options.Capabilities.ReasoningReplay, MaxOutputTokens: options.Capabilities.MaxOutputTokens},
			Compat:       llm.CompatOptions{ReasoningReplayFields: options.Compat.ReasoningReplayFields, CodexTransport: options.Compat.CodexTransport, MaxTokensField: options.Compat.MaxTokensField}}
		s.value.Models = append(s.value.Models, ConfigModel{Ref: ref, ContextWindow: window, Configuration: cfg})
	}
	return s.value, nil
}

func (o *configOptions) validate() error {
	transport, err := profile.NormalizeCodexTransport(o.Compat.CodexTransport)
	if err != nil {
		return errors.New("invalid source Codex transport")
	}
	field, err := profile.NormalizeMaxTokensField(o.Compat.MaxTokensField)
	if err != nil {
		return errors.New("invalid source token field")
	}
	o.Compat.CodexTransport, o.Compat.MaxTokensField = transport, field
	return nil
}

func mergeConfigOptions(base, override configOptions) configOptions {
	merge := func(a, b map[string]string) map[string]string {
		out := maps.Clone(a)
		if len(b) > 0 && out == nil {
			out = map[string]string{}
		}
		for key, value := range b {
			if value == "" {
				delete(out, key)
			} else {
				out[key] = value
			}
		}
		return out
	}
	base.Headers, base.Query = merge(base.Headers, override.Headers), merge(base.Query, override.Query)
	if override.Capabilities.Tools != nil {
		base.Capabilities.Tools = override.Capabilities.Tools
	}
	if override.Capabilities.Vision != nil {
		base.Capabilities.Vision = override.Capabilities.Vision
	}
	if override.Capabilities.Streaming != nil {
		base.Capabilities.Streaming = override.Capabilities.Streaming
	}
	if override.Capabilities.ReasoningEffort != nil {
		base.Capabilities.ReasoningEffort = override.Capabilities.ReasoningEffort
	}
	if override.Capabilities.ReasoningReplay != nil {
		base.Capabilities.ReasoningReplay = override.Capabilities.ReasoningReplay
	}
	if override.Capabilities.MaxOutputTokens != nil {
		base.Capabilities.MaxOutputTokens = override.Capabilities.MaxOutputTokens
	}
	if len(override.Compat.ReasoningReplayFields) > 0 {
		base.Compat.ReasoningReplayFields = slices.Clone(override.Compat.ReasoningReplayFields)
	}
	if override.Compat.CodexTransport != "" {
		base.Compat.CodexTransport = override.Compat.CodexTransport
	}
	if override.Compat.MaxTokensField != "" {
		base.Compat.MaxTokensField = override.Compat.MaxTokensField
	}
	return base
}
