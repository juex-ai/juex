// Package maildelivery implements SMTP transport without owning message state.
package maildelivery

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
)

type Message struct {
	ID, To, Subject, Body string
}

type Config struct {
	Address, From, Username, Password string
	// TLSMode is starttls (default), tls, or explicit plaintext for a local relay.
	TLSMode string
}

type SMTP struct {
	config Config
	host   string
}

func NewSMTP(config Config) (*SMTP, error) {
	host, _, err := net.SplitHostPort(config.Address)
	if err != nil || host == "" {
		return nil, errors.New("SMTP address must be host:port")
	}
	from, err := mail.ParseAddress(config.From)
	if err != nil || from.Address != config.From {
		return nil, errors.New("SMTP sender must be an email address")
	}
	if config.TLSMode == "" {
		config.TLSMode = "starttls"
	}
	if config.TLSMode != "starttls" && config.TLSMode != "tls" && config.TLSMode != "plain" {
		return nil, errors.New("SMTP TLS mode must be starttls, tls, or plain")
	}
	if config.TLSMode == "plain" && config.Username != "" {
		return nil, errors.New("SMTP authentication requires TLS")
	}
	return &SMTP{config: config, host: host}, nil
}

func (s *SMTP) Send(ctx context.Context, message Message) error {
	recipient, err := mail.ParseAddress(message.To)
	if err != nil || recipient.Address != message.To || strings.ContainsAny(message.Subject+message.ID, "\r\n") {
		return errors.New("invalid mail envelope")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	connection, err := (&net.Dialer{}).DialContext(ctx, "tcp", s.config.Address)
	if err != nil {
		return err
	}
	defer func() { _ = connection.Close() }()
	deadline, _ := ctx.Deadline()
	if err := connection.SetDeadline(deadline); err != nil {
		return err
	}
	rawConnection := connection
	stop := context.AfterFunc(ctx, func() { _ = rawConnection.Close() })
	defer stop()
	tlsConfig := &tls.Config{ServerName: s.host, MinVersion: tls.VersionTLS12}
	if s.config.TLSMode == "tls" {
		tlsConnection := tls.Client(connection, tlsConfig)
		if err := tlsConnection.HandshakeContext(ctx); err != nil {
			return err
		}
		connection = tlsConnection
	}
	client, err := smtp.NewClient(connection, s.host)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	if s.config.TLSMode == "starttls" {
		if err := client.StartTLS(tlsConfig); err != nil {
			return err
		}
	}
	if s.config.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", s.config.Username, s.config.Password, s.host)); err != nil {
			return err
		}
	}
	if err := client.Mail(s.config.From); err != nil {
		return err
	}
	if err := client.Rcpt(message.To); err != nil {
		return err
	}
	data, err := client.Data()
	if err != nil {
		return err
	}
	var body bytes.Buffer
	fmt.Fprintf(&body, "From: %s\r\nTo: %s\r\nSubject: %s\r\nMessage-ID: <%s@juex.local>\r\nDate: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n", s.config.From, message.To, mime.QEncoding.Encode("utf-8", message.Subject), message.ID, time.Now().UTC().Format(time.RFC1123Z))
	encoded := quotedprintable.NewWriter(&body)
	if _, err := encoded.Write([]byte(message.Body)); err != nil {
		return err
	}
	if err := encoded.Close(); err != nil {
		return err
	}
	if _, err := data.Write(body.Bytes()); err != nil {
		return err
	}
	// A successful DATA response confirms acceptance. A later QUIT failure must
	// not turn that confirmation into an unnecessary retry.
	if err := data.Close(); err != nil {
		return err
	}
	_ = client.Quit()
	return nil
}
