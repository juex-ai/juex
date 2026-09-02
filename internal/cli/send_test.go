package cli

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/endpoint"
	"github.com/spf13/cobra"
)

func TestWaitForInputFiltersOtherTurnsAndReportsTerminalJSON(t *testing.T) {
	client, closeClient := newSendStreamTestClient(t, []string{
		`{"id":"other","type":"turn.completed","turn_id":"turn-other"}`,
		`{"id":"delta","type":"llm.output_delta","turn_id":"turn-1","payload":{"text":"done"}}`,
		`{"id":"terminal","type":"turn.completed","turn_id":"turn-1"}`,
	})
	defer closeClient()
	cmd, output := sendWaitTestCommand()
	receipt := sendReceipt{ThreadID: "0", InputID: "input-1", TurnID: "turn-1", Cursor: "cursor-1"}
	if err := waitForInput(cmd, client, receipt, true); err != nil {
		t.Fatal(err)
	}
	got := output.String()
	if strings.Contains(got, "turn-other") || !strings.Contains(got, `"type":"llm.output_delta"`) ||
		!strings.Contains(got, `"type":"input.terminal"`) || !strings.Contains(got, `"state":"succeeded"`) {
		t.Fatalf("wait output = %q", got)
	}
}

func TestWaitForInputFollowsQueuedPromotion(t *testing.T) {
	client, closeClient := newSendStreamTestClient(t, []string{
		`{"id":"promoted","type":"pending_input.promoted","turn_id":"turn-2","payload":{"input_ids":["input-2"]}}`,
		`{"id":"terminal","type":"turn.completed","turn_id":"turn-2"}`,
	})
	defer closeClient()
	cmd, _ := sendWaitTestCommand()
	if err := waitForInput(cmd, client, sendReceipt{ThreadID: "0", InputID: "input-2"}, false); err != nil {
		t.Fatal(err)
	}
}

func TestWaitForInputFollowsInputDrainedIntoActiveTurn(t *testing.T) {
	client, closeClient := newSendStreamTestClient(t, []string{
		`{"id":"draining","type":"pending_input.draining","turn_id":"turn-1","payload":{"input_ids":["input-2"],"count":1}}`,
		`{"id":"terminal","type":"turn.completed","turn_id":"turn-1"}`,
	})
	defer closeClient()
	cmd, _ := sendWaitTestCommand()
	if err := waitForInput(cmd, client, sendReceipt{ThreadID: "0", InputID: "input-2"}, false); err != nil {
		t.Fatal(err)
	}
}

func TestWaitForInputReturnsFailureForCancellationAndPrematureEOF(t *testing.T) {
	tests := []struct {
		name   string
		events []string
		want   string
	}{
		{name: "cancelled", events: []string{`{"type":"turn.cancelled","turn_id":"turn-1"}`}, want: "was cancelled"},
		{name: "premature eof", events: []string{`{"type":"llm.output_delta","turn_id":"turn-1","payload":{"text":"partial"}}`}, want: "closed before"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, closeClient := newSendStreamTestClient(t, test.events)
			defer closeClient()
			cmd, _ := sendWaitTestCommand()
			err := waitForInput(cmd, client, sendReceipt{ThreadID: "0", InputID: "input-1", TurnID: "turn-1"}, false)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("wait error = %v, want %q", err, test.want)
			}
		})
	}
}

func sendWaitTestCommand() (*cobra.Command, *bytes.Buffer) {
	output := &bytes.Buffer{}
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(output)
	cmd.SetErr(output)
	return cmd, output
}

func newSendStreamTestClient(t *testing.T, events []string) (*agentClient, func()) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/threads/0/events" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range events {
			fmt.Fprintf(w, "data: %s\n\n", event)
		}
	}))
	target, err := endpoint.Parse(strings.Replace(server.URL, "http://", "tcp://", 1))
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	transport := target.NewTransport()
	client := &agentClient{target: target, client: &http.Client{Transport: transport}}
	return client, func() {
		transport.CloseIdleConnections()
		server.Close()
	}
}
