package migration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/foundation/application"
	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/migration/legacy"
)

func bundleFixture(t *testing.T) (legacy.Fleet, BundleInputs, BundleHeader) {
	t.Helper()
	dir := t.TempDir()
	home, work := filepath.Join(dir, "home"), filepath.Join(dir, "work")
	for _, path := range []string{filepath.Join(home, "agents/abc234"), work} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	_, evidence := configFixture()
	definition, _ := json.Marshal(legacy.AgentDefinition{ID: "abc234", Name: "Original", Workspace: work, Enabled: true, Autostart: true, CreatedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)})
	for name, data := range map[string][]byte{
		"fleet.json":               []byte(`{"id":"source-fleet"}`),
		"agents/abc234/agent.json": definition,
		"juex.yaml":                []byte("preset: minimal\nmodels: [local:model]\nproviders:\n  - id: local\n    protocol: openai/chat\n    base_url: https://model.example\n    api_key: private-model-key\n    models: [{id: model, context_window: 8192}]\n"),
	} {
		if err := os.WriteFile(filepath.Join(home, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	source, err := legacy.ReadFleet(home, home)
	if err != nil {
		t.Fatal(err)
	}
	evidence.Contexts = map[string]ConfigContext{"abc234": {WorkingDirectory: work}}
	evidence.Identities = map[string]string{}
	for _, path := range []string{work, filepath.Join(home, "juex.yaml"), filepath.Join(home, "agents/abc234/juex.yaml"), filepath.Join(work, ".juex/juex.yaml")} {
		evidence.Identities[path] = path
	}
	m := modelEvidence()
	m.ProcessWorkingDirectory = work
	empty, yes, no := "", true, false
	inputs := BundleInputs{Config: evidence, Models: []ModelEvidence{m}, Agents: map[string]BundleAgentPolicy{"abc234": {Activation: "on_demand", Instructions: &empty, FilesEnabled: &yes, ShellEnabled: &yes, CalendarEnabled: &no, CollaborationEnabled: &no, Workspace: work}}, ModelsPolicy: []BundleModelPolicy{{Key: ModelKey{Provider: "local", Name: "model"}, OutputReserve: 2048}}}
	header := BundleHeader{Target: BundleTarget{DeploymentID: "deployment-fixture", ActorID: "11111111-1111-4111-8111-111111111111", TenantID: "22222222-2222-4222-8222-222222222222", UserID: "11111111-1111-4111-8111-111111111111", FleetID: "33333333-3333-4333-8333-333333333333"}, CapturedAt: time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC), MemoryAdvancedSince: time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC), MemoryControl: application.Control{Enabled: true, Epoch: 1, Version: 1}}
	return source, inputs, header
}

func TestBundleRoundTripFreezesPrivateInputsWithoutSource(t *testing.T) {
	source, inputs, header := bundleFixture(t)
	inputs.Models[0].Environment["KNOWN_EMPTY"] = textPointer("")
	inputs.Models[0].Environment["KNOWN_ABSENT"] = nil
	auth := configFile("/source/private/auth.json", `{"private":"captured"}`)
	inputs.Models[0].CodexAuth = &auth
	inputs.Config.LocalImports = []legacy.SourceFile{configFile("/source/extra.yaml", "private: bytes\n")}
	context := inputs.Config.Contexts["abc234"]
	context.ExplicitFiles = []legacy.SourceFile{configFile("/source/second.yaml", "models: []\n"), configFile("/source/first.yaml", "models: null\n")}
	context.ModelRefs = []string{"second:model", "first:model"}
	inputs.Config.Contexts["abc234"] = context
	inputs.Extensions = []BundleExtension{{AgentID: "abc234", Snapshot: stdioSnapshot(stdioManifest, stdioServers), Bindings: map[string]MCPProcessBinding{"wire": {Executable: "/target/bin/wire", WorkingDirectory: "/target/process", RuntimeWorkDir: "/target/work"}}}}
	policy := inputs.Agents["abc234"]
	policy.ModelOrigins = map[string]map[string]managedruntime.ModelConfig{"0": {"same-message": {Provider: "first", Model: "first", ModelAuthorizationEpoch: 2}}, "worker": {"same-message": {Provider: "second", Model: "second", ModelAuthorizationEpoch: 3}}}
	inputs.Agents["abc234"] = policy
	dir := filepath.Join(t.TempDir(), "bundle")
	digest, err := WriteBundle(dir, source, inputs, header)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(source.SourceHome, source.SourceHome+"-unavailable"); err != nil {
		t.Fatal(err)
	}
	got, err := LoadBundle(dir, digest, header.Target)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.inputs, inputs) || !reflect.DeepEqual(got.source, source) || got.header != header {
		t.Fatal("private bytes, tri-state evidence or target policy lost")
	}
	if _, exists := got.inputs.Models[0].Environment["UNKNOWN"]; exists {
		t.Fatal("unknown environment became known")
	}
	printed, _ := json.Marshal(got.inputs)
	if strings.Contains(string(printed), "private") || strings.Contains(string(printed), "source/") {
		t.Fatal("private capture exposed as report")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 3 {
		t.Fatal("bundle is not the fixed three-file format")
	}
	if _, err := WriteBundle(dir, source, inputs, header); err == nil {
		t.Fatal("existing bundle overwritten")
	}
}

