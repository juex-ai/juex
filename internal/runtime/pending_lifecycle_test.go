package runtime

import (
	"context"
	"errors"
	"fmt"
	goruntime "runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
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

func TestAdmissionDurableProjectionCanSynchronouslyReceivePendingInput(t *testing.T) {
	eng, bus := newEngine(t, &mockProvider{}, false)
	sink := events.NewDurableSink(eng.Thread)
	bus.SetCommitter(sink)
	projectionResult := make(chan PendingInputResult, 1)
	projectionErr := make(chan error, 1)
	sink.AddProjection(events.DeliveryFunc(func(event events.Event) {
		if event.Type != TurnAdmittedType {
			return
		}
		result, err := eng.ReceivePendingInput(context.Background(), PendingInputRequest{
			Message: llm.TextMessage(llm.RoleUser, "projection follow-up"),
		})
		projectionResult <- result
		projectionErr <- err
	}))

	admissionDone := make(chan PendingInputResult, 1)
	admissionErr := make(chan error, 1)
	go func() {
		result, err := eng.ReceivePendingInput(context.Background(), PendingInputRequest{
			Message: llm.TextMessage(llm.RoleUser, "first"),
		})
		admissionDone <- result
		admissionErr <- err
	}()
	var admitted PendingInputResult
	select {
	case admitted = <-admissionDone:
		if err := <-admissionErr; err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("turn.admitted projection deadlocked while synchronously receiving follow-up input")
	}
	if admitted.Disposition != PendingInputStarted || admitted.TurnID == "" {
		t.Fatalf("admission = %+v, want started", admitted)
	}
	projected := <-projectionResult
	if err := <-projectionErr; err != nil {
		t.Fatal(err)
	}
	if projected.Disposition != PendingInputQueued || projected.Status.TurnID != admitted.TurnID {
		t.Fatalf("projection input = %+v, want queued behind %q", projected, admitted.TurnID)
	}
	eng.finishActiveTurn(admitted.TurnID)
	bus.SetCommitter(nil)
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDiscardPendingInputRejectsDuringAdmissionPublication(t *testing.T) {
	eng, bus := newEngine(t, &mockProvider{}, false)
	publicationStarted := make(chan struct{})
	releasePublication := make(chan struct{})
	var once sync.Once
	unsubscribe := bus.Subscribe(TurnAdmittedType, func(events.Event) {
		once.Do(func() { close(publicationStarted) })
		<-releasePublication
	})
	defer unsubscribe()

	admissionDone := make(chan PendingInputResult, 1)
	admissionErr := make(chan error, 1)
	go func() {
		result, err := eng.ReceivePendingInput(context.Background(), PendingInputRequest{
			Message: llm.TextMessage(llm.RoleUser, "first"),
			Options: &PendingInputOptions{ID: "publishing-start", TTL: time.Hour},
		})
		admissionDone <- result
		admissionErr <- err
	}()
	select {
	case <-publicationStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("turn admission publication did not start")
	}
	discarded, err := eng.DiscardPendingInput("publishing-start")
	if !errors.Is(err, ErrActiveTurnExists) {
		close(releasePublication)
		t.Fatalf("discard error = %v, want %v", err, ErrActiveTurnExists)
	}
	if discarded.Disposition != PendingInputQueued || discarded.Retry != PendingInputRetryAfterTurn || discarded.Status.TurnID == "" {
		close(releasePublication)
		t.Fatalf("discard during admission publication = %+v", discarded)
	}

	close(releasePublication)
	admitted := <-admissionDone
	if err := <-admissionErr; err != nil {
		t.Fatal(err)
	}
	if admitted.Disposition != PendingInputStarted || admitted.TurnID != discarded.Status.TurnID {
		t.Fatalf("admission = %+v, want started Turn %q", admitted, discarded.Status.TurnID)
	}
	if record := pendingLifecycleTestRecord(t, eng, "publishing-start"); record.State != PendingInputStateAdmitted {
		t.Fatalf("record after rejected discard = %+v, want admitted", record)
	}
	eng.finishActiveTurn(admitted.TurnID)
}

func TestTerminalErrorCommitDoesNotWaitForProjectionHoldingCommitBarrier(t *testing.T) {
	eng, bus := newEngine(t, &mockProvider{}, false)
	sink := events.NewDurableSink(eng.Thread)
	bus.SetCommitter(sink)
	if err := eng.ReserveTurnID("terminal-lock-order"); err != nil {
		t.Fatal(err)
	}
	projectionStarted := make(chan struct{})
	projectionResult := make(chan error, 1)
	sink.AddProjection(events.DeliveryFunc(func(event events.Event) {
		if event.Type != "projection.blocker" {
			return
		}
		close(projectionStarted)
		_, err := eng.ReceivePendingInput(context.Background(), PendingInputRequest{
			Message: llm.TextMessage(llm.RoleUser, "projection response"),
		})
		projectionResult <- err
	}))

	eng.pendingLifecycleMu.Lock()
	emitDone := make(chan error, 1)
	go func() { emitDone <- bus.Emit(events.Event{Type: "projection.blocker"}) }()
	select {
	case <-projectionStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("blocking projection did not start")
	}
	wantTurnErr := errors.New("terminal failure")
	failDone := make(chan error, 1)
	go func() { failDone <- eng.failActiveTurnLocked("terminal-lock-order", wantTurnErr, true) }()
	select {
	case err := <-failDone:
		if !errors.Is(err, wantTurnErr) {
			t.Fatalf("terminal failure = %v, want %v", err, wantTurnErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("terminal error commit deadlocked with an earlier durable projection")
	}
	if err := <-emitDone; err != nil {
		t.Fatal(err)
	}
	if err := <-projectionResult; !errors.Is(err, ErrActiveTurnExists) {
		t.Fatalf("projection admission error = %v, want terminal barrier conflict", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestReceivePendingInputRejectsDuringFallbackTerminalCommit(t *testing.T) {
	turn := startFallbackTerminalTestTurn(t)
	eng := turn.engine
	select {
	case <-turn.started:
	case err := <-turn.result:
		t.Fatalf("Turn exited before fallback terminal commit: %v\n%s", err, turn.diagnostics())
	case <-time.After(2 * time.Second):
		t.Fatalf("fallback terminal commit did not start\n%s", turn.diagnostics())
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
		if err := <-nextErr; !errors.Is(err, ErrActiveTurnExists) {
			t.Fatalf("next admission error = %v, want %v", err, ErrActiveTurnExists)
		}
		if result.Retry != PendingInputRetryAfterTurn || result.Status.TurnID == "" {
			t.Fatalf("next admission = %+v, want retry after fallback terminal commit", result)
		}
	case <-time.After(time.Second):
		t.Fatalf("next admission blocked behind fallback terminal commit\n%s", turn.diagnostics())
	}

	turn.release()
	if err := <-turn.result; !errors.Is(err, turn.completionErr) {
		t.Fatalf("first turn error = %v, want %v", err, turn.completionErr)
	}
	result, err := eng.ReceivePendingInput(context.Background(), PendingInputRequest{
		Message: llm.TextMessage(llm.RoleUser, "second"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Disposition != PendingInputStarted || result.TurnID == "" {
		t.Fatalf("next admission = %+v, want started after fallback terminal commit", result)
	}
	eng.finishActiveTurn(result.TurnID)
}

type fallbackTerminalTestTurn struct {
	engine        *Engine
	completionErr error
	started       <-chan struct{}
	result        chan error
	finished      chan struct{}
	release       func()
	committer     *blockingFallbackTerminalCommitter
}

type blockingFallbackTerminalCommitter struct {
	delegate      events.Committer
	completionErr error
	errorStarted  chan struct{}
	releaseError  chan struct{}
	once          sync.Once
	lastEvent     atomic.Value
}

func (c *blockingFallbackTerminalCommitter) Commit(event events.Event) (events.Event, error) {
	c.lastEvent.Store(event.Type)
	if event.Type == "turn.completed" {
		return events.Event{}, c.completionErr
	}
	if event.Type == "turn.errored" {
		c.once.Do(func() { close(c.errorStarted) })
		<-c.releaseError
	}
	return c.delegate.Commit(event)
}

func (turn *fallbackTerminalTestTurn) diagnostics() string {
	stacks := make([]byte, 256<<10)
	n := goruntime.Stack(stacks, true)
	return fmt.Sprintf("thread=%s last_commit=%v\ngoroutines:\n%s",
		turn.engine.Thread.Dir, turn.committer.lastEvent.Load(), stacks[:n])
}

func startFallbackTerminalTestTurn(t *testing.T) *fallbackTerminalTestTurn {
	t.Helper()
	provider := &mockProvider{script: []llm.Response{{
		Message:    llm.TextMessage(llm.RoleAssistant, "completion will fail"),
		StopReason: llm.StopEndTurn,
	}}}
	eng, bus := newEngine(t, provider, false)
	completionErr := errors.New("completion commit failed")
	started := make(chan struct{})
	release := make(chan struct{})
	committer := &blockingFallbackTerminalCommitter{
		delegate:      selectiveThreadCommitter{thread: eng.Thread},
		completionErr: completionErr,
		errorStarted:  started,
		releaseError:  release,
	}
	bus.SetCommitter(committer)
	turn := &fallbackTerminalTestTurn{
		engine:        eng,
		completionErr: completionErr,
		started:       started,
		result:        make(chan error, 1),
		finished:      make(chan struct{}),
		release:       sync.OnceFunc(func() { close(release) }),
		committer:     committer,
	}
	ctx, cancel := context.WithCancel(context.Background())
	// The committer deliberately blocks without observing context cancellation.
	// Release and join the Turn before newEngine's Thread cleanup runs.
	t.Cleanup(func() {
		cancel()
		turn.release()
		<-turn.finished
	})
	go func() {
		defer close(turn.finished)
		_, err := eng.Turn(ctx, "first")
		turn.result <- err
	}()
	return turn
}

func TestFallbackTerminalFixtureCleanupJoinsAbandonedTurn(t *testing.T) {
	var turn *fallbackTerminalTestTurn
	t.Cleanup(func() {
		if turn != nil {
			turn.release()
			<-turn.finished
		}
	})
	if !t.Run("abandoned before explicit release", func(t *testing.T) {
		turn = startFallbackTerminalTestTurn(t)
		select {
		case <-turn.started:
		case err := <-turn.result:
			t.Fatalf("Turn exited before fallback terminal commit: %v\n%s", err, turn.diagnostics())
		case <-time.After(2 * time.Second):
			t.Fatalf("fallback terminal commit did not start\n%s", turn.diagnostics())
		}
		if diagnostic := turn.diagnostics(); !strings.Contains(diagnostic, "last_commit=turn.errored") ||
			!strings.Contains(diagnostic, "goroutine ") {
			t.Fatalf("missing blocked-phase evidence: %s", diagnostic)
		}
		// Return at the failure boundary without the normal explicit release.
	}) {
		return
	}
	select {
	case <-turn.finished:
	default:
		t.Fatal("fixture cleanup left the fallback Turn running after Thread cleanup")
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
	if resolved.Disposition != PendingInputProcessed || resolved.Retry != PendingInputNoRetry || pendingLifecycleTestRecord(t, eng, resolved.RecordID).State != PendingInputStateDeadLettered {
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
		Message:       llm.Message{Role: llm.RoleUser, Kind: llm.MessageKindWorkerThread, Blocks: []llm.Block{{Type: llm.BlockText, Text: "child result"}}},
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
	if started.Disposition != PendingInputStarted || started.RecordID != accepted.RecordID || started.Message.Kind != llm.MessageKindWorkerThread {
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
	statusStore := NewStatusStore(StatusSeed{ThreadID: "discard-thread", MaxPendingInputs: 1})
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
	if snapshot := statusStore.Snapshot(); snapshot.Thread.PendingCount != 1 || snapshot.Thread.CanAcceptInput {
		t.Fatalf("queued status projection = %+v, want full queue", snapshot.Thread)
	}

	discarded, err := eng.DiscardPendingInput(queued.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	if discarded.Disposition != PendingInputDropped || discarded.Status.PendingCount != 0 {
		t.Fatalf("discarded input = %+v, want inert record removed from live queue", discarded)
	}
	if _, ok, err := eng.PersistedPendingMessage(queued.RecordID); err != nil || ok {
		t.Fatalf("discarded record remains, ok=%v err=%v", ok, err)
	}
	if snapshot := statusStore.Snapshot(); snapshot.Thread.PendingCount != 0 || !snapshot.Thread.CanAcceptInput {
		t.Fatalf("discarded status projection = %+v, want available queue", snapshot.Thread)
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
		_, nestedErr = eng.ReceivePendingInput(context.Background(), PendingInputRequest{
			Message: llm.TextMessage(llm.RoleUser, "replacement"),
		})
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
	statusStore := NewStatusStore(StatusSeed{ThreadID: "discard-start"})
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
	if len(records) != 0 {
		t.Fatalf("pending records = %+v, want discarded record removed", records)
	}
	if snapshot := statusStore.Snapshot(); snapshot.Turn == nil || snapshot.Turn.ID != started.TurnID || snapshot.Turn.State != TurnLifecycleCancelled {
		t.Fatalf("live status = %+v, want discarded turn cancelled", snapshot.Turn)
	}
	journal, err := eng.Thread.ReadEvents()
	if err != nil {
		t.Fatal(err)
	}
	replayed := NewStatusStoreFromJournal(StatusSeed{ThreadID: "discard-start"}, journal).Snapshot()
	if replayed.Turn == nil || replayed.Turn.ID != started.TurnID || replayed.Turn.State != TurnLifecycleCancelled {
		t.Fatalf("replayed status = %+v, want discarded turn cancelled", replayed.Turn)
	}
}

func TestDiscardPendingInputPreservesQueuedTailBeforeTerminalError(t *testing.T) {
	eng, bus := newEngine(t, &mockProvider{}, false)
	statusStore := NewStatusStore(StatusSeed{ThreadID: "discard-start-tail"})
	unsubscribe := bus.Subscribe("*", statusStore.Publish)
	defer unsubscribe()
	started, err := eng.ReceivePendingInput(context.Background(), PendingInputRequest{
		Message: llm.TextMessage(llm.RoleUser, "discard before execution"),
	})
	if err != nil {
		t.Fatal(err)
	}
	queued, err := eng.ReceivePendingInput(context.Background(), PendingInputRequest{
		Message: llm.TextMessage(llm.RoleUser, "preserve queued tail"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if queued.Disposition != PendingInputQueued || queued.Status.PendingCount != 1 {
		t.Fatalf("queued tail = %+v, want one pending input", queued)
	}
	queuedRecord := pendingLifecycleTestRecord(t, eng, queued.RecordID)

	if _, err := eng.DiscardPendingInput(started.RecordID); err != nil {
		t.Fatal(err)
	}
	if status := eng.PendingInputStatus(); status.TurnID != "" || status.PendingCount != 0 {
		t.Fatalf("pending status = %+v, want discarded start and preserved tail released", status)
	}
	if _, ok, err := eng.PersistedPendingMessage(queued.RecordID); err != nil || ok {
		t.Fatalf("cancelled queued tail remains, ok=%v err=%v", ok, err)
	}
	if len(eng.Thread.History) != 1 || eng.Thread.History[0].ID != queuedRecord.MessageID {
		t.Fatalf("durable history = %+v, want queued tail preserved once", eng.Thread.History)
	}
	if snapshot := statusStore.Snapshot(); snapshot.Turn == nil || snapshot.Turn.State != TurnLifecycleCancelled || snapshot.Thread.PendingCount != 0 {
		t.Fatalf("live status = %+v, want terminal cancellation with empty queue", snapshot)
	}
}

func TestDiscardPendingInputDoesNotInvalidateExecutingTurn(t *testing.T) {
	provider := &mockProvider{script: []llm.Response{{
		Message: llm.TextMessage(llm.RoleAssistant, "completed once"), StopReason: llm.StopEndTurn,
	}}}
	eng, bus := newEngine(t, provider, false)
	started, err := eng.ReceivePendingInput(context.Background(), PendingInputRequest{
		Message: llm.TextMessage(llm.RoleUser, "execute once"),
	})
	if err != nil {
		t.Fatal(err)
	}
	executionStarted := make(chan struct{})
	releaseExecution := make(chan struct{})
	installRuntimeTestModules(t, eng, &runtimeTurnInputPolicyModule{id: "block-before-turn-start", apply: func(runtimemodule.TurnInputRequest) (runtimemodule.TurnInputDecision, error) {
		close(executionStarted)
		<-releaseExecution
		return runtimemodule.TurnInputDecision{Action: runtimemodule.TurnInputAllow}, nil
	}})
	var completed, errored int
	bus.Subscribe("turn.completed", func(events.Event) { completed++ })
	bus.Subscribe("turn.errored", func(events.Event) { errored++ })
	turnDone := make(chan error, 1)
	go func() {
		_, err := eng.TurnMessageWithID(context.Background(), started.Message, started.TurnID)
		turnDone <- err
	}()
	select {
	case <-executionStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("TurnMessageWithID() did not reach its pre-start policy boundary")
	}

	discarded, err := eng.DiscardPendingInput(started.RecordID)
	if !errors.Is(err, ErrActiveTurnExists) {
		close(releaseExecution)
		t.Fatalf("discard executing Turn error = %v, want %v", err, ErrActiveTurnExists)
	}
	if discarded.Disposition != PendingInputQueued || discarded.Retry != PendingInputRetryAfterTurn || discarded.TurnID != started.TurnID {
		close(releaseExecution)
		t.Fatalf("discard executing Turn = %+v, want after-turn retry", discarded)
	}
	if record := pendingLifecycleTestRecord(t, eng, started.RecordID); record.State != PendingInputStateAdmitted {
		close(releaseExecution)
		t.Fatalf("executing record = %+v, want admitted and unchanged", record)
	}
	close(releaseExecution)
	if err := <-turnDone; err != nil {
		t.Fatal(err)
	}
	if provider.called != 1 || completed != 1 || errored != 0 {
		t.Fatalf("execution outcome: provider=%d completed=%d errored=%d, want 1/1/0", provider.called, completed, errored)
	}
	resolved, err := eng.DiscardPendingInput(started.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Disposition != PendingInputProcessed {
		t.Fatalf("discard completed record = %+v, want processed", resolved)
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
	if record := pendingLifecycleTestRecord(t, eng, started.RecordID); record.State != PendingInputStateAdmitted {
		t.Fatalf("durable record = %+v, want admitted until terminal retry", record)
	}

	bus.SetCommitter(selectiveThreadCommitter{thread: eng.Thread})
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
	journal, err := eng.Thread.ReadEvents()
	if err != nil {
		t.Fatal(err)
	}
	replayed := NewStatusStoreFromJournal(StatusSeed{ThreadID: "discard-retry"}, journal).Snapshot()
	if replayed.Turn == nil || replayed.Turn.ID != started.TurnID || replayed.Turn.State != TurnLifecycleCancelled {
		t.Fatalf("replayed status = %+v, want retried terminal cancellation", replayed.Turn)
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
