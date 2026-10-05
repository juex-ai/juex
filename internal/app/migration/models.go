package migration

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/migration/legacy"
	"github.com/juex-ai/juex/internal/providers/profile"
)

// ModelEvidence supplies the source's effective environment, after its config,
// dotenv and inherited layers merged. Missing keys are unknown; present nil
// entries prove absence; non-nil entries preserve even an explicitly empty value.
// The caller must verify the snapshot's Agent/process provenance. Current files
// or the importing process's environment cannot prove a running Agent's snapshot.
type ModelEvidence struct {
	AgentID     string             `json:"agent_id"`
	Environment map[string]*string `json:"-"`
	// ProcessWorkingDirectory can differ from the configured Agent Workspace.
	// Only the source process cwd resolves a relative CODEX_HOME lookup.
	ProcessWorkingDirectory string `json:"-"`
	// UserHome is the source os.UserHomeDir result, independently captured from
	// its inherited environment. It is not inferred from an activated snapshot.
	UserHome  string             `json:"-"`
	CodexAuth *legacy.SourceFile `json:"-"`
}

type ResolvedModels struct {
	AgentID         string          `json:"agent_id"`
	Models          []ResolvedModel `json:"models"`
	CodexAuthSHA256 string          `json:"codex_auth_sha256,omitempty"`
}

// ResolvedModel preserves source request behavior, not a target catalog policy.
// In particular, zero MaxOutputTokens means the source did not set a normal-turn
// output cap. Target budget reservation and account binding remain separate.
type ResolvedModel struct {
	Ref             string              `json:"ref"`
	ContextWindow   int                 `json:"context_window"`
	MaxOutputTokens int                 `json:"max_output_tokens"`
	Profile         llm.ProviderProfile `json:"-"`
}

// ResolveModels applies 281889e5's primary/fallback environment and Codex auth
// rules to captured configuration without file, network or environment access.
// The result does not prove credential freshness, provider availability or that
// these source values have been accepted by the target's Management catalog.
func ResolveModels(config ResolvedConfig, evidence ModelEvidence) (ResolvedModels, error) {
	if config.AgentID == "" || evidence.AgentID != config.AgentID {
		return ResolvedModels{}, errors.New("model evidence must identify the source Agent")
	}
	models := config.Models
	if len(models) == 0 {
		// The source permits an environment-only primary without disk selectors.
		models = []ConfigModel{{ContextWindow: 256000}}
	}
	result := ResolvedModels{AgentID: config.AgentID}
	seen := map[string]bool{}
	for i, model := range models {
		// The source compares configured tail refs against the effective primary
		// before it resolves a fallback's credentials or provider profile.
		if i > 0 && seen[model.Ref] {
			continue
		}
		cfg, window, err := modelEnvironment(model, evidence, i == 0 && !config.StartupModelOverride)
		if err != nil {
			return ResolvedModels{}, fmt.Errorf("source model %d: %w", i+1, err)
		}
		usedAuth, err := modelCodexAuth(&cfg, evidence)
		if err != nil {
			return ResolvedModels{}, fmt.Errorf("source model %d: %w", i+1, err)
		}
		if usedAuth {
			result.CodexAuthSHA256 = evidence.CodexAuth.SHA256
		}
		// The fixed source required a credential even for a local OpenAI endpoint.
		// Lack of captured credentials cannot authorize keyless authentication.
		if cfg.APIKey == "" || cfg.Model == "" || window <= 0 {
			return ResolvedModels{}, fmt.Errorf("source model %d: credential, model or context window is missing", i+1)
		}
		cfg.Authentication = "api_key"
		resolved, err := profile.ResolveProfile(cfg)
		if err != nil {
			// Provider errors can contain private config/header values.
			return ResolvedModels{}, fmt.Errorf("source model %d: invalid provider profile", i+1)
		}
		ref := model.Ref
		if i == 0 {
			ref = cfg.ID + ":" + cfg.Model
		}
		seen[ref] = true
		result.Models = append(result.Models, ResolvedModel{Ref: ref, ContextWindow: window, Profile: resolved})
	}
	return result, nil
}

func modelEnvironment(model ConfigModel, evidence ModelEvidence, selectPrimary bool) (profile.Config, int, error) {
	cfg, window := model.Configuration, model.ContextWindow
	values := map[string]string{}
	for _, name := range []string{"PROVIDER_API_ID", "PROVIDER_API_PROTOCOL", "PROVIDER_API_BASE", "PROVIDER_API_KEY", "PROVIDER_API_MODEL", "PROVIDER_THINKING_EFFORT", "PROVIDER_CONTEXT_WINDOW"} {
		if !selectPrimary && (name == "PROVIDER_API_ID" || name == "PROVIDER_API_PROTOCOL" || name == "PROVIDER_API_MODEL") {
			continue
		}
		value, known := evidence.Environment[name]
		if !known {
			return profile.Config{}, 0, fmt.Errorf("effective environment evidence is missing for %s", name)
		}
		if value != nil && *value != "" {
			values[name] = *value
		}
	}
	// Source selection replaces both fields, even when only one was provided.
	if values["PROVIDER_API_ID"] != "" || values["PROVIDER_API_PROTOCOL"] != "" {
		cfg.ID, cfg.Protocol = values["PROVIDER_API_ID"], values["PROVIDER_API_PROTOCOL"]
	}
	for name, dst := range map[string]*string{"PROVIDER_API_BASE": &cfg.BaseURL, "PROVIDER_API_KEY": &cfg.APIKey, "PROVIDER_API_MODEL": &cfg.Model} {
		if value := values[name]; value != "" {
			*dst = value
		}
	}
	if value := strings.TrimSpace(values["PROVIDER_THINKING_EFFORT"]); value != "" {
		switch value {
		case "low", "medium", "high", "xhigh", "max":
			cfg.ThinkingEffort = value
		default:
			return profile.Config{}, 0, errors.New("invalid effective PROVIDER_THINKING_EFFORT")
		}
	}
	if n, err := strconv.Atoi(values["PROVIDER_CONTEXT_WINDOW"]); err == nil && n > 0 {
		window = n
	}
	return cfg, window, nil
}
