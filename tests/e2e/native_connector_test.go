//go:build linux || darwin

package e2e

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/juex-ai/juex/internal/execution/connector"
	"github.com/juex-ai/juex/internal/execution/native"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

func TestNativeConnectorDisconnectRejoinsOriginalOperationAndRevokes(t *testing.T) {
	config := nativeConfig(t)
	engine := openNative(t, config)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	connections := make(chan *websocket.Conn, 8)
	errorsCh := make(chan error, 8)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/device/connect" || r.Header.Get("Authorization") != "Bearer fixture-device-credential" {
			http.Error(w, "denied", 401)
			return
		}
		connection, err := websocket.Accept(w, r, nil)
		if err != nil {
			errorsCh <- err
			return
		}
		defer connection.CloseNow()
		var hello execprotocol.Envelope
		if err := wsjson.Read(ctx, connection, &hello); err != nil {
			errorsCh <- err
			return
		}
		if hello.Type != "hello" || hello.Version != execprotocol.Version || hello.Environment == nil || hello.Environment.ID != config.EnvironmentID {
			errorsCh <- errors.New("invalid device hello")
			return
		}
		if err := wsjson.Write(ctx, connection, execprotocol.Envelope{Version: execprotocol.Version, Type: "welcome", Grants: config.Grants}); err != nil {
			errorsCh <- err
			return
		}
		select {
		case connections <- connection:
		case <-ctx.Done():
			return
		}
		<-ctx.Done()
	}))
	defer func() { cancel(); server.Close() }()
	done := make(chan error, 1)
	go func() {
		done <- connector.Run(ctx, connector.Config{URL: server.URL, Token: "fixture-device-credential", Engine: engine, Environment: execprotocol.Environment{ID: config.EnvironmentID, OS: "test"}, HTTPClient: server.Client()})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("device connector did not stop")
		}
	})
	getConnection := func() *websocket.Conn {
		t.Helper()
		select {
		case connection := <-connections:
			return connection
		case err := <-errorsCh:
			t.Fatal(err)
		case err := <-done:
			t.Fatal("device exited", err)
		case <-time.After(5 * time.Second):
			t.Fatal("device did not connect")
		}
		return nil
	}
	call := func(connection *websocket.Conn, request execprotocol.Envelope) execprotocol.Envelope {
		t.Helper()
		request.Version = execprotocol.Version
		callCtx, stop := context.WithTimeout(ctx, 5*time.Second)
		defer stop()
		if err := wsjson.Write(callCtx, connection, request); err != nil {
			t.Fatal(err)
		}
		var reply execprotocol.Envelope
		if err := wsjson.Read(callCtx, connection, &reply); err != nil {
			t.Fatal(err)
		}
		if reply.ID != request.ID {
			t.Fatal("reply lost correlation", reply.ID, request.ID)
		}
		return reply
	}
	first := getConnection()
	request := nativeRequest(t, "survives-disconnect", "exec_command", native.CommandArguments{Command: "printf start; printf x >> counter; sleep 0.6; printf done"})
	if reply := call(first, execprotocol.Envelope{ID: "submit-first", Type: "submit", Request: &request}); reply.Error != "" {
		t.Fatal(reply)
	}
	nativeEventually(t, engine, request.ID, func(snapshot execprotocol.Snapshot) bool { return strings.Contains(snapshot.Output, "start") })
	if err := first.CloseNow(); err != nil {
		t.Fatal(err)
	}
	result := nativeEventually(t, engine, request.ID, func(snapshot execprotocol.Snapshot) bool { return snapshot.State.Terminal() })
	if result.State != execprotocol.Completed || result.Output != "startdone" {
		t.Fatal("disconnect cancelled child process", result)
	}
	second := getConnection()
	if reply := call(second, execprotocol.Envelope{ID: "resend", Type: "submit", Request: &request}); reply.Error != "" || reply.Snapshot == nil || reply.Snapshot.State != execprotocol.Completed {
		t.Fatal(reply)
	}
	if reply := call(second, execprotocol.Envelope{ID: "cursor", Type: "query", AgentID: request.AgentID, OperationID: request.ID, Cursor: 5}); reply.Snapshot == nil || reply.Snapshot.Output != "done" {
		t.Fatal(reply)
	}
	data, err := os.ReadFile(filepath.Join(config.WorkingDirectory, "counter"))
	if err != nil || string(data) != "x" {
		t.Fatal("reconnection repeated side effect", string(data), err)
	}
	long := nativeRequest(t, "revoke-running", "exec_command", native.CommandArguments{Command: "printf running; sleep 120"})
	if reply := call(second, execprotocol.Envelope{ID: "start-long", Type: "submit", Request: &long}); reply.Error != "" {
		t.Fatal(reply)
	}
	nativeEventually(t, engine, long.ID, func(snapshot execprotocol.Snapshot) bool { return strings.Contains(snapshot.Output, "running") })
	if reply := call(second, execprotocol.Envelope{ID: "revoke", Type: "grants", Grants: map[string][]execprotocol.Capability{"agent-one": {execprotocol.Files}, "agent-two": {execprotocol.Shell}, "unapproved-agent": {execprotocol.Shell}}}); reply.Error != "" {
		t.Fatal(reply)
	}
	if result := nativeEventually(t, engine, long.ID, func(snapshot execprotocol.Snapshot) bool { return snapshot.State.Terminal() }); result.State != execprotocol.Cancelled {
		t.Fatal(result)
	}
	for _, actor := range []string{"agent-one", "agent-two", "unapproved-agent"} {
		denied := nativeRequest(t, "denied-"+actor, "exec_command", native.CommandArguments{Command: "printf forbidden"})
		denied.AgentID = actor
		if reply := call(second, execprotocol.Envelope{ID: "deny-" + actor, Type: "submit", Request: &denied}); reply.Error != "denied" {
			t.Fatal("remote grant expanded local approval", reply)
		}
	}
}

func TestNativeConnectorRejectsProtocolMismatch(t *testing.T) {
	config := nativeConfig(t)
	engine := openNative(t, config)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer connection.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		var hello execprotocol.Envelope
		if err := wsjson.Read(ctx, connection, &hello); err != nil {
			return
		}
		_ = wsjson.Write(ctx, connection, execprotocol.Envelope{Version: 2, Type: "welcome"})
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := connector.Run(ctx, connector.Config{URL: server.URL, Token: "test", Engine: engine, Environment: execprotocol.Environment{ID: config.EnvironmentID}, HTTPClient: server.Client()})
	if !errors.Is(err, execprotocol.ErrVersion) {
		t.Fatal("incompatible device retried instead of requiring upgrade", err)
	}
}