func TestBundlePrivateInputsRejectAmbiguityAndDataLoss(t *testing.T) {
	_, inputs, _ := bundleFixture(t)
	inputs.Config.LocalImports = []legacy.SourceFile{configFile("/source/private.yaml", "secret: original\n")}
	data, err := encodeBundleInputs(inputs)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(map[string]any){
		func(v map[string]any) { v["unknown"] = "private-value" },
		func(v map[string]any) { v["blobs"] = map[string]any{} },
		func(v map[string]any) { v["blobs"].(map[string]any)[strings.Repeat("0", 64)] = "ZXh0cmE=" },
		func(v map[string]any) {
			v["models"].([]any)[0].(map[string]any)["secret_unknown_field"] = "private-value"
		},
	} {
		var value map[string]any
		if err := json.Unmarshal(data, &value); err != nil {
			t.Fatal(err)
		}
		change(value)
		modified, _ := json.Marshal(value)
		if _, err := decodeBundleInputs(modified); err == nil {
			t.Fatal("incomplete or unknown private evidence accepted")
		}
	}
	duplicate := append([]byte(`{"blobs":{},`), data[1:]...)
	if _, err := decodeBundleInputs(duplicate); err == nil {
		t.Fatal("duplicate key accepted")
	}
	inputs.Models[0].Environment["bad"] = textPointer(string([]byte{0xff}))
	if _, err := encodeBundleInputs(inputs); err == nil {
		t.Fatal("invalid UTF-8 was silently replaced")
	}
}

