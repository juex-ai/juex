package migration

import (
	"encoding/json"
	"maps"
	"reflect"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/migration/legacy"
)

func TestMigrationEnvironmentUsesCapturedEffectiveValuesOnly(t *testing.T) {
	source, evidence := configFixture()
	base := source.Files[0]
	base = configFile(base.Path, string(base.Data)+"environment: {variables: {SHARED: disk-private, HOME_KEY: home-private}}\n")
	source.Files[0], source.DefaultHome.Files[0] = base, base
	source.Workspaces[0].Files = []legacy.SourceFile{configFile(".env", "SHARED=dotenv-private\nEMPTY=\nLITERAL='$OTHER'\n")}
	source.Agents[0].AbsentFiles = nil
	source.Agents[0].Files = []legacy.SourceFile{configFile("juex.yaml", "environment: {variables: {SHARED: agent-private}}\nfleet_client: {profile: supervisor}\nmodules: {memory: {profile: agent}}\n")}
	configs, err := ResolveConfig(source, evidence)
	if err != nil {
		t.Fatal(err)
	}
	config := configs[0]
	if !config.AgentManagement || config.MemoryProfile != "agent" {
		t.Fatal("independent source permissions collapsed")
	}
	snapshot := ModelEvidence{AgentID: config.AgentID, Environment: map[string]*string{
		"SHARED": textPointer("inherited-private"), "HOME_KEY": textPointer("home-private"), "EMPTY": textPointer(""), "LITERAL": textPointer("$OTHER"), "UNDECLARED": textPointer("do-not-copy-private"),
	}}
	t.Setenv("SHARED", "importer-wrong-value")
	got, err := convertProcessEnvironment(source, config, snapshot)
	want := map[string]string{"SHARED": "inherited-private", "HOME_KEY": "home-private", "EMPTY": "", "LITERAL": "$OTHER"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("effective source snapshot changed", err)
	}
	raw, _ := json.Marshal(config)
	if strings.Contains(string(raw), "private") {
		t.Fatal("private values entered configuration report")
	}
	for _, value := range []*string{nil, textPointer(string([]byte{0xff}))} {
		broken := snapshot
		broken.Environment = maps.Clone(snapshot.Environment)
		broken.Environment["SHARED"] = value
		if _, err := convertProcessEnvironment(source, config, broken); err == nil {
			t.Fatal("invalid effective evidence accepted")
		}
	}
	delete(snapshot.Environment, "SHARED")
	if _, err := convertProcessEnvironment(source, config, snapshot); err == nil {
		t.Fatal("missing evidence replaced with importer environment")
	}
}

func TestMigrationDotenvUsesFinalLoadSwitchAndRequiresInventory(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		source, evidence := configFixture()
		source.Workspaces[0].Files = []legacy.SourceFile{configFile(".env", "invalid secret content")}
		value := "false"
		if enabled {
			value = "true"
		}
		ctx := evidence.Contexts["abc234"]
		ctx.ExplicitFiles = []legacy.SourceFile{configFile("/source/explicit.yaml", "environment: {load_dotenv: "+value+"}\n")}
		evidence.Contexts["abc234"] = ctx
		evidence.Identities["/source/explicit.yaml"] = "/source/explicit.yaml"
		configs, err := ResolveConfig(source, evidence)
		if err != nil {
			t.Fatal(err)
		}
		_, err = convertProcessEnvironment(source, configs[0], ModelEvidence{AgentID: "abc234"})
		if (err != nil) != enabled {
			t.Fatal("dotenv ignored final explicit switch", err)
		}
	}
	source, evidence := configFixture()
	configs, err := ResolveConfig(source, evidence)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := convertProcessEnvironment(source, configs[0], ModelEvidence{AgentID: "abc234"}); err == nil {
		t.Fatal("unknown dotenv treated as absent")
	}
	source.Workspaces[0].AbsentFiles = append(source.Workspaces[0].AbsentFiles, ".env")
	if _, err := convertProcessEnvironment(source, configs[0], ModelEvidence{AgentID: "abc234"}); err != nil {
		t.Fatal(err)
	}
}

