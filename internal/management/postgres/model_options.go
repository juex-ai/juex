package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/management"
)

// Codex can infer its account from a token. Managed configuration instead pins
// that route explicitly so credential rotation cannot silently switch accounts.
func explicitCodexAccount(headers map[string]string) bool {
	count := 0
	for name, value := range headers {
		if !strings.EqualFold(name, "ChatGPT-Account-ID") {
			continue
		}
		if (name != "ChatGPT-Account-ID" && name != "chatgpt-account-id") || strings.TrimSpace(value) == "" || strings.Contains(value, "${") {
			return false
		}
		count++
	}
	return count == 1
}

func (d *Directory) modelOptions(id string, cipher []byte) (management.ModelOptions, error) {
	var options management.ModelOptions
	if len(cipher) != 0 {
		data, err := d.config.Secrets.Open("model-options:"+id, cipher)
		if err != nil {
			return options, err
		}
		if err := json.Unmarshal(data, &options); err != nil {
			return options, err
		}
	}
	return options.Normalized(), nil
}

// Route or behavior changes revoke a frozen call, including changing an account
// header and then changing it back. API key rotation alone keeps its route epoch.
func (d *Directory) modelConfigurationChanged(ctx context.Context, tx pgx.Tx, id string, config management.ModelConfiguration, encoded []byte) (bool, error) {
	var protocol llm.Protocol
	var endpoint string
	var contextWindow, maxOutput int
	var cipher []byte
	err := tx.QueryRow(ctx, `SELECT protocol,endpoint,context_window,max_output,options_cipher FROM management.models WHERE id=$1 FOR UPDATE`, id).Scan(&protocol, &endpoint, &contextWindow, &maxOutput, &cipher)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	options, err := d.modelOptions(id, cipher)
	if err != nil {
		return false, err
	}
	previous, err := json.Marshal(options)
	if err != nil {
		return false, err
	}
	return protocol != config.Protocol || endpoint != config.Endpoint || contextWindow != config.ContextWindow || maxOutput != config.MaxOutput || !bytes.Equal(previous, encoded), nil
}
