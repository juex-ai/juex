package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/modules/promptcontext"
	"github.com/juex-ai/juex/internal/prompt"
	"github.com/juex-ai/juex/internal/provenance"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/thread"
	"github.com/juex-ai/juex/internal/tools"
)

func TestReplaceThreadRuntimePublishesCoherentBundle(t *testing.T) {
	root := t.TempDir()
	first := newThreadRuntimeTestThread(t, root)
	second := newThreadRuntimeTestThread(t, root)
	engine := &Engine{
		Thread:            first,
		PendingInputQueue: NewPendingInputQueue(first.Dir, PendingInputQueueOptions{Thread: first}),
	}
	engine.Prompt = threadRuntimeTestPrompt(engine, root)
	firstModules := newThreadRuntimeTestModuleSet(t)
	secondModules := newThreadRuntimeTestModuleSet(t)
	firstTools := threadRuntimeTestTools(t, "first_tool")
	secondTools := threadRuntimeTestTools(t, "second_tool")

	if err := engine.ReplaceThreadRuntimeBundle(first, ThreadRuntimeReplacement{Modules: firstModules, Tools: firstTools}); err != nil {
		t.Fatal(err)
	}
	assertThreadRuntimeBundle(t, engine.ThreadRuntimeSnapshot(), first)
	if snapshot := engine.ThreadRuntimeSnapshot(); snapshot.Modules != firstModules || snapshot.Tools != firstTools {
		t.Fatalf("initial module bundle = modules %p tools %p, want %p %p", snapshot.Modules, snapshot.Tools, firstModules, firstTools)
	}

	if err := engine.ReplaceThreadRuntimeBundle(second, ThreadRuntimeReplacement{Modules: secondModules, Tools: secondTools}); err != nil {
		t.Fatal(err)
	}
	snapshot := engine.ThreadRuntimeSnapshot()
	assertThreadRuntimeBundle(t, snapshot, second)
	if snapshot.Modules != secondModules || snapshot.Tools != secondTools || engine.Tools != secondTools {
		t.Fatalf("replacement module bundle = modules %p tools %p engine tools %p, want %p %p", snapshot.Modules, snapshot.Tools, engine.Tools, secondModules, secondTools)
	}
	if got := engine.SystemPrompt(); !strings.Contains(got, second.ScratchpadDir()) ||
		strings.Contains(got, first.ScratchpadDir()) {
		t.Fatalf("system prompt did not switch scratchpad from %q to %q:\n%s", first.ScratchpadDir(), second.ScratchpadDir(), got)
	}
}

func TestReplaceThreadRuntimeRejectsBusyRuntimeAtomically(t *testing.T) {
	root := t.TempDir()
	first := newThreadRuntimeTestThread(t, root)
	second := newThreadRuntimeTestThread(t, root)
	engine := &Engine{
		Thread:            first,
		Prompt:            &prompt.Builder{},
		PendingInputQueue: NewPendingInputQueue(first.Dir, PendingInputQueueOptions{Thread: first}),
	}
	if err := engine.ReplaceThreadRuntime(first); err != nil {
		t.Fatal(err)
	}
	firstModules := newThreadRuntimeTestModuleSet(t)
	secondModules := newThreadRuntimeTestModuleSet(t)
	firstTools := threadRuntimeTestTools(t, "first_tool")
	secondTools := threadRuntimeTestTools(t, "second_tool")
	if err := engine.ReplaceThreadRuntimeBundle(first, ThreadRuntimeReplacement{Modules: firstModules, Tools: firstTools}); err != nil {
		t.Fatal(err)
	}
	if err := engine.ReserveTurnID("turn-busy"); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.enqueuePendingInput(t.Context(), "keep this input"); err != nil {
		t.Fatal(err)
	}

	err := engine.ReplaceThreadRuntimeBundle(second, ThreadRuntimeReplacement{Modules: secondModules, Tools: secondTools})
	if !errors.Is(err, ErrThreadRuntimeBusy) {
		t.Fatalf("ReplaceThreadRuntime() error = %v, want ErrThreadRuntimeBusy", err)
	}
	assertThreadRuntimeBundle(t, engine.ThreadRuntimeSnapshot(), first)
	if snapshot := engine.ThreadRuntimeSnapshot(); snapshot.Modules != firstModules || snapshot.Tools != firstTools || engine.Tools != firstTools {
		t.Fatalf("busy replacement changed module bundle: %+v", snapshot)
	}
	if status := engine.PendingInputStatus(); status.TurnID != "turn-busy" || status.PendingCount != 1 {
		t.Fatalf("pending status after rejected replacement = %+v", status)
	}
	records, err := engine.ThreadRuntimeSnapshot().PendingInputQueue.Records()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("pending records after rejected replacement = %+v, want one", records)
	}
}

