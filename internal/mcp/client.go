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
	"fmt"
	"io"
	"os"
	"os/exec"
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
	pending map[int]chan rpcResponse

	nextID atomic.Int64
	closed atomic.Bool
}

// Connect launches the server subprocess and performs the initialize handshake.
func Connect(ctx context.Context, name string, spec ServerSpec) (*Client, error) {
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
		name:    name,
		cmd:     cmd,
		in:      stdin,
		pending: make(map[int]chan rpcResponse),
	}
	go c.readLoop(stdout)

	if _, err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
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

// RegisterAllLayered connects servers from multiple configs in order,
// deduplicating by server name with later-wins precedence (so callers can
// pass user-level config first and project-level config second to get
// project-overrides-user behaviour).
//
// Returns the connected clients in the order they were started so the caller
// can Close them at shutdown.
func RegisterAllLayered(ctx context.Context, configs []Config, reg *tools.Registry) ([]*Client, error) {
	merged := map[string]ServerSpec{}
	for _, c := range configs {
		for name, spec := range c.MCPServers {
			merged[name] = spec
		}
	}
	return RegisterAll(ctx, Config{MCPServers: merged}, reg)
}

// RegisterAll connects servers from cfg and registers their tools (prefixed
// `mcp__<server>__<tool>`) into reg. Returns the connected clients so the
// caller can Close them at shutdown.
func RegisterAll(ctx context.Context, cfg Config, reg *tools.Registry) ([]*Client, error) {
	var clients []*Client
	for name, spec := range cfg.MCPServers {
		client, err := Connect(ctx, name, spec)
		if err != nil {
			return clients, err
		}
		clients = append(clients, client)
		descs, err := client.ListTools(ctx)
		if err != nil {
			return clients, err
		}
		for _, d := range descs {
			toolName := fmt.Sprintf("mcp__%s__%s", name, d.Name)
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
				return clients, err
			}
		}
	}
	return clients, nil
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
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func (c *Client) call(ctx context.Context, method string, params any) (rpcResponse, error) {
	id := int(c.nextID.Add(1))
	ch := make(chan rpcResponse, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	req := rpcRequest{JSONRPC: "2.0", ID: &id, Method: method, Params: params}
	if err := c.send(req); err != nil {
		return rpcResponse{}, err
	}

	select {
	case <-ctx.Done():
		return rpcResponse{}, ctx.Err()
	case resp := <-ch:
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
		var resp rpcResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			// Could be a server-initiated notification we don't handle in v0.1; ignore.
			continue
		}
		if resp.ID == 0 && resp.Result == nil && resp.Error == nil {
			continue // not a response
		}
		c.mu.Lock()
		ch, ok := c.pending[resp.ID]
		c.mu.Unlock()
		if ok {
			ch <- resp
		}
	}
}

func mergeEnv(extra map[string]string) []string {
	env := os.Environ()
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}
