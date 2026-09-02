package config

import (
	"fmt"
	"sort"
	"strings"
)

// ModulePolicy is the layered enabled envelope for compiled in-process
// Modules. Missing entries mean that the compiled Module uses its default.
type ModulePolicy map[string]ModuleSettings

type ModuleSettings struct {
	Enabled bool
}

type moduleConfig struct {
	Enabled optionalBool `yaml:"enabled"`
}

func (c Config) ModuleEnabled(id string) bool {
	settings, ok := c.Modules[strings.TrimSpace(id)]
	if !ok {
		return true
	}
	return settings.Enabled
}

// ValidateModuleIDs keeps generic config parsing independent from concrete
// Feature packages while allowing the composition root to reject typos.
func (c Config) ValidateModuleIDs(allowed []string) error {
	known := make(map[string]struct{}, len(allowed))
	for _, id := range allowed {
		id = strings.TrimSpace(id)
		if id != "" {
			known[id] = struct{}{}
		}
	}
	unknown := make([]string, 0)
	for id := range c.Modules {
		if _, ok := known[id]; !ok {
			unknown = append(unknown, id)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	if len(unknown) == 1 {
		return fmt.Errorf("config: unsupported module %q", unknown[0])
	}
	return fmt.Errorf("config: unsupported modules %q", unknown)
}

func applyModulesConfig(cfg *Config, modules map[string]moduleConfig) error {
	if len(modules) == 0 {
		return nil
	}
	if cfg.Modules == nil {
		cfg.Modules = make(ModulePolicy)
	}
	for rawID, fileSettings := range modules {
		id := strings.TrimSpace(rawID)
		if rawID != id || !validModuleConfigID(id) {
			return fmt.Errorf("modules: invalid module id %q", rawID)
		}
		if !fileSettings.Enabled.Set {
			continue
		}
		cfg.Modules[id] = ModuleSettings{Enabled: fileSettings.Enabled.Value}
	}
	return nil
}

func validModuleConfigID(id string) bool {
	if id == "" || id[0] == '-' || id[len(id)-1] == '-' {
		return false
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return false
	}
	return true
}
