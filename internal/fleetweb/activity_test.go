package fleetweb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/fleet"
)

func TestActivityClientPoolReusesAndPrunesClients(t *testing.T) {
	pool := newActivityClientPool()
	t.Cleanup(pool.close)

	const (
		firstEndpoint  = "tcp://127.0.0.1:41001"
		secondEndpoint = "tcp://127.0.0.1:41002"
	)
	first, err := pool.client(firstEndpoint)
	if err != nil {
		t.Fatal(err)
	}
	firstAgain, err := pool.client(firstEndpoint)
	if err != nil {
		t.Fatal(err)
	}
	if firstAgain != first {
		t.Fatal("same endpoint did not reuse its HTTP client")
	}
	second, err := pool.client(secondEndpoint)
	if err != nil {
		t.Fatal(err)
	}

	pool.retain(map[string]struct{}{firstEndpoint: {}})

	firstAfterRetain, err := pool.client(firstEndpoint)
	if err != nil {
		t.Fatal(err)
	}
	if firstAfterRetain != first {
		t.Fatal("retained endpoint lost its cached HTTP client")
	}
	secondAfterPrune, err := pool.client(secondEndpoint)
	if err != nil {
		t.Fatal(err)
	}
	if secondAfterPrune == second {
		t.Fatal("pruned endpoint reused a stale HTTP client")
	}
}

func TestActivityClientDecodesSharedStatusContract(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"state":"working",
			"pending_input_count":1,
			"selected_status":{
				"session":{
					"id":"session-current",
					"state":"turn_active",
					"working":true,
					"pending_count":1,
					"max_pending_inputs":4,
					"can_accept_input":true
				},
				"tools":[],
				"token_usage":{"input_tokens":0,"output_tokens":0}
			}
		}`))
	}))
	defer upstream.Close()
	pool := newActivityClientPool()
	t.Cleanup(pool.close)

	activity, err := pool.fetch(context.Background(), fleet.AgentStatus{
		Endpoint: strings.Replace(upstream.URL, "http://", "tcp://", 1),
	})
	if err != nil {
		t.Fatal(err)
	}
	if activity.SelectedStatus == nil ||
		!activity.SelectedStatus.Session.Working ||
		activity.PendingInputCount != 1 {
		t.Fatalf("activity = %+v", activity)
	}
}
