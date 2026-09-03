package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/runtime"
)

func TestAdmitTurnStartsWhenIdleWithFrameworkIdentity(t *testing.T) {
	a, _ := newStubApp(t)

	result := a.AdmitTurn(context.Background(), TurnAdmissionRequest{Prompt: "hello"})

	if result.Kind != TurnAdmissionStarted || result.Start == nil {
		t.Fatalf("result = %+v", result)
	}
	if result.Start.TurnID == "" || result.Start.Message.ID == "" || result.Start.Message.FirstText() != "hello" {
		t.Fatalf("start = %+v", result.Start)
	}
	if status := a.Engine.PendingInputStatus(); status.TurnID != result.Start.TurnID {
		t.Fatalf("runtime active turn = %+v", status)
	}
	records, err := a.Engine.PendingInputQueue.Records()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("pending records = %+v", records)
	}
	for _, record := range records {
		if record.State != runtime.PendingInputStateAdmitted || record.TurnID != result.Start.TurnID || record.MessageID != result.Start.Message.ID {
			t.Fatalf("accepted record = %+v", record)
		}
	}
}

func TestAdmitTurnStartsNextInputAfterRuntimeCompletes(t *testing.T) {
	a, _ := newStubApp(t,
		llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "first answer"), StopReason: llm.StopEndTurn},
		llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "second answer"), StopReason: llm.StopEndTurn},
	)
	first := a.AdmitTurn(context.Background(), TurnAdmissionRequest{Prompt: "first"})
	if first.Kind != TurnAdmissionStarted || first.Start == nil {
		t.Fatalf("first = %+v", first)
	}
	if _, err := a.RunAdmittedTurn(context.Background(), first.Start.TurnID, first.Start.Message); err != nil {
		t.Fatal(err)
	}

	second := a.AdmitTurn(context.Background(), TurnAdmissionRequest{Prompt: "second"})
	if second.Kind != TurnAdmissionStarted || second.Start == nil || second.Start.TurnID == first.Start.TurnID {
		t.Fatalf("second = %+v", second)
	}
}

func TestAdmitTurnSystemNoticeUsesOrdinaryLifecycleWithoutSlashParsing(t *testing.T) {
	a, _ := newStubApp(t)
	started := a.AdmitTurn(context.Background(), TurnAdmissionRequest{Prompt: "/status", Kind: llm.MessageKindSystemNotice})
	if started.Kind != TurnAdmissionStarted || started.Start == nil || started.Start.Message.Kind != llm.MessageKindSystemNotice || started.Start.Message.FirstText() != "/status" {
		t.Fatalf("started = %+v", started)
	}
	queued := a.AdmitTurn(context.Background(), TurnAdmissionRequest{Prompt: "continue", Kind: llm.MessageKindSystemNotice})
	if queued.Kind != TurnAdmissionQueued || queued.PendingCount != 1 {
		t.Fatalf("queued = %+v", queued)
	}
}

func TestAdmitTurnSystemNoticeRetriesInterruptedTurnInputs(t *testing.T) {
	a, provider := newStubApp(t, llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "resumed"), StopReason: llm.StopEndTurn})
	interrupted, err := a.Engine.PendingInputQueue.AdmitTurnInput(
		"turn-interrupted",
		llm.TextMessage(llm.RoleUser, "unfinished work"),
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Engine.PendingInputQueue.MarkProcessed([]string{interrupted.ID}); err != nil {
		t.Fatal(err)
	}
	if err := a.Engine.PendingInputQueue.ApplyTerminalEvent(events.Event{
		Type:   "turn.errored",
		TurnID: "turn-interrupted",
		Payload: runtime.TurnErroredPayload{
			Error: "restart", ErrorKind: "runtime_restart", InputIDs: []string{interrupted.ID},
		},
	}); err != nil {
		t.Fatal(err)
	}

	started := a.AdmitTurn(context.Background(), TurnAdmissionRequest{
		Prompt:      "continue after restart",
		Kind:        llm.MessageKindSystemNotice,
		RetryTurnID: "turn-interrupted",
	})
	if started.Kind != TurnAdmissionStarted || started.Start == nil {
		t.Fatalf("started = %+v", started)
	}
	records, err := a.Engine.PendingInputQueue.Records()
	if err != nil {
		t.Fatal(err)
	}
	if current := records[interrupted.ID]; current.State != runtime.PendingInputStatePending || current.TurnID != "" || current.Attempts != 1 {
		t.Fatalf("retried interrupted Input = %+v", current)
	}
	if _, err := a.RunAdmittedTurn(context.Background(), started.Start.TurnID, started.Start.Message); err != nil {
		t.Fatal(err)
	}
	if len(provider.histories) != 1 || len(provider.histories[0]) < 2 ||
		provider.histories[0][0].FirstText() != "unfinished work" ||
		provider.histories[0][1].FirstText() != "continue after restart" {
		t.Fatalf("continuation history = %+v", provider.histories)
	}
	if records, err := a.Engine.PendingInputQueue.Records(); err != nil || len(records) != 0 {
		t.Fatalf("completed continuation retained pending Inputs = %+v, err=%v", records, err)
	}
}

