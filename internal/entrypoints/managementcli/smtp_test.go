package managementcli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestSMTPSealCommand(t *testing.T) {
	t.Setenv("JUEX_MASTER_KEY", strings.Repeat("ab", 32))
	t.Setenv("TEST_SMTP_PASSWORD", "private-password")
	args := []string{"smtp", "seal", "--address", "smtp.example:587", "--from", "mail@example.test", "--username", "mail", "--password-env", "TEST_SMTP_PASSWORD"}
	for _, invalid := range []bool{false, true} {
		if invalid {
			t.Setenv("TEST_SMTP_PASSWORD", "")
		}
		var out, diagnostics bytes.Buffer
		err := Execute(context.Background(), args, &out, &diagnostics)
		if invalid {
			if err == nil || out.Len() != 0 {
				t.Fatal("missing SMTP credential emitted configuration")
			}
		} else if err != nil || !strings.HasPrefix(out.String(), "JUEX_SMTP_CONFIG=") {
			t.Fatal("could not prepare encrypted deployment setting", err)
		}
		if strings.Contains(out.String()+diagnostics.String(), "private-password") {
			t.Fatal("SMTP secret leaked through CLI")
		}
	}
}