func TestCapturedDotenvLiteralGrammar(t *testing.T) {
	got, err := sourceDotenv([]byte("\ufeff# comment\nexport A=plain # tail\nB='${HOME}#literal' # tail\nC=\"one\\ntwo\\q\\\"\"\nD=\nE=abc#def\n"))
	want := map[string]string{"A": "plain", "B": "${HOME}#literal", "C": "one\ntwo\\q\"", "D": "", "E": "abc#def"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal(got, err)
	}
	for _, data := range []string{"A=x\nA=y", "A='unclosed", "A=\"ok\" extra", "HOME=wrong", "BAD-NAME=x", "A=x\x00", "A=" + string([]byte{0xff})} {
		if _, err := sourceDotenv([]byte(data)); err == nil {
			t.Fatal("invalid source dotenv accepted")
		}
	}
}

func TestBundleSupervisorRequiresOperatorAcceptance(t *testing.T) {
	source, inputs, header := bundleFixture(t)
	source.Agents[0].AbsentFiles = nil
	source.Agents[0].Files = append(source.Agents[0].Files, configFile("juex.yaml", "fleet_client: {profile: supervisor}\nmodules: {fleet-management: {enabled: true}}\n"))
	bundle := Bundle{conversionPolicy: 3, source: source, inputs: inputs, header: header}
	if _, err := bundle.Prepare(); err == nil {
		t.Fatal("source profile silently granted new authority")
	}
	policy := bundle.inputs.Agents["abc234"]
	yes, no := true, false
	policy.AgentManagement = &no
	bundle.inputs.Agents["abc234"] = policy
	if _, err := bundle.Prepare(); err == nil {
		t.Fatal("source Supervisor silently discarded")
	}
	policy.AgentManagement = &yes
	bundle.inputs.Agents["abc234"] = policy
	plan, err := bundle.Prepare()
	if err != nil || !plan.AgentManagement["abc234"] {
		t.Fatal("accepted Supervisor not preserved", err)
	}
	bundle.source.Agents[0].Files = []legacy.SourceFile{configFile("juex.yaml", "fleet_client: {profile: supervisor}\nmodules: {fleet-management: {enabled: false}}\n")}
	if _, err := bundle.Prepare(); err == nil {
		t.Fatal("disabled source management became granted")
	}
}

func TestMigrationEnvironmentValidatesSourceBeforeTarget(t *testing.T) {
	source, evidence := configFixture()
	source.Workspaces[0].AbsentFiles = append(source.Workspaces[0].AbsentFiles, ".env")
	source.Agents[0].AbsentFiles = nil
	source.Agents[0].Files = []legacy.SourceFile{configFile("juex.yaml", "environment:\n  variables:\n    TOKEN: "+strings.Repeat("a", 5000)+"\n")}
	configs, err := ResolveConfig(source, evidence)
	if err != nil {
		t.Fatal("valid source declaration rejected before effective override", err)
	}
	got, err := convertProcessEnvironment(source, configs[0], ModelEvidence{AgentID: "abc234", Environment: map[string]*string{"TOKEN": textPointer("short-effective")}})
	if err != nil || got["TOKEN"] != "short-effective" {
		t.Fatal(err)
	}
	for _, key := range []string{"home", "UserProfile", "juex_home", "workdir"} {
		source.Agents[0].Files = []legacy.SourceFile{configFile("juex.yaml", "environment: {variables: {"+key+": bad}}\n")}
		if _, err := ResolveConfig(source, evidence); err == nil {
			t.Fatal("invalid source reserved key accepted", key)
		}
	}
}

func TestRetainedPolicyRejectsNewPrivateDeclarations(t *testing.T) {
	for _, yaml := range []string{"environment: null\n", "environment: {}\n", "environment: {variables: {TOKEN: private}}\n"} {
		source, inputs, header := bundleFixture(t)
		source.Agents[0].AbsentFiles = nil
		source.Agents[0].Files = append(source.Agents[0].Files, configFile("juex.yaml", yaml))
		bundle := Bundle{conversionPolicy: 2, source: source, inputs: inputs, header: header}
		if _, err := bundle.Prepare(); err == nil {
			t.Fatal("retained policy silently discarded environment")
		}
	}
	for _, granted := range []bool{false, true} {
		source, inputs, header := bundleFixture(t)
		policy := inputs.Agents["abc234"]
		policy.AgentManagement = &granted
		inputs.Agents["abc234"] = policy
		bundle := Bundle{conversionPolicy: 2, source: source, inputs: inputs, header: header}
		if _, err := bundle.Prepare(); err == nil {
			t.Fatal("retained policy silently discarded grant declaration")
		}
	}
}
