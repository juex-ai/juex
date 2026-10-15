package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	sdkjsonrpc "github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	sdkChannelMessage  = `{"jsonrpc":"2.0","method":"notifications/claude/channel","params":{"content":"hello","meta":{"event_type":"message","topic":"ops"},"sequence":7}}`
	sdkSentinelMessage = `{"jsonrpc":"2.0","method":"notifications/tools/list_changed","params":{}}`
	sdkResponseMessage = `{"jsonrpc":"2.0","id":1,"result":{}}`
)

func TestSDKNotificationTransportInterceptsChannelNotification(t *testing.T) {
	channel := mustDecodeSDKMessage(t, sdkChannelMessage)
	sentinel := mustDecodeSDKMessage(t, sdkSentinelMessage)
	delegate := &sdkTestConnection{reads: []sdkjsonrpc.Message{channel, sentinel}}
	got := make(chan Notification, 1)

	conn := connectSDKNotificationTransport(t, &sdkTestTransport{conn: delegate}, "remote", func(n Notification) {
		got <- n
	})
	msg, err := conn.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if msg != sentinel {
		t.Fatalf("Read returned %T %p, want sentinel %T %p", msg, msg, sentinel, sentinel)
	}

	select {
	case notification := <-got:
		if notification.ServerName != "remote" ||
			notification.Method != claudeChannelNotificationMethod ||
			notification.EventType != "message" ||
			notification.Content != "hello" {
			t.Fatalf("notification = %+v", notification)
		}
		wantParams := map[string]any{
			"content":  "hello",
			"meta":     map[string]any{"event_type": "message", "topic": "ops"},
			"sequence": float64(7),
		}
		if !reflect.DeepEqual(notification.Params, wantParams) {
			t.Fatalf("notification params = %#v, want %#v", notification.Params, wantParams)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for channel notification")
	}
}

func TestSDKNotificationTransportPassesOtherMessagesThrough(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "other notification", raw: sdkSentinelMessage},
		{
			name: "channel call with id",
			raw:  `{"jsonrpc":"2.0","id":17,"method":"notifications/claude/channel","params":{"content":"call"}}`,
		},
		{name: "response", raw: sdkResponseMessage},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			want := mustDecodeSDKMessage(t, test.raw)
			called := make(chan struct{}, 1)
			conn := connectSDKNotificationTransport(t, &sdkTestTransport{
				conn: &sdkTestConnection{reads: []sdkjsonrpc.Message{want}},
			}, "remote", func(Notification) {
				called <- struct{}{}
			})

			got, err := conn.Read(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("Read returned %T %p, want original %T %p", got, got, want, want)
			}
			select {
			case <-called:
				t.Fatal("callback ran for a pass-through message")
			case <-time.After(25 * time.Millisecond):
			}
		})
	}
}

