package llm

import (
	"bytes"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/anthropics/anthropic-sdk-go/option"
)

const anthropicStreamPreviewBytes = 512

var (
	anthropicStreamIndexRE = regexp.MustCompile(`"index"\s*:\s*(-?\d+)`)
	anthropicStreamTypeRE  = regexp.MustCompile(`"type"\s*:\s*"([^"]+)"`)
)

type anthropicStreamDiagnostic struct {
	EventType  string
	Index      int64
	HasIndex   bool
	RawPreview string
}

type anthropicStreamDiagnostics struct {
	mu             sync.Mutex
	line           []byte
	currentType    string
	currentData    []byte
	lastDispatched anthropicStreamDiagnostic
}

func (d *anthropicStreamDiagnostics) middleware(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
	resp, err := next(req)
	if err != nil || resp == nil || resp.Body == nil {
		return resp, err
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		return resp, nil
	}
	resp.Body = &anthropicDiagnosticBody{ReadCloser: resp.Body, diagnostics: d}
	return resp, nil
}

func (d *anthropicStreamDiagnostics) last() anthropicStreamDiagnostic {
	if d == nil {
		return anthropicStreamDiagnostic{}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lastDispatched
}

func (d *anthropicStreamDiagnostics) observe(chunk []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()

	for len(chunk) > 0 {
		lineEnd := bytes.IndexByte(chunk, '\n')
		if lineEnd < 0 {
			d.line = appendBoundedBytes(d.line, chunk, anthropicStreamPreviewBytes)
			return
		}
		line := append([]byte{}, d.line...)
		line = append(line, chunk[:lineEnd]...)
		d.line = d.line[:0]
		d.observeLine(bytes.TrimSuffix(line, []byte{'\r'}))
		chunk = chunk[lineEnd+1:]
	}
}

func (d *anthropicStreamDiagnostics) observeLine(line []byte) {
	if len(line) == 0 {
		d.dispatchCurrent()
		return
	}
	name, value, ok := bytes.Cut(line, []byte(":"))
	if !ok {
		return
	}
	if len(value) > 0 && value[0] == ' ' {
		value = value[1:]
	}
	switch string(name) {
	case "event":
		d.currentType = string(value)
	case "data":
		d.currentData = appendBoundedBytes(d.currentData, value, anthropicStreamPreviewBytes)
		d.currentData = appendBoundedBytes(d.currentData, []byte("\n"), anthropicStreamPreviewBytes)
	}
}

func (d *anthropicStreamDiagnostics) dispatchCurrent() {
	raw := strings.TrimSuffix(string(d.currentData), "\n")
	if d.currentType == "" && raw == "" {
		return
	}
	eventType := d.currentType
	if eventType == "" {
		eventType = extractAnthropicStreamType(raw)
	}
	idx, hasIndex := extractAnthropicStreamIndex(raw)
	d.lastDispatched = anthropicStreamDiagnostic{
		EventType:  eventType,
		Index:      idx,
		HasIndex:   hasIndex,
		RawPreview: trimStreamPreview(raw),
	}
	d.currentType = ""
	d.currentData = d.currentData[:0]
}

type anthropicDiagnosticBody struct {
	io.ReadCloser
	diagnostics *anthropicStreamDiagnostics
}

func (b *anthropicDiagnosticBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.diagnostics.observe(p[:n])
	}
	return n, err
}

func appendBoundedBytes(dst, src []byte, limit int) []byte {
	if len(dst) >= limit {
		return dst
	}
	remaining := limit - len(dst)
	if len(src) > remaining {
		src = src[:remaining]
	}
	return append(dst, src...)
}

func extractAnthropicStreamType(raw string) string {
	match := anthropicStreamTypeRE.FindStringSubmatch(raw)
	if len(match) != 2 {
		return ""
	}
	return match[1]
}

func extractAnthropicStreamIndex(raw string) (int64, bool) {
	match := anthropicStreamIndexRE.FindStringSubmatch(raw)
	if len(match) != 2 {
		return 0, false
	}
	idx, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return idx, true
}

func trimStreamPreview(raw string) string {
	raw = strings.TrimSpace(raw)
	if len(raw) <= anthropicStreamPreviewBytes {
		return raw
	}
	return raw[:anthropicStreamPreviewBytes]
}
