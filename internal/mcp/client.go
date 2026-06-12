// Package mcp implements a minimal Model Context Protocol client over stdio.
//
// Only the JSON-RPC 2.0 calls that v0.1 needs are supported:
//   - initialize
//   - tools/list
//   - tools/call
//
// Each MCP server is launched as a subprocess; messages are exchanged as
// newline-delimited JSON over the server's stdin/stdout. A separate goroutine
// reads stdout and routes responses back to waiting callers via a map keyed by
// JSON-RPC request id.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/juex-ai/juex/internal/tools"
)

const (
	protocolVersion = "2024-11-05"
	clientName      = "juex"
	clientVersion   = "0.1.0"
)

// ServerSpec mirrors a single entry in mcp.json's `mcpServers`.
type ServerSpec struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// Config mirrors the mcp.json file root.
type Config struct {
	MCPServers map[string]ServerSpec `json:"mcpServers"`
}

// LoadConfig reads mcp.json from path. Missing file -> empty Config, no error.
func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, nil
		}
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return Config{}, fmt.Errorf("mcp: %s: %w", path, err)
	}
	return c, nil
}

// ToolDescriptor is a tool advertised by an MCP server.
type ToolDescriptor struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// Client is one MCP server connection.
type Client struct {
	name string
	cmd  *exec.Cmd
	in   io.WriteCloser

	mu      sync.Mutex
	pending map[string]chan rpcCallResult
	readErr error

	nextID atomic.Int64
	closed atomic.Bool

	onNotification func(Notification)
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
}

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

// Connect launches the server subprocess and performs the initialize handshake.
func Connect(ctx context.Context, name string, spec ServerSpec) (*Client, error) {
	return ConnectWithOptions(ctx, name, spec, ConnectOptions{})
}

// ConnectWithOptions launches the server subprocess and performs the
// initialize handshake while registering optional notification callbacks.
func ConnectWithOptions(ctx context.Context, name string, spec ServerSpec, opts ConnectOptions) (*Client, error) {
	if spec.Command == "" {
		return nil, fmt.Errorf("mcp[%s]: missing command", name)
	}
	cmd := exec.CommandContext(ctx, spec.Command, spec.Args...)
	cmd.Env = mergeEnv(spec.Env)
	cmd.Stderr = os.Stderr // forward server logs

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp[%s]: start: %w", name, err)
	}

	c := &Client{
		name:           name,
		cmd:            cmd,
		in:             stdin,
		pending:        make(map[string]chan rpcCallResult),
		onNotification: opts.OnNotification,
	}
	go c.readLoop(stdout)

	capabilities := map[string]any{}
	if opts.EnableClaudeChannel {
		capabilities["experimental"] = map[string]any{
			"claude/channel": map[string]any{},
		}
	}
	if _, err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    capabilities,
		"clientInfo":      map[string]any{"name": clientName, "version": clientVersion},
	}); err != nil {
		c.Close()
		return nil, fmt.Errorf("mcp[%s]: initialize: %w", name, err)
	}
	// notifications/initialized — no id, no response expected
	if err := c.notify("notifications/initialized", map[string]any{}); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

func (c *Client) Name() string { return c.name }

