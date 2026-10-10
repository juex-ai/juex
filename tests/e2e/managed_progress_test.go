//go:build postgres

package e2e

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
	runtimepg "github.com/juex-ai/juex/internal/managedruntime/postgres"
	"github.com/juex-ai/juex/internal/management"
)

func TestManagedProgressVisibleBeforeFinalAndCancellation(t *testing.T) {
	for _, cancelTurn := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelTurn), func(t *testing.T) {
			release := make(chan struct{})
			var once sync.Once
			resume := func() { once.Do(func() { close(release) }) }
			defer resume()
			f := managedRuntimeHTTP(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"id\":\"fixture\",\"model\":\"test-model\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"First part\"}}]}\n\n")
				w.(http.Flusher).Flush()
				select {
				case <-release:
					streamManagedReply(w, " and final part")
				case <-r.Context().Done():
				}
			})
			f.submit(t, "stream", f.main.ID, "Reply in two parts")
			f.run(t)
			var preview managedruntime.ModelProgress
			runtimeEventually(t, func() bool {
				value := f.timeline(t, f.main.ID)
				if len(value.Progress) != 1 {
					return false
				}
				preview = value.Progress[0]
				return preview.State == "running" && len(preview.Snapshot.Blocks) == 1 && preview.Snapshot.Blocks[0].Text == "First part"
			})
			var finals int
			if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM runtime.events WHERE kind='message.appended' AND data->>'role'='assistant'`).Scan(&finals); err != nil || finals != 0 {
				t.Fatal("preview was a completed history message", finals, err)
			}
			if cancelTurn {
				managementCall[map[string]any](t, f.client, "POST", f.base+"/threads/"+f.main.ID+"/cancel", f.origin, map[string]any{}, 200)
			}
			resume()
			runtimeEventually(t, func() bool { return f.timeline(t, f.main.ID).Thread.PendingInputs == 0 })
			value := f.timeline(t, f.main.ID)
			if cancelTurn {
				if len(value.Progress) != 1 || value.Progress[0].State != "cancelled" {
					t.Fatal("cancelled partial disappeared or looked complete", value.Progress)
				}
			} else {
				if len(value.Progress) != 0 {
					t.Fatal("preview survived final settlement")
				}
				var text string
				if err := f.pool.QueryRow(context.Background(), `SELECT data->'blocks'->0->>'text' FROM runtime.events WHERE kind='message.appended' AND data->>'id'=$1`, preview.AttemptID).Scan(&text); err != nil || text != "First part and final part" {
					t.Fatal(text, err)
				}
			}
		})
	}
}

func TestManagedProgressRecoveryFencingAndMonotonicRevision(t *testing.T) {
	pool, store, scope, thread := runtimeDatabase(t)
	ctx := context.Background()
	input, err := store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: "preview", Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.Claim(ctx, scope.AgentID, "old", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	work, err := store.BeginTurn(ctx, lease, scope, input.ID, runtimeConfig())
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := store.BeginAttempt(ctx, lease, work.TurnID, managedruntime.ModelRequest{Messages: work.History, Purpose: "conversation", MaxOutputTokens: 4096})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := managedruntime.ProgressSnapshot{Revision: 2, Blocks: []managedruntime.ProgressBlock{{Kind: "text", Text: "latest"}}}
	if err := store.WriteProgress(ctx, lease, attempt.ID, snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot.Revision = 1
	snapshot.Blocks[0].Text = "stale"
	if err := store.WriteProgress(ctx, lease, attempt.ID, snapshot); err != nil {
		t.Fatal(err)
	}
	restarted := runtimepg.New(pool)
	timeline, err := restarted.Timeline(ctx, scope, thread.ID, 0, 200)
	if err != nil || len(timeline.Progress) != 1 || timeline.Progress[0].Snapshot.Blocks[0].Text != "latest" {
		t.Fatal(timeline.Progress, err)
	}
	sequence := timeline.Thread.Sequence
	if _, err := pool.Exec(ctx, `UPDATE runtime.agents SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, scope.AgentID); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteProgress(ctx, lease, attempt.ID, snapshot); !errors.Is(err, managedruntime.ErrFence) {
		t.Fatal("expired writer accepted", err)
	}
	timeline, err = restarted.Timeline(ctx, scope, thread.ID, 0, 200)
	if err != nil || timeline.Progress[0].State != "unknown" || timeline.Thread.Sequence != sequence {
		t.Fatal("expired progress or business sequence changed", timeline.Progress, err)
	}
	replacement, err := restarted.Claim(ctx, scope.AgentID, "new", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.BeginTurn(ctx, replacement, scope, input.ID, runtimeConfig()); err != nil {
		t.Fatal(err)
	}
	if err := restarted.WriteProgress(ctx, replacement, attempt.ID, snapshot); !errors.Is(err, managedruntime.ErrConflict) {
		t.Fatal("recovered old attempt resumed preview", err)
	}
	second, err := restarted.BeginAttempt(ctx, replacement, work.TurnID, managedruntime.ModelRequest{Messages: work.History, Purpose: "conversation", MaxOutputTokens: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.WriteProgress(ctx, replacement, second.ID, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := restarted.FinishAttempt(ctx, replacement, second.ID, llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "complete")}, ""); err != nil {
		t.Fatal(err)
	}
	if err := restarted.WriteProgress(ctx, replacement, second.ID, snapshot); !errors.Is(err, managedruntime.ErrConflict) {
		t.Fatal("late preview revived final", err)
	}
	timeline, err = restarted.Timeline(ctx, scope, thread.ID, 0, 200)
	if err != nil || len(timeline.Progress) != 1 || timeline.Progress[0].AttemptID != attempt.ID || timeline.Progress[0].State != "unknown" {
		t.Fatal(timeline.Progress, err)
	}
}

type progressFailureProvider struct{ partial bool }

func (p progressFailureProvider) Name() string { return "fixture" }
func (p progressFailureProvider) Complete(context.Context, string, []llm.Message, []llm.ToolSpec) (llm.Response, error) {
	return llm.Response{}, errors.New("options required")
}
func (p progressFailureProvider) CompleteWithOptions(_ context.Context, _ string, _ []llm.Message, _ []llm.ToolSpec, options llm.CompleteOptions) (llm.Response, error) {
	if p.partial {
		options.OnDelta(llm.StreamDelta{Kind: "text", Text: "Incomplete primary output"})
	}
	return llm.Response{}, errors.New("temporary server error: 503")
}

type progressFailureAuthority struct {
	managedruntime.Authority
	primary string
	partial bool
}

func (a progressFailureAuthority) Provider(ctx context.Context, scope managedruntime.Scope, model managedruntime.ModelConfig, requirements managedruntime.ModelRequirements) (llm.Provider, error) {
	if model.ModelID == a.primary {
		return progressFailureProvider{partial: a.partial}, nil
	}
	return a.Authority.Provider(ctx, scope, model, requirements)
}

func TestManagedProgressPartialFailureDoesNotSilentlyFallback(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprint(partial), func(t *testing.T) {
			f := managedRuntimeHTTP(t, func(w http.ResponseWriter, r *http.Request) { streamManagedReply(w, "Backup answer") })
			ctx := context.Background()
			scope, err := f.authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := f.authority.Snapshot(ctx, scope)
			if err != nil {
				t.Fatal(err)
			}
			backup, err := f.directory.ConfigureModel(ctx, management.ModelConfiguration{Provider: "alternate", Name: "backup", Protocol: llm.ProtocolOpenAIChat, Endpoint: plan.Models[0].Endpoint, APIKey: "test", ContextWindow: 32768, MaxOutput: 4096, OutputReserve: 4096, Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			if err := f.configureModels(ctx, []string{f.agent.Configuration.Models[0], backup.ID}); err != nil {
				t.Fatal(err)
			}
			f.submit(t, "stream-failure", f.main.ID, "hello")
			runner, err := managedruntime.NewRunner(f.store, progressFailureAuthority{Authority: f.authority, primary: f.agent.Configuration.Models[0], partial: partial}, managedruntime.RunnerConfig{PollInterval: 20 * time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			runCtx, cancel := context.WithCancel(ctx)
			done := make(chan struct{})
			go func() { defer close(done); runner.Run(runCtx) }()
			t.Cleanup(func() { cancel(); <-done })
			runtimeEventually(t, func() bool { return f.timeline(t, f.main.ID).Thread.PendingInputs == 0 })
			value := f.timeline(t, f.main.ID)
			var attempts int
			if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.attempts`).Scan(&attempts); err != nil {
				t.Fatal(err)
			}
			if partial {
				if attempts != 1 || value.Thread.State != "failed" || len(value.Progress) != 1 || value.Progress[0].State != "failed" {
					t.Fatal("partial output silently replayed", attempts, value)
				}
			} else if attempts != 2 || value.Thread.State != "idle" || len(value.Progress) != 0 {
				t.Fatal("pre-output fallback stopped working", attempts, value)
			}
		})
	}
}
