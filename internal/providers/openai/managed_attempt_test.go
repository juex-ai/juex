package openai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/coder/websocket"
	"github.com/juex-ai/juex/internal/foundation/llm"
	providerprofile "github.com/juex-ai/juex/internal/providers/profile"
	openaiclient "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
)

func TestManagedCodexAutoDoesNotReplayAcceptedWebsocket(t *testing.T) {
	var wsRequests, sseRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !openaiIsWebsocketRequest(r) {
			sseRequests.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: %s\n\n", openaiCodexCompletedWebsocketEvent("replay", "unexpected replay"))
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if _, _, err := conn.Read(ctx); err != nil {
			t.Error(err)
			return
		}
		wsRequests.Add(1)
		if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"partial answer"}`)); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	p := NewOpenAICodexResponses(openaiTestProfile(t, providerprofile.Config{ID: "openai-codex", BaseURL: server.URL, APIKey: "fixture", Model: "m", Compat: llm.CompatOptions{CodexTransport: providerprofile.CodexTransportAuto}}), nil)
	var preview string
	_, err := llm.CompleteWithOptions(context.Background(), p, "", []llm.Message{llm.TextMessage(llm.RoleUser, "hello")}, nil, llm.CompleteOptions{SingleAttempt: true, OnDelta: func(delta llm.StreamDelta) { preview += delta.Text }})
	if err == nil || wsRequests.Load() != 1 || sseRequests.Load() != 0 || preview != "partial answer" {
		t.Fatalf("ws=%d sse=%d preview=%q err=%v", wsRequests.Load(), sseRequests.Load(), preview, err)
	}
}

func TestManagedResponsesIdleDoesNotRetry(t *testing.T) {
	// Advance virtual time only after the wire attempt is observed, so cold
	// HTTP scheduling cannot consume the entire idle budget before admission.
	synctest.Test(t, func(t *testing.T) {
		var requests atomic.Int32
		client := &http.Client{Transport: openaiRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			requests.Add(1)
			reader, writer := io.Pipe()
			go func() {
				<-r.Context().Done()
				_ = writer.CloseWithError(r.Context().Err())
			}()
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: reader, Request: r}, nil
		})}
		p := NewOpenAIResponses(llm.ProviderProfile{ID: "fixture", Protocol: llm.ProtocolOpenAIResponses, Model: "fixture", Capabilities: llm.ProviderCapabilities{Streaming: true}}, nil).(*openAIResponsesProvider)
		p.client = openaiclient.NewClient(option.WithAPIKey("test-key"), option.WithBaseURL("https://fixture.invalid"), option.WithHTTPClient(client))
		_, err := llm.CompleteWithOptions(context.Background(), p, "", []llm.Message{llm.TextMessage(llm.RoleUser, "hello")}, nil, llm.CompleteOptions{SingleAttempt: true, StreamIdleTimeout: 20 * time.Millisecond})
		if err == nil || !strings.Contains(err.Error(), "idle timeout") || requests.Load() != 1 {
			t.Fatalf("requests=%d err=%v", requests.Load(), err)
		}
	})
}

func TestManagedCodexSSEDoesNotRetry(t *testing.T) {
	for _, failure := range []string{"idle", "read", "http"} {
		t.Run(failure, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var requests atomic.Int32
				client := &http.Client{Transport: openaiRoundTripFunc(func(r *http.Request) (*http.Response, error) {
					requests.Add(1)
					status := http.StatusOK
					var body io.ReadCloser
					switch failure {
					case "idle":
						reader, writer := io.Pipe()
						go func() { <-r.Context().Done(); _ = writer.CloseWithError(r.Context().Err()) }()
						body = reader
					case "read":
						body = openaiErrReadCloser{err: io.ErrUnexpectedEOF}
					case "http":
						status = http.StatusInternalServerError
						body = io.NopCloser(strings.NewReader(`{"error":{"message":"unavailable"}}`))
					}
					return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: body, Request: r}, nil
				})}
				p := NewOpenAICodexResponses(llm.ProviderProfile{ID: "fixture", Protocol: llm.ProtocolOpenAICodexResponses, BaseURL: "https://fixture.invalid", APIKey: "test-key", Model: "fixture"}, client)
				_, err := llm.CompleteWithOptions(context.Background(), p, "", []llm.Message{llm.TextMessage(llm.RoleUser, "hello")}, nil, llm.CompleteOptions{SingleAttempt: true, StreamIdleTimeout: 20 * time.Millisecond})
				if err == nil || requests.Load() != 1 {
					t.Fatalf("requests=%d err=%v", requests.Load(), err)
				}
				if failure == "idle" {
					openaiRequireStreamIdleTimeout(t, err)
				}
				if failure == "read" && !errors.Is(err, io.ErrUnexpectedEOF) {
					t.Fatal(err)
				}
			})
		})
	}
}
