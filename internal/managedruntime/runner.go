package managedruntime

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/juex-ai/juex/internal/foundation/llm"
)

type ExecutionStore interface {
	RunnableAgents(context.Context, int) ([]string, error)
	Claim(context.Context, string, string, time.Duration) (Lease, error)
	Renew(context.Context, Lease, time.Duration) (Lease, error)
	Release(context.Context, Lease) error
	NextInputs(context.Context, Lease, []string, int) ([]PendingWork, error)
	BeginTurn(context.Context, Lease, Scope, string, TurnConfig) (Work, error)
	BeginAttempt(context.Context, Lease, string, ModelRequest) (Attempt, error)
	FinishAttempt(context.Context, Lease, string, llm.Response, string) error
	HoldInput(context.Context, Lease, string, string) error
	WorkActive(context.Context, Lease, string) (bool, error)
}

type RunnerConfig struct {
	Tools             ToolGateway
	Concurrency       int
	IdleTimeout       time.Duration
	PollInterval      time.Duration
	AuthorityInterval time.Duration
	AttemptTimeout    time.Duration
}

type Runner struct {
	tools     *toolRunner
	store     ExecutionStore
	authority Authority
	config    RunnerConfig
	holder    string
}

func NewRunner(store ExecutionStore, authority Authority, config RunnerConfig) (*Runner, error) {
	if store == nil || authority == nil {
		return nil, ErrInvalid
	}
	if config.Concurrency == 0 {
		config.Concurrency = 10
	}
	if config.Concurrency < 1 || config.Concurrency > 100 {
		return nil, ErrInvalid
	}
	if config.IdleTimeout == 0 {
		config.IdleTimeout = 5 * time.Minute
	}
	if config.PollInterval == 0 {
		config.PollInterval = 250 * time.Millisecond
	}
	if config.AuthorityInterval == 0 {
		config.AuthorityInterval = time.Second
	}
	if config.AttemptTimeout == 0 {
		config.AttemptTimeout = 10 * time.Minute
	}
	if config.IdleTimeout < 0 || config.PollInterval < 10*time.Millisecond || config.AuthorityInterval < 10*time.Millisecond || config.AttemptTimeout < time.Second {
		return nil, ErrInvalid
	}
	runner := &Runner{store: store, authority: authority, config: config, holder: rand.Text()}
	if config.Tools != nil {
		toolStore, ok := store.(ToolStore)
		if !ok {
			return nil, ErrInvalid
		}
		observations, ok := store.(ObservationStore)
		if !ok {
			return nil, ErrInvalid
		}
		runner.tools = &toolRunner{store: toolStore, observations: observations, gateway: config.Tools, authority: authority}
	}
	return runner, nil
}

type activation struct {
	lease             Lease
	ctx               context.Context
	cancel            context.CancelFunc
	renewed, lastUsed time.Time
	running           map[string]bool
	order             uint64
}
type finishedWork struct {
	agentID, threadID string
	err               error
}
type candidate struct {
	a       *activation
	pending PendingWork
}

