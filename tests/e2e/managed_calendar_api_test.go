//go:build postgres

package e2e

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/app/managed"
	"github.com/juex-ai/juex/internal/calendar"
	calendarpg "github.com/juex-ai/juex/internal/calendar/postgres"
	calendarrpc "github.com/juex-ai/juex/internal/calendar/rpc"
	"github.com/juex-ai/juex/internal/entrypoints/managementhttp"
	serverrpc "github.com/juex-ai/juex/internal/entrypoints/platformrpc"
	"github.com/juex-ai/juex/internal/foundation/application"
	applicationrpc "github.com/juex-ai/juex/internal/foundation/application/rpc"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/management"
	managementpg "github.com/juex-ai/juex/internal/management/postgres"
)

func TestManagedCalendarFirstWakeUsesOrdinaryAgentWorker(t *testing.T) {
	var calls atomic.Int32
	f := managedRuntimeHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		streamManagedReply(w, "The scheduled check is complete.")
	})
	ctx := context.Background()
	agent, err := f.directory.CreateAgent(ctx, f.actor, f.tenant, f.actor, management.AgentConfig{Name: "Never opened", ModelID: f.agent.ModelID})
	if err != nil {
		t.Fatal(err)
	}
	if err := calendarpg.Migrate(ctx, f.pool); err != nil {
		t.Fatal(err)
	}
	store := calendarpg.New(f.pool)
	service := &calendar.Service{Repository: store, Authority: f.authority, Workers: managed.CalendarWorkers{Runtime: f.service}}
	t.Cleanup(service.CloseScheduler)
	gateway := managed.RuntimeApplications{Calendar: service}
	f.service.Applications = gateway
	access := application.Access{ActorID: f.actor, TenantID: f.tenant, UserID: f.actor}
	target := access
	target.AgentID = agent.ID
	scope, err := f.authority.AuthorizeApplication(ctx, target, true)
	if err != nil {
		t.Fatal(err)
	}
	q := calendarChange(agent.ID)
	if _, err := service.Change(ctx, access, nil, "first-wake", q); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, scope, func(state *calendar.State) error { return state.Advance(q.ID, time.Now().Add(time.Minute)) }); err != nil {
		t.Fatal(err)
	}
	if err := service.Step(ctx); err != nil {
		t.Fatal(err)
	}
	runApplicationFixture(t, f, gateway)
	runtimeEventually(t, func() bool {
		if err := service.Step(ctx); err != nil {
			return false
		}
		page, err := service.Occurrences(ctx, access, q.ID, 0, 50)
		return err == nil && len(page.Occurrences) == 1 && page.Occurrences[0].State == "completed"
	})
	if calls.Load() != 1 {
		t.Fatal("unexpected model calls", calls.Load())
	}
	var workers int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.threads WHERE agent_id=$1 AND kind='worker'`, agent.ID).Scan(&workers); err != nil || workers != 1 {
		t.Fatal(workers, err)
	}
}

func TestManagedCalendarIndependentKitexAuthority(t *testing.T) {
	f, service, store := calendarFixture(t)
	ctx := context.Background()
	pki := filepath.Join(t.TempDir(), "pki")
	if err := platformrpc.CreateCredentials(pki); err != nil {
		t.Fatal(err)
	}
	ml := platformListener(t)
	ms, err := serverrpc.NewManagement(ml, platformrpc.CredentialsAt(pki, "management"), managed.RuntimeAuthority{Directory: f.directory})
	if err != nil {
		t.Fatal(err)
	}
	runPlatformRPC(t, ms)
	authority, err := applicationrpc.NewAuthority(ml.Addr().String(), platformrpc.CredentialsAt(pki, "calendar"))
	if err != nil {
		t.Fatal(err)
	}
	service.Authority = authority
	l := platformListener(t)
	server, err := serverrpc.NewCalendar(l, platformrpc.CredentialsAt(pki, "calendar"), service, f.pool.Ping)
	if err != nil {
		t.Fatal(err)
	}
	runPlatformRPC(t, server)
	client, err := calendarrpc.NewClient(l.Addr().String(), platformrpc.CredentialsAt(pki, "management"))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := calendarrpc.NewClient(l.Addr().String(), platformrpc.CredentialsAt(pki, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	runtimeEventually(t, func() bool { return client.Health(ctx) == nil })
	q := calendarChange(f.scope.AgentID)
	if _, err := client.Change(ctx, f.human, nil, "human-create", q); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Change(ctx, f.scope.Access, &f.scope, "forged", q); !errors.Is(err, application.ErrDenied) {
		t.Fatal("Management forged Agent", err)
	}
	if _, err := runtime.Change(ctx, f.human, nil, "forged", q); !errors.Is(err, application.ErrDenied) {
		t.Fatal("Runtime forged human", err)
	}
	if _, err := runtime.Configure(ctx, f.scope.Access, 1, false); !errors.Is(err, application.ErrDenied) {
		t.Fatal("Agent disabled Calendar", err)
	}
	if err := runtime.CancelCommand(ctx, f.scope, "cancel-before"); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Change(ctx, f.scope.Access, &f.scope, "cancel-before", calendar.Change{ID: q.ID, Version: 1, Action: "pause"}); !errors.Is(err, application.ErrDenied) {
		t.Fatal(err)
	}
	var delivery calendar.Delivery
	if err := store.Update(ctx, f.scope, func(state *calendar.State) error {
		if err := state.Advance(q.ID, time.Now().Add(time.Minute)); err != nil {
			return err
		}
		for _, d := range state.Deliveries {
			delivery = *d
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Assignment(ctx, f.scope, delivery.ID, 1); !errors.Is(err, application.ErrDenied) {
		t.Fatal("Management read private assignment", err)
	}
	if d, err := runtime.Assignment(ctx, f.scope, delivery.ID, 1); err != nil || d.ID != delivery.ID {
		t.Fatal(d, err)
	}
	if page, err := runtime.Schedules(ctx, f.scope.Access, 0, 50); err != nil || len(page.Schedules) != 1 {
		t.Fatal(page, err)
	}
	if _, err := runtime.Assignment(ctx, f.scope, delivery.ID, 2); !errors.Is(err, application.ErrDenied) {
		t.Fatal("wrong app epoch", err)
	}
}

func TestManagedCalendarHumanHTTPControlsAndHistory(t *testing.T) {
	ctx := context.Background()
	f := managedRuntimeHTTP(t, func(http.ResponseWriter, *http.Request) { t.Error("human Calendar HTTP invoked model") })
	if err := calendarpg.Migrate(ctx, f.pool); err != nil {
		t.Fatal(err)
	}
	service := &calendar.Service{Repository: calendarpg.New(f.pool), Authority: f.authority}
	auth, err := managementpg.NewAuth(f.directory)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(nil)
	origin := "http://" + server.Listener.Addr().String()
	handler, err := managementhttp.New(managementhttp.Options{Auth: auth, Directory: f.directory, Calendar: service, PublicURL: origin, InsecureHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = handler
	server.Start()
	t.Cleanup(server.Close)
	base := origin + "/api/tenants/" + f.tenant + "/users/" + f.actor + "/calendar"
	status := managementCall[calendar.Status](t, f.client, "GET", base, origin, nil, 200)
	if !status.Enabled {
		t.Fatal(status)
	}
	managementCall[any](t, f.client, "GET", strings.Replace(base, f.tenant, uuid.NewString(), 1), origin, nil, 403)
	q := managementhttp.CalendarChange{RequestID: uuid.NewString(), Change: calendarChange(f.agent.ID)}
	first := managementCall[calendar.Receipt](t, f.client, "POST", base+"/changes", origin, q, 200)
	if again := managementCall[calendar.Receipt](t, f.client, "POST", base+"/changes", origin, q, 200); again != first {
		t.Fatal(again, first)
	}
	page := managementCall[calendar.SchedulePage](t, f.client, "GET", base+"/schedules", origin, nil, 200)
	if len(page.Schedules) != 1 {
		t.Fatal(page)
	}
	managementCall[any](t, f.client, "GET", base+"/occurrences?limit=51", origin, nil, 400)
	managementCall[any](t, f.client, "GET", base+"/schedules?offset=-1", origin, nil, 400)
	managementCall[any](t, f.client, "POST", base+"/changes", "https://foreign.example", q, 403)
	managementCall[calendar.Status](t, f.client, "PUT", base, origin, managementhttp.CalendarConfiguration{Version: 1, Enabled: false}, 200)
	managementCall[calendar.SchedulePage](t, f.client, "GET", base+"/schedules", origin, nil, 200)
	q.RequestID = uuid.NewString()
	q.Action = "pause"
	q.Version = 2
	q.Definition = nil
	managementCall[any](t, f.client, "POST", base+"/changes", origin, q, 409)
	managementCall[calendar.Status](t, f.client, "PUT", base, origin, managementhttp.CalendarConfiguration{Version: 2, Enabled: true}, 200)
}
