package management

import (
	"bytes"
	"encoding/json"
	"maps"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
)

// Configuration is one complete layer declaration. Nil Models and absent
// module keys inherit; a non-empty model list replaces the whole lower list.
type Configuration struct {
	ModulePreset string                          `json:"module_preset,omitempty"`
	Models       []string                        `json:"models,omitempty"`
	Modules      map[agentpolicy.Capability]bool `json:"modules,omitempty"`
}

func (c *Configuration) UnmarshalJSON(data []byte) error {
	var wire struct {
		ModulePreset string                           `json:"module_preset"`
		Models       []string                         `json:"models"`
		Modules      map[agentpolicy.Capability]*bool `json:"modules"`
	}
	// A module's absence inherits. JSON null must not silently become false and
	// revoke running work when decoding a complete layer declaration.
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	var modules map[agentpolicy.Capability]bool
	if wire.Modules != nil {
		modules = make(map[agentpolicy.Capability]bool, len(wire.Modules))
	}
	for key, value := range wire.Modules {
		if value == nil {
			return ErrInvalid
		}
		modules[key] = *value
	}
	*c = Configuration{ModulePreset: wire.ModulePreset, Models: wire.Models, Modules: modules}
	return nil
}

func (c Configuration) Validate() error {
	if c.ModulePreset != "" && c.ModulePreset != "standard" && c.ModulePreset != "minimal" {
		return ErrInvalid
	}
	if c.Models != nil && (len(c.Models) == 0 || len(c.Models) > 5) {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, model := range c.Models {
		id, err := uuid.Parse(model)
		if err != nil || id == uuid.Nil || id.String() != model || seen[model] {
			return ErrInvalid
		}
		seen[model] = true
	}
	for capability := range c.Modules {
		if !slices.Contains(agentpolicy.Capabilities(), capability) {
			return ErrInvalid
		}
	}
	return nil
}

func (c Configuration) Clone() Configuration {
	return Configuration{ModulePreset: c.ModulePreset, Models: slices.Clone(c.Models), Modules: maps.Clone(c.Modules)}
}

type ConfigurationLayer struct {
	Version     int64         `json:"version"`
	Declaration Configuration `json:"declaration"`
}

type ConfigurationLayers struct {
	Tenant    ConfigurationLayer `json:"tenant"`
	Fleet     ConfigurationLayer `json:"fleet"`
	Workspace ConfigurationLayer `json:"workspace"`
	Agent     ConfigurationLayer `json:"agent"`
}

type ConfigurationSource struct {
	Layer       string                 `json:"layer"`
	Version     int64                  `json:"version"`
	DerivedFrom agentpolicy.Capability `json:"derived_from,omitempty"`
	Preset      string                 `json:"preset,omitempty"`
}

type EffectiveModule struct {
	Enabled bool                `json:"enabled"`
	Source  ConfigurationSource `json:"source"`
}

type EffectiveConfiguration struct {
	Models      []string                                   `json:"models"`
	ModelSource ConfigurationSource                        `json:"model_source"`
	Modules     map[agentpolicy.Capability]EffectiveModule `json:"modules"`
}

func (l ConfigurationLayers) Resolve() EffectiveConfiguration {
	result := EffectiveConfiguration{Models: []string{}, ModelSource: ConfigurationSource{Layer: "default"}, Modules: map[agentpolicy.Capability]EffectiveModule{}}
	for _, key := range agentpolicy.Capabilities() {
		result.Modules[key] = EffectiveModule{Enabled: agentpolicy.DefaultEnabled(key), Source: ConfigurationSource{Layer: "default"}}
	}
	ordered := []struct {
		name  string
		layer ConfigurationLayer
	}{{"tenant", l.Tenant}, {"fleet", l.Fleet}, {"workspace", l.Workspace}, {"agent", l.Agent}}
	preset := ""
	for _, item := range ordered {
		if item.layer.Declaration.ModulePreset == "" {
			continue
		}
		preset = item.layer.Declaration.ModulePreset
		for _, key := range agentpolicy.Capabilities() {
			enabled := agentpolicy.DefaultEnabled(key) || key == agentpolicy.ApplyPatch || key == agentpolicy.ChunkedWrite
			if preset == "minimal" {
				enabled = key == agentpolicy.Files || key == agentpolicy.Shell
			}
			result.Modules[key] = EffectiveModule{Enabled: enabled, Source: ConfigurationSource{Layer: item.name, Version: item.layer.Version, Preset: preset}}
		}
	}
	// The highest preset supplies defaults. Explicit switches at any layer
	// remain declarations and take precedence over those defaults.
	explicit := map[agentpolicy.Capability]bool{}
	for _, item := range ordered {
		source := ConfigurationSource{Layer: item.name, Version: item.layer.Version}
		if item.layer.Declaration.Models != nil {
			result.Models, result.ModelSource = slices.Clone(item.layer.Declaration.Models), source
		}
		for key, enabled := range item.layer.Declaration.Modules {
			result.Modules[key] = EffectiveModule{Enabled: enabled, Source: source}
			explicit[key] = true
		}
	}
	// Existing sparse declarations retain their resource behavior until a user
	// chooses the independent switch. This follows the resolved parent through
	// all layers rather than writing a new Agent override during an upgrade.
	for resource, parent := range map[agentpolicy.Capability]agentpolicy.Capability{agentpolicy.FileSearch: agentpolicy.Files, agentpolicy.Skills: agentpolicy.Extensions} {
		if !explicit[resource] && preset == "" {
			value := result.Modules[parent]
			value.Source.DerivedFrom = parent
			result.Modules[resource] = value
		}
	}
	return result
}

func (c EffectiveConfiguration) Policy() agentpolicy.Policy {
	policy := agentpolicy.Policy{Version: 1}
	skills := c.Modules[agentpolicy.Skills]
	policy.SkillSources = skills.Enabled && skills.Source.DerivedFrom == ""
	for _, key := range agentpolicy.Capabilities() {
		if !agentpolicy.DefaultEnabled(key) {
			if c.Modules[key].Enabled {
				policy.Enabled = append(policy.Enabled, key)
			}
		} else if !c.Modules[key].Enabled {
			policy.Disabled = append(policy.Disabled, key)
		}
	}
	return policy.Normalized()
}

// WorkspaceConfiguration is an explicitly accepted snapshot, not a filesystem
// watch or lasting authorization to execute in the source environment.
type WorkspaceConfiguration struct {
	ConfigurationLayer
	Path                 string    `json:"path"`
	ReadStartedAt        time.Time `json:"read_started_at"`
	EnvironmentID        string    `json:"environment_id"`
	WorkingDirectory     string    `json:"working_directory"`
	OperationID          string    `json:"operation_id"`
	SHA256               string    `json:"sha256"`
	AuthorizationVersion int64     `json:"authorization_version"`
}