func TestAdmitTurnRejectsUnsupportedKindsAndSystemNoticeAttachments(t *testing.T) {
	for _, kind := range []string{
		llm.MessageKindRuntimeContext,
		llm.MessageKindCompact,
		llm.MessageKindModelChange,
		llm.MessageKindObservation,
		llm.MessageKindMCPEvent,
		llm.MessageKindPolicyEvent,
		"unknown",
	} {
		t.Run(kind, func(t *testing.T) {
			a, _ := newStubApp(t)
			result := a.AdmitTurn(context.Background(), TurnAdmissionRequest{Prompt: "/status", Kind: kind})
			if result.Kind != TurnAdmissionRejected || result.Error.Kind != "bad_request" {
				t.Fatalf("result = %+v", result)
			}
		})
	}
	a, _ := newStubApp(t)
	result := a.AdmitTurn(context.Background(), TurnAdmissionRequest{
		Prompt: "notice", Kind: llm.MessageKindSystemNotice, Attachments: []llm.MediaRef{turnAdmissionMediaRef()},
	})
	if result.Kind != TurnAdmissionRejected || result.Error.Kind != "bad_request" {
		t.Fatalf("result = %+v", result)
	}
	result = a.AdmitTurn(context.Background(), TurnAdmissionRequest{Prompt: "notice", RetryTurnID: "turn-old"})
	if result.Kind != TurnAdmissionRejected || result.Error.Kind != "bad_request" {
		t.Fatalf("retry without system notice = %+v", result)
	}
}

func TestAdmitTurnPreservesAttachmentsAndWarnings(t *testing.T) {
	a, _ := newStubApp(t)
	media := turnAdmissionMediaRef()
	result := a.AdmitTurn(context.Background(), TurnAdmissionRequest{Prompt: "describe this", Attachments: []llm.MediaRef{media}})
	if result.Kind != TurnAdmissionStarted || result.Start == nil {
		t.Fatalf("result = %+v", result)
	}
	if len(result.Warnings) != 1 || result.Warnings[0].Code != "attachment_vision_unavailable" {
		t.Fatalf("warnings = %+v", result.Warnings)
	}
	blocks := result.Start.Message.Blocks
	if len(blocks) != 2 || blocks[0].Text != "describe this" || blocks[1].Type != llm.BlockImage || blocks[1].Media == nil || blocks[1].Media.ArtifactPath != media.ArtifactPath {
		t.Fatalf("blocks = %+v", blocks)
	}
}

func TestAdmitTurnVisionCapabilitySuppressesAttachmentWarning(t *testing.T) {
	a, _ := newStubApp(t)
	vision := true
	a.cfg.ProviderCapabilities.Vision = &vision
	result := a.AdmitTurn(context.Background(), TurnAdmissionRequest{Attachments: []llm.MediaRef{turnAdmissionMediaRef()}})
	if result.Kind != TurnAdmissionStarted || len(result.Warnings) != 0 || len(result.Start.Message.Blocks) != 1 {
		t.Fatalf("result = %+v", result)
	}
}

func TestAdmitTurnRejectsSlashCommandWithAttachments(t *testing.T) {
	a, _ := newStubApp(t)
	result := a.AdmitTurn(context.Background(), TurnAdmissionRequest{Prompt: "/status", Attachments: []llm.MediaRef{turnAdmissionMediaRef()}})
	if result.Kind != TurnAdmissionRejected || result.Error.Kind != "bad_request" {
		t.Fatalf("result = %+v", result)
	}
}

func TestAdmitTurnQueuesBehindRuntimeOwnedTurn(t *testing.T) {
	a, _ := newStubApp(t)
	if err := a.Engine.ReserveTurnID("external-turn"); err != nil {
		t.Fatal(err)
	}
	result := a.AdmitTurn(context.Background(), TurnAdmissionRequest{Prompt: "steer"})
	if result.Kind != TurnAdmissionQueued || result.InputID == "" || result.TurnID != "" || result.PendingCount != 1 {
		t.Fatalf("result = %+v", result)
	}
	if phase, turnID := a.admissionQueue().snapshot(); phase != turnAdmissionIdle || turnID != "" {
		t.Fatalf("App mirrored runtime turn: (%q, %q)", phase, turnID)
	}
}