func TestReplaceThreadRuntimeRecoversUnconsumedPolicyContext(t *testing.T) {
	root := t.TempDir()
	threadState := newThreadRuntimeTestThread(t, root)
	first := provenanceRuntimeContextMessage("hook-1", "consumed")
	second := provenanceRuntimeContextMessage("hook-2", "pending")
	for _, event := range []events.Event{
		{Type: provenance.PolicyContextQueuedType, SchemaVersion: 1, ReplayPolicy: events.ReplayRequired, Payload: provenance.PolicyContextQueuedPayload{Messages: []llm.Message{first}}},
		{Type: provenance.RequestEpochType, SchemaVersion: 1, ReplayPolicy: events.ReplayRequired, Payload: provenance.RequestEpochPayload{Epoch: validRecoveryEpoch(t, first.ID)}},
		{Type: provenance.PolicyContextQueuedType, SchemaVersion: 1, ReplayPolicy: events.ReplayRequired, Payload: provenance.PolicyContextQueuedPayload{Messages: []llm.Message{second}}},
	} {
		if err := threadState.AppendEvent(events.Normalize(event)); err != nil {
			t.Fatal(err)
		}
	}
	engine := &Engine{Thread: threadState, Prompt: &prompt.Builder{}}
	if err := engine.ReplaceThreadRuntime(threadState); err != nil {
		t.Fatal(err)
	}
	pending := engine.pendingPolicyRuntimeContextSnapshot()
	if len(pending) != 1 || pending[0].ID != second.ID {
		t.Fatalf("recovered policy context = %+v", pending)
	}
}

