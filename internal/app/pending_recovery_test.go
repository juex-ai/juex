package app

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/llm"
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
