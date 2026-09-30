package native

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/foundation/command"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/processidentity"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type MCPArguments struct {
	ConnectionID     string            `json:"connection_id"`
	Command          string            `json:"command"`
	Args             []string          `json:"args"`
	WorkingDirectory string            `json:"working_directory"`
	Environment      map[string]string `json:"environment"`
	Name             string            `json:"name"`
	Arguments        map[string]any    `json:"arguments"`
	Cursor           string            `json:"cursor"`
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
	if args.Command == "" {
		return execprotocol.ErrInvalid
	}
	directory, err := e.workingDirectory(args.WorkingDirectory)
	if err != nil {
		return err
	}
	environment, err := processEnvironment(args.Environment)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, args.Command, args.Args...)
	cmd.Dir, cmd.Env, cmd.WaitDelay = directory, environment, 2*time.Second
	command.ConfigureContext(cmd)
	writer := outputWriter{engine: e, operation: op}
	cmd.Stderr = mcpStderr{writer: writer}
	transport := &notificationTransport{delegate: &mcp.CommandTransport{Command: cmd, TerminateDuration: time.Second}, writer: writer}
	client := mcp.NewClient(&mcp.Implementation{Name: "juex-executor", Version: "1"}, &mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{Experimental: map[string]any{"claude/channel": map[string]any{}}}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	setupCtx, cancelSetup := context.WithTimeout(ctx, 15*time.Second)
	session, err := client.Connect(setupCtx, transport, nil)
	cancelSetup()
	if err != nil {
		transport.close()
		return err
	}
	defer func() { _ = session.Close() }()
	e.mu.Lock()
	op.session, op.record.PID = session, cmd.Process.Pid
	op.record.ProcessIdentity, _ = processidentity.Fingerprint(cmd.Process.Pid)
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
		return err
	}
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
	delegate   mcp.Transport
	writer     outputWriter
	connection mcp.Connection
}

func (t *notificationTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	connection, err := t.delegate.Connect(ctx)
	if err != nil {
		return nil, err
	}
	t.connection = connection
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
		if request, ok := message.(*jsonrpc.Request); ok && !request.IsCall() && strings.HasPrefix(request.Method, "notifications/") {
			if err := writeMCPEvent(c.writer, map[string]any{"type": "notification", "method": request.Method, "params": request.Params, "received_at": time.Now().UTC()}); err != nil {
				return nil, err
			}
			if request.Method == "notifications/claude/channel" {
				continue
			}
		}
		return message, nil
	}
}
