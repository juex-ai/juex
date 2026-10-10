package migration

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	"github.com/juex-ai/juex/internal/providers/profile"
)

func modelCodexAuth(cfg *profile.Config, evidence ModelEvidence) (bool, error) {
	if (strings.TrimSpace(cfg.ID) != "openai-codex" && strings.TrimSpace(cfg.Protocol) != "openai-codex/responses") || cfg.APIKey != "" {
		return false, nil
	}
	home, known := evidence.Environment["CODEX_HOME"]
	if !known {
		return false, errors.New("effective environment evidence is missing for CODEX_HOME")
	}
	var path string
	if home != nil && *home != "" {
		path = filepath.Join(*home, "auth.json")
		if !filepath.IsAbs(path) {
			if !absoluteConfigPath(evidence.ProcessWorkingDirectory) {
				return false, errors.New("source process working directory is required for relative Codex auth")
			}
			path = filepath.Join(evidence.ProcessWorkingDirectory, path)
		}
	} else {
		if !absoluteConfigPath(evidence.UserHome) {
			return false, errors.New("source user Home evidence is required for Codex auth")
		}
		path = filepath.Join(evidence.UserHome, ".codex", "auth.json")
	}
	file := evidence.CodexAuth
	if file == nil || file.Path != path || !absoluteConfigPath(file.Path) {
		return false, errors.New("captured Codex auth does not match the source lookup path")
	}
	if len(file.Data) > 1<<20 || verifyConfigFile(*file) != nil {
		return false, errors.New("captured Codex auth size/hash is invalid")
	}
	var auth struct {
		APIKey string `json:"OPENAI_API_KEY"`
		Tokens *struct {
			AccessToken string `json:"access_token"`
			AccountID   string `json:"account_id"`
			IDToken     string `json:"id_token"`
		} `json:"tokens"`
	}
	if json.Unmarshal(file.Data, &auth) != nil {
		return false, errors.New("captured Codex auth JSON is invalid")
	}
	if key := strings.TrimSpace(auth.APIKey); key != "" {
		cfg.APIKey = key
		return true, nil
	}
	if auth.Tokens == nil || strings.TrimSpace(auth.Tokens.AccessToken) == "" {
		return false, errors.New("captured Codex auth has no usable credential")
	}
	cfg.APIKey = strings.TrimSpace(auth.Tokens.AccessToken)
	headers := map[string]string{}
	account, fedramp := codexTokenRoute(auth.Tokens.IDToken)
	if explicit := strings.TrimSpace(auth.Tokens.AccountID); explicit != "" {
		account = explicit
	}
	if account = strings.TrimSpace(account); account != "" {
		headers["ChatGPT-Account-ID"] = account
	}
	if fedramp {
		headers["X-OpenAI-Fedramp"] = "true"
	}
	for name, value := range cfg.Headers {
		if value == "" {
			delete(headers, name)
		} else {
			headers[name] = value
		}
	}
	cfg.Headers = headers
	switch strings.TrimSpace(cfg.Protocol) {
	case "", "openai/responses", "openai/chat":
		cfg.Protocol = "openai-codex/responses"
	}
	if strings.TrimSpace(cfg.ID) == "" || strings.TrimSpace(cfg.ID) == "openai" {
		cfg.ID = "openai-codex"
	}
	return true, nil
}

// The fixed source reads routing metadata from the ID token without verifying
// its signature. This is not a new trust decision or proof of token validity.
func codexTokenRoute(token string) (string, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", false
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", false
	}
	var claims map[string]any
	if json.Unmarshal(data, &claims) != nil {
		return "", false
	}
	auth, _ := claims["https://api.openai.com/auth"].(map[string]any)
	account, _ := auth["chatgpt_account_id"].(string)
	fedramp, _ := auth["chatgpt_account_is_fedramp"].(bool)
	return account, fedramp
}
