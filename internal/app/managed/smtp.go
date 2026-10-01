package managed

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/juex-ai/juex/internal/foundation/maildelivery"
	"github.com/juex-ai/juex/internal/foundation/secrets"
)

const smtpSecretScope = "management/smtp-config/v1"

func deploymentSecrets(masterKey string) (*secrets.Box, error) {
	key, err := hex.DecodeString(masterKey)
	if err != nil || len(key) != 32 {
		return nil, errors.New("JUEX_MASTER_KEY must be 64 hexadecimal characters")
	}
	return secrets.New(key)
}

// SealSMTPConfig produces the deployment setting consumed by Management.
// Seal the endpoint together with the credential so neither can be substituted.
func SealSMTPConfig(masterKey string, config maildelivery.Config) (string, error) {
	if _, err := maildelivery.NewSMTP(config); err != nil {
		return "", err
	}
	box, err := deploymentSecrets(masterKey)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(config)
	if err != nil {
		return "", err
	}
	sealed, err := box.Seal(smtpSecretScope, data)
	if err != nil {
		return "", err
	}
	return base64.RawStdEncoding.EncodeToString(sealed), nil
}

func openSMTPConfig(box *secrets.Box, sealed string) (maildelivery.Config, error) {
	var config maildelivery.Config
	data, err := base64.RawStdEncoding.DecodeString(sealed)
	if err == nil {
		data, err = box.Open(smtpSecretScope, data)
	}
	if err == nil {
		err = json.Unmarshal(data, &config)
	}
	if err != nil {
		return maildelivery.Config{}, errors.New("cannot decrypt JUEX_SMTP_CONFIG with the deployment key")
	}
	return config, nil
}
