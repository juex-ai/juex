//go:build postgres

package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/entrypoints/managementcli"
	"github.com/juex-ai/juex/internal/entrypoints/runtimecli"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/management"
)

func TestManagementOperatorCLIAndHTTP(t *testing.T) {
	pool, _ := managementDatabase(t)
	t.Setenv("JUEX_DATABASE_URL", pool.Config().ConnString())
	t.Setenv("JUEX_MASTER_KEY", hex.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
	t.Setenv("JUEX_PUBLIC_URL", "http://localhost:8680")
	t.Setenv("JUEX_SMTP_CONFIG", "")
	ctx := context.Background()
	command := func(args ...string) []byte {
		t.Helper()
		var out, diagnostics bytes.Buffer
		if err := managementcli.Execute(ctx, append([]string{"--insecure-http"}, args...), &out, &diagnostics); err != nil {
			t.Fatal(args, err, diagnostics.String())
		}
		return out.Bytes()
	}
	command("migrate")
	credentials := filepath.Join(t.TempDir(), "services")
	command("services", "init", "--directory", credentials)
	t.Setenv("JUEX_SERVICE_CERTS", credentials)
	managementRPC, runtimeRPC := availablePlatformAddress(t), availablePlatformAddress(t)
	t.Setenv("JUEX_MANAGEMENT_RPC", managementRPC)
	t.Setenv("JUEX_RUNTIME_RPC", runtimeRPC)
	var bootstrap map[string]string
	if err := json.Unmarshal(command("bootstrap", "--tenant", "CLI tenant", "--email", "cli@example.test"), &bootstrap); err != nil {
		t.Fatal(err)
	}
	token := linkToken(t, bootstrap["setup_url"])
	t.Setenv("JUEX_MODEL_API_KEY", "private-model-fixture")
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		streamManagedReply(w, "Independent Runtime process contract")
	}))
	defer provider.Close()
	var model management.Model
	if err := json.Unmarshal(command("model", "put", "--provider", "fixture", "--name", "test-model", "--endpoint", provider.URL+"/v1"), &model); err != nil {
		t.Fatal(err)
	}
	command("model", "disable", model.ID)
	command("model", "enable", model.ID)

	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	reader, writer := io.Pipe()
	defer reader.Close()
	done := make(chan error, 1)
	go func() {
		err := managementcli.Execute(serveCtx, []string{"--insecure-http", "serve", "--listen", "0.0.0.0:0", "--rpc-listen", managementRPC}, writer, io.Discard)
		if err != nil {
			t.Log("Management server exited:", err)
		}
		_ = writer.CloseWithError(err)
		done <- err
	}()
	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(reader)
		if scanner.Scan() {
			ready <- scanner.Text()
		}
	}()
	var address string
	select {
	case line := <-ready:
		address = strings.TrimPrefix(line, "Management listening on ")
	case err := <-done:
		t.Fatal("server exited before listening", err)
	case <-time.After(10 * time.Second):
		t.Fatal("server startup timeout")
	}
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	base := "http://localhost:" + port
	runtimeDone := make(chan error, 1)
	go func() {
		runtimeDone <- runtimecli.Execute(serveCtx, []string{"serve", "--listen", runtimeRPC}, io.Discard, io.Discard)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-runtimeDone:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(15 * time.Second):
			t.Error("Runtime shutdown timeout")
		}
	})
	client := managementClient(t)
	managementCall[any](t, client, "POST", base+"/api/auth/set-password", "http://localhost:8680", map[string]string{"token": token, "password": "CLI integration password"}, 200)
	managementCall[management.Session](t, client, "POST", base+"/api/auth/login", "http://localhost:8680", map[string]string{"email": "cli@example.test", "password": "CLI integration password"}, 200)
	user := managementCall[management.User](t, client, "GET", base+"/api/auth/session", "", nil, 200)
	tenants := managementCall[[]management.TenantAccess](t, client, "GET", base+"/api/tenants", "", nil, 200)
	if len(tenants) != 1 {
		t.Fatal(tenants)
	}
	apiBase := base + "/api/tenants/" + tenants[0].ID
	models := managementCall[[]management.Model](t, client, "GET", apiBase+"/models", "", nil, 200)
	if len(models) != 1 || models[0].ID != model.ID {
		t.Fatal(models)
	}
	fleet := managementCall[management.FleetOverview](t, client, "GET", apiBase+"/fleet", "", nil, 200)
	settings := fleet.Settings
	settings.DefaultModelID = model.ID
	managementCall[management.FleetSettings](t, client, "PUT", apiBase+"/users/"+user.ID+"/fleet/settings", "http://localhost:8680", settings, 200)
	agent := managementCall[management.Agent](t, client, "POST", apiBase+"/users/"+user.ID+"/agents", "http://localhost:8680", management.AgentConfig{Name: "HTTP Agent"}, 200)
	runtimeBase := apiBase + "/agents/" + agent.ID
	runtimeEventually(t, func() bool {
		response, err := client.Get(runtimeBase + "/threads")
		if err != nil {
			return false
		}
		defer response.Body.Close()
		return response.StatusCode == 200
	})
	threads := managementCall[[]managedruntime.Thread](t, client, "GET", runtimeBase+"/threads", "", nil, 200)
	input := managedruntime.InputRequest{RequestID: "cli-rpc-input", ThreadID: threads[0].ID, Text: "Verify separate services"}
	managementCall[managedruntime.InputReceipt](t, client, "POST", runtimeBase+"/inputs", "http://localhost:8680", input, 200)
	runtimeEventually(t, func() bool {
		timeline := managementCall[managedruntime.Timeline](t, client, "GET", runtimeBase+"/threads/"+threads[0].ID+"/events", "", nil, 200)
		for _, event := range timeline.Events {
			var message llm.Message
			if event.Kind == "message.appended" && json.Unmarshal(event.Data, &message) == nil && message.FirstText() == "Independent Runtime process contract" {
				return true
			}
		}
		return false
	})
	updated := managementCall[management.Agent](t, client, "PUT", apiBase+"/agents/"+agent.ID, "http://localhost:8680", map[string]any{"name": "Updated Agent", "instructions": "Be concise", "model_id": model.ID, "version": agent.Version}, 200)
	managementCall[any](t, client, "POST", apiBase+"/agents/"+agent.ID+"/archive", "http://localhost:8680", map[string]any{"version": updated.Version, "archived": true}, 200)
	fleet = managementCall[management.FleetOverview](t, client, "GET", apiBase+"/fleet", "", nil, 200)
	if len(fleet.Agents) != 1 || fleet.Agents[0].Status != management.AgentArchived {
		t.Fatal(fleet)
	}
	for path, status := range map[string]int{"/healthz": 200, "/login": 200, "/api/unknown": 404} {
		response, err := http.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != status {
			t.Fatal(path, response.StatusCode)
		}
	}
	command("recover", "--email", "cli@example.test")
	command("tenant", "create", "--name", "Second", "--admin-email", "cli@example.test")
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("graceful shutdown timeout")
	}
}

func availablePlatformAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}
