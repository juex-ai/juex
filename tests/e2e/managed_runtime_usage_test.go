//go:build postgres

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/entrypoints/managementcli"
	serverrpc "github.com/juex-ai/juex/internal/entrypoints/platformrpc"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/managedruntime"
	runtimerpc "github.com/juex-ai/juex/internal/managedruntime/rpc"
	"github.com/juex-ai/juex/internal/management"
)

func usageQuery(tenant, owner string) managedruntime.UsageQuery {
	return managedruntime.UsageQuery{TenantID: tenant, UserID: owner, From: time.Now().AddDate(0, 0, -2).Format(time.DateOnly), Until: time.Now().AddDate(0, 0, 2).Format(time.DateOnly), Group: "day", Limit: 100}
}

func usageAttempt(t *testing.T, f *managedRuntimeFixture, scope managedruntime.Scope, thread string, response llm.Response) string {
	t.Helper()
	ctx := context.Background()
	receipt, err := f.store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: uuid.NewString(), ThreadID: thread, Text: "Usage integration"})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := f.store.Claim(ctx, scope.AgentID, "usage-test", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	config, err := f.authority.Snapshot(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	work, err := f.store.BeginTurn(ctx, lease, scope, receipt.ID, config)
	if err != nil {
		t.Fatal(err)
	}
	a, err := f.store.BeginAttempt(ctx, lease, work.TurnID, managedruntime.ModelRequest{Messages: work.History})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.FinishAttempt(ctx, lease, a.ID, response, ""); err != nil {
		t.Fatal(err)
	}
	if err := f.store.FinishAttempt(ctx, lease, a.ID, response, ""); err == nil {
		t.Fatal("same attempt was settled twice")
	}
	if err := f.store.Release(ctx, lease); err != nil {
		t.Fatal(err)
	}
	return a.ID
}

func TestManagedRuntimeUsageOwnerPermissionsAndHTTP(t *testing.T) {
	f := managedRuntimeHTTP(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected model call") })
	ctx := context.Background()
	member, err := f.directory.CreateUser(ctx, "usage-member@example.test")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := f.directory.Invite(ctx, f.actor, f.tenant, member.Email, management.Member, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.directory.AcceptInvitation(ctx, member.ID, token); err != nil {
		t.Fatal(err)
	}
	agent, err := f.directory.CreateAgent(ctx, member.ID, f.tenant, member.ID, management.AgentConfig{Name: "Usage owner", ModelID: f.agent.ModelID})
	if err != nil {
		t.Fatal(err)
	}
	scope, err := f.authority.Authorize(ctx, f.actor, f.tenant, agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	main, err := f.store.EnsureAgent(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	response := llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "private text"), Usage: llm.Usage{InputTokens: 12, OutputTokens: 4, CachedInputTokens: 3}, UsageStatus: llm.UsageComplete}
	usageAttempt(t, f, scope, main.ID, response)
	q := usageQuery(f.tenant, member.ID)
	report, err := f.service.Usage(ctx, member.ID, q)
	if err != nil || report.Totals.Attempts != 1 || report.Totals.TotalTokens != 16 || report.Totals.CachedInputTokens != 3 || len(report.Rows) != 1 || report.Rows[0].UserID != member.ID {
		t.Fatal(report, err)
	}
	var actor, owner string
	if err := f.pool.QueryRow(ctx, `SELECT actor_id,user_id FROM runtime.usage_records`).Scan(&actor, &owner); err != nil || actor != f.actor || owner != member.ID {
		t.Fatal(actor, owner, err)
	}
	q.UserID = f.actor
	if _, err := f.service.Usage(ctx, member.ID, q); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("member read another owner", err)
	}
	q.UserID = ""
	if _, err := f.service.Usage(ctx, member.ID, q); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("member read tenant aggregate", err)
	}
	q.UserID = member.ID
	q.TenantID = uuid.NewString()
	if _, err := f.service.Usage(ctx, f.actor, q); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("cross-tenant usage leaked", err)
	}
	path := f.origin + "/api/tenants/" + f.tenant + "/users/" + member.ID + "/usage?from=" + q.From + "&until=" + q.Until + "&group=month"
	managementCall[any](t, http.DefaultClient, "GET", path, f.origin, nil, 401)
	got := managementCall[managedruntime.UsageReport](t, f.client, "GET", path, f.origin, nil, 200)
	if got.Totals.TotalTokens != 16 || got.Rows[0].Bucket[8:] != "01" {
		t.Fatal(got)
	}
	if _, err := f.directory.ChangeMember(ctx, f.actor, f.tenant, member.ID, management.Member, management.Removed); err != nil {
		t.Fatal(err)
	}
	managementCall[managedruntime.UsageReport](t, f.client, "GET", path, f.origin, nil, 200)
	q.TenantID = f.tenant
	if _, err := f.service.Usage(ctx, member.ID, q); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("removed owner retained access", err)
	}
}

