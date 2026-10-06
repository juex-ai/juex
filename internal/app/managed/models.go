package managed

import (
	"context"
	"maps"
	"strings"

	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/management"
	"github.com/juex-ai/juex/internal/management/postgres"
	providerprofile "github.com/juex-ai/juex/internal/providers/profile"
)

// ConfigureModel validates adapter-specific behavior at the composition boundary
// before Management seals the private configuration. Services do not import
// provider adapters or infer configuration from the operator's Home.
func ConfigureModel(ctx context.Context, directory *postgres.Directory, config management.ModelConfiguration) (management.Model, error) {
	config, err := prepareModelConfiguration(config)
	if err != nil {
		return management.Model{}, err
	}
	return directory.ConfigureModel(ctx, config)
}

// ImportModels validates every adapter before the offline operator transaction.
func ImportModels(ctx context.Context, directory *postgres.Directory, value management.ModelsImport) (map[management.ModelKey]string, error) {
	models := make([]management.ImportedModel, len(value.Models))
	for i, item := range value.Models {
		config, err := prepareModelConfiguration(item.Configuration)
		if err != nil {
			return nil, err
		}
		models[i] = management.ImportedModel{Configuration: config, Fallbacks: item.Fallbacks}
	}
	value.Models = models
	return directory.ImportModels(ctx, value)
}

func prepareModelConfiguration(config management.ModelConfiguration) (management.ModelConfiguration, error) {
	var err error
	config, err = normalizeModelConfiguration(config)
	if err != nil {
		return config, err
	}
	if _, err := modelProfile(config); err != nil {
		return config, management.ErrInvalid
	}
	return config, nil
}

func normalizeModelConfiguration(config management.ModelConfiguration) (management.ModelConfiguration, error) {
	config.Options = config.Options.Normalized()
	if config.Protocol != llm.ProtocolOpenAICodexResponses {
		return config, nil
	}
	headers := maps.Clone(config.Options.Headers)
	count, account := 0, ""
	for name, value := range headers {
		if strings.EqualFold(name, "ChatGPT-Account-ID") {
			count++
			account = strings.TrimSpace(value)
			delete(headers, name)
		}
	}
	if count != 1 || account == "" || strings.Contains(account, "${") {
		return management.ModelConfiguration{}, management.ErrInvalid
	}
	headers["ChatGPT-Account-ID"] = account
	config.Options.Headers = headers
	return config, nil
}

func modelProfile(config management.ModelConfiguration) (llm.ProviderProfile, error) {
	options := config.Options
	// Managed catalogs have always treated provider names as operator labels.
	// Preserve their explicit protocol even when a label matches a preset name;
	// only Codex requires a reserved adapter identity.
	adapter := "managed"
	if config.Protocol == llm.ProtocolOpenAICodexResponses {
		adapter = config.Provider
	}
	profile, err := providerprofile.ResolveProfile(providerprofile.Config{ID: adapter, Protocol: string(config.Protocol), BaseURL: config.Endpoint, APIKey: config.APIKey, Model: config.Name,
		Authentication: options.Authentication, ThinkingEffort: options.ThinkingEffort, Headers: options.Headers, Query: options.Query, Capabilities: options.Capabilities, Compat: options.Compat})
	profile.ID = config.Provider
	return profile, err
}