func TestBundleRejectsTamperingAndUnsafeFiles(t *testing.T) {
	for _, name := range []string{"manifest.json", "fleet.capture.json", "private-inputs.json"} {
		for _, mutation := range []string{"missing", "bytes", "symlink", "permissions", "directory"} {
			t.Run(name+"/"+mutation, func(t *testing.T) {
				source, inputs, header := bundleFixture(t)
				dir := filepath.Join(t.TempDir(), "bundle")
				digest, err := WriteBundle(dir, source, inputs, header)
				if err != nil {
					t.Fatal(err)
				}
				p := filepath.Join(dir, name)
				switch mutation {
				case "missing":
					err = os.Remove(p)
				case "bytes":
					err = os.WriteFile(p, []byte("changed"), 0600)
				case "permissions":
					err = os.Chmod(p, 0644)
				case "directory":
					if err = os.Remove(p); err == nil {
						err = os.Mkdir(p, 0700)
					}
				case "symlink":
					if err = os.Rename(p, p+".original"); err == nil {
						err = os.Symlink(name+".original", p)
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				if _, err := LoadBundle(dir, digest, header.Target); err == nil {
					t.Fatal("unsafe or changed bundle accepted")
				}
			})
		}
	}
}

func TestBundleBindsDestinationAndConversionPolicy(t *testing.T) {
	source, inputs, header := bundleFixture(t)
	dir := filepath.Join(t.TempDir(), "bundle")
	digest, err := WriteBundle(dir, source, inputs, header)
	if err != nil {
		t.Fatal(err)
	}
	wrong := header.Target
	wrong.DeploymentID = "another-deployment"
	if _, err := LoadBundle(dir, digest, wrong); err == nil {
		t.Fatal("bundle accepted in another deployment")
	}
	if _, err := LoadBundle(dir, strings.Repeat("0", 64), header.Target); err == nil {
		t.Fatal("independent digest ignored")
	}
	got, err := LoadBundle(dir, digest, header.Target)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := got.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Models.Catalog) != 1 || plan.Models.Catalog[0].Configuration.APIKey != "private-model-key" || plan.Models.Catalog[0].Configuration.OutputReserve != 2048 || plan.Models.Catalog[0].Configuration.MaxOutput != 0 {
		t.Fatal("prepared catalog lost source policy")
	}
	got.inputs.Agents["abc234"] = BundleAgentPolicy{}
	if _, err := got.Prepare(); err == nil {
		t.Fatal("unresolved Agent policy accepted")
	}
}

func TestBundlePrepareRejectsInactiveOrMisrepresentedExtensions(t *testing.T) {
	for _, name := range []string{"minimal", "empty allow", "unprobed hooks", "selected"} {
		t.Run(name, func(t *testing.T) {
			source, inputs, header := bundleFixture(t)
			if name != "minimal" {
				for i, f := range source.Files {
					if f.Path == "juex.yaml" {
						data := strings.Replace(string(f.Data), "preset: minimal", "preset: standard", 1) + "enable_user_agents_resources: false\nextensions: {allow: [wire]}\n"
						if name == "empty allow" {
							data = strings.Replace(data, "allow: [wire]", "allow: []", 1)
						}
						changed := configFile(f.Path, data)
						source.Files[i] = changed
						for j, home := range source.DefaultHome.Files {
							if home.Path == f.Path {
								source.DefaultHome.Files[j] = changed
							}
						}
					}
				}
			}
			snapshot := stdioSnapshot(stdioManifest, stdioServers)
			if name == "unprobed hooks" {
				snapshot.Selection.Hooks = false
			}
			inputs.Extensions = []BundleExtension{{AgentID: "abc234", Snapshot: snapshot, Bindings: map[string]MCPProcessBinding{"wire": {Executable: "/target/bin/wire", WorkingDirectory: "/target/process", RuntimeWorkDir: "/target/work"}}}}
			b := Bundle{source: source, inputs: inputs, header: header}
			plan, err := b.Prepare()
			if name == "selected" {
				if err != nil || len(plan.Extensions["abc234"]) != 1 {
					t.Fatal("selected extension rejected", err)
				}
			} else if err == nil {
				t.Fatal("inactive or unprobed resources entered target plan")
			}
		})
	}
}

func TestBundlePrepareRequiresSelectedSourceCalendar(t *testing.T) {
	for _, variant := range []string{"extensions-disabled", "mcp-disabled", "not-allowed", "selected", "observables-disabled"} {
		t.Run(variant, func(t *testing.T) {
			source, inputs, header := bundleFixture(t)
			for i, f := range source.Files {
				if f.Path != "juex.yaml" {
					continue
				}
				data := strings.Replace(string(f.Data), "preset: minimal", "preset: standard", 1) + "enable_user_agents_resources: false\nextensions: {allow: [calendar]}\n"
				switch variant {
				case "extensions-disabled":
					data += "modules: {extensions: {enabled: false}}\n"
				case "mcp-disabled":
					data += "modules: {mcp: {enabled: false}}\n"
				case "not-allowed":
					data = strings.Replace(data, "allow: [calendar]", "allow: []", 1)
				case "observables-disabled":
					data += "modules: {observables: {enabled: false}}\n"
				}
				changed := configFile(f.Path, data)
				source.Files[i] = changed
				for j, home := range source.DefaultHome.Files {
					if home.Path == f.Path {
						source.DefaultHome.Files[j] = changed
					}
				}
			}
			source.Agents[0].Files = append(source.Agents[0].Files, configFile("extensions/calendar/calendar.json", `{"version":1,"entries":{}}`))
			yes := true
			policy := inputs.Agents["abc234"]
			policy.CalendarEnabled = &yes
			inputs.Agents["abc234"] = policy
			b := Bundle{source: source, inputs: inputs, header: header}
			_, err := b.Prepare()
			if variant == "selected" || variant == "observables-disabled" {
				if err != nil {
					t.Fatal("selected Calendar rejected", err)
				}
			} else if err == nil {
				t.Fatal("unselected legacy Calendar state accepted")
			}
		})
	}
}

func TestBundlePrepareBindsAgentsByIdentity(t *testing.T) {
	source, inputs, header := bundleFixture(t)
	second := source.Agents[0].Definition
	second.ID, second.Name = "abc235", "Second"
	dir := filepath.Join(source.SourceHome, "agents", second.ID)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(second)
	if err := os.WriteFile(filepath.Join(dir, "agent.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	var err error
	source, err = legacy.ReadFleet(source.SourceHome, source.DefaultHome.Directory)
	if err != nil {
		t.Fatal(err)
	}
	inputs.Config.Contexts[second.ID] = inputs.Config.Contexts["abc234"]
	path := filepath.Join(dir, "juex.yaml")
	inputs.Config.Identities[path] = path
	inputs.Agents[second.ID] = inputs.Agents["abc234"]
	model := inputs.Models[0]
	model.AgentID = second.ID
	inputs.Models = append(inputs.Models, model)
	b := Bundle{source: source, inputs: inputs, header: header}
	want, err := b.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	slices.Reverse(b.source.Agents)
	capture, digest, err := legacy.EncodeCapture(b.source)
	if err != nil {
		t.Fatal(err)
	}
	b.source, err = legacy.DecodeCapture(capture, digest)
	if err != nil {
		t.Fatal(err)
	}
	got, err := b.Prepare()
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("Agent order changed target identity or policy", err)
	}
}

func TestBundlePrepareRequiresExplicitTargetLifecycle(t *testing.T) {
	for _, autostart := range []bool{false, true} {
		source, inputs, header := bundleFixture(t)
		source.Agents[0].Definition.Autostart = autostart
		b := Bundle{source: source, inputs: inputs, header: header}
		if _, err := b.Prepare(); err != nil {
			t.Fatal("explicit target policy rejected enabled source", err)
		}
		policy := b.inputs.Agents["abc234"]
		policy.Activation = ""
		b.inputs.Agents["abc234"] = policy
		if _, err := b.Prepare(); err == nil {
			t.Fatal("missing lifecycle decision accepted")
		}
		policy.Activation = "on_demand"
		b.inputs.Agents["abc234"] = policy
		b.source.Agents[0].Definition.Enabled = false
		if _, err := b.Prepare(); err == nil {
			t.Fatal("disabled source silently enabled")
		}
	}
}