func TestSDKNotificationTransportMalformedChannelParams(t *testing.T) {
	tests := []struct {
		name         string
		raw          string
		wantCallback bool
		wantParams   map[string]any
	}{
		{
			name: "missing",
			raw:  `{"jsonrpc":"2.0","method":"notifications/claude/channel"}`,
		},
		{
			name: "array",
			raw:  `{"jsonrpc":"2.0","method":"notifications/claude/channel","params":[]}`,
		},
		{
			name:         "null",
			raw:          `{"jsonrpc":"2.0","method":"notifications/claude/channel","params":null}`,
			wantCallback: true,
		},
		{
			name:         "wrong field types",
			raw:          `{"jsonrpc":"2.0","method":"notifications/claude/channel","params":{"content":3,"meta":{"event_type":false}}}`,
			wantCallback: true,
			wantParams: map[string]any{
				"content": float64(3),
				"meta":    map[string]any{"event_type": false},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sentinel := mustDecodeSDKMessage(t, sdkSentinelMessage)
			called := make(chan Notification, 1)
			conn := connectSDKNotificationTransport(t, &sdkTestTransport{
				conn: &sdkTestConnection{reads: []sdkjsonrpc.Message{
					mustDecodeSDKMessage(t, test.raw),
					sentinel,
				}},
			}, "remote", func(n Notification) {
				called <- n
			})

			got, err := conn.Read(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if got != sentinel {
				t.Fatalf("Read returned %T %p, want sentinel %T %p", got, got, sentinel, sentinel)
			}

			select {
			case notification := <-called:
				if !test.wantCallback {
					t.Fatalf("unexpected callback: %+v", notification)
				}
				if notification.EventType != "notification" || notification.Content != "" {
					t.Fatalf("notification = %+v", notification)
				}
				if !reflect.DeepEqual(notification.Params, test.wantParams) {
					t.Fatalf("notification params = %#v, want %#v", notification.Params, test.wantParams)
				}
			case <-time.After(50 * time.Millisecond):
				if test.wantCallback {
					t.Fatal("timed out waiting for callback")
				}
			}
		})
	}
}

func TestSDKNotificationTransportCallbackDoesNotBlockRead(t *testing.T) {
	sentinel := mustDecodeSDKMessage(t, sdkSentinelMessage)
	started := make(chan struct{})
	release := make(chan struct{})
	conn := connectSDKNotificationTransport(t, &sdkTestTransport{
		conn: &sdkTestConnection{reads: []sdkjsonrpc.Message{
			mustDecodeSDKMessage(t, sdkChannelMessage),
			sentinel,
		}},
	}, "remote", func(Notification) {
		close(started)
		<-release
	})

	got, err := conn.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != sentinel {
		t.Fatalf("Read returned %T %p, want sentinel %T %p", got, got, sentinel, sentinel)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("callback did not start")
	}
	close(release)
}

func TestSDKNotificationTransportDelegatesConnection(t *testing.T) {
	connectErr := errors.New("connect failed")
	transport := wrapSDKNotificationTransport(&sdkTestTransport{err: connectErr}, "remote", nil)
	if _, err := transport.Connect(context.Background()); !errors.Is(err, connectErr) {
		t.Fatalf("Connect error = %v, want %v", err, connectErr)
	}

	readErr := errors.New("read failed")
	writeErr := errors.New("write failed")
	closeErr := errors.New("close failed")
	delegate := &sdkTestConnection{
		sessionID: "session-7",
		readErr:   readErr,
		writeErr:  writeErr,
		closeErr:  closeErr,
	}
	conn := connectSDKNotificationTransport(t, &sdkTestTransport{conn: delegate}, "remote", nil)
	if _, err := conn.Read(context.Background()); !errors.Is(err, readErr) {
		t.Fatalf("Read error = %v, want %v", err, readErr)
	}
	msg := mustDecodeSDKMessage(t, sdkSentinelMessage)
	if err := conn.Write(context.Background(), msg); !errors.Is(err, writeErr) {
		t.Fatalf("Write error = %v, want %v", err, writeErr)
	}
	if got := conn.SessionID(); got != "session-7" {
		t.Fatalf("SessionID = %q, want session-7", got)
	}
	if err := conn.Close(); !errors.Is(err, closeErr) {
		t.Fatalf("Close error = %v, want %v", err, closeErr)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("second Close error = %v, want nil", err)
	}
	if !reflect.DeepEqual(delegate.writes, []sdkjsonrpc.Message{msg}) {
		t.Fatalf("delegate writes = %#v, want original message", delegate.writes)
	}
}

func TestSDKNotificationTransportSwallowsChannelWithoutCallback(t *testing.T) {
	sentinel := mustDecodeSDKMessage(t, sdkSentinelMessage)
	conn := connectSDKNotificationTransport(t, &sdkTestTransport{
		conn: &sdkTestConnection{reads: []sdkjsonrpc.Message{
			mustDecodeSDKMessage(t, sdkChannelMessage),
			sentinel,
		}},
	}, "remote", nil)

	got, err := conn.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != sentinel {
		t.Fatalf("Read returned %T %p, want sentinel %T %p", got, got, sentinel, sentinel)
	}
}

func TestClaudeChannelNotificationOverSDKTransports(t *testing.T) {
	tests := []struct {
		name    string
		connect func(*testing.T, func(Notification)) (sdkmcp.Connection, func())
		start   func(*testing.T, sdkmcp.Connection)
	}{
		{
			name:    "command",
			connect: connectSDKCommandTransport,
			start:   func(*testing.T, sdkmcp.Connection) {},
		},
		{
			name:    "streamable HTTP",
			connect: connectSDKStreamableTransport,
			start: func(t *testing.T, conn sdkmcp.Connection) {
				t.Helper()
				if err := conn.Write(context.Background(), mustDecodeSDKMessage(t,
					`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotNotification := make(chan Notification, 1)
			conn, cleanup := test.connect(t, func(n Notification) {
				gotNotification <- n
			})
			defer cleanup()
			test.start(t, conn)

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			msg, err := conn.Read(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := msg.(*sdkjsonrpc.Response); !ok {
				t.Fatalf("Read returned %T, want *jsonrpc.Response", msg)
			}
			select {
			case notification := <-gotNotification:
				if notification.ServerName != test.name ||
					notification.EventType != "message" ||
					notification.Content != "hello" {
					t.Fatalf("notification = %+v", notification)
				}
			case <-ctx.Done():
				t.Fatal("timed out waiting for channel notification")
			}
		})
	}
}

func TestSSEChannelFilterPreservesEventsSDKWouldNotDecode(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{
			name: "non-message event",
			raw:  "event: ping\ndata: " + sdkChannelMessage + "\n\n",
		},
		{
			name: "malformed line",
			raw:  "data: " + sdkChannelMessage + "\nmalformed\n\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called := make(chan Notification, 1)
			filter := newSSEChannelFilter(
				io.NopCloser(strings.NewReader(test.raw)),
				"remote",
				func(notification Notification) {
					called <- notification
				},
			)
			got, err := io.ReadAll(filter)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != test.raw {
				t.Fatalf("filtered event = %q, want original %q", got, test.raw)
			}
			select {
			case notification := <-called:
				t.Fatalf("unexpected notification: %+v", notification)
			default:
			}
		})
	}
}

func TestSSEChannelFilterRejectsOversizedEvent(t *testing.T) {
	const maxEventBytes = 64
	filter := newSSEChannelFilter(
		io.NopCloser(strings.NewReader("data: "+strings.Repeat("x", maxEventBytes)+"\n\n")),
		"remote",
		nil,
	)
	filter.(*sseChannelFilter).maxEventBytes = maxEventBytes

	got, err := io.ReadAll(filter)
	if !errors.Is(err, errSSEEventTooLarge) {
		t.Fatalf("ReadAll error = %v, want %v", err, errSSEEventTooLarge)
	}
	if len(got) != 0 {
		t.Fatalf("ReadAll returned %d bytes from oversized event", len(got))
	}
}

func TestSDKNotificationTransportCommandHelperProcess(t *testing.T) {
	if os.Getenv("JUEX_SDK_NOTIFICATION_HELPER") != "1" {
		return
	}
	fmt.Fprintln(os.Stdout, sdkChannelMessage)
	fmt.Fprintln(os.Stdout, sdkResponseMessage)
	os.Exit(0)
}

func connectSDKNotificationTransport(
	t *testing.T,
	transport sdkmcp.Transport,
	serverName string,
	onNotification func(Notification),
) sdkmcp.Connection {
	t.Helper()
	conn, err := wrapSDKNotificationTransport(transport, serverName, onNotification).Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func connectSDKCommandTransport(t *testing.T, callback func(Notification)) (sdkmcp.Connection, func()) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestSDKNotificationTransportCommandHelperProcess$")
	cmd.Env = append(os.Environ(), "JUEX_SDK_NOTIFICATION_HELPER=1")
	conn := connectSDKNotificationTransport(t, &sdkmcp.CommandTransport{
		Command:           cmd,
		TerminateDuration: time.Second,
	}, "command", callback)
	return conn, func() { _ = conn.Close() }
}

func connectSDKStreamableTransport(t *testing.T, callback func(Notification)) (sdkmcp.Connection, func()) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: %s\n\ndata: %s\n\n", sdkChannelMessage, sdkResponseMessage)
	}))
	conn, err := (&sdkmcp.StreamableClientTransport{
		Endpoint:             server.URL,
		MaxRetries:           -1,
		DisableStandaloneSSE: true,
		HTTPClient: &http.Client{Transport: &remoteDiagnosticRoundTripper{
			base:           http.DefaultTransport,
			serverName:     "streamable HTTP",
			onNotification: callback,
		}},
	}).Connect(context.Background())
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	return conn, func() {
		_ = conn.Close()
		server.Close()
	}
}

func mustDecodeSDKMessage(t *testing.T, raw string) sdkjsonrpc.Message {
	t.Helper()
	msg, err := sdkjsonrpc.DecodeMessage([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return msg
}

type sdkTestTransport struct {
	conn sdkmcp.Connection
	err  error
}

func (t *sdkTestTransport) Connect(context.Context) (sdkmcp.Connection, error) {
	return t.conn, t.err
}

type sdkTestConnection struct {
	mu        sync.Mutex
	reads     []sdkjsonrpc.Message
	writes    []sdkjsonrpc.Message
	sessionID string
	readErr   error
	writeErr  error
	closeErr  error
}

func (c *sdkTestConnection) Read(context.Context) (sdkjsonrpc.Message, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.reads) == 0 {
		if c.readErr != nil {
			return nil, c.readErr
		}
		return nil, io.EOF
	}
	msg := c.reads[0]
	c.reads = c.reads[1:]
	return msg, nil
}

func (c *sdkTestConnection) Write(_ context.Context, msg sdkjsonrpc.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writes = append(c.writes, msg)
	return c.writeErr
}

func (c *sdkTestConnection) Close() error {
	return c.closeErr
}

func (c *sdkTestConnection) SessionID() string {
	return c.sessionID
}
