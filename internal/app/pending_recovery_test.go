package app

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/app/modulecatalog"
	"github.com/juex-ai/juex/internal/features/mcp"
	observable "github.com/juex-ai/juex/internal/features/observables"
	"github.com/juex-ai/juex/internal/foundation/events"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/framework/agent"
	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
	"github.com/juex-ai/juex/internal/framework/runtime"
)

const pendingRecoveryTestTimeout = 10 * time.Second

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
		Config: config.Config{ModuleInventory: modulecatalog.Inventory(),
			ProviderID:    "openai",
			APIKey:        "x",
			Model:         "m",
			WorkDir:       dir,
			AgentStateDir: filepath.Join(dir, ".juex"),
		},
		Provider:           provider,
		WorkDir:            dir,
		DisableMCP:         true,
		disableObservables: true,
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
	case <-time.After(pendingRecoveryTestTimeout):
		t.Fatal("startup recovery did not call provider")
	}
	waitRecoveredInput(t, restarted, record.ID)
	calls, histories := provider.snapshot()
	if calls != 1 {
		t.Fatalf("provider calls = %d, want 1", calls)
	}
	if len(histories) != 1 || len(histories[0]) < 2 || histories[0][0].ID != record.MessageID || histories[0][1].ID != later.MessageID {
		t.Fatalf("provider histories = %+v, want recovered messages %q then %q once", histories, record.MessageID, later.MessageID)
	}
	_, ok, err := restarted.Engine.PersistedPendingMessage(record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatalf("completed pending record %q was retained", record.ID)
	}
	_, ok, err = restarted.Engine.PersistedPendingMessage(later.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatalf("completed pending record %q was retained", later.ID)
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
	t.Cleanup(func() {
		provider.Release()
		_ = restarted.CloseAndWait()
	})
	select {
	case <-provider.started:
	case <-time.After(pendingRecoveryTestTimeout):
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
	provider.Release()
	select {
	case result := <-done:
		if result.err != nil || result.out != "handled queued event" {
			t.Fatalf("Run after startup recovery = %q, %v", result.out, result.err)
		}
	case <-time.After(10 * time.Second):
		provider.mu.Lock()
		calls := provider.calls
		provider.mu.Unlock()
		t.Fatalf("synchronous Run did not continue after startup recovery: calls=%d status=%+v pending=%+v", calls, restarted.Status.Snapshot(), restarted.PendingInputStatus())
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.calls != 2 {
		t.Fatalf("provider calls = %d, want recovery then synchronous Run", provider.calls)
	}
	if !providerHistoryContains(provider.histories[1], "", "run after recovery") {
		t.Fatalf("second provider history missing synchronous input: %+v", provider.histories[1])
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
	t.Cleanup(func() {
		provider.Release()
		_ = restarted.CloseAndWait()
	})
	select {
	case <-provider.started:
	case <-time.After(pendingRecoveryTestTimeout):
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
	provider.Release()
	select {
	case result := <-done:
		if result.err != nil || result.outcome.State != observable.ObservationStateDelivered {
			t.Fatalf("external delivery after startup recovery = %+v, %v", result.outcome, result.err)
		}
	case <-time.After(5 * time.Second):
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
	t.Cleanup(func() {
		provider.Release()
		_ = restarted.CloseAndWait()
	})
	select {
	case <-provider.started:
	case <-time.After(pendingRecoveryTestTimeout):
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
	case <-time.After(pendingRecoveryTestTimeout):
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
	first := a.AdmitTurn(context.Background(), agent.TurnAdmissionRequest{Prompt: "accepted before cancellation"})
	if first.Kind != agent.TurnAdmissionStarted || first.Start == nil {
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

	second := a.AdmitTurn(context.Background(), agent.TurnAdmissionRequest{Prompt: "run after canceled admission"})
	if second.Kind != agent.TurnAdmissionStarted || second.Start == nil {
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
	t.Cleanup(func() {
		provider.Release()
		_ = restarted.CloseAndWait()
	})
	select {
	case <-provider.started:
	case <-time.After(pendingRecoveryTestTimeout):
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

	provider.Release()
	deadline := time.Now().Add(pendingRecoveryTestTimeout)
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
	if !ok || pending.State != runtime.PendingInputStateProcessed || !restarted.Thread.HasMessageID(pending.MessageID) {
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
	t.Cleanup(func() {
		provider.Release()
		_ = a.CloseAndWait()
	})
	a.Engine.MaxPendingInputs = 1

	turnDone := make(chan error, 1)
	go func() {
		_, err := a.Run(context.Background(), "active turn")
		turnDone <- err
	}()
	select {
	case <-provider.started:
	case <-time.After(pendingRecoveryTestTimeout):
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

	provider.Release()
	select {
	case err := <-turnDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		provider.mu.Lock()
		calls := provider.calls
		provider.mu.Unlock()
		t.Fatalf("active turn did not finish after provider release; provider calls=%d status=%+v pending=%+v", calls, a.Status.Snapshot(), a.PendingInputStatus())
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, ok, stateErr := a.Engine.PersistedPendingMessage(secondOutcome.PendingInputID)
		if stateErr != nil {
			t.Fatal(stateErr)
		}
		provider.mu.Lock()
		histories := append([][]llm.Message(nil), provider.histories...)
		provider.mu.Unlock()
		processed := false
		for _, history := range histories {
			for _, message := range history {
				if strings.Contains(message.FirstText(), second.ID) {
					processed = true
					break
				}
			}
		}
		if !ok && processed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("overflow input retained=%v histories=%+v, want App-owned retry to process and remove it", ok, histories)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAppPendingRecoveryBarrierPrecedesNotificationActivation(t *testing.T) {
	dir := t.TempDir()
	provider := newBlockingAppProvider()
	a, err := New(recoveryAppOptions(dir, provider))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		provider.Release()
		_ = a.CloseAndWait()
	})
	_, err = a.Engine.PersistPendingMessageWithOptions(
		context.Background(),
		llm.TextMessage(llm.RoleUser, "oldest durable input"),
		runtime.PendingInputOptions{ID: "oldest-before-notification", TTL: time.Hour},
	)
	if err != nil {
		t.Fatal(err)
	}
	deliveryErr := make(chan error, 1)
	notification := mcp.Notification{
		ServerName: "startup", EventType: "message", Content: "newer startup notification",
		Params: map[string]any{"content": "newer startup notification"},
	}
	installRecoveryInputSource(t, a, func(context.Context) error {
		_, deliveryErrValue := a.DeliverObservation(a.Context(), a.ObservationFromMCPNotification(notification))
		deliveryErr <- deliveryErrValue
		return deliveryErrValue
	})
	activated := make(chan struct{})
	go func() {
		_ = a.RestoreAndActivate(context.Background())
		close(activated)
	}()

	select {
	case <-provider.started:
	case <-time.After(pendingRecoveryTestTimeout):
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

	provider.Release()
	select {
	case <-activated:
	case <-time.After(pendingRecoveryTestTimeout):
		t.Fatal("notification activation did not finish after startup recovery")
	}
	select {
	case err := <-deliveryErr:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(pendingRecoveryTestTimeout):
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
	t.Cleanup(func() {
		provider.Release()
		_ = a.CloseAndWait()
	})
	_, err = a.Engine.PersistPendingMessageWithOptions(
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
	installRecoveryInputSource(t, a, func(context.Context) error {
		outcome, err := a.DeliverObservation(context.Background(), observation)
		delivered <- observableResult{outcome: outcome, err: err}
		return err
	})
	activated := make(chan struct{})
	go func() {
		_ = a.RestoreAndActivate(context.Background())
		close(activated)
	}()

	select {
	case <-provider.started:
	case <-time.After(pendingRecoveryTestTimeout):
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

	provider.Release()
	select {
	case <-activated:
	case <-time.After(pendingRecoveryTestTimeout):
		t.Fatal("Observable activation did not finish after startup recovery")
	}
	select {
	case result := <-delivered:
		if result.err != nil || result.outcome.State != observable.ObservationStateDelivered {
			t.Fatalf("Observable startup delivery = %+v, %v", result.outcome, result.err)
		}
	case <-time.After(pendingRecoveryTestTimeout):
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
	if !ok || pending.State != runtime.PendingInputStateDeadLettered || !a.Thread.HasMessageID(pending.MessageID) {
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

func TestAppDeliverObservationTreatsLiveProcessedDuplicateAsDelivered(t *testing.T) {
	dir := t.TempDir()
	provider := newBlockingAppProvider()
	a, err := New(recoveryAppOptions(dir, provider))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		provider.Release()
		_ = a.CloseAndWait()
	})
	record := testObservationRecord("obs-live-processed-duplicate")
	type deliveryResult struct {
		outcome observable.DeliveryOutcome
		err     error
	}
	firstDone := make(chan deliveryResult, 1)
	go func() {
		outcome, deliveryErr := a.DeliverObservation(context.Background(), record)
		firstDone <- deliveryResult{outcome: outcome, err: deliveryErr}
	}()
	select {
	case <-provider.started:
	case <-time.After(pendingRecoveryTestTimeout):
		t.Fatal("first delivery did not reach provider")
	}

	recordID := observationPendingInputID(record)
	pending, ok, err := a.Engine.PersistedPendingMessage(recordID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || pending.State != runtime.PendingInputStateProcessed ||
		pending.TurnID == "" || pending.TurnID != a.PendingInputStatus().TurnID {
		t.Fatalf("live pending record = %+v ok=%v status=%+v", pending, ok, a.PendingInputStatus())
	}

	duplicate, duplicateErr := a.DeliverObservation(context.Background(), record)
	if duplicateErr != nil || duplicate.State != observable.ObservationStateDelivered ||
		duplicate.PendingInputID != recordID {
		t.Fatalf("duplicate delivery = %+v error=%v, want idempotent delivered", duplicate, duplicateErr)
	}
	provider.mu.Lock()
	callsBeforeRelease := provider.calls
	provider.mu.Unlock()
	if callsBeforeRelease != 1 {
		t.Fatalf("provider calls before release = %d, want 1", callsBeforeRelease)
	}

	provider.Release()
	select {
	case result := <-firstDone:
		if result.err != nil || result.outcome.State != observable.ObservationStateDelivered {
			t.Fatalf("first delivery = %+v error=%v", result.outcome, result.err)
		}
	case <-time.After(pendingRecoveryTestTimeout):
		t.Fatal("first delivery did not finish")
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.calls != 1 {
		t.Fatalf("provider calls after live duplicate = %d, want 1", provider.calls)
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
	case <-time.After(pendingRecoveryTestTimeout):
		t.Fatal("startup recovery provider did not start")
	}
	done := make(chan error, 1)
	go func() { done <- restarted.CloseAndWait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(pendingRecoveryTestTimeout):
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
	for _, id := range []string{expired.ID, dropped.ID} {
		_, ok, err := restarted.Engine.PersistedPendingMessage(id)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			t.Fatalf("expired or dropped record %q was retained", id)
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

// A new input owner participates by registering the lifecycle contract alone.
type recoveryInputSource struct{ activate func(context.Context) error }

func (*recoveryInputSource) ID() runtimemodule.ID { return "recovery-test-source" }

func (*recoveryInputSource) StartRuntime(context.Context, runtimemodule.RuntimeContext) error {
	return nil
}

func (*recoveryInputSource) QuiesceRuntime(context.Context) error { return nil }

func (*recoveryInputSource) CloseRuntime(context.Context) error { return nil }

func (s *recoveryInputSource) ActivateRuntime(ctx context.Context) error { return s.activate(ctx) }

func installRecoveryInputSource(t *testing.T, a *App, activate func(context.Context) error) {
	t.Helper()
	var original *runtimemodule.Set
	if err := a.ReadModuleSnapshot(func(snapshot agent.ModuleSnapshot) error { original = snapshot.Runtime; return nil }); err != nil {
		t.Fatal(err)
	}
	set, err := runtimemodule.BuildAndStartRuntimeSet(context.Background(), []runtimemodule.RuntimeFactorySpec{{
		ID: "recovery-test-source", Enabled: true,
		New: func(context.Context, runtimemodule.RuntimeContext) (runtimemodule.Module, error) {
			return &recoveryInputSource{activate: activate}, nil
		},
	}}, runtimemodule.RuntimeContext{}, runtimemodule.ToolContext{})
	if err != nil {
		t.Fatal(err)
	}
	a.BindRuntimeModules(set)
	t.Cleanup(func() { _ = original.CloseRuntime(context.Background()) })
}

func waitRecoveredInput(t *testing.T, a *App, id string) {
	t.Helper()
	deadline := time.Now().Add(pendingRecoveryTestTimeout)
	for {
		_, exists, err := a.Engine.PersistedPendingMessage(id)
		if err != nil {
			t.Fatal(err)
		}
		if !exists && a.Engine.PendingInputStatus().TurnID == "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pending input %s did not settle", id)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