func TestRecoverPendingInputsUsesAdmissionEventsAndTranscriptFacts(t *testing.T) {
	root := t.TempDir()
	threadState := newThreadRuntimeTestThread(t, root)
	queue := NewPendingInputQueue(threadState.Dir, PendingInputQueueOptions{Thread: threadState})
	committed, err := queue.StageTurnInput(
		"turn-committed",
		llm.TextMessage(llm.RoleUser, "committed admission"),
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	uncommitted, err := queue.StageTurnInput(
		"turn-uncommitted",
		llm.TextMessage(llm.RoleUser, "uncommitted intent"),
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	transcribed, err := queue.Enqueue(
		llm.TextMessage(llm.RoleUser, "already consumed"),
		PendingInputOptions{ID: "already-consumed", TTL: time.Hour},
		"turn-old",
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := threadState.Append(transcribed.Message); err != nil {
		t.Fatal(err)
	}
	if err := threadState.AppendEvent(events.Normalize(events.Event{
		Type:    TurnAdmittedType,
		TurnID:  "turn-committed",
		Payload: TurnAdmittedPayload{MessageID: committed.MessageID},
	})); err != nil {
		t.Fatal(err)
	}
	if err := threadState.AppendEvent(events.Normalize(events.Event{
		Type:    TurnAdmittedType,
		TurnID:  "turn-uncommitted",
		Payload: TurnAdmittedPayload{MessageID: "message-from-earlier-process"},
	})); err != nil {
		t.Fatal(err)
	}

	engine := &Engine{Thread: threadState, PendingInputQueue: queue, Prompt: &prompt.Builder{}}
	replayable, err := engine.RecoverPendingInputs()
	if err != nil {
		t.Fatal(err)
	}
	if len(replayable) != 1 || replayable[0].RecordID != committed.ID {
		t.Fatalf("replayable records = %+v, want committed admission %q", replayable, committed.ID)
	}
	records, err := queue.Records()
	if err != nil {
		t.Fatal(err)
	}
	if records[uncommitted.ID].State != PendingInputStateAccepting {
		t.Fatalf("uncommitted state = %q, want accepting", records[uncommitted.ID].State)
	}
	if records[transcribed.ID].State != PendingInputStateProcessed {
		t.Fatalf("transcribed state = %q, want processed", records[transcribed.ID].State)
	}
}

func TestRestoreThreadRuntimeCheckpointDoesNotReplayJournal(t *testing.T) {
	root := t.TempDir()
	first := newThreadRuntimeTestThread(t, root)
	second := newThreadRuntimeTestThread(t, root)
	firstPending := provenanceRuntimeContextMessage("hook-first", "first pending")
	secondPending := provenanceRuntimeContextMessage("hook-second", "second pending")
	if err := first.AppendEvent(events.Normalize(events.Event{
		Type:          provenance.PolicyContextQueuedType,
		SchemaVersion: 1,
		ReplayPolicy:  events.ReplayRequired,
		Payload:       provenance.PolicyContextQueuedPayload{Messages: []llm.Message{firstPending}},
	})); err != nil {
		t.Fatal(err)
	}
	if err := second.AppendEvent(events.Normalize(events.Event{
		Type:          provenance.PolicyContextQueuedType,
		SchemaVersion: 1,
		ReplayPolicy:  events.ReplayRequired,
		Payload:       provenance.PolicyContextQueuedPayload{Messages: []llm.Message{secondPending}},
	})); err != nil {
		t.Fatal(err)
	}
	engine := &Engine{Thread: first, Prompt: &prompt.Builder{}}
	if err := engine.ReplaceThreadRuntime(first); err != nil {
		t.Fatal(err)
	}
	checkpoint := engine.CaptureThreadRuntimeCheckpoint()
	if err := engine.ReplaceThreadRuntime(second); err != nil {
		t.Fatal(err)
	}

	eventPath := filepath.Join(first.Dir, "journal.jsonl")
	file, err := os.OpenFile(eventPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("{invalid checkpoint test event\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	if err := engine.RestoreThreadRuntimeCheckpoint(checkpoint); err != nil {
		t.Fatal(err)
	}
	if got := engine.ThreadRuntimeSnapshot().Thread.ID; got != first.ID {
		t.Fatalf("restored Thread = %q, want %q", got, first.ID)
	}
	pending := engine.pendingPolicyRuntimeContextSnapshot()
	if len(pending) != 1 || pending[0].ID != firstPending.ID {
		t.Fatalf("restored pending policy context = %+v, want %q", pending, firstPending.ID)
	}
}

func TestRecoverThreadProvenanceDoesNotMaterializeUnrelatedEvents(t *testing.T) {
	root := t.TempDir()
	threadState := newThreadRuntimeTestThread(t, root)
	queued := provenanceRuntimeContextMessage("hook-streamed", "pending")

	for index := 0; index < 128; index++ {
		if err := threadState.AppendEvent(events.Normalize(events.Event{
			Type:          "tool.output",
			ReplayPolicy:  events.ReplayIgnorable,
			SchemaVersion: 1,
			Payload: map[string]any{
				"index": index,
				"body":  strings.Repeat("x", 32*1024),
			},
		})); err != nil {
			t.Fatal(err)
		}
	}
	if err := threadState.AppendEvent(events.Normalize(events.Event{
		Type:          provenance.PolicyContextQueuedType,
		ReplayPolicy:  events.ReplayRequired,
		SchemaVersion: 1,
		Payload:       provenance.PolicyContextQueuedPayload{Messages: []llm.Message{queued}},
	})); err != nil {
		t.Fatal(err)
	}

	tracker, err := recoverThreadProvenance(threadState.Dir)
	if err != nil {
		t.Fatal(err)
	}
	pending := tracker.PendingPolicyContext()
	if len(pending) != 1 || pending[0].ID != queued.ID {
		t.Fatalf("recovered policy context = %+v, want %q", pending, queued.ID)
	}
}

func threadRuntimeTestPrompt(engine *Engine, workDir string) *prompt.Builder {
	provider := &promptcontext.ThreadContextModule{WorkDir: workDir}
	return &prompt.Builder{ModulePromptContext: func() ([]runtimemodule.ContextSection, error) {
		snapshot := engine.ThreadRuntimeSnapshot()
		request := runtimemodule.ContextRequest{Purpose: runtimemodule.ContextPurposeProviderIteration}
		if snapshot.Thread != nil {
			request.Thread = &runtimemodule.ThreadContext{
				ID:            snapshot.Thread.ID,
				Dir:           snapshot.Thread.Dir,
				ScratchpadDir: snapshot.ScratchpadDir,
			}
		}
		return provider.Context(context.Background(), request)
	}}
}

func provenanceRuntimeContextMessage(id, text string) llm.Message {
	message := llm.TextMessage(llm.RoleUser, text)
	message.ID = id
	message.Kind = llm.MessageKindRuntimeContext
	return message
}

func validRecoveryEpoch(t *testing.T, policyID string) provenance.RequestEpoch {
	t.Helper()
	epoch, err := provenance.BuildRequestEpoch(provenance.RequestInput{
		Provider: provenance.SafeProvider{ID: "test", Model: "model"},
		History: []llm.Message{
			provenanceRuntimeContextMessage("user-1", "hello"),
			provenanceRuntimeContextMessage(policyID, "consumed"),
		},
		PolicyContextMessageIDs: []string{policyID},
	})
	if err != nil {
		t.Fatal(err)
	}
	epoch.EpochID = "epoch-1"
	if _, err := json.Marshal(epoch); err != nil {
		t.Fatal(err)
	}
	return epoch
}

func newThreadRuntimeTestThread(t *testing.T, root string) *thread.Thread {
	t.Helper()
	target, err := thread.New(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = target.Close() })
	return target
}

func newThreadRuntimeTestModuleSet(t *testing.T) *runtimemodule.Set {
	t.Helper()
	set, err := runtimemodule.BuildThreadSet(t.Context(), nil, runtimemodule.ThreadContext{ID: "test"}, runtimemodule.ToolContext{})
	if err != nil {
		t.Fatal(err)
	}
	if err := set.StartThread(t.Context(), runtimemodule.ThreadContext{ID: "test"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = set.CloseThread(context.Background()) })
	return set
}

func threadRuntimeTestTools(t *testing.T, name string) *tools.Registry {
	t.Helper()
	registry := tools.NewRegistry()
	if err := registry.Register(tools.Tool{
		Name:    name,
		Handler: func(context.Context, map[string]any) (string, error) { return name, nil },
	}); err != nil {
		t.Fatal(err)
	}
	return registry
}

func assertThreadRuntimeBundle(t *testing.T, snapshot ThreadRuntimeSnapshot, want *thread.Thread) {
	t.Helper()
	if snapshot.Thread != want {
		t.Fatalf("thread = %p (%v), want %p (%v)", snapshot.Thread, threadID(snapshot.Thread), want, want.ID)
	}
	if snapshot.ScratchpadDir != want.ScratchpadDir() {
		t.Fatalf("scratchpad = %q, want %q", snapshot.ScratchpadDir, want.ScratchpadDir())
	}
	if snapshot.PendingInputQueue == nil || snapshot.PendingInputQueue.thread != want {
		t.Fatalf("pending queue = %+v, want thread dir %q", snapshot.PendingInputQueue, want.Dir)
	}
}

func threadID(target *thread.Thread) string {
	if target == nil {
		return ""
	}
	return target.ID
}
