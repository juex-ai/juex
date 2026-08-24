package app

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/mcp"
	"github.com/juex-ai/juex/internal/observable"
	"github.com/juex-ai/juex/internal/runtime"
)

type recoveryProvider struct {
	mu        sync.Mutex
	calls     int
	histories [][]llm.Message
	called    chan struct{}
	err       error
}

type cancelAwareRecoveryProvider struct {
	called chan struct{}
}

type retryUntilReleasedEventCommitter struct {
	delegate  events.Committer
	eventType string
	err       error
	release   <-chan struct{}
	failed    chan struct{}
	once      sync.Once
	retrying  chan struct{}
	retryOnce sync.Once
	mu        sync.Mutex
	failures  int
}

func (c *retryUntilReleasedEventCommitter) Commit(event events.Event) (events.Event, error) {
	if event.Type == c.eventType {
		select {
		case <-c.release:
		default:
			c.mu.Lock()
			c.failures++
			failures := c.failures
			c.mu.Unlock()
			c.once.Do(func() { close(c.failed) })
			if failures >= 2 && c.retrying != nil {
				c.retryOnce.Do(func() { close(c.retrying) })
			}
			return events.Event{}, c.err
		}
	}
	return c.delegate.Commit(event)
}

func (*cancelAwareRecoveryProvider) Name() string { return "cancel-aware-recovery" }

func (p *cancelAwareRecoveryProvider) Complete(ctx context.Context, _ string, _ []llm.Message, _ []llm.ToolSpec) (llm.Response, error) {
	close(p.called)
	<-ctx.Done()
	return llm.Response{}, ctx.Err()
}

func (p *recoveryProvider) Name() string { return "recovery" }

func (p *recoveryProvider) Complete(_ context.Context, _ string, history []llm.Message, _ []llm.ToolSpec) (llm.Response, error) {
	p.mu.Lock()
	p.calls++
	p.histories = append(p.histories, append([]llm.Message(nil), history...))
	if p.calls == 1 && p.called != nil {
		close(p.called)
	}
	err := p.err
	p.mu.Unlock()
	if err != nil {
		return llm.Response{}, err
	}
	return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "recovered"), StopReason: llm.StopEndTurn}, nil
}

func (p *recoveryProvider) snapshot() (int, [][]llm.Message) {
	p.mu.Lock()
	defer p.mu.Unlock()
	histories := make([][]llm.Message, len(p.histories))
	for i := range p.histories {
		histories[i] = append([]llm.Message(nil), p.histories[i]...)
	}
	return p.calls, histories
}

func recoveryAppOptions(dir string, provider llm.Provider) Options {
	return Options{
		Config: config.Config{
			ProviderID:    "openai",
			APIKey:        "x",
			Model:         "m",
			WorkDir:       dir,
			AgentStateDir: filepath.Join(dir, ".juex"),
		},
		Provider:                provider,
		WorkDir:                 dir,
		DisableMCP:              true,
		disableObservables:      true,
		disableSideSessionTools: true,
	}
}

