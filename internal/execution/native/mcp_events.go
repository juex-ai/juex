package native

import (
	"bytes"
	"errors"
	"io"
)

// mcpEventBody observes SSE without replacing the SDK connection or changing
// its bytes. This also captures extension notifications the SDK does not know.
type mcpEventBody struct {
	io.ReadCloser
	receive func([]byte) error
	failed  func(error)
	line    []byte
	data    []byte
	name    string
	failure error
}

func (b *mcpEventBody) Read(p []byte) (int, error) {
	if b.failure != nil {
		return 0, b.failure
	}
	n, err := b.ReadCloser.Read(p)
	for _, value := range p[:n] {
		if value == '\n' {
			b.failure = b.endLine()
		} else {
			// Match the executor's operation-output budget while assembling a
			// wire event. Notification JSONL has its own stricter 1 MiB bound.
			if len(b.line)+len(b.data) >= 8<<20 {
				b.failure = errors.New("MCP SSE event exceeds 8 MiB")
			} else {
				b.line = append(b.line, value)
			}
		}
		if b.failure != nil {
			if b.failed != nil {
				b.failed(b.failure)
			}
			return 0, b.failure
		}
	}
	if errors.Is(err, io.EOF) {
		if len(b.line) > 0 {
			b.failure = b.endLine()
		}
		if b.failure == nil {
			b.failure = b.endLine()
		}
		if b.failure != nil {
			if b.failed != nil {
				b.failed(b.failure)
			}
			return 0, b.failure
		}
	}
	return n, err
}

func (b *mcpEventBody) endLine() error {
	line := bytes.TrimRight(b.line, "\r")
	defer func() { b.line = b.line[:0] }()
	if len(line) == 0 {
		defer func() { b.name = ""; b.data = b.data[:0] }()
		if len(b.data) == 0 {
			return nil
		}
		if b.name == "" || b.name == "message" {
			return b.receive(b.data)
		}
		return nil
	}
	key, value, ok := bytes.Cut(line, []byte(":"))
	if ok && bytes.Equal(key, []byte("event")) {
		b.name = string(bytes.TrimSpace(value))
	}
	if ok && bytes.Equal(key, []byte("data")) {
		if len(b.data) > 0 {
			b.data = append(b.data, '\n')
		}
		b.data = append(b.data, bytes.TrimSpace(value)...)
	}
	return nil
}