func TestAdmitTurnQueuesAttachmentBlocksBehindActiveTurn(t *testing.T) {
	a, _ := newStubApp(t)
	started := a.AdmitTurn(context.Background(), TurnAdmissionRequest{Prompt: "first"})
	if started.Kind != TurnAdmissionStarted {
		t.Fatalf("started = %+v", started)
	}
	media := turnAdmissionMediaRef()
	queued := a.AdmitTurn(context.Background(), TurnAdmissionRequest{Prompt: "second", Attachments: []llm.MediaRef{media}})
	if queued.Kind != TurnAdmissionQueued || len(queued.Warnings) != 1 {
		t.Fatalf("queued = %+v", queued)
	}
	records, err := a.Engine.PendingInputQueue.Records()
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.State == runtime.PendingInputStatePending && (len(record.Message.Blocks) != 2 || record.Message.Blocks[1].Media == nil || record.Message.Blocks[1].Media.ArtifactPath != media.ArtifactPath) {
			t.Fatalf("queued record = %+v", record)
		}
	}
}

func TestAdmitTurnQueuesDuringCompactAndPromotesWithFrameworkIdentity(t *testing.T) {
	a, _ := newStubApp(t)
	var admitted runtime.TurnAdmittedPayload
	unsubscribe := a.Bus.Subscribe(runtime.TurnAdmittedType, func(event events.Event) {
		admitted, _ = event.Payload.(runtime.TurnAdmittedPayload)
	})
	defer unsubscribe()

	compactID, err := a.beginCompactAdmission()
	if err != nil {
		t.Fatal(err)
	}
	if compactID == "" || admitted.Operation != runtime.TurnAdmissionOperationCompact {
		t.Fatalf("compact = %q admission = %+v", compactID, admitted)
	}
	queued := a.AdmitTurn(context.Background(), TurnAdmissionRequest{Prompt: "after compact"})
	if queued.Kind != TurnAdmissionQueued || queued.InputID == "" || queued.TurnID != "" {
		t.Fatalf("queued during compact = %+v", queued)
	}
	promoted, err := a.finishCompactAdmission(compactID)
	if err != nil {
		t.Fatal(err)
	}
	if promoted == nil || promoted.TurnID == "" || promoted.TurnID == compactID || promoted.Message.FirstText() != "after compact" {
		t.Fatalf("promoted = %+v", promoted)
	}
}

func TestFinishCompactWaitsForConcurrentQueuedAdmissionPublication(t *testing.T) {
	a, _ := newStubApp(t)
	compactID, err := a.beginCompactAdmission()
	if err != nil {
		t.Fatal(err)
	}
	queuedPublishing := make(chan struct{})
	releaseQueued := make(chan struct{})
	var publishOnce sync.Once
	unsubscribe := a.Bus.Subscribe("pending_input.queued", func(events.Event) {
		publishOnce.Do(func() { close(queuedPublishing) })
		<-releaseQueued
	})
	defer unsubscribe()
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseQueued) }) }
	defer release()

	queuedDone := make(chan TurnAdmissionResult, 1)
	go func() {
		queuedDone <- a.AdmitTurn(context.Background(), TurnAdmissionRequest{Prompt: "after compact"})
	}()
	select {
	case <-queuedPublishing:
	case <-time.After(2 * time.Second):
		t.Fatal("queued admission did not reach publication")
	}
	type compactFinishResult struct {
		start *AdmittedTurn
		err   error
	}
	finishDone := make(chan compactFinishResult, 1)
	go func() {
		start, err := a.finishCompactAdmission(compactID)
		finishDone <- compactFinishResult{start: start, err: err}
	}()
	select {
	case result := <-finishDone:
		release()
		t.Fatalf("compaction finish bypassed queued admission publication: %+v", result)
	case <-time.After(100 * time.Millisecond):
	}

	release()
	queued := <-queuedDone
	if queued.Kind != TurnAdmissionQueued || queued.InputID == "" || queued.TurnID != "" {
		t.Fatalf("queued admission = %+v", queued)
	}
	finished := <-finishDone
	if finished.err != nil {
		t.Fatal(finished.err)
	}
	if finished.start == nil || finished.start.TurnID == "" || finished.start.TurnID == compactID || finished.start.Message.FirstText() != "after compact" {
		t.Fatalf("promoted start = %+v", finished.start)
	}
	if phase, turnID := a.admissionQueue().snapshot(); phase != turnAdmissionIdle || turnID != "" {
		t.Fatalf("App compaction phase after promotion = (%q, %q)", phase, turnID)
	}
}

func TestAdmitTurnStatusSlashAllowedWhileRuntimeBusy(t *testing.T) {
	a, provider := newStubApp(t)
	started := a.AdmitTurn(context.Background(), TurnAdmissionRequest{Prompt: "first"})
	if started.Kind != TurnAdmissionStarted {
		t.Fatalf("started = %+v", started)
	}
	result := a.AdmitTurn(context.Background(), TurnAdmissionRequest{Prompt: "/status"})
	if result.Kind != TurnAdmissionCommandCompleted || result.Command == nil || result.Command.Name != SlashStatus {
		t.Fatalf("result = %+v", result)
	}
	if provider.calls != 0 {
		t.Fatalf("provider calls = %d", provider.calls)
	}
}

