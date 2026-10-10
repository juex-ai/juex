package native

import (
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

func TestMCPEventBodyPreservesBytesAndCapturesSplitEvents(t *testing.T) {
	input := ": keepalive\r\nevent: message\r\ndata: {\"method\":\r\ndata: \"notification\"}\r\n\r\nevent: message\ndata: second"
	var events []string
	body := &mcpEventBody{ReadCloser: io.NopCloser(iotest.OneByteReader(strings.NewReader(input))), receive: func(data []byte) error {
		events = append(events, string(data))
		return nil
	}}
	output, err := io.ReadAll(body)
	if err != nil || string(output) != input || len(events) != 2 || events[0] != "{\"method\":\n\"notification\"}" || events[1] != "second" {
		t.Fatal(string(output), events, err)
	}
}

func TestMCPEventBodyFailsClosedOnCaptureAndSizeErrors(t *testing.T) {
	want := errors.New("journal failed")
	for _, input := range []string{"data: notification\n\n", "data: " + strings.Repeat("x", 8<<20)} {
		body := &mcpEventBody{ReadCloser: io.NopCloser(strings.NewReader(input)), receive: func([]byte) error { return want }}
		_, err := io.ReadAll(body)
		if err == nil || len(input) < 100 && !errors.Is(err, want) {
			t.Fatal(err)
		}
		if _, retry := body.Read(make([]byte, 1)); retry != err {
			t.Fatal("capture failure must remain terminal", retry, err)
		}
	}
}

func TestMCPEventBodyIgnoresNonMessageEvents(t *testing.T) {
	const notification = `{"jsonrpc":"2.0","method":"notifications/claude/channel","params":{"content":"marker"}}`
	input := "event: ping\ndata: " + notification + "\n\nevent: endpoint\ndata: " + notification + "\n\nevent: message\ndata: " + notification + "\n\ndata: " + notification + "\n\n"
	var count int
	body := &mcpEventBody{ReadCloser: io.NopCloser(strings.NewReader(input)), receive: func([]byte) error { count++; return nil }}
	output, err := io.ReadAll(body)
	if err != nil || string(output) != input || count != 2 {
		t.Fatal(count, err)
	}
}
