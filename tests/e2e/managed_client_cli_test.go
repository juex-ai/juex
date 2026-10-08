//go:build postgres

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/entrypoints/clientcli"
	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/management"
	managementpg "github.com/juex-ai/juex/internal/management/postgres"
)

func TestManagedClientCLIWithPrivateDeploymentCA(t *testing.T) {
	f := managedRuntimeHTTP(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected model call") })
	origin, err := url.Parse(f.origin)
	if err != nil {
		t.Fatal(err)
	}
	gateway := httptest.NewTLSServer(httputil.NewSingleHostReverseProxy(origin))
	defer gateway.Close()
	c := newManagedCLI(t, gateway.URL)
	ca := filepath.Join(filepath.Dir(c.session), "deployment-ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: gateway.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	c.args = []string{"--server", gateway.URL, "--ca-file", ca, "--session-file", c.session}
	cliValue[management.User](c, "runtime test password\n", "login", "--email", "runtime@example.test", "--password-stdin")
	if user := cliValue[management.User](c, "", "whoami"); user.ID != f.actor {
		t.Fatal(user)
	}
	if fleet := cliValue[management.FleetOverview](c, "", "fleet", "show"); len(fleet.Agents) != 1 {
		t.Fatal(fleet)
	}
}

type managedCLI struct {
	t       *testing.T
	args    []string
	session string
}

func newManagedCLI(t *testing.T, origin string) managedCLI {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session.json")
	return managedCLI{t: t, session: path, args: []string{"--server", origin, "--insecure-http", "--session-file", path}}
}

func (c managedCLI) invoke(input string, args ...string) ([]byte, error) {
	c.t.Helper()
	var out, stderr bytes.Buffer
	err := clientcli.Execute(context.Background(), append(append([]string{}, c.args...), args...), strings.NewReader(input), &out, &stderr)
	if stderr.Len() != 0 {
		c.t.Fatalf("unexpected CLI stderr: %s", &stderr)
	}
	return out.Bytes(), err
}

func cliValue[T any](c managedCLI, input string, args ...string) T {
	c.t.Helper()
	data, err := c.invoke(input, args...)
	if err != nil {
		c.t.Fatal(args, err)
	}
	var value T
	if err := json.Unmarshal(data, &value); err != nil {
		c.t.Fatal(err)
	}
	return value
}

func TestManagedClientCLIConversationAndTenantSelection(t *testing.T) {
	f := managedRuntimeHTTP(t, func(w http.ResponseWriter, r *http.Request) { streamManagedReply(w, "CLI reply") })
	c := newManagedCLI(t, f.origin)
	data, err := c.invoke("runtime test password\n", "login", "--email", "runtime@example.test", "--password-stdin")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("token")) || bytes.Contains(data, []byte("password")) {
		t.Fatal("login printed credentials")
	}
	info, err := os.Stat(c.session)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("credential mode", info, err)
	}
	if u := cliValue[management.User](c, "", "whoami"); u.ID != f.actor {
		t.Fatal(u)
	}
	if fleet := cliValue[management.FleetOverview](c, "", "fleet", "show"); len(fleet.Agents) != 1 || fleet.UserID != f.actor {
		t.Fatal(fleet)
	}
	second, err := f.directory.CreateTenant(context.Background(), "Second", f.actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.invoke("", "fleet", "show"); err == nil || !strings.Contains(err.Error(), "select an active tenant") {
		t.Fatal(err)
	}
	cliValue[any](c, "", "tenant", "use", second.ID)
	if fleet := cliValue[management.FleetOverview](c, "", "fleet", "show"); fleet.TenantID != second.ID || len(fleet.Agents) != 0 {
		t.Fatal(fleet)
	}
	cliValue[any](c, "", "tenant", "use", f.tenant)
	agent := cliValue[management.Agent](c, `{"name":"CLI-created"}`, "agent", "create")
	archived := cliValue[management.Agent](c, "", "agent", "archive", agent.ID, "--version", strconv.FormatInt(agent.Version, 10))
	if archived.Status != management.AgentArchived {
		t.Fatal(archived)
	}
	if _, err := c.invoke("", "agent", "restore", agent.ID, "--version", strconv.FormatInt(agent.Version, 10)); err == nil || !strings.Contains(err.Error(), "409") {
		t.Fatal("stale configuration accepted", err)
	}
	cliValue[management.Agent](c, "", "agent", "restore", agent.ID, "--version", strconv.FormatInt(archived.Version, 10))
	threads := cliValue[[]managedruntime.Thread](c, "", "thread", "--agent", f.agent.ID, "list")
	if len(threads) != 1 || threads[0].ID != f.main.ID {
		t.Fatal(threads)
	}
	input := cliValue[managedruntime.InputReceipt](c, "From the CLI", "thread", "--agent", f.agent.ID, "send", f.main.ID, "--request-id", "cli-message")
	dup := cliValue[managedruntime.InputReceipt](c, "From the CLI", "thread", "--agent", f.agent.ID, "send", f.main.ID, "--request-id", "cli-message")
	if input.ID != dup.ID {
		t.Fatal("CLI resend duplicated durable input")
	}
	stop := f.run(t)
	runtimeEventually(t, func() bool { return f.timeline(t, f.main.ID).Thread.State == "idle" })
	stop()
	usage := cliValue[managedruntime.UsageReport](c, "", "usage")
	if usage.Query.Group != "day" || usage.Totals.TotalTokens != 16 {
		t.Fatal(usage)
	}
	timeline := cliValue[managedruntime.Timeline](c, "", "thread", "--agent", f.agent.ID, "events", f.main.ID)
	if timeline.Thread.Sequence < 5 || timeline.NextSequence == 0 {
		t.Fatal(timeline)
	}
	worker := cliValue[managedruntime.Thread](c, "", "thread", "--agent", f.agent.ID, "worker", f.main.ID, "--name", "CLI worker", "--request-id", "cli-worker")
	dupWorker := cliValue[managedruntime.Thread](c, "", "thread", "--agent", f.agent.ID, "worker", f.main.ID, "--name", "CLI worker", "--request-id", "cli-worker")
	if worker.ID != dupWorker.ID || worker.Kind != "worker" {
		t.Fatal(worker, dupWorker)
	}
	cliValue[any](c, "", "thread", "--agent", f.agent.ID, "stop", worker.ID)
	cliValue[any](c, "", "thread", "--agent", f.agent.ID, "archive", worker.ID)
	cliValue[any](c, "", "thread", "--agent", f.agent.ID, "restore", worker.ID)
	credential, err := os.ReadFile(c.session)
	if err != nil {
		t.Fatal(err)
	}
	cliValue[any](c, "", "logout")
	if _, err := os.Stat(c.session); !os.IsNotExist(err) {
		t.Fatal("logout retained credentials", err)
	}
	if err := os.WriteFile(c.session, credential, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.invoke("", "whoami"); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatal("logout did not revoke remote session", err)
	}
}

func TestManagedClientCLIAuthorizationRemainsServerOwned(t *testing.T) {
	f := managedRuntimeHTTP(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected model call") })
	ctx := context.Background()
	u, err := f.directory.CreateUser(ctx, "cli-member@example.test")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := f.directory.Invite(ctx, f.actor, f.tenant, u.Email, management.Member, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.directory.AcceptInvitation(ctx, u.ID, token); err != nil {
		t.Fatal(err)
	}
	auth, err := managementpg.NewAuth(f.directory)
	if err != nil {
		t.Fatal(err)
	}
	link, err := auth.OperatorRecovery(ctx, u.Email)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.SetPassword(ctx, linkToken(t, link), "member test password"); err != nil {
		t.Fatal(err)
	}
	c := newManagedCLI(t, f.origin)
	cliValue[management.User](c, "member test password", "login", "--email", u.Email, "--password-stdin")
	if own := cliValue[management.FleetOverview](c, "", "fleet", "show"); own.UserID != u.ID {
		t.Fatal(own)
	}
	for _, args := range [][]string{
		{"--owner", f.actor, "fleet", "show"},
		{"member", "list"},
		{"agent", "get", f.agent.ID},
		{"request", "GET", "/tenants/" + f.tenant + "/users/" + f.actor + "/fleet"},
	} {
		if _, err := c.invoke("", args...); err == nil || !strings.Contains(err.Error(), "403") {
			t.Fatal("scope bypass", args, err)
		}
	}
	if _, err := f.directory.ChangeMember(ctx, f.actor, f.tenant, u.ID, management.Member, management.Suspended); err != nil {
		t.Fatal(err)
	}
	if _, err := c.invoke("", "fleet", "show"); err == nil {
		t.Fatal("suspended membership retained access")
	}
}
