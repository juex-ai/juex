package maildelivery

import (
	"context"
	"io"
	"net"
	"net/mail"
	"net/textproto"
	"strings"
	"testing"
	"time"
)

func TestSMTPDeliveryAndRequiredTLS(t *testing.T) {
	for _, mode := range []string{"plain", "starttls"} {
		t.Run(mode, func(t *testing.T) {
			listener, err := net.Listen("tcp", "0.0.0.0:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = listener.Close() })
			messages := make(chan string, 1)
			done := make(chan struct{})
			go func() {
				defer close(done)
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer func() { _ = conn.Close() }()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				wire := textproto.NewConn(conn)
				_ = wire.PrintfLine("220 localhost SMTP test")
				for {
					line, err := wire.ReadLine()
					if err != nil {
						return
					}
					switch {
					case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"), strings.HasPrefix(line, "MAIL FROM:"), strings.HasPrefix(line, "RCPT TO:"):
						_ = wire.PrintfLine("250 OK")
					case line == "DATA":
						_ = wire.PrintfLine("354 continue")
						data, err := wire.ReadDotBytes()
						if err != nil {
							return
						}
						messages <- string(data)
						_ = wire.PrintfLine("250 queued")
					case line == "QUIT":
						_ = wire.PrintfLine("221 bye")
						return
					default:
						_ = wire.PrintfLine("502 unsupported")
					}
				}
			}()
			sender, err := NewSMTP(Config{Address: listener.Addr().String(), From: "juex@example.test", TLSMode: mode})
			if err != nil {
				t.Fatal(err)
			}
			err = sender.Send(context.Background(), Message{ID: "stable-operation", To: "member@example.test", Subject: "JueX invitation", Body: "https://example.test/join#token=one-use\n"})
			<-done
			if mode == "starttls" {
				if err == nil {
					t.Fatal("silently downgraded SMTP encryption")
				}
				if len(messages) != 0 {
					t.Fatal("sent message without required TLS")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			message, err := mail.ReadMessage(strings.NewReader(<-messages))
			if err != nil {
				t.Fatal(err)
			}
			if message.Header.Get("Message-ID") != "<stable-operation@juex.local>" || message.Header.Get("To") != "member@example.test" {
				t.Fatal(message.Header)
			}
			body, err := io.ReadAll(message.Body)
			if err != nil || !strings.Contains(string(body), "token=3Done-use") {
				t.Fatalf("body encoding: %s %v", body, err)
			}
		})
	}
}

func TestSMTPContextAndConfiguration(t *testing.T) {
	if _, err := NewSMTP(Config{Address: "localhost:25", From: "juex@example.test", TLSMode: "plain", Username: "user"}); err == nil {
		t.Fatal("plaintext credentials allowed")
	}
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = io.Copy(io.Discard, conn)
	}()
	sender, err := NewSMTP(Config{Address: listener.Addr().String(), From: "juex@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := sender.Send(ctx, Message{ID: "id", To: "user@example.test", Subject: "test"}); err == nil {
		t.Fatal("stalled SMTP ignored context deadline")
	}
	<-done
}
