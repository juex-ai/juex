package migration

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/migration/legacy"
)

func runtimeFixture() (managedruntime.Scope, legacy.Agent, RuntimeBindings) {
	scope := managedruntime.Scope{TenantID: uuid.NewString(), UserID: uuid.NewString(), FleetID: uuid.NewString(), AgentID: uuid.NewString()}
	at := "2026-09-20T01:02:03.000Z"
	created, _ := time.Parse(time.RFC3339Nano, at)
	message := func(id, text string) llm.Message {
		m := llm.TextMessage(llm.RoleUser, text)
		m.ID = id
		return m
	}
	old, kept, summary := message("old", "old conversation"), message("kept", "still relevant"), message("summary", "summary only in boundary and seed")
	summary.Kind = llm.MessageKindCompact
	summary.Compaction = &llm.CompactionMetadata{RetainedMessageIDs: []string{kept.ID}, FirstKeptMessageID: kept.ID, TailStartMessageID: kept.ID}
	gen1 := legacy.Generation{ID: "g000001", Ordinal: 1, BoundarySeq: 1}
	gen2 := legacy.Generation{ID: "g000002", Ordinal: 2, BoundarySeq: 4}
	commits := []legacy.Commit{
		{Version: 1, Seq: 1, At: at, GenerationID: gen1.ID, Facts: []legacy.Fact{{Type: "thread.created", ThreadID: "0", Alias: "main", GenerationID: gen1.ID}}},
		{Version: 1, Seq: 2, At: at, GenerationID: gen1.ID, Facts: []legacy.Fact{{Type: "message.appended", Message: &old}, {Type: "message.appended", Message: &kept}}},
		{Version: 1, Seq: 3, At: at, GenerationID: gen1.ID, Facts: []legacy.Fact{{Type: "event.recorded", Event: json.RawMessage(`{"type":"turn.cancelled","turn_id":"turn-old","payload":{"input_ids":["input-old"]}}`)}}},
		{Version: 1, Seq: 4, At: at, GenerationID: gen2.ID, Facts: []legacy.Fact{{Type: "context.compacted", Summary: &summary, Seed: &legacy.GenerationSeed{ContextScopeID: "g000001", ProviderMessages: []llm.Message{summary, kept}}}}},
	}
	main := legacy.Thread{ContextScopeID: "g000001", Metadata: legacy.ThreadMetadata{ThreadID: "0", Alias: "main", CreatedAt: at, UpdatedAt: at, RetentionState: "active", ExecutionState: "idle", CurrentGeneration: gen2, Generations: []legacy.Generation{gen1, gen2}}, Commits: commits, Context: []llm.Message{summary, kept}, Inputs: []legacy.Input{{ID: "input-old", MessageID: old.ID, TurnID: "turn-old", Message: old, State: "settled", CreatedAt: created}}}
	worker := legacy.Thread{ContextScopeID: "g000001", Metadata: legacy.ThreadMetadata{ThreadID: "abcdef", Alias: "review", ParentThreadID: "0", CreatedAt: at, UpdatedAt: at, RetentionState: "archived", CurrentGeneration: gen1, Generations: []legacy.Generation{gen1}}, Commits: []legacy.Commit{{Version: 1, Seq: 1, At: at, GenerationID: gen1.ID, Facts: []legacy.Fact{{Type: "thread.created", ThreadID: "abcdef", ParentThreadID: "0"}}}}}
	return scope, legacy.Agent{Definition: legacy.AgentDefinition{ID: "abcdef"}, Threads: []legacy.Thread{main, worker}}, RuntimeBindings{SourceSHA256: strings.Repeat("a", 64)}
}

