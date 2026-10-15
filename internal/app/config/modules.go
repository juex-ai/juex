package config

import (
	"fmt"
	"github.com/juex-ai/juex/internal/foundation/serviceendpoint"
	"sort"

	"gopkg.in/yaml.v3"
)

const (
	PresetStandard = "standard"
	PresetMinimal  = "minimal"
)

// ModulePolicy contains only explicit, layered switches. Preset defaults are
// evaluated after merging and are never materialized into the sparse overlay.
type ModulePolicy map[string]ModuleSettings

type ModuleSettings struct {
	Enabled bool
}

// A role-configured Supervisor defaults to its matching Memory capability;
// an explicit Memory profile remains a startup-only override.
func (c Config) EffectiveMemoryProfile() string {
	if c.MemoryProfile != "" {
		return c.MemoryProfile
	}
	if c.FleetClientProfile == "supervisor" {
		return "supervisor"
	}
	return "agent"
}

type moduleConfig struct {
	Enabled  optionalBool `yaml:"enabled"`
	MaxDepth yaml.Node    `yaml:"max_depth"`
	Service  *string      `yaml:"service"`
	Profile  *string      `yaml:"profile"`
}

// WorkerMaxDepth is independent of preset and module enablement. Zero is the
// unset programmatic value; YAML accepts only explicit integers 1 and 2.
func (c Config) WorkerMaxDepth() int {
	if c.WorkerThreadMaxDepth == 0 {
		return 1
	}
	return c.WorkerThreadMaxDepth
}

func (c Config) EffectivePreset() string {
	if c.Preset == "" {
		return PresetStandard
	}
	return c.Preset
}

// ModuleEnabled reports effective configuration, not the active tool catalog.
// Unknown identities fail closed; ValidateModules reports configuration errors.
func (c Config) ModuleEnabled(id string) bool {
	definition, ok := c.ModuleInventory.Lookup(id)
	if !ok {
		return false
	}
	if settings, ok := c.Modules[id]; ok {
		return settings.Enabled
	}
	switch c.EffectivePreset() {
	case PresetStandard:
		return true
	case PresetMinimal:
		return definition.Minimal
	default:
		return false
	}
}

func (c Config) ValidateModules() error {
	if c.MemoryProfile != "" && c.MemoryProfile != "agent" && c.MemoryProfile != "supervisor" {
		return fmt.Errorf("config: modules.memory.profile must be agent or supervisor")
	}
	if c.MemoryService != "" {
		if err := serviceendpoint.ValidateID(c.MemoryService); err != nil {
			return err
		}
	}
	if depth := c.WorkerMaxDepth(); depth != 1 && depth != 2 {
		return fmt.Errorf("config: modules.worker-threads.max_depth must be 1 or 2")
	}
	if err := c.ModuleInventory.validate(); err != nil {
		return err
	}
	if err := validatePreset(c.EffectivePreset()); err != nil {
		return err
	}
	ids := make([]string, 0, len(c.Modules))
	for id := range c.Modules {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if _, ok := c.ModuleInventory.Lookup(id); !ok {
			return fmt.Errorf("config: unsupported module %q", id)
		}
	}
	return nil
}

func validatePreset(preset string) error {
	if preset != PresetStandard && preset != PresetMinimal {
		return fmt.Errorf("config: unsupported preset %q (use minimal or standard)", preset)
	}
	return nil
}

func applyModulesConfig(cfg *Config, modules map[string]moduleConfig) error {
	if err := cfg.ModuleInventory.validate(); err != nil {
		return err
	}
	ids := make([]string, 0, len(modules))
	for id := range modules {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if _, ok := cfg.ModuleInventory.Lookup(id); !ok {
			return fmt.Errorf("unsupported module %q", id)
		}
		fileSettings := modules[id]
		if fileSettings.Service != nil || fileSettings.Profile != nil {
			if id != "memory" {
				return fmt.Errorf("module %q does not support service/profile", id)
			}
			if fileSettings.Service != nil {
				if err := serviceendpoint.ValidateID(*fileSettings.Service); err != nil {
					return err
				}
				cfg.MemoryService = *fileSettings.Service
			}
			if fileSettings.Profile != nil {
				if *fileSettings.Profile != "agent" && *fileSettings.Profile != "supervisor" {
					return fmt.Errorf("modules.memory.profile must be agent or supervisor")
				}
				cfg.MemoryProfile = *fileSettings.Profile
			}
		}
		if node := fileSettings.MaxDepth; node.Kind != 0 {
			if id != "worker-threads" {
				return fmt.Errorf("module %q does not support max_depth", id)
			}
			var depth int
			if node.Tag != "!!int" || node.Decode(&depth) != nil || (depth != 1 && depth != 2) {
				return fmt.Errorf("modules.worker-threads.max_depth must be the integer 1 or 2")
			}
			cfg.WorkerThreadMaxDepth = depth
		}
		if !fileSettings.Enabled.Set {
			continue
		}
		if cfg.Modules == nil {
			cfg.Modules = make(ModulePolicy)
		}
		cfg.Modules[id] = ModuleSettings{Enabled: fileSettings.Enabled.Value}
	}
	return nil
}

func applyModuleSelection(cfg *Config, preset *string, modules map[string]moduleConfig) error {
	if preset != nil {
		if err := validatePreset(*preset); err != nil {
			return err
		}
		cfg.Preset = *preset
	}
	return applyModulesConfig(cfg, modules)
}
