package runtime

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/session"
)

type observedContext struct {
	context.Context
	firstErr chan struct{}
	once     sync.Once
}

func (c *observedContext) Err() error {
	err := c.Context.Err()
	c.once.Do(func() { close(c.firstErr) })
	return err
}

func TestReceivePendingInputStartsIdleTurnWithFrameworkIdentity(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)

	result, err := eng.ReceivePendingInput(context.Background(), PendingInputRequest{
		Message: llm.TextMessage(llm.RoleUser, "hello"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Disposition != PendingInputStarted {
		t.Fatalf("disposition = %q, want %q", result.Disposition, PendingInputStarted)
	}
	if result.TurnID == "" || result.Message.ID == "" || result.Message.FirstText() != "hello" {
		t.Fatalf("started input = %+v", result)
	}
	if status := eng.PendingInputStatus(); status.TurnID != result.TurnID {
		t.Fatalf("active turn = %+v, want %q", status, result.TurnID)
	}
	record := pendingLifecycleTestRecord(t, eng, result.RecordID)
	if record.State != PendingInputStateAdmitted || record.TurnID != result.TurnID || record.MessageID != result.Message.ID {
		t.Fatalf("durable record = %+v", record)
	}
}

func TestReceivePendingInputRechecksCancellationAfterLifecycleLock(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	baseCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &observedContext{Context: baseCtx, firstErr: make(chan struct{})}
	type receiveOutcome struct {
		result PendingInputResult
		err    error
	}
	outcome := make(chan receiveOutcome, 1)
	eng.pendingLifecycleMu.Lock()
	go func() {
		result, err := eng.ReceivePendingInput(ctx, PendingInputRequest{
			Message: llm.TextMessage(llm.RoleUser, "canceled while waiting"),
		})
		outcome <- receiveOutcome{result: result, err: err}
	}()
	select {
	case <-ctx.firstErr:
	case <-time.After(5 * time.Second):
		eng.pendingLifecycleMu.Unlock()
		t.Fatal("ReceivePendingInput() did not check its context before waiting for the lifecycle lock")
	}
	cancel()
	eng.pendingLifecycleMu.Unlock()
	got := <-outcome
	result, err := got.result, got.err
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ReceivePendingInput() error = %v, want %v", err, context.Canceled)
	}
	if result.Disposition != "" || result.Retry != "" || result.RecordID != "" || result.TurnID != "" || result.Message.ID != "" {
		t.Fatalf("ReceivePendingInput() result = %+v, want empty", result)
	}
	if status := eng.PendingInputStatus(); status.TurnID != "" || status.PendingCount != 0 {
		t.Fatalf("pending status = %+v, want no admitted input", status)
	}
	records, err := eng.currentPendingInputQueue().Records()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("pending records = %+v, want none", records)
	}
}

