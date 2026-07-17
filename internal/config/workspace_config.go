package config

import (
	"fmt"
	"os"
	"path/filepath"
)

func ValidateWorkspaceConfig(content []byte, workDir string) (Config, error) {
	cfg, err := loadUserConfigForWorkDir(workDir)
	if err != nil {
		return cfg, err
	}
	if err := applyYAMLData(&cfg, content, cfg.RuntimeConfigPath(), "project", true); err != nil {
		return cfg, err
	}
	if err := finalizeConfigLoadForValidation(&cfg, "", true); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func WriteWorkspaceConfig(content []byte, workDir string) (string, error) {
	cfg, err := ValidateWorkspaceConfig(content, workDir)
	if err != nil {
		return "", err
	}
	path := cfg.RuntimeConfigPath()
	if path == "" {
		return "", fmt.Errorf("config: workspace config path is empty")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("config: create workspace config directory: %w", err)
	}
	temp, err := os.CreateTemp(dir, ".juex-config-*.tmp")
	if err != nil {
		return "", fmt.Errorf("config: create workspace config temp file: %w", err)
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return "", err
	}
	if _, err := temp.Write(content); err != nil {
		_ = temp.Close()
		return "", err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return "", err
	}
	if err := temp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return "", fmt.Errorf("config: replace workspace config %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", err
	}
	return path, nil
}
