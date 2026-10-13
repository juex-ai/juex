//go:build postgres

package e2e

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/management"
)

func TestManagedStreamMissingTerminalDoesNotCommitPartialOutput(t *testing.T) {
	for _, protocol := range []llm.Protocol{llm.ProtocolOpenAIChat, llm.ProtocolOpenAIResponses, llm.ProtocolOpenAICodexResponses} {
		for _, test := range []struct {
			name, delta, responseDelta, item string
			visible                          bool
		}{
			{"reasoning", `{"role":"assistant","reasoning_content":"unfinished thought"}`, `{"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":0,"delta":"unfinished thought"}`, `{"type":"reasoning","id":"rs","summary":[{"type":"summary_text","text":"unfinished thought"}]}`, true},
			{"text", `{"role":"assistant","content":"unfinished answer"}`, `{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"unfinished answer"}`, `{"type":"message","id":"msg","role":"assistant","status":"completed","content":[{"type":"output_text","text":"unfinished answer","annotations":[]}]}`, true},
			{"tool", `{"role":"assistant","tool_calls":[{"index":0,"id":"partial","type":"function","function":{"name":"read_context","arguments":"{\"reference\":\"missing\"}"}}]}`, "", `{"type":"function_call","id":"fc","call_id":"partial","name":"read_context","arguments":"{\"reference\":\"missing\"}","status":"completed"}`, false},
		} {
			t.Run(string(protocol)+"/"+test.name, func(t *testing.T) {
				var primaryCalls, fallbackCalls atomic.Int32
				f := managedRuntimeHTTP(t, func(w http.ResponseWriter, _ *http.Request) {
					if primaryCalls.Add(1) > 1 {
						if protocol == llm.ProtocolOpenAIChat {
							streamManagedReply(w, "Unexpected continuation")
						} else {
							w.Header().Set("Content-Type", "text/event-stream")
							fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"unexpected\",\"status\":\"completed\",\"output\":[]}}\n\n")
						}
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					if protocol == llm.ProtocolOpenAIChat {
						fmt.Fprintf(w, "data: {\"id\":\"partial\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":%s,\"finish_reason\":null}]}\n\n", test.delta)
					} else {
						if test.responseDelta != "" {
							fmt.Fprintf(w, "data: %s\n\n", test.responseDelta)
						}
						fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":%s}\n\n", test.item)
					}
				})
				backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					fallbackCalls.Add(1)
					streamManagedReply(w, "Backup answer")
				}))
				defer backup.Close()
				ctx := context.Background()
				scope, err := f.authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
				if err != nil {
					t.Fatal(err)
				}
				plan, err := f.authority.Snapshot(ctx, scope)
				if err != nil {
					t.Fatal(err)
				}
				providerID := "fixture"
				maxOutput := 4096
				options := management.ModelOptions{}
				if protocol == llm.ProtocolOpenAICodexResponses {
					providerID = "openai-codex"
					maxOutput = 0
					options.Headers = map[string]string{"ChatGPT-Account-ID": "fixture-account"}
				}
				primary, err := f.directory.ConfigureModel(ctx, management.ModelConfiguration{Provider: providerID, Name: "test-model", Protocol: protocol, Endpoint: plan.Models[0].Endpoint, APIKey: "test-key", ContextWindow: 32768, MaxOutput: maxOutput, OutputReserve: 4096, Enabled: true, Options: options})
				if err != nil {
					t.Fatal(err)
				}
				model, err := f.directory.ConfigureModel(ctx, management.ModelConfiguration{Provider: "alternate", Name: "backup", Protocol: llm.ProtocolOpenAIChat, Endpoint: backup.URL, APIKey: "test", ContextWindow: 32768, MaxOutput: 4096, OutputReserve: 4096, Enabled: true})
				if err != nil {
					t.Fatal(err)
				}
				if err := f.configureModels(ctx, []string{primary.ID, model.ID}); err != nil {
					t.Fatal(err)
				}
				f.submit(t, "truncated-stream", f.main.ID, "Read the input and answer")
				stop := f.run(t)
				runtimeEventually(t, func() bool { return f.timeline(t, f.main.ID).Thread.PendingInputs == 0 })
				stop()
				var toolCalls, failedAttempts, finals int
				if err := f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM runtime.tools), (SELECT count(*) FROM runtime.attempts WHERE state='failed'), (SELECT count(*) FROM runtime.events WHERE kind='message.appended' AND data->>'role'='assistant')`).Scan(&toolCalls, &failedAttempts, &finals); err != nil {
					t.Fatal(err)
				}
				if primaryCalls.Load() != 1 || toolCalls != 0 || failedAttempts != 1 {
					t.Fatalf("partial response committed: primary=%d tools=%d failed=%d", primaryCalls.Load(), toolCalls, failedAttempts)
				}
				value := f.timeline(t, f.main.ID)
				if test.visible {
					if fallbackCalls.Load() != 0 || finals != 0 || value.Thread.State != "failed" || len(value.Progress) != 1 || value.Progress[0].State != "failed" {
						t.Fatal("partial preview completed or silently fell back", fallbackCalls.Load(), finals, value)
					}
				} else if fallbackCalls.Load() != 1 || finals != 1 || value.Thread.State != "idle" || len(value.Progress) != 0 {
					t.Fatal("unexecuted partial tool prevented safe fallback", fallbackCalls.Load(), finals, value)
				}
			})
		}
	}
}