func TestRuntimeConversionPreservesHistoryContextIdentityAndCommitIntervals(t *testing.T) {
	scope, source, bindings := runtimeFixture()
	before, _ := json.Marshal(source)
	converted, err := ConvertRuntime(scope, source, bindings)
	if err != nil {
		t.Fatal(err)
	}
	if err := converted.Import.Validate(scope.AgentID); err != nil {
		t.Fatal("converter produced invalid owner import", err)
	}
	main := converted.Import.Threads[0]
	if main.Thread.ID != converted.Identities.Threads["0"] || main.Thread.Generation != 2 || main.Thread.State != "idle" || converted.Import.Threads[1].Thread.ParentID != main.Thread.ID || converted.Import.Threads[1].Thread.Retention != "archived" {
		t.Fatal("Thread topology or state changed")
	}
	if got := main.Inputs[0]; got.ID != converted.Identities.Inputs["0"]["input-old"] || got.State != "cancelled" || got.Text != "old conversation" || got.RequestID != "import:"+got.ID {
		t.Fatal("source settled cancellation became a new or successful input", got)
	}
	if !reflect.DeepEqual(main.Context, []string{converted.Identities.Messages["0"]["summary"], converted.Identities.Messages["0"]["kept"]}) {
		t.Fatal("current context order was not retained", main.Context)
	}
	var messages, commits int
	for _, event := range main.Events {
		switch event.Kind {
		case "message.appended":
			messages++
		case "import.commit":
			commits++
			var record SourceCommit
			if err := json.Unmarshal(event.Data, &record); err != nil || record.ThreadID != "0" || record.GenerationID == "" || record.Commit.Seq != uint64(commits) || len(record.Commit.Facts) == 0 {
				t.Fatal("source commit lost its original facts", err)
			}
		}
	}
	if messages != 3 || commits != 4 || len(main.ModelOrigins) != 0 {
		t.Fatal("seed duplicates, lost history, or invented model origins", messages, commits)
	}
	span := converted.CommitSpans["0"][2]
	if span.Last-span.First != 2 || main.Events[span.First-1].Kind != "import.commit" || main.Events[span.Last-1].Kind != "message.appended" {
		t.Fatal("source Commit no longer maps to its full closed event interval", span)
	}
	if _, exists := converted.Identities.Turns["0"]["turn-old"]; !exists {
		t.Fatal("terminal Turn identity not mapped")
	}
	again, err := ConvertRuntime(scope, source, bindings)
	after, _ := json.Marshal(source)
	if err != nil || !reflect.DeepEqual(converted, again) || string(before) != string(after) {
		t.Fatal("conversion is not deterministic or mutated its source", err)
	}
}

func TestRuntimeConversionRejectsUnprovenContextAndOwnership(t *testing.T) {
	for _, scenario := range []string{"conflicting-seed", "missing-context", "changed-context", "sequence-gap", "unknown-generation", "missing-parent", "unbound-application", "missing-model-message", "unknown-binding-thread", "live-input", "missing-reference"} {
		t.Run(scenario, func(t *testing.T) {
			scope, source, bindings := runtimeFixture()
			switch scenario {
			case "conflicting-seed":
				m := llm.TextMessage(llm.RoleUser, "different body with same identity")
				m.ID = "kept"
				source.Threads[0].Commits[3].Facts[0].Seed.ProviderMessages[1] = m
			case "missing-context":
				source.Threads[0].Context[1].ID = "missing"
			case "changed-context":
				source.Threads[0].Context[1] = llm.Message{ID: "kept", Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockText, Text: "altered"}}}
			case "sequence-gap":
				source.Threads[0].Commits[2].Seq = 9
			case "unknown-generation":
				source.Threads[0].Commits[2].GenerationID = "missing"
			case "missing-parent":
				source.Threads[1].Metadata.ParentThreadID = "missing"
			case "unbound-application":
				source.Files = []legacy.SourceFile{{Path: "modules/memory-client/workers/abcdef.json"}}
			case "missing-model-message":
				bindings.ModelOrigins = map[string]map[string]managedruntime.ModelConfig{"0": {"missing": {}}}
			case "unknown-binding-thread":
				bindings.Applications = map[string]managedruntime.ImportedApplication{"missing": {Application: "memory", JobID: "job", State: "completed"}}
			case "live-input":
				source.Threads[0].Inputs[0].State = "pending"
			case "missing-reference":
				source.Threads[0].Commits[3].Facts[0].Summary.Compaction.PreviousSummaryID = "missing"
			}
			if _, err := ConvertRuntime(scope, source, bindings); err == nil {
				t.Fatal("unproven conversion accepted")
			}
		})
	}
}

func TestRuntimeConversionPreservesRestrictedApplicationBindings(t *testing.T) {
	scope, source, bindings := runtimeFixture()
	bindings.Applications = map[string]managedruntime.ImportedApplication{"abcdef": {Application: "memory", JobID: "converted-history-role", State: "failed"}}
	value, err := ConvertRuntime(scope, source, bindings)
	if err != nil || value.Import.Threads[1].Thread.Application != "memory" || value.Import.Threads[1].Application.State != "failed" {
		t.Fatal("application purpose was dropped", err)
	}
}

