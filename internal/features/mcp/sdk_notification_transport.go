package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"

	sdkjsonrpc "github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const claudeChannelNotificationMethod = "notifications/claude/channel"

type sdkTrackedTransport struct {
	delegate   sdkmcp.Transport
	mu         sync.Mutex
	connection sdkmcp.Connection
}

func trackSDKTransport(delegate sdkmcp.Transport) *sdkTrackedTransport {
	return &sdkTrackedTransport{delegate: delegate}
}

func (t *sdkTrackedTransport) Connect(ctx context.Context) (sdkmcp.Connection, error) {
	connection, err := t.delegate.Connect(ctx)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	t.connection = connection
	t.mu.Unlock()
	return connection, nil
}

func (t *sdkTrackedTransport) Close() error {
	t.mu.Lock()
	connection := t.connection
	t.connection = nil
	t.mu.Unlock()
	if connection == nil {
		return nil
	}
	return connection.Close()
}

type sdkNotificationTransport struct {
	delegate       sdkmcp.Transport
	serverName     string
	onNotification func(Notification)
	mu             sync.Mutex
	connection     sdkmcp.Connection
}

// wrapSDKNotificationTransport intercepts custom channel notifications from
// command transports before the SDK rejects the unknown method. Streamable
// HTTP uses an SSE body filter so the SDK retains its private session updates.
func wrapSDKNotificationTransport(
	delegate sdkmcp.Transport,
	serverName string,
	onNotification func(Notification),
) *sdkNotificationTransport {
	return &sdkNotificationTransport{
		delegate:       delegate,
		serverName:     serverName,
		onNotification: onNotification,
	}
}

func (t *sdkNotificationTransport) Connect(ctx context.Context) (sdkmcp.Connection, error) {
	conn, err := t.delegate.Connect(ctx)
	if err != nil {
		return nil, err
	}
	wrapped := &sdkNotificationConnection{
		Connection:     conn,
		serverName:     t.serverName,
		onNotification: t.onNotification,
		requestIDs:     map[string]pendingResponseID{},
	}
	t.mu.Lock()
	t.connection = wrapped
	t.mu.Unlock()
	return wrapped, nil
}

func (t *sdkNotificationTransport) Close() error {
	t.mu.Lock()
	connection := t.connection
	t.connection = nil
	t.mu.Unlock()
	if connection == nil {
		return nil
	}
	return connection.Close()
}

type pendingResponseID struct {
	id   sdkjsonrpc.ID
	done chan struct{}
}

type sdkNotificationConnection struct {
	sdkmcp.Connection
	serverName     string
	onNotification func(Notification)
	mu             sync.Mutex
	requestIDs     map[string]pendingResponseID
	closed         atomic.Bool
}

func (c *sdkNotificationConnection) Read(ctx context.Context) (sdkjsonrpc.Message, error) {
	for {
		msg, err := c.Connection.Read(ctx)
		if err != nil {
			return nil, err
		}
		if response, ok := msg.(*sdkjsonrpc.Response); ok {
			key := fmt.Sprint(response.ID.Raw())
			if pending, ok := c.takeRequestID(key); ok {
				responseCopy := *response
				responseCopy.ID = pending.id
				msg = &responseCopy
			}
		}
		req, ok := msg.(*sdkjsonrpc.Request)
		if !ok || req.IsCall() || req.Method != claudeChannelNotificationMethod {
			return msg, nil
		}
		dispatchClaudeChannelNotification(c.serverName, req.Method, req.Params, c.onNotification)
	}
}

func (c *sdkNotificationConnection) Write(ctx context.Context, msg sdkjsonrpc.Message) error {
	var requestKey string
	if request, ok := msg.(*sdkjsonrpc.Request); ok && request.IsCall() {
		requestKey = fmt.Sprint(request.ID.Raw())
		pending := pendingResponseID{id: request.ID, done: make(chan struct{})}
		c.mu.Lock()
		c.requestIDs[requestKey] = pending
		c.mu.Unlock()
		if done := ctx.Done(); done != nil {
			go func() {
				select {
				case <-done:
					c.takeRequestID(requestKey)
				case <-pending.done:
				}
			}()
		}
	}
	if err := c.Connection.Write(ctx, msg); err != nil {
		if requestKey != "" {
			c.takeRequestID(requestKey)
		}
		return err
	}
	return nil
}

func (c *sdkNotificationConnection) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	c.mu.Lock()
	pending := c.requestIDs
	c.requestIDs = map[string]pendingResponseID{}
	c.mu.Unlock()
	for _, entry := range pending {
		close(entry.done)
	}
	return c.Connection.Close()
}

func (c *sdkNotificationConnection) takeRequestID(key string) (pendingResponseID, bool) {
	c.mu.Lock()
	pending, ok := c.requestIDs[key]
	if ok {
		delete(c.requestIDs, key)
	}
	c.mu.Unlock()
	if ok {
		close(pending.done)
	}
	return pending, ok
}

func dispatchClaudeChannelNotification(
	serverName string,
	method string,
	rawParams json.RawMessage,
	callback func(Notification),
) {
	if callback == nil {
		return
	}
	var params map[string]any
	if err := json.Unmarshal(rawParams, &params); err != nil {
		return
	}
	eventType := "notification"
	if meta, ok := params["meta"].(map[string]any); ok {
		if raw, ok := meta["event_type"].(string); ok && raw != "" {
			eventType = raw
		}
	}
	content, _ := params["content"].(string)
	go callback(Notification{
		ServerName: serverName,
		Method:     method,
		EventType:  eventType,
		Content:    content,
		Params:     params,
	})
}
