package config

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

type codexAuthCredential struct {
	key     string
	headers map[string]string
	chatGPT bool
}

func resolveCodexAuth(cfg *Config) error {
	if !providerUsesCodexAuth(*cfg) {
		return nil
	}
	if cfg.APIKey != "" {
		return nil
	}
	path, err := codexAuthPath()
	if err != nil {
		return err
	}
	cred, err := loadCodexAuth(path)
	if err != nil {
		return err
	}
	cfg.APIKey = cred.key
	cfg.ProviderHeaders = mergeStringMap(cred.headers, cfg.ProviderHeaders)
	if cred.chatGPT {
		routeCodexChatGPTProvider(cfg)
	}
	return nil
}

func providerUsesCodexAuth(cfg Config) bool {
	switch strings.TrimSpace(cfg.ProviderID) {
	case "openai-codex":
		return true
	}
	return strings.TrimSpace(cfg.ProviderProtocol) == "openai-codex/responses"
}

func routeCodexChatGPTProvider(cfg *Config) {
	switch strings.TrimSpace(cfg.ProviderProtocol) {
	case "", "openai/responses", "openai/chat":
		cfg.ProviderProtocol = "openai-codex/responses"
	}
	if strings.TrimSpace(cfg.ProviderID) == "" || strings.TrimSpace(cfg.ProviderID) == "openai" {
		cfg.ProviderID = "openai-codex"
	}
}

func codexAuthPath() (string, error) {
	if home := os.Getenv("CODEX_HOME"); home != "" {
		return filepath.Join(home, "auth.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: cannot resolve Codex auth file: %w", err)
	}
	return filepath.Join(home, ".codex", "auth.json"), nil
}

func loadCodexAuth(path string) (codexAuthCredential, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return codexAuthCredential{}, fmt.Errorf("config: read Codex auth file %s: %w", path, err)
	}
	var auth codexAuthFile
	if err := json.Unmarshal(data, &auth); err != nil {
		return codexAuthCredential{}, fmt.Errorf("config: parse Codex auth file %s: %w", path, err)
	}
	if key := strings.TrimSpace(auth.OpenAIAPIKey); key != "" {
		return codexAuthCredential{key: key}, nil
	}
	if auth.Tokens == nil || strings.TrimSpace(auth.Tokens.AccessToken) == "" {
		return codexAuthCredential{}, fmt.Errorf("config: Codex auth file %s has no OPENAI_API_KEY or tokens.access_token", path)
	}
	headers := map[string]string{}
	if accountID := firstNonEmpty(auth.Tokens.AccountID, codexAccountIDFromIDToken(auth.Tokens.IDToken)); accountID != "" {
		headers["ChatGPT-Account-ID"] = accountID
	}
	if codexFedRAMPFromIDToken(auth.Tokens.IDToken) {
		headers["X-OpenAI-Fedramp"] = "true"
	}
	return codexAuthCredential{
		key:     strings.TrimSpace(auth.Tokens.AccessToken),
		headers: headers,
		chatGPT: true,
	}, nil
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
