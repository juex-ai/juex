package config

import (
	"fmt"
	"sort"

	"github.com/juex-ai/juex/internal/app/modulecatalog"
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

type moduleConfig struct {
	Enabled optionalBool `yaml:"enabled"`
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
	definition, ok := modulecatalog.Lookup(id)
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
	if err := validatePreset(c.EffectivePreset()); err != nil {
		return err
	}
	ids := make([]string, 0, len(c.Modules))
	for id := range c.Modules {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if _, ok := modulecatalog.Lookup(id); !ok {
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
	ids := make([]string, 0, len(modules))
	for id := range modules {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if _, ok := modulecatalog.Lookup(id); !ok {
			return fmt.Errorf("unsupported module %q", id)
		}
		fileSettings := modules[id]
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
