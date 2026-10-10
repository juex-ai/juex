//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/app/managed"
	"github.com/juex-ai/juex/internal/calendar"
	calendarpg "github.com/juex-ai/juex/internal/calendar/postgres"
	calendarrpc "github.com/juex-ai/juex/internal/calendar/rpc"
	serverrpc "github.com/juex-ai/juex/internal/entrypoints/platformrpc"
	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/application"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/managedruntime"
	runtimerpc "github.com/juex-ai/juex/internal/managedruntime/rpc"
	"github.com/juex-ai/juex/internal/management"
)

type lostMainReply struct {
	calendar.MainGateway
	lose atomic.Bool
}

func (g *lostMainReply) Admit(ctx context.Context, d calendar.Delivery) (calendar.MainReceipt, error) {
	r, err := g.MainGateway.Admit(ctx, d)
	if err == nil && g.lose.Swap(false) {
		return calendar.MainReceipt{}, errors.New("lost accepted reply")
	}
	return r, err
}

func TestManagedCalendarMainRPCUsesExistingContextAndRecoversReceipt(t *testing.T) {
	bodies := make(chan string, 4)
	f := managedRuntimeHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		bodies <- string(data)
		streamManagedReply(w, "Context preserved")
	})
	ctx := context.Background()
	if err := calendarpg.Migrate(ctx, f.pool); err != nil {
		t.Fatal(err)
	}
	store := calendarpg.New(f.pool)
	service := &calendar.Service{Repository: store, Authority: f.authority}
	t.Cleanup(service.CloseScheduler)
	pki := filepath.Join(t.TempDir(), "pki")
	if err := platformrpc.CreateCredentials(pki); err != nil {
		t.Fatal(err)
	}
	cl := platformListener(t)
	cs, err := serverrpc.NewCalendar(cl, platformrpc.CredentialsAt(pki, "calendar"), service, f.pool.Ping)
	if err != nil {
		t.Fatal(err)
	}
	runPlatformRPC(t, cs)
	cc, err := calendarrpc.NewClient(cl.Addr().String(), platformrpc.CredentialsAt(pki, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	gateway := managed.RuntimeApplications{Calendar: cc}
	f.service.Applications = gateway
	f.service.Triggers = gateway
	rl := platformListener(t)
	rs, err := serverrpc.NewRuntime(rl, platformrpc.CredentialsAt(pki, "runtime"), f.service, f.pool.Ping)
	if err != nil {
		t.Fatal(err)
	}
	runPlatformRPC(t, rs)
	rc, err := runtimerpc.NewClient(rl.Addr().String(), platformrpc.CredentialsAt(pki, "calendar"))
	if err != nil {
		t.Fatal(err)
	}
	runtimeEventually(t, func() bool { return rc.Health(ctx) == nil && cc.Health(ctx) == nil })
	inputs := &lostMainReply{MainGateway: managed.CalendarMainInputs{Runtime: rc}}
	inputs.lose.Store(true)
	service.MainInputs = inputs
	scope, err := f.authority.AuthorizeApplication(ctx, application.Access{ActorID: f.actor, TenantID: f.tenant, UserID: f.actor, AgentID: f.agent.ID}, true)
	if err != nil {
		t.Fatal(err)
	}
	human := scope.Access
	human.AgentID = ""
	f.submit(t, "context-before-calendar", f.main.ID, "Remember calibration violet-73")
	stop := runApplicationFixture(t, f, gateway)
	runtimeEventually(t, func() bool { return f.timeline(t, f.main.ID).Thread.State == "idle" })
	stop()
	<-bodies
	q := calendarChange(f.agent.ID)
	q.Definition.Mode = "main"
	q.Definition.CatchUp = "none"
	if _, err := service.Change(ctx, human, nil, "main", q); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, scope, func(s *calendar.State) error { return s.Advance(q.ID, time.Now().Add(time.Minute)) }); err != nil {
		t.Fatal(err)
	}
	if err := service.Step(ctx); err == nil {
		t.Fatal("lost reply hidden")
	}
	service.CloseScheduler()
	service.Repository = calendarpg.New(f.pool)
	if err := service.Step(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := service.Occurrences(ctx, human, q.ID, 0, 50)
	if err != nil || len(page.Occurrences) != 1 {
		t.Fatal(page, err)
	}
	occurrence := page.Occurrences[0]
	if occurrence.State != "accepted" || occurrence.MainThreadID != f.main.ID || occurrence.InputID == "" || occurrence.WorkerID != "" {
		t.Fatal(occurrence)
	}
	var count int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.inputs WHERE source->>'application_job_id'=$1`, occurrence.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	// Application disable does not retract an input already owned by Main.
	if _, err := service.Configure(ctx, human, 1, false); err != nil {
		t.Fatal(err)
	}
	runApplicationFixture(t, f, gateway)
	runtimeEventually(t, func() bool { return f.timeline(t, f.main.ID).Thread.State == "idle" })
	body := <-bodies
	if !strings.Contains(body, "Remember calibration violet-73") || !strings.Contains(body, "Calendar trigger") {
		t.Fatal("Main context was not preserved", body)
	}
	var message map[string]any
	if err := f.pool.QueryRow(ctx, `SELECT data FROM runtime.events WHERE thread_id=$1 AND kind='message.appended' AND data->>'id'=$2`, f.main.ID, occurrence.InputID).Scan(&message); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(message)
	if !strings.Contains(string(encoded), "system_notice") {
		t.Fatal("trigger rendered as direct human message", string(encoded))
	}
}

func TestManagedMainTriggerRPCRejectsWrongRoleAndStaleCalendarEpoch(t *testing.T) {
	f := managedRuntimeHTTP(t, func(w http.ResponseWriter, _ *http.Request) { streamManagedReply(w, "unused") })
	ctx := context.Background()
	if err := calendarpg.Migrate(ctx, f.pool); err != nil {
		t.Fatal(err)
	}
	store := calendarpg.New(f.pool)
	service := &calendar.Service{Repository: store, Authority: f.authority}
	f.service.Triggers = managed.RuntimeApplications{Calendar: service}
	pki := filepath.Join(t.TempDir(), "pki")
	if err := platformrpc.CreateCredentials(pki); err != nil {
		t.Fatal(err)
	}
	l := platformListener(t)
	server, err := serverrpc.NewRuntime(l, platformrpc.CredentialsAt(pki, "runtime"), f.service, f.pool.Ping)
	if err != nil {
		t.Fatal(err)
	}
	runPlatformRPC(t, server)
	scope, err := f.authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	q := mainTrigger()
	for _, role := range []string{"memory", "management"} {
		c, err := runtimerpc.NewClient(l.Addr().String(), platformrpc.CredentialsAt(pki, role))
		if err != nil {
			t.Fatal(err)
		}
		runtimeEventually(t, func() bool { return c.Health(ctx) == nil })
		if _, err := c.AdmitMainTrigger(ctx, scope, q); !errors.Is(err, managedruntime.ErrDenied) {
			t.Fatal(role, err)
		}
		if _, err := c.CancelMainTrigger(ctx, scope, q.ID); !errors.Is(err, managedruntime.ErrDenied) {
			t.Fatal(role, err)
		}
		if _, err := c.MainTriggerReceipt(ctx, scope, q.ID); !errors.Is(err, managedruntime.ErrDenied) {
			t.Fatal(role, err)
		}
	}
	c, err := runtimerpc.NewClient(l.Addr().String(), platformrpc.CredentialsAt(pki, "calendar"))
	if err != nil {
		t.Fatal(err)
	}
	a, err := f.authority.AuthorizeApplication(ctx, application.Access{ActorID: f.actor, TenantID: f.tenant, UserID: f.actor, AgentID: f.agent.ID}, true)
	if err != nil {
		t.Fatal(err)
	}
	human := a.Access
	human.AgentID = ""
	change := calendarChange(f.agent.ID)
	change.Definition.Mode = "main"
	if _, err := service.Change(ctx, human, nil, "main", change); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, a, func(s *calendar.State) error {
		if err := s.Advance(change.ID, time.Now().Add(time.Minute)); err != nil {
			return err
		}
		for _, d := range s.Deliveries {
			q = managedruntime.MainTrigger{ID: d.ID, Epoch: d.Epoch, Name: d.Name, Content: d.Content, ScheduledAt: d.ScheduledAt}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	forged := q
	forged.Content = "unassigned work"
	if _, err := c.AdmitMainTrigger(ctx, scope, forged); !errors.Is(err, managedruntime.ErrConflict) {
		t.Fatal("forged content", err)
	}
	if _, err := service.Configure(ctx, human, 1, false); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Configure(ctx, human, 2, true); err != nil {
		t.Fatal(err)
	}
	if _, err := c.AdmitMainTrigger(ctx, scope, q); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("revived previous Calendar epoch", err)
	}
	if _, err := c.CancelMainTrigger(ctx, scope, q.ID); err != nil {
		t.Fatal(err)
	}
	if receipt, err := c.AdmitMainTrigger(ctx, scope, q); err != nil || receipt.State != "cancelled" {
		t.Fatal(receipt, err)
	}
}

type staleMainDeliveryAuthority struct {
	application.Authority
	entered chan struct{}
	release chan struct{}
}

func (a staleMainDeliveryAuthority) AuthorizeApplication(ctx context.Context, access application.Access, execute bool) (application.Scope, error) {
	close(a.entered)
	select {
	case <-ctx.Done():
		return application.Scope{}, ctx.Err()
	case <-a.release:
	}
	return a.Authority.AuthorizeApplication(ctx, access, execute)
}

func TestManagedCalendarStaleAttemptCannotCancelAcceptedMainReceipt(t *testing.T) {
	f := managedRuntimeHTTP(t, func(w http.ResponseWriter, _ *http.Request) { streamManagedReply(w, "unused") })
	ctx := context.Background()
	if err := calendarpg.Migrate(ctx, f.pool); err != nil {
		t.Fatal(err)
	}
	store := calendarpg.New(f.pool)
	inputs := managed.CalendarMainInputs{Runtime: f.service}
	service := &calendar.Service{Repository: store, Authority: f.authority, MainInputs: inputs}
	t.Cleanup(service.CloseScheduler)
	f.service.Triggers = managed.RuntimeApplications{Calendar: service}
	scope, err := f.authority.AuthorizeApplication(ctx, application.Access{ActorID: f.actor, TenantID: f.tenant, UserID: f.actor, AgentID: f.agent.ID}, true)
	if err != nil {
		t.Fatal(err)
	}
	human := scope.Access
	human.AgentID = ""
	q := calendarChange(f.agent.ID)
	q.Definition.Mode = "main"
	if _, err := service.Change(ctx, human, nil, "create", q); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, scope, func(s *calendar.State) error { return s.Advance(q.ID, time.Now().Add(time.Minute)) }); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Change(ctx, human, nil, "pause", calendar.Change{ID: q.ID, Version: 1, Action: "pause"}); err != nil {
		t.Fatal(err)
	}
	authority := staleMainDeliveryAuthority{Authority: f.authority, entered: make(chan struct{}), release: make(chan struct{})}
	stale := &calendar.Service{Repository: calendarpg.New(f.pool), Authority: authority, MainInputs: inputs}
	t.Cleanup(stale.CloseScheduler)
	done := make(chan error, 1)
	go func() { done <- stale.Step(ctx) }()
	select {
	case <-authority.entered:
	case <-time.After(time.Second):
		t.Fatal("stale delivery did not enter authority")
	}
	if err := service.Step(ctx); err != nil {
		close(authority.release)
		t.Fatal(err)
	}
	close(authority.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	page, err := service.Occurrences(ctx, human, q.ID, 0, 50)
	if err != nil || len(page.Occurrences) != 1 || page.Occurrences[0].State != "accepted" || page.Occurrences[0].CancelRequested {
		t.Fatal("stale attempt mutated settled receipt", page, err)
	}
}

func TestManagedCalendarPendingDeliveryDoesNotBlockAcceptedNotice(t *testing.T) {
	f, service, store := calendarFixture(t)
	ctx := context.Background()
	for i := range 2 {
		q := calendarChange(f.scope.AgentID)
		q.Definition.Mode = "main"
		if _, err := service.Change(ctx, f.human, nil, q.ID, q); err != nil {
			t.Fatal(err)
		}
		if err := store.Update(ctx, f.scope, func(s *calendar.State) error {
			if err := s.Advance(q.ID, time.Now().Add(time.Minute)); err != nil {
				return err
			}
			if i == 1 {
				for _, d := range s.Deliveries {
					if d.ScheduleID == q.ID {
						d.State = "accepted"
						d.Finished = true
						d.Settled = true
					}
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	notices, err := store.PendingNotifications(ctx, 10)
	if err != nil || len(notices) != 1 || !notices[0].MainDone || notices[0].Event.Title != "日程已送达 Main" {
		t.Fatal(notices, err)
	}
}

func TestManagedCalendarTriggerCannotUseRestoredAgentAuthority(t *testing.T) {
	f := managedRuntimeHTTP(t, func(w http.ResponseWriter, _ *http.Request) { streamManagedReply(w, "unused") })
	ctx := context.Background()
	if err := calendarpg.Migrate(ctx, f.pool); err != nil {
		t.Fatal(err)
	}
	store := calendarpg.New(f.pool)
	service := &calendar.Service{Repository: store, Authority: f.authority}
	f.service.Triggers = managed.RuntimeApplications{Calendar: service}
	scope, err := f.authority.AuthorizeApplication(ctx, application.Access{ActorID: f.actor, TenantID: f.tenant, UserID: f.actor, AgentID: f.agent.ID}, true)
	if err != nil {
		t.Fatal(err)
	}
	runtimeScope, err := f.authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	human := scope.Access
	human.AgentID = ""
	q := calendarChange(f.agent.ID)
	q.Definition.Mode = "main"
	if _, err := service.Change(ctx, human, nil, "create", q); err != nil {
		t.Fatal(err)
	}
	var trigger managedruntime.MainTrigger
	if err := store.Update(ctx, scope, func(s *calendar.State) error {
		if err := s.Advance(q.ID, time.Now().Add(time.Minute)); err != nil {
			return err
		}
		for _, d := range s.Deliveries {
			trigger = managedruntime.MainTrigger{ID: d.ID, Epoch: d.Epoch, Name: d.Name, Content: d.Content, ScheduledAt: d.ScheduledAt}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	policy := agentpolicy.Policy{Disabled: []agentpolicy.Capability{agentpolicy.Calendar}}
	changed, err := f.directory.ConfigureAgent(ctx, f.actor, f.tenant, f.agent.ID, f.agent.Version, management.AgentConfig{Name: f.agent.Name, Configuration: &management.Configuration{Models: f.agent.Configuration.Models, Modules: modulesForPolicy(&policy)}})
	if err != nil {
		t.Fatal(err)
	}
	policy.Disabled = nil
	if _, err := f.directory.ConfigureAgent(ctx, f.actor, f.tenant, f.agent.ID, changed.Version, management.AgentConfig{Name: f.agent.Name, Configuration: &management.Configuration{Models: f.agent.Configuration.Models, Modules: modulesForPolicy(&policy)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.AdmitMainTrigger(ctx, runtimeScope, trigger); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("old target authority revived", err)
	}
	current, err := f.authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.AdmitMainTrigger(ctx, current, trigger); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("new authority adopted old occurrence", err)
	}
}
