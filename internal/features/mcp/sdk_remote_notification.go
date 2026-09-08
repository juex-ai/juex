package mcp

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	sdkjsonrpc "github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

const sseChannelEventMaxBytes = 16 * 1024 * 1024

var errSSEEventTooLarge = errors.New("SSE event exceeds read limit")

type sseChannelFilter struct {
	body           io.ReadCloser
	reader         *bufio.Reader
	maxEventBytes  int
	output         bytes.Buffer
	pendingErr     error
	serverName     string
	onNotification func(Notification)
}

func newSSEChannelFilter(
	body io.ReadCloser,
	serverName string,
	onNotification func(Notification),
) io.ReadCloser {
	return &sseChannelFilter{
		body:           body,
		reader:         bufio.NewReader(body),
		maxEventBytes:  sseChannelEventMaxBytes,
		serverName:     serverName,
		onNotification: onNotification,
	}
}

func (f *sseChannelFilter) Read(p []byte) (int, error) {
	for f.output.Len() == 0 {
		if f.pendingErr != nil {
			err := f.pendingErr
			f.pendingErr = nil
			return 0, err
		}
		event, err := readSSEEvent(f.reader, f.maxEventBytes)
		if len(event) > 0 {
			if f.intercept(event) {
				event = sseResumeMetadata(event)
			}
			if len(event) > 0 {
				_, _ = f.output.Write(event)
			}
		}
		if err != nil {
			f.pendingErr = err
		}
	}
	return f.output.Read(p)
}

func (f *sseChannelFilter) Close() error {
	return f.body.Close()
}

func (f *sseChannelFilter) intercept(event []byte) bool {
	data, eligible := sseSDKMessageData(event)
	if !eligible || len(data) == 0 {
		return false
	}
	message, err := sdkjsonrpc.DecodeMessage(data)
	if err != nil {
		return false
	}
	request, ok := message.(*sdkjsonrpc.Request)
	if !ok || request.IsCall() || request.Method != claudeChannelNotificationMethod {
		return false
	}
	dispatchClaudeChannelNotification(
		f.serverName,
		request.Method,
		request.Params,
		f.onNotification,
	)
	return true
}

func readSSEEvent(reader *bufio.Reader, maxBytes int) ([]byte, error) {
	var event bytes.Buffer
	for {
		line, err := reader.ReadSlice('\n')
		if len(line) > maxBytes-event.Len() {
			return nil, fmt.Errorf("%w: maximum is %d bytes", errSSEEventTooLarge, maxBytes)
		}
		if len(line) > 0 {
			_, _ = event.Write(line)
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if len(line) > 0 && len(bytes.TrimRight(line, "\r\n")) == 0 {
			return event.Bytes(), err
		}
		if err != nil {
			return event.Bytes(), err
		}
	}
}

func sseSDKMessageData(event []byte) ([]byte, bool) {
	eventName := ""
	var data strings.Builder
	for _, line := range strings.Split(string(event), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			continue
		}
		field, value, found := strings.Cut(line, ":")
		if !found {
			return nil, false
		}
		switch field {
		case "event":
			eventName = strings.TrimSpace(value)
		case "data":
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimSpace(value))
		}
	}
	if eventName != "" && eventName != "message" {
		return nil, false
	}
	return []byte(data.String()), true
}

func sseResumeMetadata(event []byte) []byte {
	var metadata strings.Builder
	for _, line := range strings.Split(string(event), "\n") {
		line = strings.TrimSuffix(line, "\r")
		field, _, found := strings.Cut(line, ":")
		if !found || (field != "id" && field != "retry") {
			continue
		}
		metadata.WriteString(line)
		metadata.WriteByte('\n')
	}
	if metadata.Len() > 0 {
		metadata.WriteByte('\n')
	}
	return []byte(metadata.String())
}
