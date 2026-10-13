package openai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/juex-ai/juex/internal/foundation/llm"
	openaiclient "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
)

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
