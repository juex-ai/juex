//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/app/migration"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
	runtimepg "github.com/juex-ai/juex/internal/managedruntime/postgres"
	"github.com/juex-ai/juex/internal/management"
	"github.com/juex-ai/juex/internal/migration/legacy"
)

func TestLegacyRuntimeConversionPreservesAPIHistoryAndContinuation(t *testing.T) {
	var calls atomic.Int32
	var providerBody atomic.Value
	f := managedRuntimeHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		providerBody.Store(string(body))
		streamManagedReply(w, "New turn completed")
	})
	ctx := context.Background()
	agent, err := f.directory.CreateAgent(ctx, f.actor, f.tenant, f.actor, management.AgentConfig{Name: "Imported source", Configuration: &management.Configuration{Models: f.agent.Configuration.Models}})
	if err != nil {
		t.Fatal(err)
	}
	scope, err := f.authority.Authorize(ctx, f.actor, f.tenant, agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	original := strings.Repeat("完整正文", 1200) + "PRESERVED-MIDDLE" + strings.Repeat("末尾正文", 1200)
	source := legacyRuntimeSource(original)
	converted, err := migration.ConvertRuntime(scope, source, migration.RuntimeBindings{SourceSHA256: strings.Repeat("a", 64)})
	if err != nil {
		t.Fatal(err)
	}
	var before int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.notification_outbox`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := f.store.ImportAgent(ctx, scope, converted.Import); err != nil {
		t.Fatal(err)
	}
	var after, attempts int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.notification_outbox`).Scan(&after); err != nil || after != before {
		t.Fatal("historical import published live notifications", before, after, err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.attempts`).Scan(&attempts); err != nil || attempts != 0 || calls.Load() != 0 {
		t.Fatal("historical import ran provider work", attempts, calls.Load(), err)
	}
	base := f.origin + "/api/tenants/" + f.tenant + "/agents/" + agent.ID
	threads := managementCall[[]managedruntime.Thread](t, f.client, "GET", base+"/threads", f.origin, nil, 200)
	main := converted.Identities.Threads["0"]
	if len(threads) != 1 || threads[0].ID != main || threads[0].State != "idle" || threads[0].PendingInputs != 0 {
		t.Fatal("API replaced the imported Main or queued historical input", threads)
	}
	timeline := managementCall[managedruntime.Timeline](t, f.client, "GET", base+"/threads/"+main+"/events", f.origin, nil, 200)
	var foundText, foundCommit, foundInput bool
	policyHistory := map[string]bool{}
	for _, event := range timeline.Events {
		switch event.Kind {
		case "message.appended":
			var message llm.Message
			if err := json.Unmarshal(event.Data, &message); err != nil {
				t.Fatal(err)
			}
			if message.ID == converted.Identities.Messages["0"]["source-text"] {
				foundText = message.FirstText() == original && message.Blocks[0].Artifact == nil
			}
			for _, oldID := range []string{"blocked", "policy"} {
				if message.ID == converted.Identities.Messages["0"][oldID] {
					policyHistory[oldID] = true
				}
			}
		case "import.commit":
			var record migration.SourceCommit
			if err := json.Unmarshal(event.Data, &record); err != nil {
				t.Fatal(err)
			}
			if record.Commit.Seq == 3 {
				foundCommit = strings.Contains(string(record.Commit.Facts[0].Event), "turn.cancelled")
			}
		case "import.input":
			var record struct {
				Receipt managedruntime.InputReceipt `json:"receipt"`
			}
			if err := json.Unmarshal(event.Data, &record); err != nil {
				t.Fatal(err)
			}
			foundInput = record.Receipt.State == "cancelled"
		}
	}
	if !foundText || !foundCommit || !foundInput || len(policyHistory) != 2 {
		t.Fatal("API lost original text, historical facts or input outcome", foundText, foundCommit, foundInput)
	}
	// A reopened owner store resolves generated references to canonical messages,
	// without any legacy spool directory or historical request replay.
	restarted := runtimepg.New(f.pool)
	reference := managedruntime.ContextReference(converted.Identities.Messages["0"]["source-text"], 0, "text")
	var recovered strings.Builder
	for offset := 0; offset < len(original); {
		page, err := restarted.ReadContext(ctx, scope, main, reference, offset, 257)
		if err != nil || page.NextOffset <= offset {
			t.Fatal("converted context reference unreadable", err)
		}
		recovered.WriteString(page.Text)
		offset = page.NextOffset
	}
	if recovered.String() != original {
		t.Fatal("restored spool bytes changed")
	}
	foreign := scope
	foreign.UserID = uuid.NewString()
	if _, err := restarted.ReadContext(ctx, foreign, main, reference, 0, 100); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("historical reference crossed owner authority", err)
	}
	request := managedruntime.InputRequest{RequestID: "new-after-import", ThreadID: main, Text: "continue from imported context"}
	input := managementCall[managedruntime.InputReceipt](t, f.client, "POST", base+"/inputs", f.origin, request, 200)
	f.run(t)
	runtimeEventually(t, func() bool {
		var state string
		return f.pool.QueryRow(ctx, `SELECT state FROM runtime.inputs WHERE id=$1`, input.ID).Scan(&state) == nil && state == "completed"
	})
	body, _ := providerBody.Load().(string)
	if calls.Load() != 1 || !strings.Contains(body, "imported summary") || !strings.Contains(body, reference) || !strings.Contains(body, "continue from imported context") || strings.Contains(body, "cancelled-old-input") {
		t.Fatal("provider did not receive only the retained context and new input", calls.Load())
	}
	if strings.Contains(body, "POLICY-BLOCKED-PROOF") || strings.Contains(body, "POLICY-DISPLAY-PROOF") {
		t.Error("continuation sent source policy-only content to the provider")
	}
	compact := managementCall[managedruntime.InputReceipt](t, f.client, "POST", base+"/threads/"+main+"/compact", f.origin, managedruntime.CompactionRequest{RequestID: "compact-after-import"}, 200)
	runtimeEventually(t, func() bool {
		var state string
		return f.pool.QueryRow(ctx, `SELECT state FROM runtime.inputs WHERE id=$1`, compact.ID).Scan(&state) == nil && state == "completed"
	})
	body, _ = providerBody.Load().(string)
	if calls.Load() != 2 || !strings.Contains(body, "Summarize this conversation") {
		t.Fatal("expected real compaction protocol request", calls.Load())
	}
	if strings.Contains(body, "POLICY-BLOCKED-PROOF") || strings.Contains(body, "POLICY-DISPLAY-PROOF") {
		t.Error("compaction sent source policy-only content to the provider")
	}
	if err := restarted.ImportAgent(ctx, scope, converted.Import); err != nil {
		t.Fatal("retry after live continuation was not idempotent", err)
	}
	var oldState string
	if err := f.pool.QueryRow(ctx, `SELECT state FROM runtime.inputs WHERE id=$1`, converted.Identities.Inputs["0"]["old-input"]).Scan(&oldState); err != nil || oldState != "cancelled" {
		t.Fatal("historical input changed during continuation", oldState, err)
	}
}

func legacyRuntimeSource(original string) legacy.Agent {
	at := time.Date(2026, 9, 20, 1, 2, 3, 0, time.UTC)
	stamp := at.Format("2006-01-02T15:04:05.000Z")
	gen1, gen2 := legacy.Generation{ID: "g000001", Ordinal: 1, BoundarySeq: 1}, legacy.Generation{ID: "g000002", Ordinal: 2, BoundarySeq: 4}
	text := llm.Message{ID: "source-text", Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockText, Text: "old bounded preview", Artifact: &llm.ContextArtifactProjection{SourceKind: "user_input", MessageID: "source-text", StoredPath: "text.txt", SHA256: execprotocol.FileDigest([]byte(original)), OriginalBytes: len(original)}}}}
	summary := llm.Message{ID: "source-summary", Role: llm.RoleUser, Kind: llm.MessageKindCompact, Blocks: []llm.Block{{Type: llm.BlockText, Text: "Context compacted automatically because the provider context window is nearing its limit.\n\nSummary of earlier conversation:\nimported summary\n\nRetained Input References\n\nMessage source-text:\nold bounded preview"}}, Compaction: &llm.CompactionMetadata{SummaryChars: len("imported summary"), RetainedInputReferences: []llm.Message{text}}}
	input := llm.TextMessage(llm.RoleUser, "cancelled-old-input")
	input.ID = "original-input-message"
	// Compaction changes the generation while retaining the original input scope.
	thread := legacy.Thread{ContextScopeID: gen1.ID, Metadata: legacy.ThreadMetadata{ThreadID: "0", Alias: "main", CreatedAt: stamp, UpdatedAt: stamp, RetentionState: "active", ExecutionState: "idle", CurrentGeneration: gen2, Generations: []legacy.Generation{gen1, gen2}}, Context: []llm.Message{summary}, Inputs: []legacy.Input{{ID: "old-input", TurnID: "old-turn", MessageID: input.ID, Message: input, State: "settled", CreatedAt: at}}, Commits: []legacy.Commit{
		{Version: 1, Seq: 1, At: stamp, GenerationID: gen1.ID, Facts: []legacy.Fact{{Type: "thread.created", ThreadID: "0", Alias: "main"}}},
		{Version: 1, Seq: 2, At: stamp, GenerationID: gen1.ID, Facts: []legacy.Fact{{Type: "message.appended", Message: &text}, {Type: "message.appended", Message: &input}}},
		{Version: 1, Seq: 3, At: stamp, GenerationID: gen1.ID, Facts: []legacy.Fact{{Type: "event.recorded", Event: json.RawMessage(`{"type":"turn.cancelled","turn_id":"old-turn","payload":{"input_ids":["old-input"]}}`)}}},
		{Version: 1, Seq: 4, At: stamp, GenerationID: gen2.ID, Facts: []legacy.Fact{{Type: "context.compacted", Summary: &summary, Seed: &legacy.GenerationSeed{ContextScopeID: gen1.ID, ProviderMessages: []llm.Message{summary}}}}},
	}}
	continued := legacy.Commit{Version: 1, Seq: 5, At: stamp, GenerationID: gen2.ID}
	for i := range 8 {
		message := llm.TextMessage(llm.RoleAssistant, strings.Repeat("Visible retained detail. ", 100))
		message.ID = fmt.Sprintf("history-%d", i)
		continued.Facts = append(continued.Facts, legacy.Fact{Type: "message.appended", Message: &message})
		thread.Context = append(thread.Context, message)
	}
	blocked, policy := llm.TextMessage(llm.RoleUser, "POLICY-BLOCKED-PROOF"), llm.TextMessage(llm.RoleUser, "POLICY-DISPLAY-PROOF")
	blocked.ID, blocked.PolicyBlocked = "blocked", true
	policy.ID, policy.Kind = "policy", llm.MessageKindPolicyEvent
	continued.Facts = append(continued.Facts, legacy.Fact{Type: "message.appended", Message: &blocked}, legacy.Fact{Type: "message.appended", Message: &policy})
	thread.Commits = append(thread.Commits, continued)
	thread.Context = append(thread.Context, blocked, policy)
	return legacy.Agent{Definition: legacy.AgentDefinition{ID: "abcdef"}, Threads: []legacy.Thread{thread}, Files: []legacy.SourceFile{{Path: "threads/0/spool/text.txt", Data: []byte(original), SHA256: execprotocol.FileDigest([]byte(original)), Size: int64(len(original))}}}
}
