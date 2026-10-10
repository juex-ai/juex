//go:build postgres

package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/juex-ai/juex/internal/app/managed"
	"github.com/juex-ai/juex/internal/app/migration"
	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/management"
	"github.com/juex-ai/juex/internal/migration/legacy"
)

func TestMigrationAgentConfigPreservesResolvedPolicyThroughManagement(t *testing.T) {
	_, directory := managementDatabase(t)
	ctx := context.Background()
	user, err := directory.CreateUser(ctx, "agent-import@example.test")
	if err != nil {
		t.Fatal(err)
	}
	tenant, err := directory.CreateTenant(ctx, "Agent import", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	model, err := directory.ConfigureModel(ctx, management.ModelConfiguration{Provider: "fixture", Name: "model", Protocol: llm.ProtocolOpenAIChat, Endpoint: "http://localhost:17777/v1", APIKey: "fixture-key", ContextWindow: 32768, OutputReserve: 8192, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := directory.SetTenantModels(ctx, tenant.ID, false, []string{model.ID}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, yaml, globalPath string
		calendar               bool
		wantPolicy             agentpolicy.Policy
	}{
		{"standard", "preset: standard\nenable_user_agents_resources: false\nmodules: {worker-threads: {max_depth: 2}}\n", "", true, agentpolicy.Policy{Version: 1, SkillSources: true, Disabled: []agentpolicy.Capability{agentpolicy.Collaboration}, Enabled: []agentpolicy.Capability{agentpolicy.ApplyPatch, agentpolicy.ChunkedWrite}}},
		{"minimal", "preset: minimal\nenable_user_agents_resources: false\nmodules: {worker-threads: {max_depth: 2}}\n", "", false, agentpolicy.Policy{Version: 1, Disabled: []agentpolicy.Capability{agentpolicy.Calendar, agentpolicy.Collaboration, agentpolicy.ContextControl, agentpolicy.Extensions, agentpolicy.FileSearch, agentpolicy.Skills, agentpolicy.Hooks, agentpolicy.InputTracking, agentpolicy.MCP, agentpolicy.Memory, agentpolicy.Notes, agentpolicy.Observations, agentpolicy.Tasks, agentpolicy.Workers, agentpolicy.WorkingFiles}}},
		{"global guidance", "preset: standard\nmodules: {worker-threads: {max_depth: 2}}\n", "/target/user/.agents/AGENTS.md", true, agentpolicy.Policy{Version: 1, SkillSources: true, Disabled: []agentpolicy.Capability{agentpolicy.Collaboration}, Enabled: []agentpolicy.Capability{agentpolicy.ApplyPatch, agentpolicy.ChunkedWrite}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolved, definition := resolvedMigrationAgentConfig(t, tc.name, tc.yaml)
			instructions, files, shell, collaboration := "Captured static instructions", true, true, false
			config, err := migration.ConvertAgentConfig(resolved, definition, migration.AgentConfigBindings{ModelID: model.ID, Instructions: &instructions, FilesEnabled: &files, ShellEnabled: &shell, CalendarEnabled: &tc.calendar, CollaborationEnabled: &collaboration, GlobalInstructionPath: tc.globalPath})
			if err != nil {
				t.Fatal(err)
			}
			agent, err := directory.CreateAgent(ctx, user.ID, tenant.ID, user.ID, config)
			if err != nil {
				t.Fatal(err)
			}
			read, err := directory.ReadAgent(ctx, user.ID, tenant.ID, agent.ID)
			if err != nil {
				t.Fatal(err)
			}
			stored := read.Agent
			want := tc.wantPolicy.Normalized()
			if stored.Name != tc.name || stored.Instructions != instructions || stored.WorkerDepth != 2 || !reflect.DeepEqual(stored.Configuration, *config.Configuration) || stored.DynamicInstructions != *config.DynamicInstructions || len(stored.Hooks) != 0 || len(stored.Extensions) != 0 {
				t.Fatal("stored initial configuration changed")
			}
			authority := managed.RuntimeAuthority{Directory: directory}
			scope, err := authority.Authorize(ctx, user.ID, tenant.ID, agent.ID, true)
			if err != nil {
				t.Fatal(err)
			}
			frozen, err := authority.Snapshot(ctx, scope)
			if err != nil || !reflect.DeepEqual(frozen.Capabilities, want) || frozen.DynamicInstructions != stored.DynamicInstructions || frozen.WorkerDepth != 2 || frozen.Instructions != instructions || len(frozen.Models) != 1 || frozen.Models[0].ModelID != model.ID {
				t.Fatalf("Runtime policy differs from stored converted configuration: got %+v, want %+v, error %v", frozen.Capabilities, want, err)
			}
		})
	}
}

func TestMigrationAgentConfigExplicitFilesChoiceReachesRuntimeCatalog(t *testing.T) {
	for _, tc := range []struct {
		name, modules string
		files, search bool
	}{
		{"minimal files enabled", "", true, false},
		{"minimal files disabled", "", false, false},
		{"search-only files enabled", "modules: {basic-file-tools: {enabled: false}, file-search: {enabled: true}}\n", true, true},
		{"search-only files disabled", "modules: {basic-file-tools: {enabled: false}, file-search: {enabled: true}}\n", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			f := executionDatabaseWithProvider(t, func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Tools []struct {
						Function struct{ Name string } `json:"function"`
					} `json:"tools"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				var names []string
				for _, tool := range body.Tools {
					names = append(names, tool.Function.Name)
				}
				for _, name := range []string{"read", "write", "edit"} {
					if slices.Contains(names, name) != tc.files {
						t.Errorf("target Files=%v did not govern %s", tc.files, name)
					}
				}
				for _, name := range []string{"grep", "glob"} {
					if slices.Contains(names, name) != tc.search {
						t.Errorf("source FileSearch=%v did not govern %s independently of Files=%v", tc.search, name, tc.files)
					}
				}
				for _, name := range []string{"update_notes", "list_tasks", "create_task", "update_task", "delete_task", "context_new", "context_compact"} {
					if slices.Contains(names, name) {
						t.Errorf("target Files=%v enabled separately disabled %s", tc.files, name)
					}
				}
				calls.Add(1)
				streamManagedReply(w, "Checked catalog")
			})
			resolved, definition := resolvedMigrationAgentConfig(t, tc.name, "preset: minimal\nenable_user_agents_resources: false\n"+tc.modules)
			instructions, shell, calendar, collaboration := "", true, false, false
			config, err := migration.ConvertAgentConfig(resolved, definition, migration.AgentConfigBindings{ModelID: f.agent.Configuration.Models[0], Instructions: &instructions, FilesEnabled: &tc.files, ShellEnabled: &shell, CalendarEnabled: &calendar, CollaborationEnabled: &collaboration})
			if err != nil {
				t.Fatal(err)
			}
			f.agent, err = f.directory.CreateAgent(context.Background(), f.actor, f.tenant, f.actor, config)
			if err != nil {
				t.Fatal(err)
			}
			f.base = f.origin + "/api/tenants/" + f.tenant + "/agents/" + f.agent.ID
			threads := managementCall[[]managedruntime.Thread](t, f.client, "GET", f.base+"/threads", f.origin, nil, 200)
			if len(threads) != 1 {
				t.Fatal("new Agent has no unique Main")
			}
			f.main = threads[0]
			runRuntimeTools(t, f, runtimeExecutionGateway(t, f))
			f.submit(t, "catalog", f.main.ID, "Check available tools")
			runtimeEventually(t, func() bool { return calls.Load() == 1 && f.timeline(t, f.main.ID).Thread.State == "idle" })
		})
	}
}

func resolvedMigrationAgentConfig(t *testing.T, name, sourceYAML string) (migration.ResolvedConfig, legacy.AgentDefinition) {
	t.Helper()
	digest := sha256.Sum256([]byte(sourceYAML))
	file := legacy.SourceFile{Path: "juex.yaml", Data: []byte(sourceYAML), Size: int64(len(sourceYAML)), SHA256: hex.EncodeToString(digest[:]), Mode: 0600}
	definition := legacy.AgentDefinition{ID: "abc234", Name: name, Workspace: "/source/work"}
	source := legacy.Fleet{SourceHome: "/source/home", DefaultHome: legacy.HomeConfig{Directory: "/source/home", Files: []legacy.SourceFile{file}}, Files: []legacy.SourceFile{file},
		Agents:     []legacy.Agent{{Definition: definition, AbsentFiles: []string{"juex.yaml"}}},
		Workspaces: []legacy.Workspace{{AgentID: definition.ID, Path: definition.Workspace, ResolvedPath: definition.Workspace, AbsentFiles: []string{".juex/juex.yaml"}}}}
	evidence := migration.ConfigEvidence{Contexts: map[string]migration.ConfigContext{definition.ID: {WorkingDirectory: definition.Workspace}}, Identities: map[string]string{}}
	for _, path := range []string{"/source/home/juex.yaml", "/source/work", "/source/work/.juex/juex.yaml", "/source/home/agents/abc234/juex.yaml"} {
		evidence.Identities[path] = path
	}
	resolved, err := migration.ResolveConfig(source, evidence)
	if err != nil {
		t.Fatal(err)
	}
	return resolved[0], definition
}
