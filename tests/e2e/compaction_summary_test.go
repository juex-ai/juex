package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/runtime/workmem"
)

func TestEndToEnd_AnthropicCompactionRecoversFromReasoningBudgetExhaustion(t *testing.T) {
	const goal = "Preserve task CMP-2417."
	const acceptance = "Keep the exact branch and pending check."
	const note = "Run the live compaction evaluation."
	const summary = "Goal\n" + goal + "\n" + acceptance + "\nStatus: success\nCritical Context\nhigh/context-projection\nNext Steps\n" + note
	var mu sync.Mutex
	var budgets []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			MaxTokens int             `json:"max_tokens"`
			Stream    bool            `json:"stream"`
			System    json.RawMessage `json:"system"`
			Messages  json.RawMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode provider request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if !request.Stream {
			t.Error("expected the real streaming Anthropic adapter")
		}
		isSummary := strings.Contains(string(request.System), "preparing a compact summary")
		text, stopReason, outputTokens := "continued", "end_turn", 1
		if isSummary {
			mu.Lock()
			budgets = append(budgets, request.MaxTokens)
			mu.Unlock()
			for _, want := range []string{goal, acceptance, note, "high/context-projection"} {
				if !strings.Contains(string(request.Messages), want) {
					t.Errorf("summary request missing authoritative value %q", want)
				}
			}
			text, outputTokens = summary, 1558
			if request.MaxTokens < 2048 {
				text, stopReason, outputTokens = "", "max_tokens", request.MaxTokens
			}
		} else if !strings.Contains(string(request.Messages), "high/context-projection") {
			t.Error("continuation request lost the compacted branch")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(kind string, fields map[string]any) {
			fields["type"] = kind
			raw, err := json.Marshal(fields)
			if err != nil {
				t.Errorf("encode stream event: %v", err)
				return
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, raw)
		}
		send("message_start", map[string]any{"message": map[string]any{
			"id": "msg_summary", "type": "message", "role": "assistant", "model": "thinking-model",
			"content": []any{}, "stop_reason": nil, "usage": map[string]any{"input_tokens": 10, "output_tokens": 0},
		}})
		index := 0
		if isSummary {
			send("content_block_start", map[string]any{"index": index, "content_block": map[string]any{"type": "thinking", "thinking": "", "signature": ""}})
			send("content_block_delta", map[string]any{"index": index, "delta": map[string]any{"type": "thinking_delta", "thinking": "prepare the summary"}})
			send("content_block_delta", map[string]any{"index": index, "delta": map[string]any{"type": "signature_delta", "signature": "test-signature"}})
			send("content_block_stop", map[string]any{"index": index})
			index++
		}
		if text != "" {
			send("content_block_start", map[string]any{"index": index, "content_block": map[string]any{"type": "text", "text": ""}})
			send("content_block_delta", map[string]any{"index": index, "delta": map[string]any{"type": "text_delta", "text": text}})
			send("content_block_stop", map[string]any{"index": index})
		}
		send("message_delta", map[string]any{"delta": map[string]any{"stop_reason": stopReason}, "usage": map[string]any{"output_tokens": outputTokens}})
		send("message_stop", map[string]any{})
	}))
	defer server.Close()
	provider, err := llm.New(llm.Config{ID: "summary-test", Protocol: "anthropic/messages", BaseURL: server.URL, APIKey: "test-key", Model: "thinking-model"})
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	compaction := config.DefaultCompactionConfig()
	compaction.KeepRecentTokens = 1
	compaction.SummaryMaxTokens = 2048
	a, err := app.New(app.Options{
		Config: config.Config{
			ProviderProtocol: "anthropic/messages", ContextWindow: 32000,
			WorkDir: work, HomeJuexDir: t.TempDir(), AgentStateDir: t.TempDir(), Compaction: compaction,
		},
		Provider: provider, WorkDir: work, DisableMCP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	goals, notes := runtime.ThreadStateStoresFromModules(a.Engine.ThreadRuntimeSnapshot().Modules)
	if goals == nil || notes == nil {
		t.Fatal("missing authoritative Session state stores")
	}
	if _, err := goals.Create(goal, acceptance); err != nil {
		t.Fatal(err)
	}
	// Keep the Goal contract without asking the scripted model to finish work.
	if _, err := goals.Update(workmem.GoalStateUpdate{Status: workmem.GoalStatusSuccess}); err != nil {
		t.Fatal(err)
	}
	if _, err := notes.Update("- [ ] " + note); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		for _, message := range []llm.Message{
			llm.TextMessage(llm.RoleUser, "Branch: high/context-projection. "+strings.Repeat("old context ", 50)),
			llm.TextMessage(llm.RoleAssistant, "stored"),
		} {
			if err := a.Thread.Append(message); err != nil {
				t.Fatal(err)
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := a.CompactWithInstructions(ctx, "manual", false, ""); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	gotBudgets := slices.Clone(budgets)
	mu.Unlock()
	if !slices.Equal(gotBudgets, []int{160, 2048}) {
		t.Fatalf("summary output budgets = %v, want [160 2048]", gotBudgets)
	}
	markers := 0
	for _, message := range a.Thread.History {
		if message.Kind == llm.MessageKindCompact {
			markers++
			if message.Compaction == nil || message.Compaction.SummaryChars != len(summary) || !strings.Contains(message.FirstText(), "Summary of earlier conversation:\n"+summary) || strings.Contains(message.FirstText(), "prepare the summary") {
				t.Fatalf("compact marker = %+v, want complete text without reasoning", message)
			}
		}
	}
	if markers != 1 {
		t.Fatalf("compact markers = %d, want 1", markers)
	}
	if usage := a.Thread.TokenUsageSnapshot(); usage.OutputTokens != 1718 {
		t.Fatalf("summary usage = %+v, want both attempts counted", usage)
	}
	eventText := strings.Join(readLines(t, filepath.Join(a.Thread.Dir, "journal.jsonl")), "\n")
	if strings.Count(eventText, `"type":"context.compact.summary_retry"`) != 1 || !strings.Contains(eventText, `"reasoning_only":true`) {
		t.Fatalf("missing single reasoning-only retry: %s", eventText)
	}
	if out, err := a.Run(ctx, "Continue from the summary."); err != nil || out != "continued" {
		t.Fatalf("continuation = %q, %v", out, err)
	}
}
