package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

// ReadModuleInspectionConfig resolves only module selection, without provider
// setup, Agent registration, remote fetching, cache publication, or recovery.
// Remote imports require the existing validated cache for this Workspace.
func ReadModuleInspectionConfig(workDir, homeDir, agentConfigPath string) (Config, error) {
	resolution, err := resolveHomeConfigSources(homeDir)
	if err != nil {
		return Config{}, err
	}
	loader := newConfigImportLoader(resolution.EffectiveHomeDir)
	loader.contextDigest = configImportContextDigest(workDir)
	if _, err := os.Stat(configImportCacheJournalPath(loader.homeDir)); err == nil {
		return Config{}, fmt.Errorf("config: import publication requires recovery before inspection")
	} else if !os.IsNotExist(err) {
		return Config{}, err
	}
	cfg := Config{WorkDir: workDir}
	sources := append(resolution.Sources, workspaceYAMLSource(cfg.WorkspaceConfigPath()), agentYAMLSource(agentConfigPath))
	for _, source := range sources {
		if source.Path == "" {
			continue
		}
		data, err := os.ReadFile(source.Path)
		if os.IsNotExist(err) && source.MissingOK {
			continue
		}
		if err != nil {
			return Config{}, err
		}
		parsed, err := decodeFileConfig(data, source.Path)
		if err != nil {
			return Config{}, err
		}
		for _, item := range parsed.Imports {
			document, err := readInspectionImport(loader, source, item.Source)
			if err != nil {
				return Config{}, fmt.Errorf("config: inspect import from %s: %w", source.Path, err)
			}
			imported, err := decodeFileConfig(document.data, document.source.Path)
			if err != nil {
				return Config{}, err
			}
			if len(imported.Imports) > 0 {
				return Config{}, fmt.Errorf("config: nested imports are not supported")
			}
			if err := applyModuleSelection(&cfg, imported.Preset, imported.Modules); err != nil {
				return Config{}, err
			}
		}
		if err := applyModuleSelection(&cfg, parsed.Preset, parsed.Modules); err != nil {
			return Config{}, err
		}
	}
	return cfg, cfg.ValidateModules()
}

func readInspectionImport(loader *configImportLoader, source yamlConfigSource, raw string) (configImportDocument, error) {
	raw = strings.TrimSpace(raw)
	if !strings.Contains(raw, "://") {
		return loader.loadLocal(source, raw)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return configImportDocument{}, fmt.Errorf("invalid import URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return configImportDocument{}, fmt.Errorf("unsupported import scheme")
	}
	identity := parsed.String()
	record, err := loader.readCachePath(loader.cachePath(identity, source.Path), identity, source.Path, loader.cacheContextDigest())
	if err != nil {
		return configImportDocument{}, fmt.Errorf("validated import unavailable for inspection: %w", err)
	}
	return loader.remoteDocument(source, sanitizedRemoteSource(parsed), record, "cached", false), nil
}
