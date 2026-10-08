//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/foundation/application"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
	runtimepg "github.com/juex-ai/juex/internal/managedruntime/postgres"
)

type lostRuntimeNoticeReply struct {
	managedruntime.NotificationGateway
	calls atomic.Int32
}

func (g *lostRuntimeNoticeReply) RecordNotification(ctx context.Context, e application.Event) error {
	if err := g.NotificationGateway.RecordNotification(ctx, e); err != nil {
		return err
	}
	if g.calls.Add(1) == 1 {
		return errors.New("reply lost after Inbox commit")
	}
	return nil
}

func TestManagedRuntimeNotificationOutboxRetriesFrozenOwnerFact(t *testing.T) {
	f := managedRuntimeHTTP(t, func(w http.ResponseWriter, _ *http.Request) {
		streamManagedReply(w, "private assistant result must not be copied into notification")
	})
	ctx := context.Background()
	p, err := f.directory.NotificationPreferences(ctx, f.actor, f.tenant)
	if err != nil {
		t.Fatal(err)
	}
	p.Completions = true
	if _, err := f.directory.ConfigureNotifications(ctx, f.actor, f.tenant, p); err != nil {
		t.Fatal(err)
	}
	g := &lostRuntimeNoticeReply{NotificationGateway: f.authority}
	runner, err := managedruntime.NewRunner(f.store, f.authority, managedruntime.RunnerConfig{Notifications: g, PollInterval: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	run, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); runner.Run(run) }()
	defer func() { stop(); <-done }()
	f.submit(t, "notified", f.main.ID, "private user content must not be in notification")
	runtimeEventually(t, func() bool { return g.calls.Load() >= 1 })
	if _, err := f.pool.Exec(ctx, `UPDATE runtime.threads SET name='Renamed after notification acceptance' WHERE id=$1`, f.main.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE runtime.notification_outbox SET not_before='-infinity' WHERE state='pending'`); err != nil {
		t.Fatal(err)
	}
	runtimeEventually(t, func() bool { return g.calls.Load() >= 2 })
	page, err := f.directory.Notifications(ctx, f.actor, f.tenant, 0, 20)
	if err != nil || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	n := page.Items[0]
	if n.Application != "runtime" || n.ResourceID != f.main.ID || n.Kind != "completed" || strings.Contains(n.Summary, "private") || strings.Contains(n.Summary, "Renamed") {
		t.Fatal(n)
	}
	var inputs int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.inputs`).Scan(&inputs); err != nil || inputs != 1 {
		t.Fatal("notification woke model", inputs, err)
	}
}

