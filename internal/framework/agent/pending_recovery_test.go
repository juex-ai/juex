package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/foundation/events"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/framework/runtime"
)

const pendingRecoveryTestTimeout = 10 * time.Second

func TestAgentExternalDeliveryHandsOffAcceptedInputWhenResumeIsCanceled(t *testing.T) {
	dir := t.TempDir()
	provider := &recoveryProvider{}
	a, err := newTestAgent(dir, provider)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.CloseAndWait() })

	a.turnAdmission.transitionMu.Lock()
	transitionLocked := true
	defer func() {
		if transitionLocked {
			a.turnAdmission.transitionMu.Unlock()
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	record := testExternalInput("obs-canceled-before-resume")
	type deliveryResult struct {
		outcome ExternalDelivery
		err     error
	}
	done := make(chan deliveryResult, 1)
	go func() {
		outcome, err := deliverTestInput(a, ctx, record)
		done <- deliveryResult{outcome: outcome, err: err}
	}()

	recordID := record.ID
	deadline := time.Now().Add(pendingRecoveryTestTimeout)
	for {
		pending, ok, stateErr := a.Engine.PersistedPendingMessage(recordID)
		if stateErr != nil {
			t.Fatal(stateErr)
		}
		if ok && pending.State == runtime.PendingInputStatePending {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("external input was not persisted before resume cancellation")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	a.turnAdmission.transitionMu.Unlock()
	transitionLocked = false

	delivery := <-done
	if !errors.Is(delivery.err, context.Canceled) {
		t.Fatalf("DeliverExternalInput error = %v, want context.Canceled", delivery.err)
	}
	if !delivery.outcome.Queued || delivery.outcome.RecordID != recordID {
		t.Fatalf("delivery outcome = %+v, want queued durable input %q", delivery.outcome, recordID)
	}
	deadline = time.Now().Add(pendingRecoveryTestTimeout)
	for {
		calls, _ := provider.snapshot()
		if calls == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("provider calls = %d, want App-owned handoff after caller cancellation", calls)
		}
		time.Sleep(10 * time.Millisecond)
	}
	pending, ok, stateErr := a.Engine.PersistedPendingMessage(recordID)
	if stateErr != nil {
		t.Fatal(stateErr)
	}
	if !ok || pending.State != runtime.PendingInputStateProcessed || !a.Thread.HasMessageID(pending.MessageID) {
		t.Fatalf("pending after canceled-delivery handoff = %+v ok=%v", pending, ok)
	}
}

func TestAgentExternalDeliveryRetriesReplayableAdmissionCommitFailure(t *testing.T) {
	dir := t.TempDir()
	provider := &recoveryProvider{}
	a, err := newTestAgent(dir, provider)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.CloseAndWait() })
	wantErr := errors.New("injected turn admission commit failure")
	a.Bus.SetCommitter(&failOnceEventCommitter{
		delegate:  a.eventSink,
		eventType: runtime.TurnAdmittedType,
		err:       wantErr,
	})

	record := testExternalInput("obs-admission-commit-retry")
	outcome, err := deliverTestInput(a, context.Background(), record)
	if !errors.Is(err, wantErr) {
		t.Fatalf("DeliverExternalInput error = %v, want injected commit failure", err)
	}
	if !outcome.Queued || outcome.RecordID != record.ID {
		t.Fatalf("delivery outcome = %+v, want replayable queued record", outcome)
	}
	deadline := time.Now().Add(pendingRecoveryTestTimeout)
	for {
		pending, ok, stateErr := a.Engine.PersistedPendingMessage(outcome.RecordID)
		if stateErr != nil {
			t.Fatal(stateErr)
		}
		calls, _ := provider.snapshot()
		if ok && pending.State == runtime.PendingInputStateProcessed && a.Thread.HasMessageID(pending.MessageID) && calls == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("failed-admission input = %+v ok=%v, want App-owned retry to process it", pending, ok)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestResumePersistedInputPreservesAdmissionRetry(t *testing.T) {
	dir := t.TempDir()
	a, err := newTestAgent(dir, &recoveryProvider{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.CloseAndWait() })
	record, err := a.Engine.PersistPendingMessageWithOptions(
		context.Background(),
		llm.TextMessage(llm.RoleUser, "preserve admission retry"),
		runtime.PendingInputOptions{ID: "preserve-admission-retry", TTL: time.Hour},
	)
	if err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("injected turn admission failure")
	a.Bus.SetCommitter(&failOnceEventCommitter{
		delegate:  a.eventSink,
		eventType: runtime.TurnAdmittedType,
		err:       wantErr,
	})

	delivery, err := a.resumePersistedInputLocked(context.Background(), record.ID)
	if !errors.Is(err, wantErr) {
		t.Fatalf("resumePersistedInputLocked() error = %v, want %v", err, wantErr)
	}
	if delivery.RecordID != record.ID || !delivery.Queued || delivery.Retry != runtime.PendingInputRetryAdmission {
		t.Fatalf("resumePersistedInputLocked() delivery = %+v, want bounded admission retry", delivery)
	}
}

func TestResumePersistedInputWaitsForExclusiveCommand(t *testing.T) {
	dir := t.TempDir()
	a, err := newTestAgent(dir, &recoveryProvider{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.CloseAndWait() })
	record, err := a.Engine.PersistPendingMessageWithOptions(
		context.Background(),
		llm.TextMessage(llm.RoleUser, "wait for new-generation marker"),
		runtime.PendingInputOptions{ID: "exclusive-command-wait", TTL: time.Hour},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !a.beginExclusiveCommand() {
		t.Fatal("beginExclusiveCommand() = false")
	}

	delivery, err := a.resumePersistedInputLocked(context.Background(), record.ID)
	if !errors.Is(err, errTurnAdmissionBusy) {
		t.Fatalf("resumePersistedInputLocked() error = %v, want %v", err, errTurnAdmissionBusy)
	}
	if delivery.RecordID != record.ID || !delivery.Queued || delivery.Retry != runtime.PendingInputRetryAfterTurn {
		t.Fatalf("resumePersistedInputLocked() delivery = %+v, want command retry", delivery)
	}
	if status := a.Engine.PendingInputStatus(); status.TurnID != "" {
		t.Fatalf("external input started during exclusive command: %+v", status)
	}

	a.finishExclusiveCommand()
	delivery, err = a.resumePersistedInputLocked(context.Background(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !delivery.Delivered {
		t.Fatalf("delivery after command = %+v, want delivered", delivery)
	}
}

func TestAppStartupRecoveryRetriesReplayableAdmissionFailure(t *testing.T) {
	dir := t.TempDir()
	provider := &recoveryProvider{}
	a, err := newTestAgent(dir, provider)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.CloseAndWait() })
	record, err := a.Engine.PersistPendingMessageWithOptions(
		context.Background(),
		llm.TextMessage(llm.RoleUser, "retry failed startup admission"),
		runtime.PendingInputOptions{ID: "startup-admission-retry", TTL: time.Hour},
	)
	if err != nil {
		t.Fatal(err)
	}
	a.Bus.SetCommitter(&failOnceEventCommitter{
		delegate:  a.eventSink,
		eventType: runtime.TurnAdmittedType,
		err:       errors.New("injected startup admission failure"),
	})
	a.startPendingInputRecovery([]runtime.PendingInputRecovery{{RecordID: record.ID}})

	deadline := time.Now().Add(pendingRecoveryTestTimeout)
	for {
		pending, ok, stateErr := a.Engine.PersistedPendingMessage(record.ID)
		if stateErr != nil {
			t.Fatal(stateErr)
		}
		calls, _ := provider.snapshot()
		if ok && pending.State == runtime.PendingInputStateProcessed && a.Thread.HasMessageID(pending.MessageID) && calls == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("startup admission input = %+v ok=%v calls=%d, want handoff retry", pending, ok, calls)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAppStartupRecoveryKeepsBarrierThroughReplayableAdmissionRetry(t *testing.T) {
	dir := t.TempDir()
	provider := &recoveryProvider{}
	a, err := newTestAgent(dir, provider)
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	released := false
	t.Cleanup(func() {
		if !released {
			close(release)
		}
		_ = a.CloseAndWait()
	})
	record, err := a.Engine.PersistPendingMessageWithOptions(
		context.Background(),
		llm.TextMessage(llm.RoleUser, "retry startup admission before Context Generation change"),
		runtime.PendingInputOptions{ID: "startup-admission-barrier", TTL: time.Hour},
	)
	if err != nil {
		t.Fatal(err)
	}
	failed := make(chan struct{})
	a.Bus.SetCommitter(&retryUntilReleasedEventCommitter{
		delegate:  a.eventSink,
		eventType: runtime.TurnAdmittedType,
		err:       errors.New("injected persistent startup admission failure"),
		release:   release,
		failed:    failed,
	})
	a.startPendingInputRecovery([]runtime.PendingInputRecovery{{RecordID: record.ID}})
	recoveryDone := a.pendingRecoveryDone

	select {
	case <-failed:
	case <-time.After(pendingRecoveryTestTimeout):
		t.Fatal("startup recovery did not attempt admission")
	}
	select {
	case <-recoveryDone:
		t.Fatal("startup recovery barrier closed while replayable admission retry was still pending")
	case <-time.After(100 * time.Millisecond):
	}

	switchDone := make(chan error, 1)
	go func() { switchDone <- a.NewContext(context.Background()) }()
	select {
	case err := <-switchDone:
		t.Fatalf("Context Generation change completed while startup admission retry was pending: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	released = true
	select {
	case <-recoveryDone:
	case <-time.After(pendingRecoveryTestTimeout):
		t.Fatal("startup recovery did not finish after admission recovered")
	}
	select {
	case err := <-switchDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(pendingRecoveryTestTimeout):
		t.Fatal("Context Generation change did not continue after startup recovery")
	}
	if calls, _ := provider.snapshot(); calls != 1 {
		t.Fatalf("provider calls = %d, want recovered input processed once before Context Generation change", calls)
	}
}

func TestAgentExternalDeliveryHandoffKeepsOriginThreadThroughRetry(t *testing.T) {
	dir := t.TempDir()
	provider := &recoveryProvider{}
	a, err := newTestAgent(dir, provider)
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	released := false
	t.Cleanup(func() {
		if !released {
			close(release)
		}
		_ = a.CloseAndWait()
	})
	failed := make(chan struct{})
	retrying := make(chan struct{})
	wantErr := errors.New("injected persistent external admission failure")
	a.Bus.SetCommitter(&retryUntilReleasedEventCommitter{
		delegate:  a.eventSink,
		eventType: runtime.TurnAdmittedType,
		err:       wantErr,
		release:   release,
		failed:    failed,
		retrying:  retrying,
	})

	outcome, err := deliverTestInput(a, context.Background(), testExternalInput("obs-thread-bound-handoff"))
	if !errors.Is(err, wantErr) {
		t.Fatalf("DeliverExternalInput error = %v, want injected admission failure", err)
	}
	if !outcome.Queued {
		t.Fatalf("DeliverExternalInput outcome = %+v, want queued", outcome)
	}
	select {
	case <-retrying:
	case <-time.After(pendingRecoveryTestTimeout):
		t.Fatal("App-owned handoff did not retry admission")
	}

	switchDone := make(chan error, 1)
	go func() { switchDone <- a.NewContext(context.Background()) }()
	select {
	case err := <-switchDone:
		t.Fatalf("Context Generation change completed while origin-Thread handoff was retrying: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	released = true
	select {
	case err := <-switchDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Context Generation change did not continue after handoff became safe")
	}
	if calls, _ := provider.snapshot(); calls != 1 {
		t.Fatalf("provider calls = %d, want origin-Thread handoff processed once", calls)
	}
}

func TestAppCompactWaitsForStartupPendingInputRecovery(t *testing.T) {
	a, _ := newStubAgent(t)
	recoveryDone := make(chan struct{})
	a.pendingRecoveryDone = recoveryDone
	admitted := make(chan struct{}, 1)
	unsubscribe := a.Bus.Subscribe(runtime.TurnAdmittedType, func(events.Event) {
		admitted <- struct{}{}
	})
	t.Cleanup(unsubscribe)

	type compactResult struct {
		err error
	}
	finished := make(chan compactResult, 1)
	go func() {
		_, err := a.CompactWithInstructions(context.Background(), "manual", false, "")
		finished <- compactResult{err: err}
	}()
	select {
	case <-admitted:
		t.Fatal("compaction admission committed before startup recovery completed")
	case result := <-finished:
		t.Fatalf("compaction completed before startup recovery: %+v", result)
	case <-time.After(100 * time.Millisecond):
	}
	close(recoveryDone)
	select {
	case result := <-finished:
		if result.err != nil {
			t.Fatal(result.err)
		}
	case <-time.After(pendingRecoveryTestTimeout):
		t.Fatal("compaction did not continue after startup recovery")
	}
	select {
	case <-admitted:
	case <-time.After(pendingRecoveryTestTimeout):
		t.Fatal("compaction admission was not committed after startup recovery")
	}
}

func TestAppBeginCompactAdmissionWaitsForStartupPendingInputRecovery(t *testing.T) {
	a, _ := newStubAgent(t)
	recoveryDone := make(chan struct{})
	a.pendingRecoveryDone = recoveryDone

	type compactAdmission struct {
		turnID string
		err    error
	}
	admitted := make(chan compactAdmission, 1)
	go func() {
		turnID, err := a.BeginCompactAdmission(context.Background())
		admitted <- compactAdmission{turnID: turnID, err: err}
	}()
	select {
	case result := <-admitted:
		t.Fatalf("admitted compaction reserved before startup recovery: %+v", result)
	case <-time.After(100 * time.Millisecond):
	}

	close(recoveryDone)
	var compactTurnID string
	select {
	case result := <-admitted:
		if result.err != nil {
			t.Fatal(result.err)
		}
		compactTurnID = result.turnID
	case <-time.After(pendingRecoveryTestTimeout):
		t.Fatal("admitted compaction did not continue after startup recovery")
	}
	if compactTurnID == "" {
		t.Fatal("compaction has no Framework turn id")
	}
	if _, err := a.FinishCompactAdmission(compactTurnID); err != nil {
		t.Fatal(err)
	}
}

func TestAppStartupRecoveryAdvancesPastOldestRecordThatExpiresBeforeWorker(t *testing.T) {
	dir := t.TempDir()
	provider := &recoveryProvider{}
	a, err := newTestAgent(dir, provider)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.CloseAndWait() })
	oldest, err := a.Engine.PersistPendingMessageWithOptions(
		context.Background(),
		llm.TextMessage(llm.RoleUser, "expires before startup worker"),
		runtime.PendingInputOptions{ID: "expires-before-worker", TTL: time.Millisecond},
	)
	if err != nil {
		t.Fatal(err)
	}
	later, err := a.Engine.PersistPendingMessageWithOptions(
		context.Background(),
		llm.TextMessage(llm.RoleUser, "recover after expired oldest"),
		runtime.PendingInputOptions{ID: "recover-after-expired-oldest", TTL: time.Hour},
	)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)

	if err := a.activateExternalInputAfterPendingRecovery(context.Background(), []runtime.PendingInputRecovery{{RecordID: oldest.ID}, {RecordID: later.ID}}); err != nil {
		t.Fatal(err)
	}
	if err := a.waitPendingInputRecovery(); err != nil {
		t.Fatal(err)
	}
	calls, histories := provider.snapshot()
	if calls != 1 || len(histories) != 1 {
		t.Fatalf("provider calls = %d histories=%+v, want later record recovered once", calls, histories)
	}
	if !providerHistoryContains(histories[0], later.MessageID, "recover after expired oldest") {
		t.Fatalf("recovered history = %+v, want later message %q", histories[0], later.MessageID)
	}
	for _, id := range []string{oldest.ID, later.ID} {
		_, ok, err := a.Engine.PersistedPendingMessage(id)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			t.Fatalf("expired or completed record %q was retained", id)
		}
	}
}

func providerHistoryContains(history []llm.Message, id, text string) bool {
	for _, message := range history {
		if (id == "" || message.ID == id) && message.FirstText() == text {
			return true
		}
	}
	return false
}
