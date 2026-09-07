package config

import (
	"fmt"
	"strings"

	"github.com/juex-ai/juex/internal/features/hooks"
	"gopkg.in/yaml.v3"
)

// Keep declarations immutable until all layers establish the effective switches.
// Retaining the document also preserves YAML aliases outside the module subtree.
type moduleDeclaration struct {
	data   string
	source yamlConfigSource
	hooks  bool
	skills bool
}

func resolveModuleDeclarations(cfg *Config) error {
	resolved := Config{Hooks: cloneHooksConfig(cfg.Hooks), Skills: cfg.Skills}
	for _, declaration := range cfg.moduleDeclarations {
		if declaration.hooks && cfg.ModuleEnabled("hooks") {
			var fields struct {
				Hooks hooks.FileConfig     `yaml:"hooks"`
				Other map[string]yaml.Node `yaml:",inline"`
			}
			if err := decodeModuleDeclaration(declaration, &fields); err != nil {
				return err
			}
			if err := applyHooksConfig(&resolved, fields.Hooks, declaration.source.hookSource(), declaration.source.requireHookTrust()); err != nil {
				return fmt.Errorf("config: parse %s: %w", declaration.source.Path, err)
			}
		}
		if declaration.skills && cfg.ModuleEnabled("skills") {
			var fields struct {
				Skills skillsConfig         `yaml:"skills"`
				Other  map[string]yaml.Node `yaml:",inline"`
			}
			if err := decodeModuleDeclaration(declaration, &fields); err != nil {
				return err
			}
			if err := applySkillsConfig(&resolved, fields.Skills); err != nil {
				return fmt.Errorf("config: parse %s: %w", declaration.source.Path, err)
			}
		}
	}
	cfg.Hooks, cfg.Skills = resolved.Hooks, resolved.Skills
	cfg.moduleDeclarations = nil
	return nil
}

func decodeModuleDeclaration(declaration moduleDeclaration, target any) error {
	decoder := yaml.NewDecoder(strings.NewReader(declaration.data))
	decoder.KnownFields(true)
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("config: parse %s: %w", declaration.source.Path, err)
	}
	return nil
}
