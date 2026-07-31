// Package mcp adapts the official Model Context Protocol Go SDK to Juex tools.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/juex-ai/juex/internal/environment"
	"github.com/juex-ai/juex/internal/tools"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	clientName    = "juex"
	clientVersion = "0.1.0"
)

// ToolDescriptor is a tool advertised by an MCP server.
type ToolDescriptor struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func toolDefinition(name string, descriptor ToolDescriptor) tools.ToolDefinition {
	schema := descriptor.InputSchema
	if schema == nil {
		schema = map[string]any{"type": "object"}
	}
	return tools.ToolDefinition{
		Name:        name,
		Group:       tools.ToolGroupMCP,
		Description: descriptor.Description,
		Schema:      schema,
	}
}

// Client is one MCP server connection.
type Client struct {
	name       string
	session    *sdkmcp.ClientSession
	cmd        *exec.Cmd
	remote     bool
	closed     atomic.Bool
	stderrTail *stderrTailBuffer
}

// Notification is a server-initiated MCP JSON-RPC notification that Juex
// understands. Claude channel notifications carry realtime content and
// meta.event_type for the agent-facing message formatter.
type Notification struct {
	ServerName string
	Method     string
	EventType  string
	Content    string
	Params     map[string]any
}

type ConnectOptions struct {
	OnNotification      func(Notification)
	EnableClaudeChannel bool
	Environment         environment.Snapshot
	// Stderr receives MCP server stderr only when ForwardStderr is true.
	// Stderr is always retained in a bounded diagnostic tail for startup
	// errors so normal CLI output is not polluted by server logs.
	Stderr        io.Writer
	ForwardStderr bool
}

const mcpStderrTailBytes = 32 * 1024

// ServerError marks an MCP setup failure with the server name that produced
// it so callers can surface per-server diagnostics.
type ServerError struct {
	Server string
	Op     string
	Err    error
}

func (e *ServerError) Error() string {
	if e == nil {
		return ""
	}
	if e.Op == "" {
		return fmt.Sprintf("mcp[%s]: %v", e.Server, e.Err)
	}
	return fmt.Sprintf("mcp[%s]: %s: %v", e.Server, e.Op, e.Err)
}