// ListTools queries the server for its tools.
func (c *Client) ListTools(ctx context.Context) ([]ToolDescriptor, error) {
	resp, err := c.call(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var payload struct {
		Tools []ToolDescriptor `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &payload); err != nil {
		return nil, err
	}
	return payload.Tools, nil
}

// CallTool invokes one tool and returns the textual result.
// Server responses can have multiple content blocks; we concatenate text blocks.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (string, error) {
	resp, err := c.call(ctx, "tools/call", map[string]any{
		"name":      name,
		"arguments": args,
	})
	if err != nil {
		return "", err
	}
	var payload struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError,omitempty"`
	}
	if err := json.Unmarshal(resp.Result, &payload); err != nil {
		return "", err
	}
	var sb strings.Builder
	for _, b := range payload.Content {
		if b.Type == "text" {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(b.Text)
		}
	}
	if payload.IsError {
		return sb.String(), fmt.Errorf("mcp[%s].%s: %s", c.name, name, sb.String())
	}
	return sb.String(), nil
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
			schema := d.InputSchema
			if schema == nil {
				schema = map[string]any{"type": "object"}
			}
			cli := client
			descName := d.Name
			err := reg.Register(tools.Tool{
				Name:        toolName,
				Description: d.Description,
				Schema:      schema,
				Handler: func(ctx context.Context, in map[string]any) (string, error) {
					return cli.CallTool(ctx, descName, in)
				},
			})
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
	if c.in != nil {
		_ = c.in.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		_ = c.cmd.Wait()
	}
	return nil
}

// ----- internals -----

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      *int   `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcCallResult struct {
	resp rpcResponse
	err  error
}

type rpcEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func (c *Client) call(ctx context.Context, method string, params any) (rpcResponse, error) {
	id := int(c.nextID.Add(1))
	idKey := rpcIDKey(json.RawMessage(strconv.Itoa(id)))
	ch := make(chan rpcCallResult, 1)
	c.mu.Lock()
	if c.readErr != nil {
		err := c.readErr
		c.mu.Unlock()
		return rpcResponse{}, err
	}
	c.pending[idKey] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, idKey)
		c.mu.Unlock()
	}()

	req := rpcRequest{JSONRPC: "2.0", ID: &id, Method: method, Params: params}
	if err := c.send(req); err != nil {
		return rpcResponse{}, err
	}

	select {
	case <-ctx.Done():
		return rpcResponse{}, ctx.Err()
	case result := <-ch:
		if result.err != nil {
			return rpcResponse{}, result.err
		}
		resp := result.resp
		if resp.Error != nil {
			return resp, fmt.Errorf("mcp[%s].%s: %s (code=%d)", c.name, method, resp.Error.Message, resp.Error.Code)
		}
		return resp, nil
	}
}

func (c *Client) notify(method string, params any) error {
	return c.send(rpcRequest{JSONRPC: "2.0", Method: method, Params: params})
}

func (c *Client) send(req rpcRequest) error {
	buf, err := json.Marshal(req)
	if err != nil {
		return err
	}
	buf = append(buf, '\n')
	_, err = c.in.Write(buf)
	return err
}

func (c *Client) readLoop(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var msg rpcEnvelope
		if err := json.Unmarshal(line, &msg); err != nil {
			c.failPending(fmt.Errorf("mcp[%s]: invalid stdout from server: %q", c.name, truncateProtocolLine(string(line), 300)))
			return
		}
		idKey := rpcIDKey(msg.ID)
		if idKey == "" {
			c.handleNotification(msg)
			continue
		}
		resp := rpcResponse{JSONRPC: msg.JSONRPC, Result: msg.Result, Error: msg.Error}
		c.mu.Lock()
		ch, ok := c.pending[idKey]
		c.mu.Unlock()
		if ok {
			ch <- rpcCallResult{resp: resp}
		}
	}
	if err := scanner.Err(); err != nil {
		c.failPending(fmt.Errorf("mcp[%s]: stdout read error: %w", c.name, err))
		return
	}
	c.failPending(fmt.Errorf("mcp[%s]: stdout closed before response", c.name))
}

func (c *Client) failPending(err error) {
	c.mu.Lock()
	if c.readErr == nil {
		c.readErr = err
	}
	pending := c.pending
	c.pending = make(map[string]chan rpcCallResult)
	c.mu.Unlock()
	for _, ch := range pending {
		select {
		case ch <- rpcCallResult{err: err}:
		default:
		}
	}
}

func truncateProtocolLine(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated)"
}

func (c *Client) handleNotification(msg rpcEnvelope) {
	if c.onNotification == nil || msg.Method != "notifications/claude/channel" {
		return
	}
	var params map[string]any
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return
	}
	eventType := "notification"
	if meta, ok := params["meta"].(map[string]any); ok {
		if raw, ok := meta["event_type"].(string); ok && raw != "" {
			eventType = raw
		}
	}
	content, _ := params["content"].(string)
	go c.onNotification(Notification{
		ServerName: c.name,
		Method:     msg.Method,
		EventType:  eventType,
		Content:    content,
		Params:     params,
	})
}

func rpcIDKey(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err == nil {
		return n.String()
	}
	return string(raw)
}

func mergeEnv(extra map[string]string) []string {
	env := os.Environ()
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}
