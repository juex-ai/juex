//go:build postgres

package e2e

import (
	"bytes"
	"context"
	"encoding/hex"
	"io"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/app/managed"
	"github.com/juex-ai/juex/internal/entrypoints/managementcli"
	"github.com/juex-ai/juex/internal/management"
)

func TestManagementEncryptedSMTPDeployment(t *testing.T) {
	pool, _ := managementDatabase(t)
	key := hex.EncodeToString([]byte(strings.Repeat("k", 32)))
	t.Setenv("JUEX_MASTER_KEY", key)
	t.Setenv("JUEX_SMTP_CREDENTIAL", "test-smtp-private-credential")
	ctx := context.Background()
	config := managed.ManagementConfig{DatabaseURL: pool.Config().ConnString(), MasterKey: key, PublicURL: "https://juex.example.test"}
	var adminID, tenantID string
	for _, recipient := range []string{"first@example.test", "replacement@example.test"} {
		address, messages := smtpCapture(t)
		var out, diagnostics bytes.Buffer
		err := managementcli.Execute(ctx, []string{"smtp", "seal", "--address", address, "--from", "juex@example.test", "--tls-mode", "plain"}, &out, &diagnostics)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String()+diagnostics.String(), "test-smtp-private-credential") {
			t.Fatal("CLI exposed SMTP credential")
		}
		config.SMTP = strings.TrimPrefix(strings.TrimSpace(out.String()), "JUEX_SMTP_CONFIG=")
		app, err := managed.OpenManagement(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(app.Close)
		if adminID == "" {
			admin, err := app.Directory.CreateUser(ctx, "admin@example.test")
			if err != nil {
				t.Fatal(err)
			}
			tenant, err := app.Directory.CreateTenant(ctx, "Mail", admin.ID)
			if err != nil {
				t.Fatal(err)
			}
			adminID, tenantID = admin.ID, tenant.ID
		}
		_, token, err := app.Directory.Invite(ctx, adminID, tenantID, recipient, management.Member, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		worked, err := app.Directory.DeliverMail(ctx, app.Mailer)
		if err != nil || !worked {
			t.Fatal("encrypted SMTP configuration failed delivery", err)
		}
		select {
		case message := <-messages:
			if !strings.Contains(message, recipient) || !strings.Contains(message, token) {
				t.Fatal("SMTP invitation content missing")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("SMTP did not receive invitation")
		}
		app.Close()
	}
	config.MasterKey = hex.EncodeToString([]byte(strings.Repeat("x", 32)))
	if app, err := managed.OpenManagement(ctx, config); err == nil {
		app.Close()
		t.Fatal("wrong SMTP key allowed startup")
	}
	t.Setenv("JUEX_DATABASE_URL", config.DatabaseURL)
	t.Setenv("JUEX_PUBLIC_URL", config.PublicURL)
	t.Setenv("JUEX_SMTP_CONFIG", "invalid-ciphertext")
	if err := managementcli.Execute(ctx, []string{"migrate"}, io.Discard, io.Discard); err == nil {
		t.Fatal("CLI startup ignored invalid SMTP configuration")
	}
	t.Setenv("JUEX_SMTP_CONFIG", config.SMTP)
	if err := managementcli.Execute(ctx, []string{"migrate"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	config.MasterKey, config.SMTP = key, ""
	app, err := managed.OpenManagement(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if app.Mailer != nil {
		t.Fatal("removing SMTP configuration did not disable delivery")
	}
	if _, _, err := app.Directory.Invite(ctx, adminID, tenantID, "copy-only@example.test", management.Member, time.Hour); err != nil {
		t.Fatal(err)
	}
	items, err := app.Directory.Invitations(ctx, adminID, tenantID)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Email == "copy-only@example.test" && item.DeliveryStatus != "manual" {
			t.Fatal("disabled SMTP still queued invitation", item.DeliveryStatus)
		}
	}
}

func smtpCapture(t *testing.T) (string, <-chan string) {
	t.Helper()
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	messages := make(chan string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		wire := textproto.NewConn(conn)
		_ = wire.PrintfLine("220 localhost SMTP test")
		for {
			line, err := wire.ReadLine()
			if err != nil {
				return
			}
			switch {
			case line == "DATA":
				_ = wire.PrintfLine("354 send mail")
				body, err := io.ReadAll(wire.DotReader())
				if err != nil {
					return
				}
				message, err := mail.ReadMessage(bytes.NewReader(body))
				if err != nil {
					return
				}
				decoded, err := io.ReadAll(quotedprintable.NewReader(message.Body))
				if err != nil {
					return
				}
				messages <- message.Header.Get("To") + "\n" + string(decoded)
				_ = wire.PrintfLine("250 accepted")
			case line == "QUIT":
				_ = wire.PrintfLine("221 bye")
				return
			default:
				_ = wire.PrintfLine("250 OK")
			}
		}
	}()
	return listener.Addr().String(), messages
}