func (e *ServerError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func ErrorServerName(err error) (string, bool) {
	var serverErr *ServerError
	if errors.As(err, &serverErr) && serverErr.Server != "" {
		return serverErr.Server, true
	}
	return "", false
}

// Connect opens one local or remote MCP session and performs SDK negotiation.
func Connect(ctx context.Context, name string, spec ServerSpec) (*Client, error) {
	return ConnectWithOptions(ctx, name, spec, ConnectOptions{})
}

// ConnectWithOptions opens one SDK session while registering optional
// notification callbacks.
func ConnectWithOptions(ctx context.Context, name string, spec ServerSpec, opts ConnectOptions) (*Client, error) {
	if spec.Command == "" && spec.URL == "" {
		return nil, fmt.Errorf("mcp[%s]: missing command", name)
	}
	if spec.Command != "" && spec.URL != "" {
		return nil, fmt.Errorf("mcp[%s]: exactly one of command or url is required", name)
	}

	transport, cmd, stderrTail, remote, err := newSDKTransport(name, spec, opts)
	if err != nil {
		return nil, err
	}
	capabilities := &sdkmcp.ClientCapabilities{}
	if opts.EnableClaudeChannel {
		capabilities.Experimental = map[string]any{
			"claude/channel": map[string]any{},
		}
	}
	sdkClient := sdkmcp.NewClient(
		&sdkmcp.Implementation{Name: clientName, Version: clientVersion},
		&sdkmcp.ClientOptions{
			Capabilities: capabilities,
			Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
	)
	sdkClient.AddSendingMiddleware(omitEmptyToolArguments)

	var transportCloser interface{ Close() error }
	if remote {
		trackedTransport := trackSDKTransport(transport)
		transport = trackedTransport
		transportCloser = trackedTransport
	} else {
		wrappedTransport := wrapSDKNotificationTransport(transport, name, opts.OnNotification)
		transport = wrappedTransport
		transportCloser = wrappedTransport
	}
	diagnostic := newRemoteDiagnostic()
	session, err := sdkClient.Connect(withRemoteDiagnostic(ctx, diagnostic), transport, nil)
	if err != nil {
		_ = transportCloser.Close()
		if remote {
			err = diagnostic.enrich(err)
		} else {
			err = commandProtocolError(err)
			err = withStderrTail(stderrTail, err)
		}
		return nil, fmt.Errorf("mcp[%s]: connect: %w", name, err)
	}
	return &Client{
		name:       name,
		session:    session,
		cmd:        cmd,
		remote:     remote,
		stderrTail: stderrTail,
	}, nil
}

func commandProtocolError(err error) error {
	if err == nil {
		return nil
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "invalid character") ||
		strings.Contains(lower, "invalid json") ||
		strings.Contains(lower, "decoding json") {
		return fmt.Errorf("invalid stdout from server: %w", err)
	}
	return err
}

func newSDKTransport(
	name string,
	spec ServerSpec,
	opts ConnectOptions,
) (sdkmcp.Transport, *exec.Cmd, *stderrTailBuffer, bool, error) {
	if spec.URL != "" {
		endpoint, err := url.Parse(spec.URL)
		if err != nil {
			return nil, nil, nil, true, fmt.Errorf("mcp[%s]: url: %w", name, err)
		}
		httpClient := newSecureEndpointHTTPClient(&remoteDiagnosticRoundTripper{
			base:           http.DefaultTransport,
			endpoint:       endpoint,
			headers:        spec.Headers,
			redactions:     headerCredentialValues(spec.Headers),
			serverName:     name,
			onNotification: opts.OnNotification,
		})
		return &sdkmcp.StreamableClientTransport{
			Endpoint:   spec.URL,
			HTTPClient: httpClient,
		}, nil, nil, true, nil
	}

	command, err := opts.Environment.LookPath(spec.Command)
	if err != nil {
		return nil, nil, nil, false, fmt.Errorf("mcp[%s]: resolve command %q: %w", name, spec.Command, err)
	}
	cmd := exec.Command(command, spec.Args...)
	cmd.Env = opts.Environment.Environ(spec.Env)
	stderrTail := newStderrTailBuffer(mcpStderrTailBytes)
	stderrWriters := []io.Writer{stderrTail}
	if opts.ForwardStderr && opts.Stderr != nil {
		stderrWriters = append(stderrWriters, newLinePrefixWriter(opts.Stderr, fmt.Sprintf("[mcp:%s] ", name)))
	}
	cmd.Stderr = io.MultiWriter(stderrWriters...)
	return &sdkmcp.CommandTransport{Command: cmd}, cmd, stderrTail, false, nil
}

func (c *Client) Name() string { return c.name }

// ListTools queries the server for its tools.
func (c *Client) ListTools(ctx context.Context) ([]ToolDescriptor, error) {
	var descriptors []ToolDescriptor
	var cursor string
	seenCursors := map[string]bool{}
	for {
		diagnostic := &remoteDiagnostic{}
		result, err := c.session.ListTools(withRemoteDiagnostic(ctx, diagnostic), &sdkmcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, c.enrich(err, diagnostic)
		}
		for _, tool := range result.Tools {
			descriptor, err := descriptorFromSDK(tool)
			if err != nil {
				return nil, fmt.Errorf("mcp[%s]: tools/list: %w", c.name, err)
			}
			descriptors = append(descriptors, descriptor)
		}
		if result.NextCursor == "" {
			return descriptors, nil
		}
		if seenCursors[result.NextCursor] {
			return nil, fmt.Errorf("mcp[%s]: tools/list repeated cursor %q", c.name, result.NextCursor)
		}
		seenCursors[result.NextCursor] = true
		cursor = result.NextCursor
	}
}

// CallTool invokes one tool and returns the textual result.
// Server responses can have multiple content blocks; we concatenate text blocks.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (string, error) {
	diagnostic := &remoteDiagnostic{}
	result, err := c.session.CallTool(withRemoteDiagnostic(ctx, diagnostic), &sdkmcp.CallToolParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		return "", c.enrich(err, diagnostic)
	}
	var sb strings.Builder
	for _, content := range result.Content {
		if text, ok := content.(*sdkmcp.TextContent); ok {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(text.Text)
		}
	}
	if result.IsError {
		return sb.String(), fmt.Errorf("mcp[%s].%s: %s", c.name, name, sb.String())
	}
	return sb.String(), nil
}

func descriptorFromSDK(tool *sdkmcp.Tool) (ToolDescriptor, error) {
	if tool == nil {
		return ToolDescriptor{}, fmt.Errorf("server returned a null tool")
	}
	var schema map[string]any
	if tool.InputSchema != nil {
		data, err := json.Marshal(tool.InputSchema)
		if err != nil {
			return ToolDescriptor{}, fmt.Errorf("tool %q input schema: %w", tool.Name, err)
		}
		if err := json.Unmarshal(data, &schema); err != nil {
			return ToolDescriptor{}, fmt.Errorf("tool %q input schema: %w", tool.Name, err)
		}
	}
	return ToolDescriptor{
		Name:        tool.Name,
		Description: tool.Description,
		InputSchema: schema,
	}, nil
}

func omitEmptyToolArguments(next sdkmcp.MethodHandler) sdkmcp.MethodHandler {
	return func(ctx context.Context, method string, req sdkmcp.Request) (sdkmcp.Result, error) {
		if method == "tools/call" {
			if params, ok := req.GetParams().(*sdkmcp.CallToolParams); ok {
				if args, ok := params.Arguments.(map[string]any); ok && len(args) == 0 {
					params.Arguments = nil
				}
			}
		}
		return next(ctx, method, req)
	}
}

// RegisterAll connects servers from cfg and registers their tools into reg
// using ToolName. Returns the connected clients so the
// caller can Close them at shutdown. On error, any clients opened during this
// call are closed before returning.
func RegisterAll(ctx context.Context, cfg Config, reg *tools.Registry) ([]*Client, error) {
	return RegisterAllWithOptions(ctx, cfg, reg, ConnectOptions{})
}

func RegisterAllWithOptions(ctx context.Context, cfg Config, reg *tools.Registry, opts ConnectOptions) ([]*Client, error) {
	var clients []*Client
	for name, spec := range cfg.MCPServers {
		if err := validateToolNameServer(name); err != nil {
			closeAll(clients)
			return nil, &ServerError{Server: name, Op: "tool name", Err: err}
		}
		client, err := ConnectWithOptions(ctx, name, spec, opts)
		if err != nil {
			closeAll(clients)
			return nil, &ServerError{Server: name, Op: "connect", Err: err}
		}
		clients = append(clients, client)
		descs, err := client.ListTools(ctx)
		if err != nil {
			closeAll(clients)
			return nil, &ServerError{Server: name, Op: "tools/list", Err: err}
		}
		for _, d := range descs {
			if err := validateToolNameParts(name, d.Name); err != nil {
				closeAll(clients)
				return nil, &ServerError{Server: name, Op: "tool name", Err: err}
			}
			toolName := ToolName(name, d.Name)
			cli := client
			descName := d.Name
			err := reg.Register(toolDefinition(toolName, d).Bind(func(ctx context.Context, in map[string]any) (string, error) {
				return cli.CallTool(ctx, descName, in)
			}))
			if err != nil {
				closeAll(clients)
				return nil, &ServerError{Server: name, Op: "register tool " + toolName, Err: err}
			}
		}
	}
	return clients, nil
}

func closeAll(clients []*Client) {
	for _, c := range clients {
		c.Close()
	}
}

func (c *Client) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	if c.session == nil {
		return nil
	}
	return c.session.Close()
}

