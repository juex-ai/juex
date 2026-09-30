// Package connector maintains a device-initiated execution connection. Losing
// that connection never cancels the independently owned native Engine.
package connector

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/juex-ai/juex/internal/execution/native"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

type Config struct {
	URL          string
	Token        string
	Environment  execprotocol.Environment
	Engine       *native.Engine
	HTTPClient   *http.Client
	InsecureHTTP bool
	OnState      func(string)
}

func (c Config) endpoint() (string, error) {
	u, err := url.Parse(c.URL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", execprotocol.ErrInvalid
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		if !c.InsecureHTTP {
			return "", errors.New("device connection requires HTTPS")
		}
		u.Scheme = "ws"
	default:
		return "", execprotocol.ErrInvalid
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/device/connect"
	return u.String(), nil
}

func Run(ctx context.Context, config Config) error {
	endpoint, err := config.endpoint()
	if err != nil {
		return err
	}
	if config.Token == "" || config.Engine == nil || config.Environment.ID == "" {
		return execprotocol.ErrInvalid
	}
	environmentID, journalID := config.Engine.Identity()
	if config.Environment.ID != environmentID {
		return execprotocol.ErrInvalid
	}
	config.Environment.JournalID = journalID
	client := &http.Client{}
	if config.HTTPClient != nil {
		*client = *config.HTTPClient
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	backoff := 250 * time.Millisecond
	state := ""
	update := func(next string) {
		if next != state {
			state = next
			if config.OnState != nil {
				config.OnState(next)
			}
		}
	}
	for ctx.Err() == nil {
		update("connecting")
		connectCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		connection, response, err := websocket.Dial(connectCtx, endpoint, &websocket.DialOptions{HTTPClient: client, HTTPHeader: http.Header{"Authorization": {"Bearer " + config.Token}}})
		cancel()
		if err == nil {
			err = serve(ctx, connection, config, func(state string) { backoff = 250 * time.Millisecond; update(state) })
			_ = connection.CloseNow()
		} else if response != nil {
			if response.Body != nil {
				_ = response.Body.Close()
			}
			if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
				return execprotocol.ErrDenied
			}
		}
		if ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, execprotocol.ErrVersion) || errors.Is(err, execprotocol.ErrDenied) {
			return err
		}
		update("offline")
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		backoff = min(30*time.Second, backoff*2)
	}
	return nil
}

func serve(ctx context.Context, connection *websocket.Conn, config Config, ready func(string)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	connection.SetReadLimit(3 << 20)
	setupCtx, stopSetup := context.WithTimeout(ctx, 15*time.Second)
	defer stopSetup()
	if err := wsjson.Write(setupCtx, connection, execprotocol.Envelope{Version: execprotocol.Version, Type: "hello", Environment: &config.Environment}); err != nil {
		return err
	}
	var welcome execprotocol.Envelope
	if err := wsjson.Read(setupCtx, connection, &welcome); err != nil {
		return err
	}
	if welcome.Version != execprotocol.Version {
		return execprotocol.ErrVersion
	}
	if welcome.Type != "welcome" {
		if welcome.Error == "" {
			return execprotocol.ErrVersion
		}
		return execprotocol.FromErrorCode(welcome.Error)
	}
	if err := config.Engine.Restrict(welcome.Grants); err != nil {
		return err
	}
	if welcome.Revoked {
		ready("revoked")
	} else {
		ready("online")
	}
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pingCtx, stop := context.WithTimeout(ctx, 5*time.Second)
				err := connection.Ping(pingCtx)
				stop()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-heartbeatDone }()
	for {
		var request execprotocol.Envelope
		if err := wsjson.Read(ctx, connection, &request); err != nil {
			return err
		}
		reply := execprotocol.Envelope{Version: execprotocol.Version, Type: "result", ID: request.ID}
		var snapshot execprotocol.Snapshot
		var err error
		if request.Version != execprotocol.Version {
			err = execprotocol.ErrVersion
		} else if request.ID == "" || len(request.ID) > 256 {
			err = execprotocol.ErrInvalid
		} else {
			switch request.Type {
			case "file_read":
				chunk, readErr := config.Engine.ReadFile(request.AgentID, request.OperationID, request.Cursor, request.Limit)
				err = readErr
				reply.FileChunk = &chunk
			case "file_write":
				if request.FileChunk == nil {
					err = execprotocol.ErrInvalid
				} else {
					status, writeErr := config.Engine.WriteFile(request.AgentID, request.OperationID, *request.FileChunk)
					err = writeErr
					reply.FileStatus = &status
				}
			case "file_commit":
				status, commitErr := config.Engine.CommitFile(request.AgentID, request.OperationID)
				err = commitErr
				reply.FileStatus = &status
			case "file_ack":
				if request.FileManifest == nil {
					err = execprotocol.ErrInvalid
				} else {
					err = config.Engine.AcknowledgeFile(request.AgentID, request.OperationID, *request.FileManifest)
				}
			case "submit":
				if request.Request == nil {
					err = execprotocol.ErrInvalid
				} else {
					snapshot, err = config.Engine.Submit(*request.Request)
					reply.Snapshot = &snapshot
				}
			case "query":
				limit := request.Limit
				if limit == 0 {
					limit = 64 << 10
				}
				snapshot, err = config.Engine.Snapshot(request.AgentID, request.OperationID, request.Cursor, limit)
				reply.Snapshot = &snapshot
			case "cancel":
				err = config.Engine.Cancel(request.AgentID, request.OperationID)
			case "ack":
				err = config.Engine.Acknowledge(request.AgentID, request.OperationID, request.Cursor)
			case "grants":
				err = config.Engine.Restrict(request.Grants)
				if err == nil {
					if request.Revoked {
						ready("revoked")
					} else {
						ready("online")
					}
				}
			default:
				err = execprotocol.ErrInvalid
			}
		}
		reply.Error = execprotocol.ErrorCode(err)
		if err != nil {
			reply.Snapshot = nil
			reply.FileChunk, reply.FileStatus = nil, nil
		}
		writeCtx, stop := context.WithTimeout(ctx, 10*time.Second)
		writeErr := wsjson.Write(writeCtx, connection, reply)
		stop()
		if writeErr != nil {
			return writeErr
		}
		if errors.Is(err, execprotocol.ErrVersion) {
			return err
		}
	}
}
