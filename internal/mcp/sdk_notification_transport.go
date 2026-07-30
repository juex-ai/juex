package mcp

import (
	"context"
	"encoding/json"

	sdkjsonrpc "github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const claudeChannelNotificationMethod = "notifications/claude/channel"

type sdkNotificationTransport struct {
	delegate       sdkmcp.Transport
	serverName     string
	onNotification func(Notification)
}

// wrapSDKNotificationTransport intercepts Juex's custom channel notification
// before the SDK rejects the unknown method. A wrapped StreamableClientTransport
// must use the 2026-07-28 protocol until the SDK exposes client session updates;
// wrapping currently hides the private callback that starts legacy standalone SSE.
func wrapSDKNotificationTransport(
	delegate sdkmcp.Transport,
	serverName string,
	onNotification func(Notification),
) sdkmcp.Transport {
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
	return &sdkNotificationConnection{
		Connection:     conn,
		serverName:     t.serverName,
		onNotification: t.onNotification,
	}, nil
}

type sdkNotificationConnection struct {
	sdkmcp.Connection
	serverName     string
	onNotification func(Notification)
}

func (c *sdkNotificationConnection) Read(ctx context.Context) (sdkjsonrpc.Message, error) {
	for {
		msg, err := c.Connection.Read(ctx)
		if err != nil {
			return nil, err
		}
		req, ok := msg.(*sdkjsonrpc.Request)
		if !ok || req.IsCall() || req.Method != claudeChannelNotificationMethod {
			return msg, nil
		}
		dispatchClaudeChannelNotification(c.serverName, req.Method, req.Params, c.onNotification)
	}
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
