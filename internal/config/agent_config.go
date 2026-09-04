package config

import (
	"errors"
	"fmt"

	"github.com/juex-ai/juex/internal/agentstate"
	"github.com/juex-ai/juex/internal/homestore"
)

// AgentConfigValidationError identifies a rejected Agent config candidate.
// Fleet uses it to distinguish user input from persistence or restart errors.
type AgentConfigValidationError struct {
	Err error
}

func (e *AgentConfigValidationError) Error() string {
	return fmt.Sprintf("config: invalid Agent config: %v", e.Err)
}

func (e *AgentConfigValidationError) Unwrap() error {
	return e.Err
}

func ValidateAgentConfig(content []byte, homeDir, agentID string) (cfg Config, returnErr error) {
	cfg, returnErr = validateAgentConfig(content, homeDir, agentID)
	if returnErr != nil {
		return cfg, &AgentConfigValidationError{Err: returnErr}
	}
	cfg.pendingImportCache = nil
	if cfg.importLoader != nil {
		returnErr = cfg.importLoader.closeConfigImportCacheLock()
		cfg.importLoader = nil
	}
	return cfg, returnErr
}

func validateAgentConfig(content []byte, homeDir, agentID string) (Config, error) {
	resolution, err := agentstate.ResolveByID(agentstate.Options{HomeDir: homeDir}, agentID)
	if err != nil {
		return Config{}, err
	}
	cfg, err := loadConfigFilesForWorkDir(resolution.Agent.Workspace, homeDir)
	if err != nil {
		return cfg, err
	}
	bindAgentState(&cfg, resolution)
	loader := configImportLoaderFor(&cfg)
	if err := applyYAMLContentWithImportLoader(
		&cfg,
		content,
		agentYAMLSource(resolution.Address.ConfigPath()),
		loader,
		applyYAMLDataOptions{},
	); err != nil {
		cfg.pendingImportCache = nil
		cfg.importLoader = nil
		return cfg, errors.Join(err, loader.closeConfigImportCacheLock())
	}
	if err := finalizeConfigLoadForValidationRetainingImportCacheLock(&cfg, nil, true); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// WriteAgentConfig validates the candidate against the complete Home and
// Workspace chain, then atomically publishes the sparse Agent overlay and any
// remote-import cache generation under one recovery journal.
func WriteAgentConfig(content []byte, homeDir, agentID string) (string, error) {
	cfg, err := validateAgentConfig(content, homeDir, agentID)
	if err != nil {
		return "", &AgentConfigValidationError{Err: err}
	}
	return writeValidatedConfig(
		content,
		&cfg,
		cfg.AgentConfigPath(),
		"agent",
		0o700,
		func(cfg *Config) error {
			return publishPendingConfigImportCachesWhileLocked(cfg, func(path string, data []byte) error {
				return homestore.WriteFileAtomic(path, data, 0o600, 0o700)
			})
		},
	)
}
