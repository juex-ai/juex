package native

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/juex-ai/juex/internal/foundation/command"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/processidentity"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type MCPArguments struct {
	execprotocol.MCPRemote
	Extension        *execprotocol.ExtensionContext `json:"extension,omitempty"`
	ConnectionID     string                         `json:"connection_id"`
	Command          string                         `json:"command"`
	Args             []string                       `json:"args"`
	WorkingDirectory string                         `json:"working_directory"`
	Environment      map[string]string              `json:"environment"`
	Name             string                         `json:"name"`
	Arguments        map[string]any                 `json:"arguments"`
	Cursor           string                         `json:"cursor"`
}

func (e *Engine) connectMCP(ctx context.Context, op *operation) error {
	select {
	case e.connections <- struct{}{}:
		defer func() { <-e.connections }()
	default:
		return errors.New("MCP connection limit reached")
	}
	var args MCPArguments
	if err := decodeArguments(op.record.Request.Arguments, &args); err != nil {
		return err
	}
	if args.Validate() != nil || args.Kind() == "stdio" && args.Command == "" || args.Kind() != "stdio" && (args.Command != "" || len(args.Args) != 0 || len(args.Environment) != 0) {
		return execprotocol.ErrInvalid
	}
	connectionCtx, cancelConnection := context.WithCancel(ctx)
	defer cancelConnection()
	transport, cmd, cleanup, err := e.mcpTransport(connectionCtx, op, args)
	if err != nil {
		return err
	}
	defer cleanup()
	writer := outputWriter{engine: e, operation: op}
	client := mcp.NewClient(&mcp.Implementation{Name: "juex-executor", Version: "1"}, &mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{Experimental: map[string]any{"claude/channel": map[string]any{}}}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	setupCtx, cancelSetup := context.WithTimeout(ctx, 15*time.Second)
	stopSetup := context.AfterFunc(setupCtx, cancelConnection)
	session, err := client.Connect(setupCtx, transport, nil)
	stopSetup()
	cancelSetup()
	if err != nil {
		transport.close()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if args.Kind() != "stdio" {
			if captured := transport.capture.failure(); captured != nil {
				return captured
			}
			return errors.New("MCP HTTP initialization failed")
		}
		return err
	}
	defer func() { _ = session.Close() }()
	e.mu.Lock()
	op.session = session
	if cmd != nil {
		op.record.PID = cmd.Process.Pid
		op.record.ProcessIdentity, _ = processidentity.Fingerprint(cmd.Process.Pid)
	}
	initialized := session.InitializeResult()
	op.record.MCP = &execprotocol.MCPConnection{Transport: args.Kind(), ConnectedAt: time.Now().UTC(), ProtocolVersion: mcpLabel(initialized.ProtocolVersion)}
	if initialized.ServerInfo != nil {
		op.record.MCP.ServerName, op.record.MCP.ServerVersion = mcpLabel(initialized.ServerInfo.Name), mcpLabel(initialized.ServerInfo.Version)
	}
	err = e.save(&op.record)
	e.mu.Unlock()
	if err != nil {
		return err
	}
	if err := writeMCPEvent(writer, map[string]any{"type": "connected", "connection_id": op.record.Request.ID}); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- session.Wait() }()
	select {
	case <-ctx.Done():
		_ = session.Close()
		<-done
		return ctx.Err()
	case err := <-done:
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if captured := transport.capture.failure(); captured != nil {
			return captured
		}
		if err != nil && args.Kind() != "stdio" {
			return errors.New("MCP HTTP session disconnected")
		}
		return err
	}
}

func mcpLabel(value string) string {
	runes := []rune(value)
	return string(runes[:min(len(runes), 512)])
}

