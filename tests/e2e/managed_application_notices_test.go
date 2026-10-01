//go:build postgres

package e2e

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/app/managed"
	serverrpc "github.com/juex-ai/juex/internal/entrypoints/platformrpc"
	"github.com/juex-ai/juex/internal/foundation/application"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/managedruntime"
	runtimepg "github.com/juex-ai/juex/internal/managedruntime/postgres"
	runtimerpc "github.com/juex-ai/juex/internal/managedruntime/rpc"
	"github.com/juex-ai/juex/internal/memory"
)

type lostNotificationReplies struct {
	managed.ApplicationNotifications
	main, inbox int
}

func (n *lostNotificationReplies) Main(ctx context.Context, event application.Event) error {
	n.main++
	if err := n.ApplicationNotifications.Main(ctx, event); err != nil {
		return err
	}
	if n.main == 1 {
		return errors.New("reply lost")
	}
	return nil
}
func (n *lostNotificationReplies) Inbox(ctx context.Context, event application.Event) error {
	n.inbox++
	if err := n.ApplicationNotifications.Inbox(ctx, event); err != nil {
		return err
	}
	if n.inbox == 1 {
		return errors.New("reply lost")
	}
	return nil
}

type endedMemoryWorker struct{}

func (endedMemoryWorker) Admit(context.Context, memory.Review) (memory.WorkerState, error) {
	return memory.WorkerState{}, errors.New("should recover existing Worker")
}
func (endedMemoryWorker) State(context.Context, memory.Review) (memory.WorkerState, error) {
	return memory.WorkerState{State: "failed"}, nil
}
func (endedMemoryWorker) Cancel(context.Context, memory.Review) error { return nil }

