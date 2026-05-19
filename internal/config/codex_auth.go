package config

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	providerAuthAPIKey = "api_key"
	providerAuthCodex  = "codex"
)

type codexAuthFile struct {
	AuthMode     string           `json:"auth_mode"`
	OpenAIAPIKey string           `json:"OPENAI_API_KEY"`
	Tokens       *codexAuthTokens `json:"tokens"`
}

type codexAuthTokens struct {
	AccessToken  string `json:"access_token"`
	AccountID    string `json:"account_id"`
	IDToken      string `json:"id_token"`
	RefreshToken string `json:"refresh_token"`
}

func resolveProviderAuth(cfg *Config) error {
	auth, err := normalizeProviderAuth(cfg.ProviderAuth)
	if err != nil {
		return err
	}
	cfg.ProviderAuth = auth
	if auth != providerAuthCodex {
		return nil
	}
	if cfg.APIKey != "" {
		return nil
	}
	path, err := codexAuthPath(*cfg)
	if err != nil {
		return err
	}
	key, headers, err := loadCodexAuth(path)
	if err != nil {
		return err
	}
	cfg.APIKey = key
	cfg.ProviderHeaders = mergeStringMap(headers, cfg.ProviderHeaders)
	return nil
}

func normalizeProviderAuth(auth string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(auth)) {
	case "":
		return "", nil
	case providerAuthAPIKey, "api-key", "apikey":
		return providerAuthAPIKey, nil
	case providerAuthCodex, "codex-oauth", "codex_oauth":
		return providerAuthCodex, nil
	default:
		return "", fmt.Errorf("config: unknown provider auth %q", auth)
	}
}

func codexAuthPath(cfg Config) (string, error) {
	if cfg.ProviderCodexAuthFile != "" {
		return expandHomePath(cfg.ProviderCodexAuthFile)
	}
	if home := os.Getenv("CODEX_HOME"); home != "" {
		return filepath.Join(home, "auth.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: cannot resolve Codex auth file: %w", err)
	}
	return filepath.Join(home, ".codex", "auth.json"), nil
}

func expandHomePath(path string) (string, error) {
	if path == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return home, nil
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, path[2:]), nil
	}
	return path, nil
}

func loadCodexAuth(path string) (string, map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil, fmt.Errorf("config: read Codex auth file %s: %w", path, err)
	}
	var auth codexAuthFile
	if err := json.Unmarshal(data, &auth); err != nil {
		return "", nil, fmt.Errorf("config: parse Codex auth file %s: %w", path, err)
	}
	if key := strings.TrimSpace(auth.OpenAIAPIKey); key != "" {
		return key, nil, nil
	}
	if auth.Tokens == nil || strings.TrimSpace(auth.Tokens.AccessToken) == "" {
		return "", nil, fmt.Errorf("config: Codex auth file %s has no OPENAI_API_KEY or tokens.access_token", path)
	}
	headers := map[string]string{}
	if accountID := firstNonEmpty(auth.Tokens.AccountID, codexAccountIDFromIDToken(auth.Tokens.IDToken)); accountID != "" {
		headers["ChatGPT-Account-ID"] = accountID
	}
	if codexFedRAMPFromIDToken(auth.Tokens.IDToken) {
		headers["X-OpenAI-Fedramp"] = "true"
	}
	return strings.TrimSpace(auth.Tokens.AccessToken), headers, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

func codexAccountIDFromIDToken(idToken string) string {
	claims, ok := codexIDTokenClaims(idToken)
	if !ok {
		return ""
	}
	auth, _ := claims["https://api.openai.com/auth"].(map[string]any)
	accountID, _ := auth["chatgpt_account_id"].(string)
	return accountID
}

func codexFedRAMPFromIDToken(idToken string) bool {
	claims, ok := codexIDTokenClaims(idToken)
	if !ok {
		return false
	}
	auth, _ := claims["https://api.openai.com/auth"].(map[string]any)
	fedramp, _ := auth["chatgpt_account_is_fedramp"].(bool)
	return fedramp
}

func codexIDTokenClaims(idToken string) (map[string]any, bool) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 || parts[1] == "" {
		return nil, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, false
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, false
	}
	return claims, true
}
