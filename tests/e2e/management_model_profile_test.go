//go:build postgres

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/app/managed"
	"github.com/juex-ai/juex/internal/entrypoints/managementcli"
	serverrpc "github.com/juex-ai/juex/internal/entrypoints/platformrpc"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/managedruntime"
	runtimeclient "github.com/juex-ai/juex/internal/managedruntime/rpc"
	"github.com/juex-ai/juex/internal/management"
)

func TestManagedModelProfilePreservesCodexIdentity(t *testing.T) {
	pool, directory := managementDatabase(t)
	ctx := context.Background()
	user, err := directory.CreateUser(ctx, "codex-profile@example.test")
	if err != nil {
		t.Fatal(err)
	}
	tenant, err := directory.CreateTenant(ctx, "Profile", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	config := management.ModelConfiguration{Provider: "openai-codex", Name: "fixture-model", Endpoint: "https://chatgpt.com/backend-api/codex", Protocol: llm.ProtocolOpenAICodexResponses, APIKey: "test-access-token", ContextWindow: 32768, MaxOutput: 4096, OutputReserve: 8192, Enabled: true}
	vision := true
	config.Options = management.ModelOptions{ThinkingEffort: "xhigh", Headers: map[string]string{"ChatGPT-Account-ID": "test-private-account", "X-Thread": "${juex_thread_id}"}, Query: map[string]string{"route": "private-route"}, Capabilities: llm.CapabilityOverrides{Vision: &vision}, Compat: llm.CompatOptions{CodexTransport: "sse"}}
	model, err := managed.ConfigureModel(ctx, directory, config)
	if err != nil {
		t.Fatal("operator rejected supported Codex adapter", err)
	}
	agent, err := directory.CreateAgent(ctx, user.ID, tenant.ID, user.ID, management.AgentConfig{Name: "Codex", Configuration: &management.Configuration{Models: []string{model.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	authority := managed.RuntimeAuthority{Directory: directory}
	scope, err := authority.Authorize(ctx, user.ID, tenant.ID, agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := authority.Snapshot(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := authority.Profile(ctx, scope, plan.Models[0])
	if err != nil || profile.ID != config.Provider || profile.Protocol != config.Protocol || profile.APIKey != config.APIKey {
		t.Fatal("provider identity or credential lost", err)
	}
	if profile.ThinkingEffort != "xhigh" || !profile.Capabilities.Vision || profile.Compat.CodexTransport != "sse" || !reflect.DeepEqual(profile.Headers, config.Options.Headers) || !reflect.DeepEqual(profile.Query, config.Options.Query) {
		t.Fatal("private provider options lost")
	}
	var stored []byte
	if err := pool.QueryRow(ctx, `SELECT options_cipher FROM management.models WHERE id=$1`, model.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	public, err := json.Marshal(model)
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{config.APIKey, "test-private-account", "private-route"} {
		for _, surface := range [][]byte{stored, public, frozen} {
			if bytes.Contains(surface, []byte(secret)) {
				t.Fatal("private options entered plaintext persistence or a public/frozen model")
			}
		}
	}
	// Only the Runtime service identity may retrieve the connection profile.
	pki := filepath.Join(t.TempDir(), "pki")
	if err := platformrpc.CreateCredentials(pki); err != nil {
		t.Fatal(err)
	}
	listener := platformListener(t)
	server, err := serverrpc.NewManagement(listener, platformrpc.CredentialsAt(pki, "management"), authority)
	if err != nil {
		t.Fatal(err)
	}
	runPlatformRPC(t, server)
	var received llm.ProviderProfile
	factoryCalls := 0
	factory := func(value llm.ProviderProfile) (llm.Provider, error) {
		factoryCalls++
		received = value
		return nil, nil
	}
	rpc, err := runtimeclient.NewAuthority(listener.Addr().String(), platformrpc.CredentialsAt(pki, "runtime"), factory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rpc.Provider(ctx, scope, plan.Models[0], managedruntime.ModelRequirements{}); err != nil || !reflect.DeepEqual(received, profile) {
		t.Fatal("private options lost over authenticated Runtime RPC", err)
	}
	if profile.Capabilities.MaxOutputTokens {
		t.Fatal("fixture must retain the Codex default without output caps")
	}
	if p, err := authority.Provider(ctx, scope, plan.Models[0], managedruntime.ModelRequirements{OutputLimit: true}); p != nil || !errors.Is(err, managedruntime.ErrModelUnavailable) {
		t.Error("local authority admitted an unsupported bounded request", err)
	}
	before := factoryCalls
	if p, err := rpc.Provider(ctx, scope, plan.Models[0], managedruntime.ModelRequirements{OutputLimit: true}); p != nil || !errors.Is(err, managedruntime.ErrModelUnavailable) || factoryCalls != before {
		t.Error("RPC authority invoked factory for unsupported bounded request", err, factoryCalls, before)
	}
	other, err := runtimeclient.NewAuthority(listener.Addr().String(), platformrpc.CredentialsAt(pki, "execution"), factory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Provider(ctx, scope, plan.Models[0], managedruntime.ModelRequirements{}); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("Execution obtained the private model profile", err)
	}
	// A new credential for the same account and route keeps a frozen plan usable.
	config.APIKey = "rotated-access-token"
	if _, err := managed.ConfigureModel(ctx, directory, config); err != nil {
		t.Fatal(err)
	}
	rotated, err := authority.Profile(ctx, scope, plan.Models[0])
	if err != nil || rotated.APIKey != config.APIKey {
		t.Fatal("credential rotation revoked unchanged route", err)
	}
	for _, change := range []func(){
		func() { config.Options.Headers["ChatGPT-Account-ID"] = "different-account" },
		func() { config.Options.Headers["ChatGPT-Account-ID"] = "test-private-account" },
		func() { config.Options.ThinkingEffort = "high" },
		func() { config.Options.Query["route"] = "different-route" },
		func() { config.MaxOutput++ },
		func() { config.OutputReserve++ },
	} {
		previous, err := authority.Snapshot(ctx, scope)
		if err != nil {
			t.Fatal(err)
		}
		change()
		if _, err := managed.ConfigureModel(ctx, directory, config); err != nil {
			t.Fatal(err)
		}
		if _, err := authority.Profile(ctx, scope, previous.Models[0]); !errors.Is(err, managedruntime.ErrModelUnavailable) {
			t.Fatal("changed route/account/behavior used a frozen plan", err)
		}
	}
	if _, err := authority.Profile(ctx, scope, plan.Models[0]); !errors.Is(err, managedruntime.ErrModelUnavailable) {
		t.Fatal("restoring old account revived old plan", err)
	}
	for _, headers := range []map[string]string{nil, {"ChatGPT-Account-ID": ""}, {"ChatGPT-Account-ID": "${juex_agent_id}"}, {"ChatGPT-Account-ID": "one", "chatgpt-account-id": "two"}} {
		invalid := config
		invalid.Options.Headers = headers
		if _, err := managed.ConfigureModel(ctx, directory, invalid); !errors.Is(err, management.ErrInvalid) {
			t.Fatal("Codex accepted an implicit or ambiguous account route", err)
		}
	}
	// HTTP names are case insensitive; normalization must not leave the adapter
	// free to fall back to a token-derived account or mutate the caller's map.
	config.Options.Headers = map[string]string{"CHATGPT-ACCOUNT-ID": "case-account"}
	if _, err := managed.ConfigureModel(ctx, directory, config); err != nil {
		t.Fatal("explicit case-insensitive account route rejected", err)
	}
	fresh, err := authority.Snapshot(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := authority.Profile(ctx, scope, fresh.Models[0])
	if err != nil || canonical.Headers["ChatGPT-Account-ID"] != "case-account" || len(canonical.Headers) != 1 || config.Options.Headers["CHATGPT-ACCOUNT-ID"] != "case-account" {
		t.Fatal("account route was not pinned canonically", err)
	}
}

func TestManagedModelProfileRetainsExplicitProtocol(t *testing.T) {
	observed := make(chan string, 1)
	f := managedRuntimeHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		observed <- r.Header.Get("Authorization")
		streamManagedReply(w, "existing model still works")
	})
	ctx := context.Background()
	// This was a valid stored configuration before private options existed:
	// provider labels were operator-defined, and explicit protocol was authority.
	if _, err := f.pool.Exec(ctx, `UPDATE management.models SET provider='openai',options_cipher=NULL WHERE id=$1`, f.agent.Configuration.Models[0]); err != nil {
		t.Fatal(err)
	}
	scope, err := f.authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := f.authority.Snapshot(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := f.authority.Profile(ctx, scope, plan.Models[0])
	if err != nil || profile.ID != "openai" || profile.Protocol != llm.ProtocolOpenAIChat {
		t.Fatal("preset name overrode the stored explicit protocol", err)
	}
	provider, err := f.authority.Provider(ctx, scope, plan.Models[0], managedruntime.ModelRequirements{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Complete(ctx, "", []llm.Message{llm.TextMessage(llm.RoleUser, "continue")}, nil); err != nil {
		t.Fatal("old valid model stopped working", err)
	}
	if <-observed != "Bearer test-key" {
		t.Fatal("old authenticated model credential changed")
	}
}

func TestManagedModelProfileExplicitNoAuthentication(t *testing.T) {
	pool, directory := managementDatabase(t)
	ctx := context.Background()
	observed := make(chan string, 1)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observed <- r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"reply","object":"chat.completion","model":"local-model","choices":[{"index":0,"message":{"role":"assistant","content":"local response"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	t.Cleanup(endpoint.Close)
	streaming := false
	config := management.ModelConfiguration{Provider: "local", Name: "local-model", Protocol: llm.ProtocolOpenAIChat, Endpoint: endpoint.URL + "/v1", ContextWindow: 32768, MaxOutput: 4096, OutputReserve: 8192, Enabled: true}
	config.Options.Capabilities.Streaming = &streaming
	if _, err := managed.ConfigureModel(ctx, directory, config); !errors.Is(err, management.ErrInvalid) {
		t.Fatal("missing key silently became no authentication", err)
	}
	config.Options.Authentication = "none"
	model, err := managed.ConfigureModel(ctx, directory, config)
	if err != nil {
		t.Fatal("explicit local model authentication rejected", err)
	}
	user, err := directory.CreateUser(ctx, "no-auth@example.test")
	if err != nil {
		t.Fatal(err)
	}
	tenant, err := directory.CreateTenant(ctx, "Local", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := directory.CreateAgent(ctx, user.ID, tenant.ID, user.ID, management.AgentConfig{Name: "Local", Configuration: &management.Configuration{Models: []string{model.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	authority := managed.RuntimeAuthority{Directory: directory}
	scope, err := authority.Authorize(ctx, user.ID, tenant.ID, agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := authority.Snapshot(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := authority.Profile(ctx, scope, plan.Models[0])
	if err != nil || profile.APIKey != "" || profile.ID != "local" {
		t.Fatal("local model received a fabricated credential", err)
	}
	provider, err := authority.Provider(ctx, scope, plan.Models[0], managedruntime.ModelRequirements{})
	if err != nil {
		t.Fatal("provider factory rejected explicit no authentication", err)
	}
	if _, err := provider.Complete(ctx, "", []llm.Message{llm.TextMessage(llm.RoleUser, "Hello")}, nil); err != nil {
		t.Fatal("unauthenticated protocol request failed", err)
	}
	if <-observed != "" {
		t.Fatal("unauthenticated endpoint received an Authorization header")
	}
	config.APIKey = "unexpected-key"
	if _, err := managed.ConfigureModel(ctx, directory, config); !errors.Is(err, management.ErrInvalid) {
		t.Fatal("authentication none silently ignored a supplied key", err)
	}
	// An old row has no options ciphertext and must retain the api_key default.
	config.Options = management.ModelOptions{Authentication: "api_key"}
	if _, err := managed.ConfigureModel(ctx, directory, config); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE management.models SET options_cipher=NULL WHERE id=$1`, model.ID); err != nil {
		t.Fatal(err)
	}
	plan, err = authority.Snapshot(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := managed.ConfigureModel(ctx, directory, config); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.Profile(ctx, scope, plan.Models[0]); err != nil {
		t.Fatal("writing defaults revoked an unchanged old-schema model", err)
	}
}

func TestManagementModelCLIPrivateOptions(t *testing.T) {
	pool, _ := managementDatabase(t)
	t.Setenv("JUEX_DATABASE_URL", pool.Config().ConnString())
	t.Setenv("JUEX_MASTER_KEY", strings.Repeat("ab", 32))
	t.Setenv("JUEX_MODEL_API_KEY", "must-not-be-used-for-auth-none")
	t.Setenv("JUEX_MAINTENANCE_DIR", "")
	path := filepath.Join(t.TempDir(), "options.json")
	if err := os.WriteFile(path, []byte(`{"authentication":"none","thinking_effort":"high","headers":{"X-Account":"private-cli-account"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	var out, diagnostics bytes.Buffer
	err := managementcli.Execute(context.Background(), []string{"--public-url", "http://localhost:8680", "--insecure-http", "model", "put", "--provider", "local", "--name", "local-model", "--endpoint", "http://localhost:11434/v1", "--options-file", path}, &out, &diagnostics)
	if err != nil {
		t.Fatal(err)
	}
	var model management.Model
	if err := json.Unmarshal(out.Bytes(), &model); err != nil || model.ID == "" {
		t.Fatal("model put did not return its public identity", err)
	}
	for _, secret := range []string{"private-cli-account", "must-not-be-used-for-auth-none"} {
		if strings.Contains(out.String()+diagnostics.String(), secret) {
			t.Fatal("private model configuration leaked through CLI")
		}
	}
}