func TestManagedMemoryNotificationsBusinessCommitAndLostReplies(t *testing.T) {
	f := managedMemory(t)
	ctx := context.Background()
	if err := runtimepg.Migrate(ctx, f.pool); err != nil {
		t.Fatal(err)
	}
	store := runtimepg.New(f.pool)
	scope := toRuntimeScope(f.scope)
	main, err := store.EnsureAgent(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &managedruntime.Service{Store: store, Authority: managed.RuntimeAuthority{Directory: f.directory}, Applications: managed.RuntimeApplications{Memory: f.service}}
	preferences, err := f.directory.NotificationPreferences(ctx, f.scope.UserID, f.scope.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	preferences.Completions = true
	if _, err := f.directory.ConfigureNotifications(ctx, f.scope.UserID, f.scope.TenantID, preferences); err != nil {
		t.Fatal(err)
	}
	notifier := &lostNotificationReplies{ApplicationNotifications: managed.ApplicationNotifications{Runtime: runtime, Management: managed.RuntimeAuthority{Directory: f.directory}}}
	f.service.Notifier = notifier
	f.service.Workers = endedMemoryWorker{}
	binding, proposal := f.propose(t, "notification")
	if _, err := f.service.Decide(ctx, f.scope, binding, memoryDecision("entry", proposal), "decide"); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Step(ctx); err == nil {
		t.Fatal("lost acknowledgements hidden")
	}
	if err := f.service.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if notifier.main != 2 || notifier.inbox != 2 {
		t.Fatal("independent delivery retries", notifier.main, notifier.inbox)
	}
	page, err := f.directory.Notifications(ctx, f.scope.UserID, f.scope.TenantID, 0, 20)
	if err != nil || len(page.Items) != 1 || page.Items[0].Kind != "completed" {
		t.Fatal("Worker failure changed committed business result", page, err)
	}
	notices, err := store.PendingNotices(ctx, scope, main.ID)
	if err != nil || len(notices) != 1 {
		t.Fatal(notices, err)
	}
	var inputs int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.inputs`).Scan(&inputs); err != nil || inputs != 0 {
		t.Fatal("notice woke a model", inputs, err)
	}
	if pending, err := f.store.PendingNotifications(ctx, 20); err != nil || len(pending) != 0 {
		t.Fatal(pending, err)
	}
	result, err := f.service.Result(ctx, f.human, "", binding.ReviewID)
	if err != nil || !result.Committed {
		t.Fatal("completed knowledge overwritten by Worker shutdown", result, err)
	}
}

type noticeFixtureGateway struct {
	runtimeApplicationGateway
	Invalid atomic.Bool
}

func (g *noticeFixtureGateway) NoticeValid(context.Context, application.Event) error {
	if g.Invalid.Load() {
		return managedruntime.ErrDenied
	}
	return nil
}

func TestManagedRuntimeApplicationNoticeNextMainAndRevocation(t *testing.T) {
	for _, revoked := range []bool{false, true} {
		t.Run(map[bool]string{false: "summary", true: "revoked"}[revoked], func(t *testing.T) {
			var calls atomic.Int32
			f := managedRuntimeHTTP(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				body, _ := io.ReadAll(r.Body)
				if strings.Contains(string(body), "stable_application_notice") == revoked {
					t.Error("incorrect notice projection")
				}
				streamManagedReply(w, "Done")
			})
			ctx := context.Background()
			scope, err := f.authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
			if err != nil {
				t.Fatal(err)
			}
			g := &noticeFixtureGateway{}
			f.service.Applications = g
			pki := filepath.Join(t.TempDir(), "pki")
			if err := platformrpc.CreateCredentials(pki); err != nil {
				t.Fatal(err)
			}
			listener := platformListener(t)
			server, err := serverrpc.NewRuntime(listener, platformrpc.CredentialsAt(pki, "runtime"), f.service, f.pool.Ping)
			if err != nil {
				t.Fatal(err)
			}
			runPlatformRPC(t, server)
			client, err := runtimerpc.NewClient(listener.Addr().String(), platformrpc.CredentialsAt(pki, "memory"))
			if err != nil {
				t.Fatal(err)
			}
			foreign, err := runtimerpc.NewClient(listener.Addr().String(), platformrpc.CredentialsAt(pki, "calendar"))
			if err != nil {
				t.Fatal(err)
			}
			event := application.Event{ID: uuid.NewString(), Application: "memory", ResourceID: "review", Kind: "completed", Title: "stable_application_notice", Epoch: 1, Fence: 1, CreatedAt: time.Now(), Scope: application.Scope{Access: application.Access{ActorID: scope.ActorID, TenantID: scope.TenantID, UserID: scope.UserID, AgentID: scope.AgentID}, FleetID: scope.FleetID, ActorEpoch: scope.ActorAuthorizationEpoch, MemberEpoch: scope.MembershipExecutionEpoch, AgentEpoch: scope.AgentExecutionEpoch}}
			for i := 0; i < 2; i++ {
				if err := client.RecordApplicationNotice(ctx, event); err != nil {
					t.Fatal(err)
				}
			}
			if err := foreign.RecordApplicationNotice(ctx, event); !errors.Is(err, managedruntime.ErrDenied) {
				t.Fatal("foreign app forged notice", err)
			}
			g.Invalid.Store(revoked)
			stop := runApplicationFixture(t, f, g)
			var inputs int
			if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.inputs`).Scan(&inputs); err != nil || inputs != 0 {
				t.Fatal(inputs, err)
			}
			input := f.submit(t, "notice", "", "Continue")
			runtimeEventually(t, func() bool {
				var state string
				_ = f.pool.QueryRow(ctx, `SELECT state FROM runtime.inputs WHERE id=$1`, input.ID).Scan(&state)
				return state == "completed"
			})
			stop()
			if calls.Load() != 1 {
				t.Fatal(calls.Load())
			}
			var evidence int
			if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.memory_evidence`).Scan(&evidence); err != nil || evidence != 1 {
				t.Fatal("notice entered user evidence", evidence, err)
			}
		})
	}
}