func TestReceivePendingInputRejectsDuringTerminalPublication(t *testing.T) {
	provider := &mockProvider{script: []llm.Response{{
		Message:    llm.TextMessage(llm.RoleAssistant, "first complete"),
		StopReason: llm.StopEndTurn,
	}}}
	eng, bus := newEngine(t, provider, false)
	completionStarted := make(chan struct{})
	releaseCompletion := make(chan struct{})
	var completionOnce sync.Once
	unsubscribe := bus.Subscribe("turn.completed", func(events.Event) {
		completionOnce.Do(func() { close(completionStarted) })
		<-releaseCompletion
	})
	defer unsubscribe()

	turnDone := make(chan error, 1)
	go func() {
		_, err := eng.Turn(context.Background(), "first")
		turnDone <- err
	}()
	select {
	case <-completionStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first turn did not begin its terminal commit")
	}

	nextResult := make(chan PendingInputResult, 1)
	nextErr := make(chan error, 1)
	go func() {
		result, err := eng.ReceivePendingInput(context.Background(), PendingInputRequest{
			Message: llm.TextMessage(llm.RoleUser, "second"),
		})
		nextResult <- result
		nextErr <- err
	}()
	var result PendingInputResult
	select {
	case result = <-nextResult:
	case <-time.After(time.Second):
		close(releaseCompletion)
		t.Fatal("next admission did not return terminal-publication retry")
	}
	if err := <-nextErr; !errors.Is(err, ErrActiveTurnExists) {
		close(releaseCompletion)
		t.Fatalf("next admission error = %v, want %v", err, ErrActiveTurnExists)
	}
	if result.Retry != PendingInputRetryAfterTurn || result.Status.TurnID == "" {
		close(releaseCompletion)
		t.Fatalf("next admission = %+v, want retry after terminal publication", result)
	}

	close(releaseCompletion)
	if err := <-turnDone; err != nil {
		t.Fatal(err)
	}
	result, err := eng.ReceivePendingInput(context.Background(), PendingInputRequest{
		Message: llm.TextMessage(llm.RoleUser, "second"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Disposition != PendingInputStarted || result.TurnID == "" {
		t.Fatalf("retried admission = %+v, want started after terminal publication", result)
	}
	eng.finishActiveTurn(result.TurnID)
}

func TestReceivePendingInputPersistsDeferredExternalInputDuringTerminalPublication(t *testing.T) {
	provider := &mockProvider{script: []llm.Response{{
		Message:    llm.TextMessage(llm.RoleAssistant, "first complete"),
		StopReason: llm.StopEndTurn,
	}}}
	eng, bus := newEngine(t, provider, false)
	completionStarted := make(chan struct{})
	releaseCompletion := make(chan struct{})
	var completionOnce sync.Once
	unsubscribe := bus.Subscribe("turn.completed", func(events.Event) {
		completionOnce.Do(func() { close(completionStarted) })
		<-releaseCompletion
	})
	defer unsubscribe()

	turnDone := make(chan error, 1)
	go func() {
		_, err := eng.Turn(context.Background(), "first")
		turnDone <- err
	}()
	select {
	case <-completionStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("first turn did not begin terminal publication")
	}

	result, err := eng.ReceivePendingInput(context.Background(), PendingInputRequest{
		Message:       llm.TextMessage(llm.RoleUser, "external during terminal publication"),
		Options:       &PendingInputOptions{ID: "terminal-external", TTL: time.Hour},
		DeferDelivery: true,
	})
	if err != nil {
		close(releaseCompletion)
		t.Fatal(err)
	}
	if result.Disposition != PendingInputQueued || result.RecordID != "terminal-external" || result.Retry != PendingInputRetryAfterRecovery {
		close(releaseCompletion)
		t.Fatalf("deferred external input = %+v", result)
	}
	if record := pendingLifecycleTestRecord(t, eng, result.RecordID); record.State != PendingInputStatePending {
		close(releaseCompletion)
		t.Fatalf("durable external input = %+v, want pending", record)
	}

	close(releaseCompletion)
	if err := <-turnDone; err != nil {
		t.Fatal(err)
	}
}

func TestLegacyPendingEnqueueRejectsDuringTerminalPublication(t *testing.T) {
	provider := &mockProvider{script: []llm.Response{{
		Message:    llm.TextMessage(llm.RoleAssistant, "first complete"),
		StopReason: llm.StopEndTurn,
	}}}
	eng, bus := newEngine(t, provider, false)
	completionStarted := make(chan struct{})
	releaseCompletion := make(chan struct{})
	var completionOnce sync.Once
	unsubscribe := bus.Subscribe("turn.completed", func(events.Event) {
		completionOnce.Do(func() { close(completionStarted) })
		<-releaseCompletion
	})
	defer unsubscribe()

	turnDone := make(chan error, 1)
	go func() {
		_, err := eng.Turn(context.Background(), "first")
		turnDone <- err
	}()
	select {
	case <-completionStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first turn did not begin its terminal commit")
	}

	enqueueDone := make(chan error, 1)
	go func() {
		_, err := eng.EnqueuePendingMessageWithOptions(context.Background(), llm.TextMessage(llm.RoleUser, "late legacy input"), PendingInputOptions{
			ID:  "late-legacy-input",
			TTL: time.Hour,
		})
		enqueueDone <- err
	}()
	select {
	case err := <-enqueueDone:
		if !errors.Is(err, ErrNoActiveTurn) {
			close(releaseCompletion)
			t.Fatalf("legacy enqueue error = %v, want %v", err, ErrNoActiveTurn)
		}
	case <-time.After(time.Second):
		close(releaseCompletion)
		t.Fatal("legacy enqueue blocked during terminal publication")
	}

	close(releaseCompletion)
	if err := <-turnDone; err != nil {
		t.Fatal(err)
	}
	if _, ok, err := eng.PersistedPendingMessage("late-legacy-input"); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("legacy enqueue persisted input against a completed Turn")
	}
	if status := eng.PendingInputStatus(); status.TurnID != "" || status.PendingCount != 0 {
		t.Fatalf("pending status = %+v, want no stranded input", status)
	}
}

func TestTerminalSubscriberCanSynchronouslyUseLegacyPendingEnqueue(t *testing.T) {
	provider := &mockProvider{script: []llm.Response{{
		Message:    llm.TextMessage(llm.RoleAssistant, "complete"),
		StopReason: llm.StopEndTurn,
	}}}
	eng, bus := newEngine(t, provider, false)
	enqueueResult := make(chan error, 1)
	unsubscribe := bus.Subscribe("turn.completed", func(events.Event) {
		_, err := eng.EnqueuePendingMessage(context.Background(), llm.TextMessage(llm.RoleUser, "subscriber input"))
		enqueueResult <- err
	})
	defer unsubscribe()

	turnDone := make(chan error, 1)
	go func() {
		_, err := eng.Turn(context.Background(), "first")
		turnDone <- err
	}()
	select {
	case err := <-turnDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Turn deadlocked while terminal subscriber used legacy enqueue")
	}
	if err := <-enqueueResult; !errors.Is(err, ErrNoActiveTurn) {
		t.Fatalf("terminal subscriber enqueue error = %v, want %v", err, ErrNoActiveTurn)
	}
	if status := eng.PendingInputStatus(); status.TurnID != "" || status.PendingCount != 0 {
		t.Fatalf("pending status = %+v, want no input attached after completion", status)
	}
}

func TestErrorSubscriberCanSynchronouslyUseLegacyPendingEnqueue(t *testing.T) {
	eng, bus := newEngine(t, errorProvider{}, false)
	enqueueResult := make(chan error, 1)
	unsubscribe := bus.Subscribe("turn.errored", func(events.Event) {
		_, err := eng.EnqueuePendingMessage(context.Background(), llm.TextMessage(llm.RoleUser, "subscriber input"))
		enqueueResult <- err
	})
	defer unsubscribe()

	turnDone := make(chan error, 1)
	go func() {
		_, err := eng.Turn(context.Background(), "first")
		turnDone <- err
	}()
	select {
	case err := <-turnDone:
		if err == nil || !strings.Contains(err.Error(), "boom") {
			t.Fatalf("Turn() error = %v, want provider failure", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Turn deadlocked while error subscriber used legacy enqueue")
	}
	if err := <-enqueueResult; !errors.Is(err, ErrNoActiveTurn) {
		t.Fatalf("error subscriber enqueue error = %v, want %v", err, ErrNoActiveTurn)
	}
}

func TestTerminalSubscriberCanSynchronouslyReadPendingLifecycleStatus(t *testing.T) {
	provider := &mockProvider{script: []llm.Response{{
		Message:    llm.TextMessage(llm.RoleAssistant, "complete"),
		StopReason: llm.StopEndTurn,
	}}}
	eng, bus := newEngine(t, provider, false)
	statusResult := make(chan PendingInputStatus, 1)
	unsubscribe := bus.Subscribe("turn.completed", func(events.Event) {
		statusResult <- eng.PendingInputLifecycleStatus()
	})
	defer unsubscribe()

	turnDone := make(chan error, 1)
	go func() {
		_, err := eng.Turn(context.Background(), "first")
		turnDone <- err
	}()
	select {
	case err := <-turnDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Turn deadlocked while terminal subscriber read pending lifecycle status")
	}
	if status := <-statusResult; status.TurnID == "" {
		t.Fatalf("terminal publication status = %+v, want publishing Turn", status)
	}
}

func TestTerminalDurableProjectionCanSynchronouslyUseLegacyPendingEnqueue(t *testing.T) {
	provider := &mockProvider{script: []llm.Response{{
		Message:    llm.TextMessage(llm.RoleAssistant, "complete"),
		StopReason: llm.StopEndTurn,
	}}}
	eng, bus := newEngine(t, provider, false)
	sink := events.NewDurableSink(eng.Session)
	bus.SetCommitter(sink)
	defer func() {
		bus.SetCommitter(nil)
		_ = sink.Close()
	}()

	projectionResult := make(chan error, 1)
	sink.AddProjection(events.DeliveryFunc(func(event events.Event) {
		if event.Type != "turn.completed" {
			return
		}
		_, err := eng.EnqueuePendingMessage(context.Background(), llm.TextMessage(llm.RoleUser, "projection input"))
		projectionResult <- err
	}))

	turnDone := make(chan error, 1)
	go func() {
		_, err := eng.Turn(context.Background(), "first")
		turnDone <- err
	}()
	select {
	case err := <-turnDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Turn deadlocked while terminal durable projection used legacy enqueue")
	}
	if err := <-projectionResult; !errors.Is(err, ErrNoActiveTurn) {
		t.Fatalf("projection enqueue error = %v, want %v", err, ErrNoActiveTurn)
	}
}

func TestErrorDurableProjectionCanSynchronouslyUseLegacyPendingEnqueue(t *testing.T) {
	eng, bus := newEngine(t, errorProvider{}, false)
	sink := events.NewDurableSink(eng.Session)
	bus.SetCommitter(sink)
	defer func() {
		bus.SetCommitter(nil)
		_ = sink.Close()
	}()

	projectionResult := make(chan error, 1)
	sink.AddProjection(events.DeliveryFunc(func(event events.Event) {
		if event.Type != "turn.errored" {
			return
		}
		_, err := eng.EnqueuePendingMessage(context.Background(), llm.TextMessage(llm.RoleUser, "projection input"))
		projectionResult <- err
	}))

	turnDone := make(chan error, 1)
	go func() {
		_, err := eng.Turn(context.Background(), "first")
		turnDone <- err
	}()
	select {
	case err := <-turnDone:
		if err == nil || !strings.Contains(err.Error(), "boom") {
			t.Fatalf("Turn() error = %v, want provider failure", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Turn deadlocked while error durable projection used legacy enqueue")
	}
	if err := <-projectionResult; !errors.Is(err, ErrNoActiveTurn) {
		t.Fatalf("projection enqueue error = %v, want %v", err, ErrNoActiveTurn)
	}
}

func TestReceivePendingInputWaitsForFallbackTerminalCommit(t *testing.T) {
	provider := &mockProvider{script: []llm.Response{{
		Message:    llm.TextMessage(llm.RoleAssistant, "completion will fail"),
		StopReason: llm.StopEndTurn,
	}}}
	eng, bus := newEngine(t, provider, false)
	completionErr := errors.New("completion commit failed")
	errorStarted := make(chan struct{})
	releaseError := make(chan struct{})
	bus.SetCommitter(&blockingFallbackTerminalCommitter{
		delegate:      selectiveSessionCommitter{session: eng.Session},
		completionErr: completionErr,
		errorStarted:  errorStarted,
		releaseError:  releaseError,
	})

	turnDone := make(chan error, 1)
	go func() {
		_, err := eng.Turn(context.Background(), "first")
		turnDone <- err
	}()
	select {
	case <-errorStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("fallback terminal commit did not start")
	}

	nextResult := make(chan PendingInputResult, 1)
	nextErr := make(chan error, 1)
	go func() {
		result, err := eng.ReceivePendingInput(context.Background(), PendingInputRequest{
			Message: llm.TextMessage(llm.RoleUser, "second"),
		})
		nextResult <- result
		nextErr <- err
	}()
	select {
	case result := <-nextResult:
		close(releaseError)
		t.Fatalf("next admission completed before fallback terminal commit: %+v", result)
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseError)
	if err := <-turnDone; !errors.Is(err, completionErr) {
		t.Fatalf("first turn error = %v, want %v", err, completionErr)
	}
	result := <-nextResult
	if err := <-nextErr; err != nil {
		t.Fatal(err)
	}
	if result.Disposition != PendingInputStarted || result.TurnID == "" {
		t.Fatalf("next admission = %+v, want started after fallback terminal commit", result)
	}
	eng.finishActiveTurn(result.TurnID)
}

func TestFailedPendingPreservationClearsReservationBeforeAdmissionUnlock(t *testing.T) {
	eng, bus := newEngine(t, &mockProvider{}, false)
	turnID := eng.beginActiveTurn("failed-preservation-turn")
	if _, err := eng.EnqueuePendingMessageWithOptions(context.Background(), llm.TextMessage(llm.RoleUser, "preserve me"), PendingInputOptions{
		ID:  "pending-before-preservation-failure",
		TTL: time.Hour,
	}); err != nil {
		t.Fatal(err)
	}

	queue := eng.currentPendingInputQueue()
	originalWrite := queue.fileOps.write
	wantStorageErr := errors.New("pending terminal storage failure")
	queue.fileOps.write = func(*os.File, []byte) (int, error) { return 0, wantStorageErr }
	t.Cleanup(func() { queue.fileOps.write = originalWrite })
	errorStarted := make(chan struct{})
	releaseError := make(chan struct{})
	bus.SetCommitter(&blockingFallbackTerminalCommitter{
		delegate:     selectiveSessionCommitter{session: eng.Session},
		errorStarted: errorStarted,
		releaseError: releaseError,
	})

	wantTurnErr := errors.New("provider failed")
	failDone := make(chan error, 1)
	go func() { failDone <- eng.failActiveTurnLocked(turnID, wantTurnErr, false) }()
	select {
	case <-errorStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("fallback terminal commit did not start")
	}

	nextResult := make(chan PendingInputResult, 1)
	nextErr := make(chan error, 1)
	go func() {
		result, err := eng.ReceivePendingInput(context.Background(), PendingInputRequest{
			Message: llm.TextMessage(llm.RoleUser, "next input"),
		})
		nextResult <- result
		nextErr <- err
	}()
	select {
	case result := <-nextResult:
		close(releaseError)
		t.Fatalf("next admission completed before failed turn released its reservation: %+v", result)
	case <-time.After(50 * time.Millisecond):
	}

	queue.fileOps.write = originalWrite
	close(releaseError)
	if err := <-failDone; !errors.Is(err, wantTurnErr) || !errors.Is(err, wantStorageErr) {
		t.Fatalf("failed turn error = %v, want joined turn and storage failures", err)
	}
	result := <-nextResult
	if err := <-nextErr; err != nil {
		t.Fatal(err)
	}
	if result.Disposition != PendingInputStarted || result.TurnID == "" {
		t.Fatalf("next admission = %+v, want a fresh started turn", result)
	}
	if status := eng.PendingInputStatus(); status.TurnID != result.TurnID {
		t.Fatalf("active turn = %+v, want only fresh reservation %q", status, result.TurnID)
	}
	eng.finishActiveTurn(result.TurnID)
}

type blockingFallbackTerminalCommitter struct {
	delegate      events.Committer
	completionErr error
	errorStarted  chan struct{}
	releaseError  chan struct{}
	once          sync.Once
}

func (c *blockingFallbackTerminalCommitter) Commit(event events.Event) (events.Event, error) {
	if event.Type == "turn.completed" {
		return events.Event{}, c.completionErr
	}
	if event.Type == "turn.errored" {
		c.once.Do(func() { close(c.errorStarted) })
		<-c.releaseError
	}
	return c.delegate.Commit(event)
}

func TestReceivePendingInputReturnsStartedAfterCommittedAdmissionWithoutJournalReread(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	queue := eng.currentPendingInputQueue()
	originalPath := queue.path
	writes := 0
	originalWrite := queue.fileOps.write
	queue.fileOps.write = func(file *os.File, body []byte) (int, error) {
		n, err := originalWrite(file, body)
		writes++
		if err == nil && writes == 2 {
			queue.path = originalPath + ".unavailable"
		}
		return n, err
	}
	t.Cleanup(func() { queue.path = originalPath })

	result, err := eng.ReceivePendingInput(context.Background(), PendingInputRequest{
		Message: llm.TextMessage(llm.RoleUser, "start despite journal reread failure"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Disposition != PendingInputStarted || result.RecordID == "" || result.TurnID == "" || result.Message.ID == "" {
		t.Fatalf("ReceivePendingInput() result = %+v, want committed start action", result)
	}
}

func TestResolvePendingInputTreatsTranscriptAsDeliveredAfterTerminalFailure(t *testing.T) {
	eng, _ := newEngine(t, errorProvider{}, false)
	opts := PendingInputOptions{ID: "mcp-event-1", TTL: time.Hour}
	started, err := eng.ReceivePendingInput(context.Background(), PendingInputRequest{
		Message: llm.Message{Role: llm.RoleUser, Kind: llm.MessageKindMCPEvent, Blocks: []llm.Block{{Type: llm.BlockText, Text: "event"}}},
		Options: &opts,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, runErr := eng.TurnMessageWithID(context.Background(), started.Message, started.TurnID)
	if runErr == nil {
		t.Fatal("TurnMessageWithID() error = nil, want provider failure")
	}

	resolved, err := eng.ResolvePendingInput(started.RecordID, runErr)
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("ResolvePendingInput() error = %v, want provider failure", err)
	}
	if resolved.Disposition != PendingInputProcessed || resolved.Retry != PendingInputNoRetry || pendingLifecycleTestRecord(t, eng, resolved.RecordID).State != PendingInputStateProcessed {
		t.Fatalf("resolved input = %+v", resolved)
	}
}

func TestReceivePendingInputDeduplicatesStableExternalIdentity(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	opts := PendingInputOptions{ID: "observation-1", TTL: time.Hour}

	started, err := eng.ReceivePendingInput(context.Background(), PendingInputRequest{
		Message: llm.Message{Role: llm.RoleUser, Kind: llm.MessageKindObservation, Blocks: []llm.Block{{Type: llm.BlockText, Text: "changed"}}},
		Options: &opts,
	})
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := eng.ReceivePendingInput(context.Background(), PendingInputRequest{RecordID: "observation-1"})
	if err != nil {
		t.Fatal(err)
	}
	if started.Disposition != PendingInputStarted || duplicate.Disposition != PendingInputQueued {
		t.Fatalf("started = %+v; duplicate = %+v", started, duplicate)
	}
	if duplicate.RecordID != started.RecordID || duplicate.Status.PendingCount != 0 {
		t.Fatalf("duplicate attached a second live input: %+v", duplicate)
	}
	record := pendingLifecycleTestRecord(t, eng, duplicate.RecordID)
	if record.Message.Kind != llm.MessageKindObservation || record.Origin != PendingInputOriginTurn || !record.ExpiresAt.IsZero() {
		t.Fatalf("stable source metadata changed: %+v", record)
	}
}

func TestReceivePendingInputDefersAcceptedExternalInputUntilRecoveryBarrier(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	opts := PendingInputOptions{ID: "side-result-1", TTL: time.Hour}

	accepted, err := eng.ReceivePendingInput(context.Background(), PendingInputRequest{
		Message:       llm.Message{Role: llm.RoleUser, Kind: llm.MessageKindSideSession, Blocks: []llm.Block{{Type: llm.BlockText, Text: "child result"}}},
		Options:       &opts,
		DeferDelivery: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Disposition != PendingInputQueued || accepted.Retry != PendingInputRetryAfterRecovery || pendingLifecycleTestRecord(t, eng, accepted.RecordID).State != PendingInputStatePending {
		t.Fatalf("accepted input = %+v", accepted)
	}
	if status := eng.PendingInputStatus(); status.TurnID != "" || status.PendingCount != 0 {
		t.Fatalf("deferred input became live before barrier: %+v", status)
	}

	started, err := eng.ReceivePendingInput(context.Background(), PendingInputRequest{RecordID: accepted.RecordID})
	if err != nil {
		t.Fatal(err)
	}
	if started.Disposition != PendingInputStarted || started.RecordID != accepted.RecordID || started.Message.Kind != llm.MessageKindSideSession {
		t.Fatalf("resumed input = %+v", started)
	}
}

func TestReceivePendingInputQueuesBehindActiveTurn(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	started, err := eng.ReceivePendingInput(context.Background(), PendingInputRequest{
		Message: llm.TextMessage(llm.RoleUser, "first"),
	})
	if err != nil {
		t.Fatal(err)
	}

	queued, err := eng.ReceivePendingInput(context.Background(), PendingInputRequest{
		Message: llm.TextMessage(llm.RoleUser, "second"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if queued.Disposition != PendingInputQueued || queued.Status.TurnID != started.TurnID || queued.Status.PendingCount != 1 {
		t.Fatalf("queued input = %+v", queued)
	}
	record := pendingLifecycleTestRecord(t, eng, queued.RecordID)
	if record.ID == "" || record.MessageID == "" || record.Message.FirstText() != "second" || record.State != PendingInputStatePending {
		t.Fatalf("durable queued record = %+v", record)
	}
}

func TestTurnRejectsSynchronousInputBeforeQueueingWhenTurnIsReserved(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	if err := eng.ReserveTurnID("external-turn"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.finishActiveTurn("external-turn") })

	if _, err := eng.Turn(context.Background(), "synchronous input"); !errors.Is(err, ErrActiveTurnExists) {
		t.Fatalf("Turn() error = %v, want %v", err, ErrActiveTurnExists)
	}
	if status := eng.PendingInputStatus(); status.PendingCount != 0 {
		t.Fatalf("pending status = %+v, want rejected synchronous input to remain unqueued", status)
	}
	records, err := eng.currentPendingInputQueue().Records()
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.Message.FirstText() == "synchronous input" {
			t.Fatalf("synchronous input was durably accepted before Turn() returned an error: %+v", record)
		}
	}
}

func TestDiscardPendingInputRemovesAttachedLiveQueueEntry(t *testing.T) {
	eng, bus := newEngine(t, &mockProvider{}, false)
	eng.MaxPendingInputs = 1
	statusStore := NewStatusStore(StatusSeed{SessionID: "discard-session", MaxPendingInputs: 1})
	unsubscribe := bus.Subscribe("*", statusStore.Publish)
	defer unsubscribe()
	if err := eng.ReserveTurnID("active-turn"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.finishActiveTurn("active-turn") })
	opts := PendingInputOptions{ID: "discard-attached", TTL: time.Hour}
	queued, err := eng.ReceivePendingInput(context.Background(), PendingInputRequest{
		Message: llm.TextMessage(llm.RoleUser, "discard me"),
		Options: &opts,
	})
	if err != nil {
		t.Fatal(err)
	}
	if queued.Disposition != PendingInputQueued || queued.Status.PendingCount != 1 {
		t.Fatalf("queued input = %+v", queued)
	}
	if snapshot := statusStore.Snapshot(); snapshot.Session.PendingCount != 1 || snapshot.Session.CanAcceptInput {
		t.Fatalf("queued status projection = %+v, want full queue", snapshot.Session)
	}

	discarded, err := eng.DiscardPendingInput(queued.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	if discarded.Disposition != PendingInputDropped || discarded.Status.PendingCount != 0 {
		t.Fatalf("discarded input = %+v, want inert record removed from live queue", discarded)
	}
	if record := pendingLifecycleTestRecord(t, eng, queued.RecordID); record.State != PendingInputStateDropped {
		t.Fatalf("durable record = %+v, want dropped", record)
	}
	if snapshot := statusStore.Snapshot(); snapshot.Session.PendingCount != 0 || !snapshot.Session.CanAcceptInput {
		t.Fatalf("discarded status projection = %+v, want available queue", snapshot.Session)
	}
}

func TestPendingDroppedSubscriberCanSynchronouslyEnqueue(t *testing.T) {
	eng, bus := newEngine(t, &mockProvider{}, false)
	if err := eng.ReserveTurnID("active-turn"); err != nil {
		t.Fatal(err)
	}
	queued, err := eng.ReceivePendingInput(context.Background(), PendingInputRequest{
		Message: llm.TextMessage(llm.RoleUser, "discard me"),
		Options: &PendingInputOptions{ID: "discard-reentrant", TTL: time.Hour},
	})
	if err != nil {
		t.Fatal(err)
	}

	var nestedErr error
	bus.Subscribe("pending_input.dropped", func(events.Event) {
		_, nestedErr = eng.EnqueuePendingInput(context.Background(), "replacement")
	})
	done := make(chan error, 1)
	go func() {
		_, err := eng.DiscardPendingInput(queued.RecordID)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pending_input.dropped subscriber deadlocked while synchronously enqueueing")
	}
	if nestedErr != nil {
		t.Fatalf("nested enqueue: %v", nestedErr)
	}
	if status := eng.PendingInputStatus(); status.PendingCount != 1 {
		t.Fatalf("pending status = %+v, want replacement input queued", status)
	}
}

func TestDiscardPendingInputInvalidatesOutstandingStart(t *testing.T) {
	provider := &mockProvider{script: []llm.Response{{
		Message:    llm.TextMessage(llm.RoleAssistant, "must not run"),
		StopReason: llm.StopEndTurn,
	}}}
	eng, bus := newEngine(t, provider, false)
	statusStore := NewStatusStore(StatusSeed{SessionID: "discard-start"})
	unsubscribe := bus.Subscribe("*", statusStore.Publish)
	defer unsubscribe()
	started, err := eng.ReceivePendingInput(context.Background(), PendingInputRequest{
		Message: llm.TextMessage(llm.RoleUser, "discard before execution"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if started.Disposition != PendingInputStarted {
		t.Fatalf("started input = %+v", started)
	}
	if _, err := eng.DiscardPendingInput(started.RecordID); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.TurnMessageWithID(context.Background(), started.Message, started.TurnID); !errors.Is(err, ErrPendingInputHandled) {
		t.Fatalf("discarded start execution error = %v, want %v", err, ErrPendingInputHandled)
	}
	if provider.called != 0 {
		t.Fatalf("provider calls = %d, want none", provider.called)
	}
	if status := eng.PendingInputStatus(); status.TurnID != "" || status.PendingCount != 0 {
		t.Fatalf("pending status = %+v, want released discarded start", status)
	}
	records, err := eng.currentPendingInputQueue().Records()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[started.RecordID].State != PendingInputStateDropped {
		t.Fatalf("pending records = %+v, want only original dropped record", records)
	}
	if snapshot := statusStore.Snapshot(); snapshot.Turn == nil || snapshot.Turn.ID != started.TurnID || snapshot.Turn.State != TurnLifecycleErrored {
		t.Fatalf("live status = %+v, want discarded turn errored", snapshot.Turn)
	}
	journal, err := session.ReadEvents(eng.Session.Dir)
	if err != nil {
		t.Fatal(err)
	}
	replayed := NewStatusStoreFromJournal(StatusSeed{SessionID: "discard-start"}, journal).Snapshot()
	if replayed.Turn == nil || replayed.Turn.ID != started.TurnID || replayed.Turn.State != TurnLifecycleErrored {
		t.Fatalf("replayed status = %+v, want discarded turn errored", replayed.Turn)
	}
}

func TestDiscardPendingInputRetriesStartedTurnTerminalCommit(t *testing.T) {
	eng, bus := newEngine(t, &mockProvider{}, false)
	started, err := eng.ReceivePendingInput(context.Background(), PendingInputRequest{
		Message: llm.TextMessage(llm.RoleUser, "retry discarded terminal"),
	})
	if err != nil {
		t.Fatal(err)
	}
	wantCommitErr := errors.New("terminal journal unavailable")
	bus.SetCommitter(selectiveFailCommitter{eventType: "turn.errored", err: wantCommitErr})

	result, err := eng.DiscardPendingInput(started.RecordID)
	if !errors.Is(err, wantCommitErr) {
		t.Fatalf("first discard error = %v, want %v", err, wantCommitErr)
	}
	if result.Disposition != PendingInputDropped || result.Retry != PendingInputRetryAfterStorage {
		t.Fatalf("first discard = %+v, want dropped storage retry", result)
	}
	if status := eng.PendingInputStatus(); status.TurnID != started.TurnID {
		t.Fatalf("pending status = %+v, want active Turn retained for terminal retry", status)
	}
	if record := pendingLifecycleTestRecord(t, eng, started.RecordID); record.State != PendingInputStateDropped {
		t.Fatalf("durable record = %+v, want dropped before terminal retry", record)
	}

	bus.SetCommitter(selectiveSessionCommitter{session: eng.Session})
	result, err = eng.DiscardPendingInput(started.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Disposition != PendingInputDropped || result.Retry != PendingInputNoRetry {
		t.Fatalf("retried discard = %+v, want terminally dropped", result)
	}
	if status := eng.PendingInputStatus(); status.TurnID != "" {
		t.Fatalf("pending status = %+v, want released after terminal retry", status)
	}
	journal, err := session.ReadEvents(eng.Session.Dir)
	if err != nil {
		t.Fatal(err)
	}
	replayed := NewStatusStoreFromJournal(StatusSeed{SessionID: "discard-retry"}, journal).Snapshot()
	if replayed.Turn == nil || replayed.Turn.ID != started.TurnID || replayed.Turn.State != TurnLifecycleErrored {
		t.Fatalf("replayed status = %+v, want retried terminal error", replayed.Turn)
	}
}

func TestReceivePendingInputReturnsStorageRetryWhenPersistedRecordCannotBeRead(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	record, err := eng.PersistPendingMessageWithOptions(
		context.Background(),
		llm.TextMessage(llm.RoleUser, "retry journal read"),
		PendingInputOptions{ID: "journal-read-retry", TTL: time.Hour},
	)
	if err != nil {
		t.Fatal(err)
	}
	queue := eng.currentPendingInputQueue()
	pendingPath := queue.path
	backupPath := pendingPath + ".before-read-failure"
	if err := os.Rename(pendingPath, backupPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(pendingPath, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Remove(pendingPath)
		_ = os.Rename(backupPath, pendingPath)
	})

	result, err := eng.ReceivePendingInput(context.Background(), PendingInputRequest{RecordID: record.ID})
	if err == nil {
		t.Fatal("ReceivePendingInput() error = nil, want journal read failure")
	}
	if result.RecordID != record.ID || result.Retry != PendingInputRetryAfterStorage {
		t.Fatalf("ReceivePendingInput() result = %+v, want record %q with storage retry", result, record.ID)
	}
}

func TestReceivePersistedPendingInputReturnsStartedAfterCommittedAdmissionWithoutJournalReread(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	record, err := eng.PersistPendingMessageWithOptions(
		context.Background(),
		llm.TextMessage(llm.RoleUser, "resume despite journal reread failure"),
		PendingInputOptions{ID: "resume-without-reread", TTL: time.Hour},
	)
	if err != nil {
		t.Fatal(err)
	}
	queue := eng.currentPendingInputQueue()
	originalPath := queue.path
	originalWrite := queue.fileOps.write
	queue.fileOps.write = func(file *os.File, body []byte) (int, error) {
		n, err := originalWrite(file, body)
		if err == nil {
			queue.path = originalPath + ".unavailable"
		}
		return n, err
	}
	t.Cleanup(func() { queue.path = originalPath })

	result, err := eng.ReceivePendingInput(context.Background(), PendingInputRequest{RecordID: record.ID})
	if err != nil {
		t.Fatal(err)
	}
	if result.Disposition != PendingInputStarted || result.RecordID != record.ID || result.TurnID == "" || result.Message.ID != record.MessageID {
		t.Fatalf("ReceivePendingInput() result = %+v, want committed persisted start action", result)
	}
}

func TestFinishPendingInputCompactionReturnsPromotedStartWithoutJournalReread(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	compactTurnID, err := eng.ReservePendingInputCompaction()
	if err != nil {
		t.Fatal(err)
	}
	queued, err := eng.ReceivePendingInput(context.Background(), PendingInputRequest{
		Message: llm.TextMessage(llm.RoleUser, "promote despite journal reread failure"),
	})
	if err != nil {
		t.Fatal(err)
	}
	queue := eng.currentPendingInputQueue()
	originalPath := queue.path
	originalWrite := queue.fileOps.write
	queue.fileOps.write = func(file *os.File, body []byte) (int, error) {
		n, err := originalWrite(file, body)
		if err == nil {
			queue.path = originalPath + ".unavailable"
		}
		return n, err
	}
	t.Cleanup(func() { queue.path = originalPath })

	result, err := eng.FinishPendingInputCompaction(compactTurnID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Disposition != PendingInputStarted || result.RecordID != queued.RecordID || result.TurnID == "" || result.Message.ID == "" {
		t.Fatalf("FinishPendingInputCompaction() result = %+v, want promoted start action", result)
	}
}

func TestResolvePendingInputReturnsStorageRetryWhenRecordCannotBeRead(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	record, err := eng.PersistPendingMessageWithOptions(
		context.Background(),
		llm.TextMessage(llm.RoleUser, "resolve after storage recovers"),
		PendingInputOptions{ID: "resolve-read-retry", TTL: time.Hour},
	)
	if err != nil {
		t.Fatal(err)
	}
	queue := eng.currentPendingInputQueue()
	originalPath := queue.path
	queue.path = originalPath + ".unavailable"
	t.Cleanup(func() { queue.path = originalPath })

	result, err := eng.ResolvePendingInput(record.ID, errors.New("provider failed"))
	if err == nil {
		t.Fatal("ResolvePendingInput() error = nil, want journal read failure")
	}
	if result.RecordID != record.ID || result.Retry != PendingInputRetryAfterStorage {
		t.Fatalf("ResolvePendingInput() result = %+v, want record %q with storage retry", result, record.ID)
	}
}

func TestReceivePendingInputReturnsStorageRetryWhenPersistedEnqueueWriteFails(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	record, err := eng.PersistPendingMessageWithOptions(
		context.Background(),
		llm.TextMessage(llm.RoleUser, "retry enqueue write"),
		PendingInputOptions{ID: "enqueue-write-retry", TTL: time.Hour},
	)
	if err != nil {
		t.Fatal(err)
	}
	queue := eng.currentPendingInputQueue()
	queue.now = func() time.Time { return record.ExpiresAt.Add(time.Second) }
	wantErr := errors.New("journal append failed")
	queue.fileOps.write = func(*os.File, []byte) (int, error) { return 0, wantErr }

	result, err := eng.ReceivePendingInput(context.Background(), PendingInputRequest{RecordID: record.ID})
	if !errors.Is(err, wantErr) {
		t.Fatalf("ReceivePendingInput() error = %v, want %v", err, wantErr)
	}
	if result.RecordID != record.ID || result.Retry != PendingInputRetryAfterStorage {
		t.Fatalf("ReceivePendingInput() result = %+v, want record %q with storage retry", result, record.ID)
	}
}

func TestReceivePendingInputDoesNotRetryCanceledPersistedEnqueue(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	record, err := eng.PersistPendingMessageWithOptions(
		context.Background(),
		llm.TextMessage(llm.RoleUser, "cancel enqueue"),
		PendingInputOptions{ID: "cancel-enqueue", TTL: time.Hour},
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx := &cancelOnSecondErrContext{Context: context.Background()}

	result, err := eng.ReceivePendingInput(ctx, PendingInputRequest{RecordID: record.ID})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ReceivePendingInput() error = %v, want %v", err, context.Canceled)
	}
	if result.RecordID != record.ID || result.Retry != PendingInputNoRetry {
		t.Fatalf("ReceivePendingInput() result = %+v, want canceled record without retry", result)
	}
}

func TestRestorePendingInputSerializesWithLifecycleDiscard(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	eng.pendingLifecycleMu.Lock()
	locked := true
	t.Cleanup(func() {
		if locked {
			eng.pendingLifecycleMu.Unlock()
		}
	})
	done := make(chan error, 1)
	go func() { done <- eng.restorePendingInput(context.Background(), "restore-turn", "") }()

	select {
	case err := <-done:
		t.Fatalf("restorePendingInput() completed outside lifecycle serialization: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	eng.pendingLifecycleMu.Unlock()
	locked = false
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("restorePendingInput() did not continue after lifecycle serialization released")
	}
}

type cancelOnSecondErrContext struct {
	context.Context
	calls int
}

func (c *cancelOnSecondErrContext) Err() error {
	c.calls++
	if c.calls > 1 {
		return context.Canceled
	}
	return nil
}

func pendingLifecycleTestRecord(t *testing.T, eng *Engine, recordID string) PendingInputRecord {
	t.Helper()
	record, ok, err := eng.PersistedPendingMessage(recordID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("pending input %q not found", recordID)
	}
	return record
}