func TestManagedRuntimeUsageUnknownRetentionTimezoneAndPagination(t *testing.T) {
	f := managedRuntimeHTTP(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected model call") })
	ctx := context.Background()
	scope, err := f.authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	response := llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "answer"), Usage: llm.Usage{InputTokens: 12, OutputTokens: 4, CachedInputTokens: 3}, UsageStatus: llm.UsageComplete}
	first := usageAttempt(t, f, scope, f.main.ID, response)
	period, err := f.store.ConfigureUsage(ctx, managedruntime.UsagePolicy{Timezone: "America/Los_Angeles", DetailDays: 90})
	if err != nil || period.ID != 2 {
		t.Fatal(period, err)
	}
	worker, err := f.service.Worker(ctx, f.actor, f.tenant, f.agent.ID, f.main.ID, "usage-worker", "Research")
	if err != nil {
		t.Fatal(err)
	}
	response.UsageStatus = llm.UsagePartial
	usageAttempt(t, f, scope, worker.ID, response)
	response.UsageStatus = llm.UsageUnknown
	usageAttempt(t, f, scope, f.main.ID, response)
	q := usageQuery(f.tenant, f.actor)
	q.Limit = 1
	got, err := f.service.Usage(ctx, f.actor, q)
	if err != nil || got.Totals.Attempts != 3 || got.Totals.Reported != 2 || got.Totals.Partial != 1 || got.Totals.Unknown != 1 || got.Totals.TotalTokens != 32 || !got.HasMore || len(got.Rows) != 1 || len(got.Periods) != 2 || got.Periods[0].EffectiveUntil == nil {
		t.Fatal(got, err)
	}
	var originalPeriod int64
	if err := f.pool.QueryRow(ctx, `SELECT period_id FROM runtime.usage_records WHERE attempt_id=$1`, first).Scan(&originalPeriod); err != nil || originalPeriod != 1 {
		t.Fatal("timezone rewrote history", originalPeriod, err)
	}
	// Separate accounting remains after detailed model calls are erased.
	if _, err := f.pool.Exec(ctx, `DELETE FROM runtime.attempts`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE runtime.usage_records SET started_at=clock_timestamp()-interval '91 days'`); err != nil {
		t.Fatal(err)
	}
	if err := f.store.PruneUsage(ctx); err != nil {
		t.Fatal(err)
	}
	var details int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.usage_records`).Scan(&details); err != nil || details != 0 {
		t.Fatal(details, err)
	}
	q.Group = "month"
	q.Limit = 100
	retained, err := f.service.Usage(ctx, f.actor, q)
	if err != nil || retained.Totals != got.Totals || len(retained.Rows) != 3 {
		t.Fatal("retention changed aggregate history", retained, err)
	}
	if _, err := f.store.ConfigureUsage(ctx, managedruntime.UsagePolicy{Timezone: "not-a-zone", DetailDays: 90}); !errors.Is(err, managedruntime.ErrInvalid) {
		t.Fatal(err)
	}
}