func TestManagedRuntimeNotificationWaitRechecksOriginalTool(t *testing.T) {
	for _, tool := range []string{"read", "copy_file"} {
		for _, ending := range []string{"still_waiting", "recovered", "cancelled", "transient_failure"} {
			t.Run(tool+"/"+ending, func(t *testing.T) {
				f := managedRuntimeHTTP(t, func(http.ResponseWriter, *http.Request) { t.Error("fixture unexpectedly called model") })
				ctx := context.Background()
				_, work := prepareRuntimeCall(t, f, f.main.ID, "waiting", llm.Block{Type: llm.BlockToolUse, ToolUseID: "wait", ToolName: tool})
				if err := f.store.FinishTool(ctx, work, managedruntime.ToolOutcome{State: "waiting", OperationLive: true, WaitReason: "environment"}); err != nil {
					t.Fatal(err)
				}
				if _, err := f.store.ClaimNotification(ctx); !errors.Is(err, managedruntime.ErrNoWork) {
					t.Fatal("brief wait immediately notified", err)
				}
				if ending == "cancelled" {
					if err := f.store.CancelThread(ctx, work.Scope, f.main.ID); err != nil {
						t.Fatal(err)
					}
				} else if ending == "recovered" || ending == "transient_failure" {
					if _, err := f.pool.Exec(ctx, `UPDATE runtime.tools SET next_check='-infinity' WHERE id=$1`, work.ID); err != nil {
						t.Fatal(err)
					}
					retry, err := f.store.ClaimTool(ctx, "retry")
					if err != nil {
						t.Fatal(err)
					}
					outcome := managedruntime.ToolOutcome{State: "waiting", OperationLive: true}
					if ending == "recovered" {
						outcome.WaitReason = "execution"
					}
					if err := f.store.FinishTool(ctx, retry, outcome); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := f.pool.Exec(ctx, `UPDATE runtime.notification_outbox SET not_before='-infinity'`); err != nil {
					t.Fatal(err)
				}
				d, err := runtimepg.New(f.pool).ClaimNotification(ctx)
				if ending == "cancelled" || ending == "recovered" {
					if !errors.Is(err, managedruntime.ErrNoWork) {
						t.Fatal("obsolete wait sent", d, err)
					}
				} else if err != nil || d.Event.Kind != "attention" || d.Event.ResourceID != f.main.ID {
					t.Fatal(d, err)
				}
			})
		}
	}
}

func TestManagedRuntimeNotificationTerminalFactsAndApplicationExclusion(t *testing.T) {
	for _, outcome := range []string{"failed", "held", "unknown", "application"} {
		t.Run(outcome, func(t *testing.T) {
			pool, store, scope, main := runtimeDatabase(t)
			ctx := context.Background()
			var input managedruntime.InputReceipt
			if outcome == "application" {
				job := applicationJob()
				r, err := store.AdmitApplication(ctx, scope, job)
				if err != nil {
					t.Fatal(err)
				}
				input.ID = r.InputID
				main.ID = r.ThreadID
			} else {
				var err error
				input, err = store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: "notice", Text: "secret request"})
				if err != nil {
					t.Fatal(err)
				}
			}
			lease, err := store.Claim(ctx, scope.AgentID, "fixture", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if outcome == "held" {
				if err := store.HoldInput(ctx, lease, input.ID, "model_unavailable"); err != nil {
					t.Fatal(err)
				}
			} else {
				work, err := store.BeginTurn(ctx, lease, scope, input.ID, runtimeConfig())
				if err != nil {
					t.Fatal(err)
				}
				a, err := store.BeginAttempt(ctx, lease, work.TurnID, managedruntime.ModelRequest{MaxOutputTokens: work.Config.Models[work.ModelIndex].MaxOutput, Messages: work.History})
				if err != nil {
					t.Fatal(err)
				}
				response := llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "secret partial upstream response")}
				failure := "provider_error"
				if outcome == "unknown" {
					failure = ""
					response.Message = llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ToolUseID: "unknown", ToolName: "read"}}}
				}
				if err := store.FinishAttempt(ctx, lease, a.ID, response, failure); err != nil {
					t.Fatal(err)
				}
				if outcome == "unknown" {
					tool, err := store.ClaimTool(ctx, "tool")
					if err != nil {
						t.Fatal(err)
					}
					if err := store.FinishTool(ctx, tool, managedruntime.ToolOutcome{State: "unknown", Content: "secret output"}); err != nil {
						t.Fatal(err)
					}
				}
			}
			d, err := store.ClaimNotification(ctx)
			if outcome == "application" {
				if !errors.Is(err, managedruntime.ErrNoWork) {
					t.Fatal("Runtime duplicated application result", d, err)
				}
				return
			}
			if err != nil || d.Event.Kind != "attention" || d.Event.ResourceID != main.ID {
				t.Fatal(d, err)
			}
			data, _ := json.Marshal(d.Event)
			if strings.Contains(string(data), "secret") {
				t.Fatal("raw provider/tool/user data entered notice", string(data))
			}
			var count int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM runtime.notification_outbox`).Scan(&count); err != nil || count != 1 {
				t.Fatal(count, err)
			}
		})
	}
}

func TestManagedRuntimeNotificationRearmsOnlyUnsentWait(t *testing.T) {
	for _, claimed := range []bool{false, true} {
		t.Run(map[bool]string{false: "brief_source_then_offline_target", true: "lost_reply_retains_identity"}[claimed], func(t *testing.T) {
			f := managedRuntimeHTTP(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected model call") })
			ctx := context.Background()
			_, work := prepareRuntimeCall(t, f, f.main.ID, "copy", llm.Block{Type: llm.BlockToolUse, ToolUseID: "copy", ToolName: "copy_file"})
			finish := func(reason string) {
				t.Helper()
				if err := f.store.FinishTool(ctx, work, managedruntime.ToolOutcome{State: "waiting", OperationLive: true, WaitReason: reason}); err != nil {
					t.Fatal(err)
				}
			}
			claimTool := func() {
				t.Helper()
				if _, err := f.pool.Exec(ctx, `UPDATE runtime.tools SET next_check='-infinity' WHERE id=$1`, work.ID); err != nil {
					t.Fatal(err)
				}
				var err error
				work, err = f.store.ClaimTool(ctx, "phase")
				if err != nil {
					t.Fatal(err)
				}
			}
			finish("environment")
			if claimed {
				if _, err := f.pool.Exec(ctx, `UPDATE runtime.notification_outbox SET not_before='-infinity'`); err != nil {
					t.Fatal(err)
				}
				d, err := f.store.ClaimNotification(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if err := f.store.FinishNotification(ctx, d, false); err != nil {
					t.Fatal(err)
				}
			}
			claimTool()
			finish("execution")
			claimTool()
			finish("environment")
			if _, err := f.store.ClaimNotification(ctx); !errors.Is(err, managedruntime.ErrNoWork) {
				t.Fatal("second phase did not get a fresh delay", err)
			}
			if _, err := f.pool.Exec(ctx, `UPDATE runtime.notification_outbox SET not_before='-infinity'`); err != nil {
				t.Fatal(err)
			}
			_, err := f.store.ClaimNotification(ctx)
			if claimed && !errors.Is(err, managedruntime.ErrNoWork) || !claimed && err != nil {
				t.Fatal("incorrect wait rearm", claimed, err)
			}
		})
	}
}
