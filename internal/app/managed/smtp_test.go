package managed

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/maildelivery"
)

func TestSMTPConfigurationEncryption(t *testing.T) {
	key := hex.EncodeToString([]byte(strings.Repeat("k", 32)))
	box, err := deploymentSecrets(key)
	if err != nil {
		t.Fatal(err)
	}
	config := maildelivery.Config{Address: "mail.example:465", From: "juex@example.test", Username: "mailer", Password: "sensitive-smtp-password", TLSMode: "tls"}
	sealed, err := SealSMTPConfig(key, config)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawStdEncoding.DecodeString(sealed)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), config.Password) || strings.Contains(sealed, config.Password) {
		t.Fatal("SMTP credential is not encrypted")
	}
	recovered, err := openSMTPConfig(box, sealed)
	if err != nil || recovered != config {
		t.Fatal("encrypted SMTP configuration did not round trip")
	}
	second, err := SealSMTPConfig(key, config)
	if err != nil || second == sealed {
		t.Fatal("SMTP encryption reused a nonce")
	}
	wrongBox, err := deploymentSecrets(hex.EncodeToString([]byte(strings.Repeat("x", 32))))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openSMTPConfig(wrongBox, sealed); err == nil {
		t.Fatal("wrong deployment key accepted")
	}
	raw[len(raw)-1] ^= 1
	for _, invalid := range []string{base64.RawStdEncoding.EncodeToString(raw), "invalid!", config.Password} {
		if _, err := openSMTPConfig(box, invalid); err == nil || strings.Contains(err.Error(), config.Password) {
			t.Fatal("invalid ciphertext accepted or leaked")
		}
	}
	config.TLSMode = "plain"
	if _, err := SealSMTPConfig(key, config); err == nil || strings.Contains(err.Error(), config.Password) {
		t.Fatal("unencrypted authenticated SMTP accepted or leaked")
	}
}