func (e *Engine) mcpTransport(ctx context.Context, op *operation, args MCPArguments) (*notificationTransport, *exec.Cmd, func(), error) {
	writer := outputWriter{engine: e, operation: op}
	if args.Kind() != "stdio" {
		endpoint, _ := url.Parse(args.URL) // Validated before constructing transport.
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.ResponseHeaderTimeout = 15 * time.Second
		capture := &mcpCapture{}
		client := &http.Client{Transport: mcpHeaders{base: transport, origin: endpoint, headers: args.Headers, writer: writer, lifetime: ctx, capture: capture}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		var delegate mcp.Transport = &mcp.StreamableClientTransport{Endpoint: args.URL, HTTPClient: client, MaxRetries: -1}
		if args.Kind() == "sse" {
			delegate = &mcp.SSEClientTransport{Endpoint: args.URL, HTTPClient: client}
		}
		return &notificationTransport{delegate: delegate, writer: writer, connectionContext: ctx, capture: capture}, nil, transport.CloseIdleConnections, nil
	}
	directory, err := e.workingDirectory(args.WorkingDirectory)
	if err != nil {
		return nil, nil, nil, err
	}
	environment, err := e.operationEnvironment(op, args.Environment)
	if err != nil {
		return nil, nil, nil, err
	}
	cmd := exec.CommandContext(ctx, args.Command, args.Args...)
	cmd.Dir, cmd.Env, cmd.WaitDelay = directory, environment, 2*time.Second
	if err := e.extensionCommand(cmd, op.record.Request.AgentID, args.Extension); err != nil {
		return nil, nil, nil, err
	}
	if err := configureProcessUser(cmd, e.config.ProcessUser); err != nil {
		return nil, nil, nil, err
	}
	command.ConfigureContext(cmd)
	cmd.Stderr = mcpStderr{writer: writer}
	return &notificationTransport{delegate: &mcp.CommandTransport{Command: cmd, TerminateDuration: time.Second}, writer: writer}, cmd, func() {}, nil
}

type mcpHeaders struct {
	base     http.RoundTripper
	origin   *url.URL
	headers  map[string]string
	writer   outputWriter
	lifetime context.Context
	capture  *mcpCapture
}

func (t mcpHeaders) RoundTrip(request *http.Request) (*http.Response, error) {
	// An SSE server can advertise a separate POST endpoint. It must not carry
	// configured credentials to an unrelated origin, even without a redirect.
	if request.URL.Scheme != t.origin.Scheme || !strings.EqualFold(request.URL.Host, t.origin.Host) {
		return nil, errors.New("MCP endpoint changed origin")
	}
	ctx, cancel := context.WithCancel(request.Context())
	stop := context.AfterFunc(t.lifetime, cancel)
	cleanup := func() { stop(); cancel() }
	if t.lifetime.Err() != nil {
		cleanup()
		return nil, t.lifetime.Err()
	}
	copy := request.Clone(ctx)
	for name, value := range t.headers {
		copy.Header.Set(name, value)
	}
	response, err := t.base.RoundTrip(copy)
	if err != nil {
		cleanup()
		return nil, err
	}
	response.Body = &mcpResponseBody{ReadCloser: response.Body, cleanup: cleanup}
	if contentType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type")); contentType == "text/event-stream" {
		response.Body = &mcpEventBody{ReadCloser: response.Body, failed: t.capture.fail, receive: func(data []byte) error {
			message, err := jsonrpc.DecodeMessage(data)
			if err != nil { // The legacy SSE endpoint event is a URL, not JSON-RPC.
				return nil
			}
			return captureMCPNotification(t.writer, message)
		}}
	}
	return response, nil
}

type mcpResponseBody struct {
	io.ReadCloser
	cleanup func()
}

func (b *mcpResponseBody) Close() error {
	b.cleanup()
	return b.ReadCloser.Close()
}