func TestRuntimeConversionMaterializesTextReferencesAndRejectsMetadataOnlyActiveImages(t *testing.T) {
	for _, media := range []bool{false, true} {
		t.Run(map[bool]string{false: "text", true: "image"}[media], func(t *testing.T) {
			scope, source, bindings := runtimeFixture()
			reference := llm.Message{ID: "reference-only", Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockText, Text: "retained original"}}}
			if media {
				data := []byte("already verified image bytes")
				digest := execprotocol.FileDigest(data)
				reference.Blocks = append(reference.Blocks, llm.Block{Type: llm.BlockImage, Media: &llm.MediaRef{ArtifactPath: "source.png", MediaType: "image/png", SHA256: digest}})
				source.Files = []legacy.SourceFile{{Path: "media/source.png", Data: data, SHA256: digest, Size: int64(len(data))}}
				bindings.Artifacts = map[string]execution.Artifact{"media/source.png": {ID: uuid.NewString(), State: "ready", Scope: execution.Scope{OwnerScope: execution.OwnerScope{TenantID: scope.TenantID, UserID: scope.UserID, FleetID: scope.FleetID}, AgentID: scope.AgentID}, Request: execution.ArtifactRequest{Visibility: "agent", MediaType: "image/png", Manifest: execprotocol.FileManifest{Size: int64(len(data)), SHA256: digest}}}}
			}
			summary := *source.Threads[0].Commits[3].Facts[0].Summary
			summary.Compaction = &llm.CompactionMetadata{SummaryChars: len("summary"), RetainedInputReferences: []llm.Message{reference}}
			summary.Blocks = []llm.Block{{Type: llm.BlockText, Text: compactPrefix + sourceCompactReferences("summary", []llm.Message{reference})}}
			source.Threads[0].Commits[3].Facts[0].Summary = &summary
			source.Threads[0].Commits[3].Facts[0].Seed.ProviderMessages = []llm.Message{summary}
			source.Threads[0].Context = []llm.Message{summary}
			value, err := ConvertRuntime(scope, source, bindings)
			if media {
				if err == nil || !strings.Contains(err.Error(), "only through metadata") {
					t.Fatal("unusable active image reference accepted", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			id := value.Identities.Messages["0"]["reference-only"]
			var target, referring bool
			for _, event := range value.Import.Threads[0].Events {
				if event.Kind != "message.appended" {
					continue
				}
				var message llm.Message
				if err := json.Unmarshal(event.Data, &message); err != nil {
					t.Fatal(err)
				}
				target = target || message.ID == id && message.FirstText() == "retained original"
				referring = referring || strings.Contains(message.FirstText(), managedruntime.ContextReference(id, 0, "text"))
			}
			if !target || !referring {
				t.Fatal("generated read_context reference has no canonical target", target, referring)
			}
		})
	}
}

func TestRuntimeConversionPreservesEmptyRenewedContext(t *testing.T) {
	scope, source, bindings := runtimeFixture()
	source.Threads[0].Commits[3].Facts = []legacy.Fact{{Type: "context.renewed", Seed: &legacy.GenerationSeed{ContextScopeID: "g000002"}}}
	source.Threads[0].ContextScopeID = "g000002"
	source.Threads[0].Context = nil
	value, err := ConvertRuntime(scope, source, bindings)
	if err != nil || len(value.Import.Threads[0].Context) != 0 || len(value.Identities.Messages["0"]) != 2 {
		t.Fatal("renewed context resurrected prior messages or lost readable history", err)
	}
}

func TestRuntimeConversionKeepsPolicyHistoryOutsideProviderContext(t *testing.T) {
	scope, source, bindings := runtimeFixture()
	blocked, policy := llm.TextMessage(llm.RoleUser, "rejected input"), llm.TextMessage(llm.RoleUser, "UI-only policy event")
	blocked.ID, blocked.PolicyBlocked = "blocked", true
	policy.ID, policy.Kind = "policy", llm.MessageKindPolicyEvent
	source.Threads[0].Commits = append(source.Threads[0].Commits, legacy.Commit{Version: 1, Seq: 5, At: source.Threads[0].Metadata.UpdatedAt, GenerationID: "g000002", Facts: []legacy.Fact{{Type: "message.appended", Message: &blocked}, {Type: "message.appended", Message: &policy}}})
	source.Threads[0].Context = append(source.Threads[0].Context, blocked, policy)
	value, err := ConvertRuntime(scope, source, bindings)
	if err != nil {
		t.Fatal(err)
	}
	if len(value.Import.Threads[0].Context) != 2 {
		t.Fatal("source display-only or blocked content entered provider context")
	}
	for _, id := range []string{"blocked", "policy"} {
		if value.Identities.Messages["0"][id] == "" {
			t.Fatal("policy audit history was dropped", id)
		}
	}
}