// Run is a shared scheduler. Only active Thread work consumes a slot; dormant
// Agents and queued inputs have no process or model-call loop of their own.
func (r *Runner) Run(ctx context.Context) {
	if r.tools != nil {
		toolCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { defer close(done); r.tools.run(toolCtx) }()
		defer func() { cancel(); <-done }()
	}
	const ttl = 30 * time.Second
	activations := map[string]*activation{}
	ownerOrder := map[string]uint64{}
	var serial uint64
	finished := make(chan finishedWork, r.config.Concurrency)
	var workers sync.WaitGroup
	active := 0
	ticker := time.NewTicker(r.config.PollInterval)
	defer ticker.Stop()
	defer func() {
		for _, a := range activations {
			a.cancel()
		}
		workers.Wait()
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, a := range activations {
			_ = r.store.Release(cleanup, a.lease)
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case done := <-finished:
			active--
			if a := activations[done.agentID]; a != nil {
				delete(a.running, done.threadID)
				a.lastUsed = time.Now()
				if done.err != nil {
					a.cancel()
				}
			}
			if done.err != nil && ctx.Err() == nil && !errors.Is(done.err, ErrDenied) && !errors.Is(done.err, ErrFence) && !errors.Is(done.err, ErrConflict) {
				slog.Warn("Runtime work interrupted; durable state retained", "agent_id", done.agentID, "thread_id", done.threadID)
			}
		case <-ticker.C:
		}
		if ctx.Err() != nil {
			return
		}
		now := time.Now()
		for id, a := range activations {
			if len(a.running) == 0 && (a.ctx.Err() != nil || now.Sub(a.lastUsed) >= r.config.IdleTimeout) {
				a.cancel()
				_ = r.store.Release(ctx, a.lease)
				delete(activations, id)
				continue
			}
			if now.Sub(a.renewed) >= ttl/3 {
				lease, err := r.store.Renew(ctx, a.lease, ttl)
				if err != nil {
					a.cancel()
					continue
				}
				a.lease = lease
				a.renewed = now
			}
		}
		if active >= r.config.Concurrency {
			continue
		}
		ids, err := r.store.RunnableAgents(ctx, 100)
		if err != nil {
			continue
		}
		for _, id := range ids {
			if _, exists := activations[id]; exists {
				continue
			}
			if len(activations) >= 256 {
				var oldestID string
				var oldest *activation
				for otherID, a := range activations {
					if len(a.running) == 0 && (oldest == nil || a.lastUsed.Before(oldest.lastUsed)) {
						oldestID, oldest = otherID, a
					}
				}
				if oldest == nil {
					break
				}
				oldest.cancel()
				_ = r.store.Release(ctx, oldest.lease)
				delete(activations, oldestID)
			}
			lease, err := r.store.Claim(ctx, id, r.holder, ttl)
			if err != nil {
				continue
			}
			activationCtx, cancel := context.WithCancel(ctx)
			activations[id] = &activation{lease: lease, ctx: activationCtx, cancel: cancel, renewed: now, lastUsed: now, running: map[string]bool{}}
		}
		candidates := []candidate{}
		for _, a := range activations {
			if a.ctx.Err() != nil {
				continue
			}
			excluded := make([]string, 0, len(a.running))
			for id := range a.running {
				excluded = append(excluded, id)
			}
			items, err := r.store.NextInputs(ctx, a.lease, excluded, r.config.Concurrency-active)
			if err != nil {
				continue
			}
			for _, item := range items {
				candidates = append(candidates, candidate{a: a, pending: item})
			}
		}
		for active < r.config.Concurrency && len(candidates) > 0 {
			sort.SliceStable(candidates, func(i, j int) bool {
				a, b := candidates[i], candidates[j]
				oa := ownerOrder[a.pending.Scope.TenantID+":"+a.pending.Scope.UserID]
				ob := ownerOrder[b.pending.Scope.TenantID+":"+b.pending.Scope.UserID]
				if oa != ob {
					return oa < ob
				}
				if a.a.order != b.a.order {
					return a.a.order < b.a.order
				}
				return a.pending.InputID < b.pending.InputID
			})
			item := candidates[0]
			candidates = candidates[1:]
			a := item.a
			serial++
			a.order = serial
			ownerOrder[item.pending.Scope.TenantID+":"+item.pending.Scope.UserID] = serial
			a.running[item.pending.ThreadID] = true
			active++
			lease := a.lease
			workers.Go(func() {
				err := r.execute(a.ctx, lease, item.pending)
				finished <- finishedWork{agentID: lease.AgentID, threadID: item.pending.ThreadID, err: err}
			})
		}
	}
}

func (r *Runner) currentScope(ctx context.Context, original Scope) (Scope, error) {
	fresh, err := r.authority.Authorize(ctx, original.ActorID, original.TenantID, original.AgentID, true)
	if err != nil {
		return fresh, err
	}
	if !original.SameAuthority(fresh) {
		return fresh, ErrDenied
	}
	return fresh, nil
}