func (e *Engine) useMCP(ctx context.Context, op *operation) error {
	var args MCPArguments
	if err := decodeArguments(op.record.Request.Arguments, &args); err != nil {
		return err
	}
	e.mu.Lock()
	target := e.operations[args.ConnectionID]
	if target == nil {
		e.mu.Unlock()
		return execprotocol.ErrNotFound
	}
	if target.record.Request.AgentID != op.record.Request.AgentID {
		e.mu.Unlock()
		return execprotocol.ErrDenied
	}
	session := target.session
	remote := target.record.MCP != nil && target.record.MCP.Transport != "stdio"
	e.mu.Unlock()
	if session == nil {
		return errors.New("MCP connection is unavailable; inspect its operation before reconnecting")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	var result any
	var err error
	switch op.record.Request.Kind {
	case "mcp_list":
		result, err = session.ListTools(ctx, &mcp.ListToolsParams{Cursor: args.Cursor})
	case "mcp_call":
		if args.Name == "" {
			return execprotocol.ErrInvalid
		}
		result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: args.Name, Arguments: args.Arguments})
		if err != nil {
			return execprotocol.ErrOutcomeUnknown
		}
	case "mcp_close":
		err = e.Cancel(op.record.Request.AgentID, args.ConnectionID)
		result = map[string]any{"connection_id": args.ConnectionID, "cancellation_requested": true}
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if remote {
			return errors.New("MCP HTTP request failed")
		}
		return err
	}
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = (outputWriter{engine: e, operation: op}).Write(data)
	return err
}

func writeMCPEvent(writer outputWriter, event any) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if len(data)+1 > execprotocol.MaxMCPEventBytes {
		return errors.New("MCP notification record exceeds 1 MiB")
	}
	_, err = writer.write(append(data, '\n'), true)
	return err
}

type mcpStderr struct{ writer outputWriter }

func (w mcpStderr) Write(data []byte) (int, error) {
	if err := writeMCPEvent(w.writer, map[string]any{"type": "stderr", "text": string(data)}); err != nil {
		return 0, err
	}
	return len(data), nil
}

type notificationTransport struct {
	delegate          mcp.Transport
	writer            outputWriter
	connection        mcp.Connection
	connectionContext context.Context
	capture           *mcpCapture
}

// Legacy SSE discards response-reader errors before Wait returns. Retain the
// first journal/parser failure independently so a lost notification is visible.
type mcpCapture struct {
	mu  sync.Mutex
	err error
}

func (c *mcpCapture) fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err == nil {
		c.err = err
	}
}

func (c *mcpCapture) failure() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (t *notificationTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	// SSE uses Connect's context for its long-lived HTTP body. Initialization
	// has a separate deadline, cancelled without closing a successful session.
	if t.connectionContext != nil {
		ctx = t.connectionContext
	}
	connection, err := t.delegate.Connect(ctx)
	if err != nil {
		return nil, err
	}
	t.connection = connection
	if t.connectionContext != nil {
		// Streamable HTTP's concrete connection owns the SDK's private handshake
		// callback. Preserve its type; capture raw notifications in the SSE body.
		return connection, nil
	}
	return &notificationConnection{Connection: connection, writer: t.writer}, nil
}

func (t *notificationTransport) close() {
	if t.connection != nil {
		_ = t.connection.Close()
	}
}

type notificationConnection struct {
	mcp.Connection
	writer outputWriter
}

func (c *notificationConnection) Read(ctx context.Context) (jsonrpc.Message, error) {
	for {
		message, err := c.Connection.Read(ctx)
		if err != nil {
			return nil, err
		}
		if err := captureMCPNotification(c.writer, message); err != nil {
			return nil, err
		}
		if request, ok := message.(*jsonrpc.Request); ok && !request.IsCall() {
			if request.Method == "notifications/claude/channel" {
				continue
			}
		}
		return message, nil
	}
}

func captureMCPNotification(writer outputWriter, message jsonrpc.Message) error {
	if request, ok := message.(*jsonrpc.Request); ok && !request.IsCall() && strings.HasPrefix(request.Method, "notifications/") {
		return writeMCPEvent(writer, map[string]any{"type": "notification", "method": request.Method, "params": request.Params, "received_at": time.Now().UTC()})
	}
	return nil
}