func TestManagedRuntimeUsagePrivateRPCAndOperatorCLI(t *testing.T) {
	f := managedRuntimeHTTP(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected model call") })
	ctx := context.Background()
	certs := filepath.Join(t.TempDir(), "pki")
	if err := platformrpc.CreateCredentials(certs); err != nil {
		t.Fatal(err)
	}
	listener := platformListener(t)
	server, err := serverrpc.NewRuntime(listener, platformrpc.CredentialsAt(certs, "runtime"), f.service, f.pool.Ping)
	if err != nil {
		t.Fatal(err)
	}
	runPlatformRPC(t, server)
	client, err := runtimerpc.NewClient(listener.Addr().String(), platformrpc.CredentialsAt(certs, "management"))
	if err != nil {
		t.Fatal(err)
	}
	runtimeEventually(t, func() bool { return client.Health(ctx) == nil })
	q := usageQuery(f.tenant, f.actor)
	got, err := client.Usage(ctx, f.actor, q)
	if err != nil || got.DetailDays != 90 || len(got.Periods) != 1 {
		t.Fatal(got, err)
	}
	appClient, err := runtimerpc.NewClient(listener.Addr().String(), platformrpc.CredentialsAt(certs, "memory"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := appClient.OperatorUsage(ctx, q); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("application gained operator report", err)
	}
	if _, err := appClient.ConfigureUsage(ctx, managedruntime.UsagePolicy{Timezone: "UTC", DetailDays: 1}); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("application changed reporting policy", err)
	}
	var out, errOut bytes.Buffer
	if err := managementcli.Execute(ctx, []string{"--credentials", certs, "usage", "--runtime", listener.Addr().String(), "report", "--from", q.From, "--until", q.Until}, &out, &errOut); err != nil {
		t.Fatal(err, errOut.String())
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.Query.TenantID != "" || got.DetailDays != 90 {
		t.Fatal(got, err)
	}
	period, err := client.ConfigureUsage(ctx, managedruntime.UsagePolicy{Timezone: "Pacific/Kiritimati", DetailDays: 30})
	if err != nil || period.Timezone != "Pacific/Kiritimati" {
		t.Fatal(period, err)
	}
	out.Reset()
	if err := managementcli.Execute(ctx, []string{"--credentials", certs, "usage", "--runtime", listener.Addr().String(), "report"}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want, err := (managedruntime.UsageQuery{}).WithDefaultRange(time.Now(), period.Timezone)
	if err != nil || got.Query.From != want.From || got.Query.Until != want.Until {
		t.Fatal("CLI default used machine date", got.Query, want, err)
	}
	web := managementCall[managedruntime.UsageReport](t, f.client, "GET", f.origin+"/api/tenants/"+f.tenant+"/usage?group=day", f.origin, nil, 200)
	if web.Query.From != want.From || web.Query.Until != want.Until {
		t.Fatal("Web default did not receive deployment date", web.Query, want)
	}
}

func TestManagedRuntimeUsageLocalDayAndMonthBoundaries(t *testing.T) {
	f := managedRuntimeHTTP(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected model call") })
	ctx := context.Background()
	scope, err := f.authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	response := llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "answer"), Usage: llm.Usage{InputTokens: 12, OutputTokens: 4}, UsageStatus: llm.UsageComplete}
	id := usageAttempt(t, f, scope, f.main.ID, response)
	for i, timestamp := range []string{"2025-03-31T15:59:59Z", "2025-03-31T16:00:00Z"} {
		at, err := time.Parse(time.RFC3339, timestamp)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(ctx, `INSERT INTO runtime.attempts(turn_id,ordinal,request,response,state,usage,usage_status,started_at,completed_at) SELECT turn_id,ordinal+$2,request,response,state,usage,usage_status,$3,$3 FROM runtime.attempts WHERE id=$1`, id, i+10, at); err != nil {
			t.Fatal(err)
		}
	}
	q := managedruntime.UsageQuery{TenantID: f.tenant, UserID: f.actor, From: "2025-03-31", Until: "2025-04-02", Group: "day", Limit: 100}
	for _, group := range []string{"day", "month"} {
		q.Group = group
		got, err := f.service.Usage(ctx, f.actor, q)
		if err != nil || len(got.Rows) != 2 || got.Totals.TotalTokens != 32 || got.Rows[0].Bucket != "2025-04-01" || got.Rows[1].Bucket[:7] != "2025-03" {
			t.Fatal("UTC boundary used instead of reporting timezone", got, err)
		}
	}
}