func (r *Runner) execute(ctx context.Context, lease Lease, pending PendingWork) error {
	scope, err := r.currentScope(ctx, pending.Scope)
	if errors.Is(err, ErrDenied) {
		return r.store.HoldInput(ctx, lease, pending.InputID, "authority_changed")
	}
	if err != nil {
		return err
	}
	var config TurnConfig
	if pending.State == "queued" {
		config, err = r.authority.Snapshot(ctx, scope)
		if errors.Is(err, ErrModelUnavailable) {
			return r.store.HoldInput(ctx, lease, pending.InputID, "model_unavailable")
		}
		if err != nil {
			return err
		}
	}
	work, err := r.store.BeginTurn(ctx, lease, scope, pending.InputID, config)
	if errors.Is(err, ErrDenied) {
		return r.store.HoldInput(ctx, lease, pending.InputID, "authority_changed")
	}
	if err != nil {
		return err
	}
	provider, err := r.authority.Provider(ctx, scope, work.Config)
	if errors.Is(err, ErrDenied) {
		return r.store.HoldInput(ctx, lease, pending.InputID, "authority_changed")
	}
	if errors.Is(err, ErrModelUnavailable) {
		return r.store.HoldInput(ctx, lease, pending.InputID, "model_unavailable")
	}
	if err != nil {
		return err
	}
	request := ModelRequest{System: work.Config.Instructions, Messages: work.History, Purpose: "conversation"}
	if work.Source.Kind == "observation" {
		request.Purpose = "observation"
	}
	if r.tools != nil {
		environments, err := r.tools.gateway.Environments(ctx, scope)
		if err != nil {
			return err
		}
		if work.Source.Kind == "observation" && !observationGrant(environments, work.Source.EnvironmentID, work.Source.AuthorizationVersion, work.Source.Capability) {
			return r.store.HoldInput(ctx, lease, pending.InputID, "authority_changed")
		}
		request.System += executionContext(environments)
		request.Tools = executionTools()
	}
	attempt, err := r.store.BeginAttempt(ctx, lease, work.TurnID, request)
	if errors.Is(err, ErrDenied) {
		return r.store.HoldInput(ctx, lease, pending.InputID, "authority_changed")
	}
	if err != nil {
		return err
	}
	callCtx, timeout := context.WithTimeout(ctx, r.config.AttemptTimeout)
	defer timeout()
	callCtx, cancel := context.WithCancelCause(callCtx)
	defer cancel(nil)
	watchDone := make(chan struct{})
	go func() { defer close(watchDone); r.watch(callCtx, cancel, lease, work) }()
	response, callErr := llm.CompleteWithOptions(callCtx, provider, request.System, request.Messages, request.Tools, llm.CompleteOptions{SingleAttempt: true, MaxOutputTokens: work.Config.MaxOutput, Purpose: request.Purpose,
		Identity: llm.RequestIdentity{AgentID: scope.AgentID, ThreadID: work.ThreadID, GenerationID: strconv.FormatInt(work.Generation, 10), ContextScopeID: work.TurnID}})
	// Shutdown or lease loss preserves recovery authority. The next Activation
	// records an unacknowledged attempt as unknown before continuing the Turn.
	if ctx.Err() != nil {
		cancel(nil)
		<-watchDone
		return ctx.Err()
	}
	cause := context.Cause(callCtx)
	cancel(nil)
	<-watchDone
	if cause != nil && !errors.Is(cause, context.DeadlineExceeded) && !errors.Is(cause, ErrDenied) && !errors.Is(cause, ErrConflict) {
		return cause
	}
	failure := ""
	if callErr != nil {
		failure = "provider_error"
	}
	if errors.Is(cause, ErrDenied) || errors.Is(cause, ErrConflict) {
		failure = "cancelled"
	}
	if _, err := r.currentScope(ctx, scope); err != nil {
		if !errors.Is(err, ErrDenied) {
			return err
		}
		failure = "cancelled"
	}
	if failure == "" && (response.Message.Role != llm.RoleAssistant || len(response.Message.Blocks) == 0 || !validToolResponse(response.Message, r.tools != nil)) {
		failure = "invalid_response"
	}
	return r.store.FinishAttempt(ctx, lease, attempt.ID, response, failure)
}

func (r *Runner) watch(ctx context.Context, cancel context.CancelCauseFunc, lease Lease, work Work) {
	ticker := time.NewTicker(r.config.AuthorityInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if _, err := r.currentScope(ctx, work.Scope); err != nil {
			cancel(err)
			return
		}
		active, err := r.store.WorkActive(ctx, lease, work.TurnID)
		if err != nil {
			cancel(err)
			return
		}
		if !active {
			cancel(ErrConflict)
			return
		}
	}
}