func (c *Client) withStderrTail(err error) error {
	if c == nil {
		return err
	}
	return withStderrTail(c.stderrTail, err)
}

func withStderrTail(stderrTail *stderrTailBuffer, err error) error {
	if err == nil || stderrTail == nil {
		return err
	}
	tail := strings.TrimSpace(stderrTail.String())
	if tail == "" {
		return err
	}
	return fmt.Errorf("%w\nmcp stderr tail:\n%s", err, tail)
}

func (c *Client) enrich(err error, diagnostic *remoteDiagnostic) error {
	if c == nil || !c.remote {
		return c.withStderrTail(err)
	}
	return diagnostic.enrich(err)
}

type stderrTailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func newStderrTailBuffer(max int) *stderrTailBuffer {
	if max <= 0 {
		max = mcpStderrTailBytes
	}
	return &stderrTailBuffer{max: max}
}

func (b *stderrTailBuffer) Write(p []byte) (int, error) {
	if b == nil {
		return len(p), nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(p) >= b.max {
		b.buf = append(b.buf[:0], p[len(p)-b.max:]...)
		return len(p), nil
	}
	b.buf = append(b.buf, p...)
	if overflow := len(b.buf) - b.max; overflow > 0 {
		copy(b.buf, b.buf[overflow:])
		b.buf = b.buf[:b.max]
	}
	return len(p), nil
}

func (b *stderrTailBuffer) String() string {
	if b == nil {
		return ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(append([]byte(nil), b.buf...))
}

type linePrefixWriter struct {
	mu          sync.Mutex
	w           io.Writer
	prefix      string
	atLineStart bool
}

func newLinePrefixWriter(w io.Writer, prefix string) io.Writer {
	return &linePrefixWriter{w: w, prefix: prefix, atLineStart: true}
}

func (w *linePrefixWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	written := 0
	for len(p) > 0 {
		if w.atLineStart {
			if _, err := io.WriteString(w.w, w.prefix); err != nil {
				return written, err
			}
			w.atLineStart = false
		}
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			n, err := w.w.Write(p)
			written += n
			if err != nil {
				return written, err
			}
			return written, nil
		}
		n, err := w.w.Write(p[:i+1])
		written += n
		if err != nil {
			return written, err
		}
		w.atLineStart = true
		p = p[i+1:]
	}
	return written, nil
}