func TestAdmitTurnNewSlashRejectsWhileBusy(t *testing.T) {
	a, _ := newStubApp(t)
	started := a.AdmitTurn(context.Background(), TurnAdmissionRequest{Prompt: "first"})
	if started.Kind != TurnAdmissionStarted {
		t.Fatalf("started = %+v", started)
	}
	result := a.AdmitTurn(context.Background(), TurnAdmissionRequest{Prompt: "/new"})
	if result.Kind != TurnAdmissionConflict || result.Error.Message != "Thread busy" {
		t.Fatalf("result = %+v", result)
	}
}

func TestPendingInputTerminalPublicationMapsToRetryableConflict(t *testing.T) {
	status := runtime.PendingInputStatus{TurnID: "turn-1", PendingCount: 1, MaxPendingInputs: 4}
	result := admissionResultFromPendingInput(runtime.PendingInputResult{
		Retry:  runtime.PendingInputRetryAfterTurn,
		Status: status,
	}, runtime.ErrActiveTurnExists)

	if result.Kind != TurnAdmissionConflict || !result.Error.Retryable || !errors.Is(result.Err, runtime.ErrActiveTurnExists) {
		t.Fatalf("result = %+v, want retryable conflict", result)
	}
	if result.TurnID != status.TurnID || result.PendingCount != status.PendingCount || result.MaxPendingInputs != status.MaxPendingInputs {
		t.Fatalf("result status = %+v, want %+v", result, status)
	}
}

func TestAdmitTurnNewSlashRenewsContextWithoutStartingTurn(t *testing.T) {
	a, _ := newStubApp(t)
	result := a.AdmitTurn(context.Background(), TurnAdmissionRequest{Prompt: "/new"})
	if result.Kind != TurnAdmissionCommandCompleted || result.Start != nil || result.TurnID != "" {
		t.Fatalf("result = %+v", result)
	}
	if got := a.Thread.Info().GenerationID; got != "g000002" {
		t.Fatalf("generation = %q", got)
	}
}

func TestAdmitTurnMapsQueueFull(t *testing.T) {
	a, _ := newStubApp(t)
	a.Engine.MaxPendingInputs = 1
	if started := a.AdmitTurn(context.Background(), TurnAdmissionRequest{Prompt: "first"}); started.Kind != TurnAdmissionStarted {
		t.Fatalf("started = %+v", started)
	}
	if queued := a.AdmitTurn(context.Background(), TurnAdmissionRequest{Prompt: "second"}); queued.Kind != TurnAdmissionQueued {
		t.Fatalf("queued = %+v", queued)
	}
	result := a.AdmitTurn(context.Background(), TurnAdmissionRequest{Prompt: "third"})
	if result.Kind != TurnAdmissionRejected || result.Error.Kind != "pending_input_full" || !result.Error.Retryable {
		t.Fatalf("result = %+v", result)
	}
}

func TestAdmitTurnMalformedSlashReturnsBadRequest(t *testing.T) {
	a, _ := newStubApp(t)
	result := a.AdmitTurn(context.Background(), TurnAdmissionRequest{Prompt: "/status verbose"})
	if result.Kind != TurnAdmissionRejected || result.Error.Kind != "bad_request" || !strings.Contains(result.Error.Suggestion, AvailableSlashCommandsText()) {
		t.Fatalf("result = %+v", result)
	}
}

func TestAdmitTurnRequiresInitializedApp(t *testing.T) {
	for _, test := range []struct {
		name string
		app  func(*testing.T) *App
	}{
		{name: "nil app", app: func(*testing.T) *App { return nil }},
		{name: "nil engine", app: func(t *testing.T) *App { a, _ := newStubApp(t); a.Engine = nil; return a }},
		{name: "nil thread", app: func(t *testing.T) *App { a, _ := newStubApp(t); a.Thread = nil; return a }},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := test.app(t).AdmitTurn(context.Background(), TurnAdmissionRequest{Prompt: "hello"})
			if result.Kind != TurnAdmissionError || result.Error.Message != "turn admission: app, engine, or Thread is not initialized" {
				t.Fatalf("result = %+v", result)
			}
		})
	}
}

func turnAdmissionMediaRef() llm.MediaRef {
	return llm.MediaRef{
		ArtifactPath: "threads/123456/media/image.png", MediaType: "image/png",
		SHA256: strings.Repeat("a", 64), OriginalBytes: 123, Width: 2, Height: 3,
	}
}