func TestAppStartupReplaysDurablePendingInputWithoutNewUserTurn(t *testing.T) {
	dir := t.TempDir()
	firstProvider := &recoveryProvider{}
	first, err := New(recoveryAppOptions(dir, firstProvider))
	if err != nil {
		t.Fatal(err)
	}
	record, err := first.Engine.PersistPendingMessageWithOptions(
		context.Background(),
		llm.TextMessage(llm.RoleUser, "resume me after restart"),
		runtime.PendingInputOptions{ID: "restart-input", TTL: time.Hour},
	)
	if err != nil {
		t.Fatal(err)
	}
	later, err := first.Engine.PersistPendingMessageWithOptions(
		context.Background(),
		llm.TextMessage(llm.RoleUser, "drain me after the oldest record"),
		runtime.PendingInputOptions{ID: "restart-input-later", TTL: time.Hour},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.CloseAndWait(); err != nil {
		t.Fatal(err)
	}

	provider := &recoveryProvider{called: make(chan struct{})}
	restarted, err := New(recoveryAppOptions(dir, provider))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.CloseAndWait() })
	select {
	case <-provider.called:
	case <-time.After(2 * time.Second):
		t.Fatal("startup recovery did not call provider")
	}
	if err := restarted.waitPendingInputRecovery(); err != nil {
		t.Fatal(err)
	}
	calls, histories := provider.snapshot()
	if calls != 1 {
		t.Fatalf("provider calls = %d, want 1", calls)
	}
	if len(histories) != 1 || len(histories[0]) < 2 || histories[0][0].ID != record.MessageID || histories[0][1].ID != later.MessageID {
		t.Fatalf("provider histories = %+v, want recovered messages %q then %q once", histories, record.MessageID, later.MessageID)
	}
	recovered, ok, err := restarted.Engine.PersistedPendingMessage(record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || recovered.State != runtime.PendingInputStateProcessed {
		t.Fatalf("recovered pending record = %+v ok=%v", recovered, ok)
	}
	recoveredLater, ok, err := restarted.Engine.PersistedPendingMessage(later.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || recoveredLater.State != runtime.PendingInputStateProcessed {
		t.Fatalf("later pending record = %+v ok=%v", recoveredLater, ok)
	}
}

func TestAppRunWaitsForStartupPendingInputRecovery(t *testing.T) {
	dir := t.TempDir()
	first, err := New(recoveryAppOptions(dir, &recoveryProvider{}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Engine.PersistPendingMessageWithOptions(
		context.Background(),
		llm.TextMessage(llm.RoleUser, "recover before synchronous input"),
		runtime.PendingInputOptions{ID: "restart-before-run", TTL: time.Hour},
	); err != nil {
		t.Fatal(err)
	}
	if err := first.CloseAndWait(); err != nil {
		t.Fatal(err)
	}

	provider := newBlockingAppProvider()
	restarted, err := New(recoveryAppOptions(dir, provider))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.CloseAndWait() })
	select {
	case <-provider.started:
	case <-time.After(2 * time.Second):
		t.Fatal("startup recovery did not reach provider")
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := restarted.Run(canceled, "canceled while recovery runs"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run with canceled context error = %v, want context.Canceled", err)
	}

	type runResult struct {
		out string
		err error
	}
	done := make(chan runResult, 1)
	go func() {
		out, err := restarted.Run(context.Background(), "run after recovery")
		done <- runResult{out: out, err: err}
	}()
	select {
	case result := <-done:
		t.Fatalf("synchronous Run completed during startup recovery: %+v", result)
	case <-time.After(100 * time.Millisecond):
	}
	close(provider.release)
	select {
	case result := <-done:
		if result.err != nil || result.out != "handled queued event" {
			t.Fatalf("Run after startup recovery = %q, %v", result.out, result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("synchronous Run did not continue after startup recovery")
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.calls != 2 {
		t.Fatalf("provider calls = %d, want recovery then synchronous Run", provider.calls)
	}
	if got := provider.histories[1][len(provider.histories[1])-1].FirstText(); got != "run after recovery" {
		t.Fatalf("second provider history ended with %q, want synchronous input", got)
	}
}

func TestAppExternalDeliveryWaitsForStartupPendingInputRecovery(t *testing.T) {
	dir := t.TempDir()
	first, err := New(recoveryAppOptions(dir, &recoveryProvider{}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Engine.PersistPendingMessageWithOptions(
		context.Background(),
		llm.TextMessage(llm.RoleUser, "recover before external delivery"),
		runtime.PendingInputOptions{ID: "restart-before-observation", TTL: time.Hour},
	); err != nil {
		t.Fatal(err)
	}
	if err := first.CloseAndWait(); err != nil {
		t.Fatal(err)
	}

	provider := newBlockingAppProvider()
	restarted, err := New(recoveryAppOptions(dir, provider))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.CloseAndWait() })
	select {
	case <-provider.started:
	case <-time.After(2 * time.Second):
		t.Fatal("startup recovery did not reach provider")
	}

	type deliveryResult struct {
		outcome observable.DeliveryOutcome
		err     error
	}
	done := make(chan deliveryResult, 1)
	go func() {
		outcome, err := restarted.DeliverObservation(context.Background(), testObservationRecord("obs-during-recovery"))
		done <- deliveryResult{outcome: outcome, err: err}
	}()
	select {
	case result := <-done:
		t.Fatalf("external delivery completed during startup recovery: %+v", result)
	case <-time.After(100 * time.Millisecond):
	}
	close(provider.release)
	select {
	case result := <-done:
		if result.err != nil || result.outcome.State != observable.ObservationStateDelivered {
			t.Fatalf("external delivery after startup recovery = %+v, %v", result.outcome, result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("external delivery did not continue after startup recovery")
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.calls != 2 {
		t.Fatalf("provider calls = %d, want recovery then external delivery", provider.calls)
	}
}

func TestAppBeginCloseCancelsTurnWaitingForStartupPendingInputRecovery(t *testing.T) {
	dir := t.TempDir()
	first, err := New(recoveryAppOptions(dir, &recoveryProvider{}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Engine.PersistPendingMessageWithOptions(
		context.Background(),
		llm.TextMessage(llm.RoleUser, "recover before closing"),
		runtime.PendingInputOptions{ID: "restart-before-close", TTL: time.Hour},
	); err != nil {
		t.Fatal(err)
	}
	if err := first.CloseAndWait(); err != nil {
		t.Fatal(err)
	}

	provider := newBlockingAppProvider()
	restarted, err := New(recoveryAppOptions(dir, provider))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-provider.started:
	case <-time.After(2 * time.Second):
		t.Fatal("startup recovery did not reach provider")
	}

	done := make(chan error, 1)
	go func() {
		_, err := restarted.Run(context.Background(), "must not run during close")
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("Run completed during startup recovery: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := restarted.BeginClose(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run after BeginClose error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop when the App began closing")
	}
	if err := restarted.CloseAndWait(); err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.calls != 1 {
		t.Fatalf("provider calls = %d, want startup recovery only", provider.calls)
	}
}

func TestAppCanceledAdmittedTurnReleasesEngineReservation(t *testing.T) {
	a, provider := newStubApp(t, llm.Response{
		Message:    llm.TextMessage(llm.RoleAssistant, "handled after cancellation"),
		StopReason: llm.StopEndTurn,
	})
	first := a.AdmitTurn(context.Background(), TurnAdmissionRequest{Prompt: "accepted before cancellation"})
	if first.Kind != TurnAdmissionStarted || first.Start == nil {
		t.Fatalf("first admission = %+v, want started", first)
	}
	wantCause := errors.New("admitted turn stopped by owner")
	var terminal runtime.TurnErroredPayload
	unsubscribe := a.Bus.Subscribe("turn.errored", func(event events.Event) {
		if event.TurnID == first.Start.TurnID {
			terminal, _ = event.Payload.(runtime.TurnErroredPayload)
		}
	})
	defer unsubscribe()
	canceled, cancel := context.WithCancelCause(context.Background())
	cancel(wantCause)
	if _, err := a.RunAdmittedTurn(canceled, first.Start.TurnID, first.Start.Message); !errors.Is(err, wantCause) {
		t.Fatalf("RunAdmittedTurn error = %v, want %v", err, wantCause)
	}
	if terminal.Error != wantCause.Error() {
		t.Fatalf("turn error = %+v, want preserved cancellation cause", terminal)
	}

	second := a.AdmitTurn(context.Background(), TurnAdmissionRequest{Prompt: "run after canceled admission"})
	if second.Kind != TurnAdmissionStarted || second.Start == nil {
		t.Fatalf("second admission = %+v, want started instead of queued behind a phantom turn", second)
	}
	out, err := a.RunAdmittedTurn(context.Background(), second.Start.TurnID, second.Start.Message)
	if err != nil || out != "handled after cancellation" {
		t.Fatalf("second RunAdmittedTurn = %q, %v", out, err)
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls = %d, want one post-cancellation turn", provider.calls)
	}
}

func TestAppExternalDeliveryTransfersToAppAfterRecoveryWaitTimeout(t *testing.T) {
	dir := t.TempDir()
	first, err := New(recoveryAppOptions(dir, &recoveryProvider{}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Engine.PersistPendingMessageWithOptions(
		context.Background(),
		llm.TextMessage(llm.RoleUser, "recover before timed delivery"),
		runtime.PendingInputOptions{ID: "restart-before-timed-observation", TTL: time.Hour},
	); err != nil {
		t.Fatal(err)
	}
	if err := first.CloseAndWait(); err != nil {
		t.Fatal(err)
	}

	provider := newBlockingAppProvider()
	restarted, err := New(recoveryAppOptions(dir, provider))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.CloseAndWait() })
	select {
	case <-provider.started:
	case <-time.After(2 * time.Second):
		t.Fatal("startup recovery did not reach provider")
	}

	record := testObservationRecord("obs-timeout-during-recovery")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	outcome, err := restarted.DeliverObservation(ctx, record)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("DeliverObservation error = %v, want context.DeadlineExceeded", err)
	}
	if outcome.State != observable.ObservationStateQueued || outcome.PendingInputID != observationPendingInputID(record) {
		t.Fatalf("delivery outcome = %+v, want queued durable input", outcome)
	}

	close(provider.release)
	deadline := time.Now().Add(2 * time.Second)
	for {
		provider.mu.Lock()
		calls := provider.calls
		provider.mu.Unlock()
		if calls == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("provider calls = %d, want App-owned handoff after recovery", calls)
		}
		time.Sleep(10 * time.Millisecond)
	}
	pending, ok, stateErr := restarted.Engine.PersistedPendingMessage(outcome.PendingInputID)
	if stateErr != nil {
		t.Fatal(stateErr)
	}
	if !ok || pending.State != runtime.PendingInputStateProcessed || !restarted.Session.HasMessageID(pending.MessageID) {
		t.Fatalf("pending after App-owned handoff = %+v ok=%v", pending, ok)
	}
}

func TestAppExternalDeliveryRetriesDurableInputAfterLiveQueueFull(t *testing.T) {
	dir := t.TempDir()
	provider := newBlockingAppProvider()
	a, err := New(recoveryAppOptions(dir, provider))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.CloseAndWait() })
	a.Engine.MaxPendingInputs = 1

	turnDone := make(chan error, 1)
	go func() {
		_, err := a.Run(context.Background(), "active turn")
		turnDone <- err
	}()
	select {
	case <-provider.started:
	case <-time.After(2 * time.Second):
		t.Fatal("active turn did not reach provider")
	}

	first := testObservationRecord("obs-fills-live-queue")
	firstOutcome, err := a.DeliverObservation(context.Background(), first)
	if err != nil || firstOutcome.State != observable.ObservationStateQueued {
		t.Fatalf("first delivery = %+v, %v, want queued", firstOutcome, err)
	}
	second := testObservationRecord("obs-overflows-live-queue")
	secondOutcome, err := a.DeliverObservation(context.Background(), second)
	if !errors.Is(err, runtime.ErrPendingInputQueueFull) {
		t.Fatalf("second delivery error = %v, want ErrPendingInputQueueFull", err)
	}
	if secondOutcome.State != observable.ObservationStateQueued || secondOutcome.PendingInputID != observationPendingInputID(second) {
		t.Fatalf("second delivery = %+v, want durable queued outcome", secondOutcome)
	}

	close(provider.release)
	select {
	case err := <-turnDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("active turn did not finish after provider release")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		pending, ok, stateErr := a.Engine.PersistedPendingMessage(secondOutcome.PendingInputID)
		if stateErr != nil {
			t.Fatal(stateErr)
		}
		if ok && pending.State == runtime.PendingInputStateProcessed && a.Session.HasMessageID(pending.MessageID) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("overflow input = %+v ok=%v, want App-owned retry to process it", pending, ok)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAppExternalDeliveryRetriesReplayableAdmissionCommitFailure(t *testing.T) {
	dir := t.TempDir()
	provider := &recoveryProvider{}
	a, err := New(recoveryAppOptions(dir, provider))
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

	record := testObservationRecord("obs-admission-commit-retry")
	outcome, err := a.DeliverObservation(context.Background(), record)
	if !errors.Is(err, wantErr) {
		t.Fatalf("DeliverObservation error = %v, want injected commit failure", err)
	}
	if outcome.State != observable.ObservationStateQueued || outcome.PendingInputID != observationPendingInputID(record) {
		t.Fatalf("delivery outcome = %+v, want replayable queued record", outcome)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		pending, ok, stateErr := a.Engine.PersistedPendingMessage(outcome.PendingInputID)
		if stateErr != nil {
			t.Fatal(stateErr)
		}
		calls, _ := provider.snapshot()
		if ok && pending.State == runtime.PendingInputStateProcessed && a.Session.HasMessageID(pending.MessageID) && calls == 1 {
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
	a, err := New(recoveryAppOptions(dir, &recoveryProvider{}))
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
	a, err := New(recoveryAppOptions(dir, &recoveryProvider{}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.CloseAndWait() })
	record, err := a.Engine.PersistPendingMessageWithOptions(
		context.Background(),
		llm.TextMessage(llm.RoleUser, "wait for new-session greeting"),
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
	a, err := New(recoveryAppOptions(dir, provider))
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

	deadline := time.Now().Add(2 * time.Second)
	for {
		pending, ok, stateErr := a.Engine.PersistedPendingMessage(record.ID)
		if stateErr != nil {
			t.Fatal(stateErr)
		}
		calls, _ := provider.snapshot()
		if ok && pending.State == runtime.PendingInputStateProcessed && a.Session.HasMessageID(pending.MessageID) && calls == 1 {
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
	a, err := New(recoveryAppOptions(dir, provider))
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
		llm.TextMessage(llm.RoleUser, "retry startup admission before session switch"),
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
	case <-time.After(2 * time.Second):
		t.Fatal("startup recovery did not attempt admission")
	}
	select {
	case <-recoveryDone:
		t.Fatal("startup recovery barrier closed while replayable admission retry was still pending")
	case <-time.After(100 * time.Millisecond):
	}

	switchDone := make(chan error, 1)
	go func() { switchDone <- a.SwitchToNewPrimarySession() }()
	select {
	case err := <-switchDone:
		t.Fatalf("session switch completed while startup admission retry was pending: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	released = true
	select {
	case <-recoveryDone:
	case <-time.After(2 * time.Second):
		t.Fatal("startup recovery did not finish after admission recovered")
	}
	select {
	case err := <-switchDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("session switch did not continue after startup recovery")
	}
	if calls, _ := provider.snapshot(); calls != 1 {
		t.Fatalf("provider calls = %d, want recovered input processed once before session switch", calls)
	}
}

func TestAppExternalDeliveryHandoffKeepsOriginSessionThroughRetry(t *testing.T) {
	dir := t.TempDir()
	provider := &recoveryProvider{}
	a, err := New(recoveryAppOptions(dir, provider))
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

	outcome, err := a.DeliverObservation(context.Background(), testObservationRecord("obs-session-bound-handoff"))
	if !errors.Is(err, wantErr) {
		t.Fatalf("DeliverObservation error = %v, want injected admission failure", err)
	}
	if outcome.State != observable.ObservationStateQueued {
		t.Fatalf("DeliverObservation outcome = %+v, want queued", outcome)
	}
	select {
	case <-retrying:
	case <-time.After(2 * time.Second):
		t.Fatal("App-owned handoff did not retry admission")
	}

	switchDone := make(chan error, 1)
	go func() { switchDone <- a.SwitchToNewPrimarySession() }()
	select {
	case err := <-switchDone:
		t.Fatalf("session switch completed while origin-session handoff was retrying: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	released = true
	select {
	case err := <-switchDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("session switch did not continue after handoff became safe")
	}
	if calls, _ := provider.snapshot(); calls != 1 {
		t.Fatalf("provider calls = %d, want origin-session handoff processed once", calls)
	}
}

func TestAppPendingRecoveryBarrierPrecedesNotificationActivation(t *testing.T) {
	dir := t.TempDir()
	provider := newBlockingAppProvider()
	a, err := New(recoveryAppOptions(dir, provider))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.CloseAndWait() })
	record, err := a.Engine.PersistPendingMessageWithOptions(
		context.Background(),
		llm.TextMessage(llm.RoleUser, "oldest durable input"),
		runtime.PendingInputOptions{ID: "oldest-before-notification", TTL: time.Hour},
	)
	if err != nil {
		t.Fatal(err)
	}
	deliveryErr := make(chan error, 1)
	gate := newMCPNotificationGate(func(notification mcp.Notification) {
		deliveryErr <- a.HandleMCPNotification(a.ctx, notification)
	})
	gate.Enqueue(mcp.Notification{
		ServerName: "startup",
		EventType:  "message",
		Content:    "newer startup notification",
		Params:     map[string]any{"content": "newer startup notification"},
	})
	activated := make(chan struct{})
	go func() {
		a.activateExternalInputAfterPendingRecovery(gate, []runtime.PendingInputRecovery{{RecordID: record.ID}}, nil)
		close(activated)
	}()

	select {
	case <-provider.started:
	case <-time.After(2 * time.Second):
		t.Fatal("startup recovery did not reach provider")
	}
	provider.mu.Lock()
	firstHistory := append([]llm.Message(nil), provider.histories[0]...)
	provider.mu.Unlock()
	oldestIndex := -1
	notificationIndex := -1
	for i, message := range firstHistory {
		text := message.FirstText()
		if text == "oldest durable input" {
			oldestIndex = i
		}
		if strings.Contains(text, "newer startup notification") {
			notificationIndex = i
		}
	}
	if oldestIndex < 0 || (notificationIndex >= 0 && notificationIndex < oldestIndex) {
		t.Fatalf("first provider history = %+v, want oldest durable input before notification", firstHistory)
	}
	select {
	case <-activated:
		t.Fatal("notification activation completed while startup recovery was blocked")
	case <-time.After(100 * time.Millisecond):
	}

	close(provider.release)
	select {
	case <-activated:
	case <-time.After(2 * time.Second):
		t.Fatal("notification activation did not finish after startup recovery")
	}
	select {
	case err := <-deliveryErr:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("startup notification was not delivered")
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.calls < 1 || provider.calls > 2 {
		t.Fatalf("provider calls = %d, want one ordered recovery Turn or recovery then notification", provider.calls)
	}
	if notificationIndex < 0 {
		if provider.calls != 2 {
			t.Fatalf("provider histories = %+v, want startup notification", provider.histories)
		}
		got := provider.histories[1][len(provider.histories[1])-1].FirstText()
		if !strings.Contains(got, "newer startup notification") {
			t.Fatalf("second provider input = %q, want startup notification", got)
		}
	}
}

func TestAppPendingRecoveryBarrierPrecedesObservableActivation(t *testing.T) {
	dir := t.TempDir()
	provider := newBlockingAppProvider()
	a, err := New(recoveryAppOptions(dir, provider))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.CloseAndWait() })
	record, err := a.Engine.PersistPendingMessageWithOptions(
		context.Background(),
		llm.TextMessage(llm.RoleUser, "oldest before observable startup"),
		runtime.PendingInputOptions{ID: "oldest-before-observable", TTL: time.Hour},
	)
	if err != nil {
		t.Fatal(err)
	}
	type observableResult struct {
		outcome observable.DeliveryOutcome
		err     error
	}
	delivered := make(chan observableResult, 1)
	observation := testObservationRecord("obs-during-observable-startup")
	activated := make(chan struct{})
	go func() {
		a.activateExternalInputAfterPendingRecovery(nil, []runtime.PendingInputRecovery{{RecordID: record.ID}}, func() {
			outcome, err := a.DeliverObservation(context.Background(), observation)
			delivered <- observableResult{outcome: outcome, err: err}
		})
		close(activated)
	}()

	select {
	case <-provider.started:
	case <-time.After(2 * time.Second):
		t.Fatal("startup recovery did not reach provider")
	}
	provider.mu.Lock()
	firstHistory := append([]llm.Message(nil), provider.histories[0]...)
	provider.mu.Unlock()
	oldestIndex := -1
	observationIndex := -1
	for i, message := range firstHistory {
		text := message.FirstText()
		if text == "oldest before observable startup" {
			oldestIndex = i
		}
		if strings.Contains(text, observation.ID) {
			observationIndex = i
		}
	}
	if oldestIndex < 0 || (observationIndex >= 0 && observationIndex < oldestIndex) {
		t.Fatalf("first provider history = %+v, want oldest durable input before observation", firstHistory)
	}
	select {
	case <-activated:
		t.Fatal("Observable activation completed while startup recovery was blocked")
	case <-time.After(100 * time.Millisecond):
	}

	close(provider.release)
	select {
	case <-activated:
	case <-time.After(2 * time.Second):
		t.Fatal("Observable activation did not finish after startup recovery")
	}
	select {
	case result := <-delivered:
		if result.err != nil || result.outcome.State != observable.ObservationStateDelivered {
			t.Fatalf("Observable startup delivery = %+v, %v", result.outcome, result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("startup observation was not delivered")
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.calls < 1 || provider.calls > 2 {
		t.Fatalf("provider calls = %d, want one ordered recovery Turn or recovery then observation", provider.calls)
	}
	if observationIndex < 0 {
		if provider.calls != 2 {
			t.Fatalf("provider histories = %+v, want startup observation", provider.histories)
		}
		got := provider.histories[1][len(provider.histories[1])-1].FirstText()
		if !strings.Contains(got, observation.ID) {
			t.Fatalf("second provider input = %q, want startup observation", got)
		}
	}
}

func TestAppCompactWaitsForStartupPendingInputRecovery(t *testing.T) {
	a, _ := newStubApp(t)
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
	case <-time.After(2 * time.Second):
		t.Fatal("compaction did not continue after startup recovery")
	}
	select {
	case <-admitted:
	case <-time.After(2 * time.Second):
		t.Fatal("compaction admission was not committed after startup recovery")
	}
}

func TestAppBeginCompactAdmissionWaitsForStartupPendingInputRecovery(t *testing.T) {
	a, _ := newStubApp(t)
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
	case <-time.After(2 * time.Second):
		t.Fatal("admitted compaction did not continue after startup recovery")
	}
	if compactTurnID == "" {
		t.Fatal("compaction has no Framework turn id")
	}
	if _, err := a.FinishCompactAdmission(compactTurnID); err != nil {
		t.Fatal(err)
	}
}

func TestAppDeliverObservationReportsTranscriptConsumptionDespiteTurnError(t *testing.T) {
	dir := t.TempDir()
	wantErr := errors.New("provider unavailable")
	provider := &recoveryProvider{err: wantErr}
	a, err := New(recoveryAppOptions(dir, provider))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.CloseAndWait() })
	record := testObservationRecord("obs-provider-failure")
	outcome, err := a.DeliverObservation(context.Background(), record)
	if !errors.Is(err, wantErr) {
		t.Fatalf("DeliverObservation error = %v, want %v", err, wantErr)
	}
	if outcome.State != observable.ObservationStateDelivered || outcome.PendingInputID != observationPendingInputID(record) {
		t.Fatalf("delivery outcome = %+v, want delivered with stable pending id", outcome)
	}
	pending, ok, stateErr := a.Engine.PersistedPendingMessage(outcome.PendingInputID)
	if stateErr != nil {
		t.Fatal(stateErr)
	}
	if !ok || pending.State != runtime.PendingInputStateProcessed || !a.Session.HasMessageID(pending.MessageID) {
		t.Fatalf("pending after failed turn = %+v ok=%v", pending, ok)
	}
	duplicate, duplicateErr := a.DeliverObservation(context.Background(), record)
	if duplicateErr != nil || duplicate.State != observable.ObservationStateDelivered {
		t.Fatalf("duplicate delivery = %+v error=%v, want idempotent delivered", duplicate, duplicateErr)
	}
	if calls, _ := provider.snapshot(); calls != 1 {
		t.Fatalf("provider calls after duplicate = %d, want 1", calls)
	}
}

func TestAppCloseCancelsAndWaitsForStartupPendingInputRecovery(t *testing.T) {
	dir := t.TempDir()
	first, err := New(recoveryAppOptions(dir, &recoveryProvider{}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Engine.PersistPendingMessageWithOptions(
		context.Background(),
		llm.TextMessage(llm.RoleUser, "cancel recovery during shutdown"),
		runtime.PendingInputOptions{ID: "shutdown-recovery", TTL: time.Hour},
	); err != nil {
		t.Fatal(err)
	}
	if err := first.CloseAndWait(); err != nil {
		t.Fatal(err)
	}

	provider := &cancelAwareRecoveryProvider{called: make(chan struct{})}
	restarted, err := New(recoveryAppOptions(dir, provider))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-provider.called:
	case <-time.After(2 * time.Second):
		t.Fatal("startup recovery provider did not start")
	}
	done := make(chan error, 1)
	go func() { done <- restarted.CloseAndWait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CloseAndWait did not cancel and drain startup recovery")
	}
}

func TestAppStartupDoesNotReplayExpiredOrExplicitlyDroppedInput(t *testing.T) {
	dir := t.TempDir()
	first, err := New(recoveryAppOptions(dir, &recoveryProvider{}))
	if err != nil {
		t.Fatal(err)
	}
	expired, err := first.Engine.PersistPendingMessageWithOptions(
		context.Background(),
		llm.TextMessage(llm.RoleUser, "expired input"),
		runtime.PendingInputOptions{ID: "expired-recovery", TTL: time.Millisecond},
	)
	if err != nil {
		t.Fatal(err)
	}
	dropped, err := first.Engine.PersistPendingMessageWithOptions(
		context.Background(),
		llm.TextMessage(llm.RoleUser, "explicitly dropped input"),
		runtime.PendingInputOptions{ID: "dropped-recovery", TTL: time.Hour},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Engine.DropPersistedPendingMessage(dropped.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if err := first.CloseAndWait(); err != nil {
		t.Fatal(err)
	}

	provider := &recoveryProvider{called: make(chan struct{})}
	restarted, err := New(recoveryAppOptions(dir, provider))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.CloseAndWait() })
	select {
	case <-provider.called:
		t.Fatal("startup replayed expired or explicitly dropped input")
	case <-time.After(50 * time.Millisecond):
	}
	if calls, _ := provider.snapshot(); calls != 0 {
		t.Fatalf("provider calls = %d, want 0", calls)
	}
	for _, want := range []struct {
		id    string
		state runtime.PendingInputState
	}{
		{id: expired.ID, state: runtime.PendingInputStateExpired},
		{id: dropped.ID, state: runtime.PendingInputStateDropped},
	} {
		current, ok, err := restarted.Engine.PersistedPendingMessage(want.id)
		if err != nil {
			t.Fatal(err)
		}
		if !ok || current.State != want.state {
			t.Fatalf("record %q = %+v ok=%v, want state %q", want.id, current, ok, want.state)
		}
	}
}

func TestAppStartupRecoveryAdvancesPastOldestRecordThatExpiresBeforeWorker(t *testing.T) {
	dir := t.TempDir()
	provider := &recoveryProvider{}
	a, err := New(recoveryAppOptions(dir, provider))
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

	a.activateExternalInputAfterPendingRecovery(nil, []runtime.PendingInputRecovery{{RecordID: oldest.ID}, {RecordID: later.ID}}, nil)
	if err := a.waitPendingInputRecovery(); err != nil {
		t.Fatal(err)
	}
	calls, histories := provider.snapshot()
	if calls != 1 || len(histories) != 1 {
		t.Fatalf("provider calls = %d histories=%+v, want later record recovered once", calls, histories)
	}
	last := histories[0][len(histories[0])-1]
	if last.ID != later.MessageID {
		t.Fatalf("recovered message = %q, want later message %q", last.ID, later.MessageID)
	}
	for _, want := range []struct {
		id    string
		state runtime.PendingInputState
	}{
		{id: oldest.ID, state: runtime.PendingInputStateExpired},
		{id: later.ID, state: runtime.PendingInputStateProcessed},
	} {
		record, ok, err := a.Engine.PersistedPendingMessage(want.id)
		if err != nil {
			t.Fatal(err)
		}
		if !ok || record.State != want.state {
			t.Fatalf("record %q = %+v ok=%v, want %q", want.id, record, ok, want.state)
		}
	}
}
