package legacy

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestReadFleetCapturesOwnerStateAndConfigWithoutCopyingWorkspaces(t *testing.T) {
	dir, workspace := legacyFleetFixture(t)
	before := fixtureHashes(t, dir)
	got, err := ReadFleet(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "fleet_fixture" || len(got.Agents) != 1 || got.Memory == nil || len(got.Memory.Entries) != 1 {
		t.Fatal("lost Fleet ownership")
	}
	if len(got.Workspaces) != 1 || got.Workspaces[0].AgentID != "abc234" || got.Workspaces[0].Path != workspace {
		t.Fatal("lost Workspace binding")
	}
	if len(got.Workspaces[0].Files) != 1 || string(got.Workspaces[0].Files[0].Data) != "model: fixture:workspace\n" {
		t.Fatal("lost Workspace configuration")
	}
	found := map[string]bool{}
	for _, file := range got.Files {
		found[file.Path] = true
	}
	for _, name := range []string{"fleet.json", "juex.yaml", "fleet/supervisor.json", "agents/abc234/agent.json", "agents/abc234/threads/0/thread.json", "services/memory/state/state.json", "services/memory/memory/known_fact.md"} {
		if !found[name] {
			t.Fatal("missing snapshot file", name)
		}
	}
	if found["logs/live.log"] || found["workspaces/user-work.txt"] {
		t.Fatal("platform snapshot absorbed logs or user-owned files")
	}
	if !reflect.DeepEqual(before, fixtureHashes(t, dir)) {
		t.Fatal("Fleet reader changed source")
	}
}

func TestReadFleetRejectsUnknownOwnerAndDanglingWorkspace(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*testing.T, string, string)
	}{
		{"unfinished config publication", func(t *testing.T, dir, workspace string) {
			if err := os.MkdirAll(filepath.Join(dir, "cache/config-imports"), 0700); err != nil {
				t.Fatal(err)
			}
			writeFixture(t, filepath.Join(dir, "cache/config-imports/.publication-journal.json"), []byte(`{"version":2,"state":"prepared","entries":[]}`))
		}},
		{"unknown root", func(t *testing.T, dir, workspace string) {
			writeFixture(t, filepath.Join(dir, "other-state.json"), []byte(`{}`))
		}},
		{"unregistered Agent state", func(t *testing.T, dir, workspace string) {
			if err := os.Mkdir(filepath.Join(dir, "agents/.creating-something"), 0700); err != nil {
				t.Fatal(err)
			}
		}},
		{"missing Workspace", func(t *testing.T, dir, workspace string) {
			if err := os.Rename(workspace, workspace+"-moved"); err != nil {
				t.Fatal(err)
			}
		}},
		{"wrong Memory owner", func(t *testing.T, dir, workspace string) {
			mutateJSONFile(t, filepath.Join(dir, "services/memory/state/state.json"), func(v map[string]any) { v["fleet"] = "wrong" })
		}},
		{"unknown service", func(t *testing.T, dir, workspace string) {
			if err := os.Mkdir(filepath.Join(dir, "services/other-service"), 0700); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir, workspace := legacyFleetFixture(t)
			test.change(t, dir, workspace)
			before := fixtureHashes(t, dir)
			if _, err := ReadFleet(dir, dir); err == nil {
				t.Fatal("unsafe Fleet accepted")
			}
			if !reflect.DeepEqual(before, fixtureHashes(t, dir)) {
				t.Fatal("reader wrote to source")
			}
		})
	}
}

func TestReadFleetPreservesConfigImportCache(t *testing.T) {
	dir, _ := legacyFleetFixture(t)
	name := "cache/config-imports/source-declaring-context.json"
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0700); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(dir, name), []byte(`{"content":"model: provider:model"}`))
	got, err := ReadFleet(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range got.Files {
		if file.Path == name {
			return
		}
	}
	t.Fatal("lost last-known-good imported configuration bytes")
}

func TestWorkspaceResourcesCaptureExactInputsWithoutCopyingUserFiles(t *testing.T) {
	dir, workspace := legacyFleetFixture(t)
	if err := os.Mkdir(filepath.Join(workspace, ".agents"), 0700); err != nil {
		t.Fatal(err)
	}
	resources := map[string]string{
		".env":              "PROVIDER_MODEL=fixture:local\n",
		"AGENTS.md":         "workspace guidance\r\n",
		".agents/AGENTS.md": "local guidance\n",
	}
	for name, value := range resources {
		writeFixture(t, filepath.Join(workspace, name), []byte(value))
	}
	writeFixture(t, filepath.Join(workspace, "ordinary-user-file.txt"), []byte("not a config resource"))
	before := fixtureHashes(t, workspace)
	captured, err := ReadFleet(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, file := range captured.Workspaces[0].Files {
		got[file.Path] = string(file.Data)
	}
	for name, want := range resources {
		if got[name] != want {
			t.Fatalf("resource %s was not preserved exactly", name)
		}
	}
	if _, exists := got["ordinary-user-file.txt"]; exists || !reflect.DeepEqual(before, fixtureHashes(t, workspace)) {
		t.Fatal("capture changed or absorbed unrelated Workspace data")
	}
}

func TestWorkspaceResourcesRejectChangesAndLateCreation(t *testing.T) {
	for _, name := range []string{".env", "AGENTS.md", ".agents/AGENTS.md"} {
		for _, present := range []bool{false, true} {
			t.Run(name+fmt.Sprint("/present=", present), func(t *testing.T) {
				workspace := t.TempDir()
				if err := os.Mkdir(filepath.Join(workspace, ".agents"), 0700); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(workspace, name)
				if present {
					writeFixture(t, path, []byte("before"))
				}
				_, reader, err := readWorkspaceConfig(AgentDefinition{ID: "abc234", Workspace: workspace})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = reader.root.Close() })
				if err := reader.unchanged(); err != nil {
					t.Fatal(err)
				}
				writeFixture(t, path, []byte("after and changed size"))
				if err := reader.unchanged(); err == nil {
					t.Fatal("resource changed without invalidating capture")
				}
			})
		}
	}
}

func TestReadFleetCapturesExplicitSourceDefaultHome(t *testing.T) {
	dir, _ := legacyFleetFixture(t)
	defaultHome := filepath.Join(t.TempDir(), ".juex")
	if err := os.Mkdir(defaultHome, 0700); err != nil {
		t.Fatal(err)
	}
	const content = "model: fixture:source-default\n"
	writeFixture(t, filepath.Join(defaultHome, "juex.yaml"), []byte(content))
	// The invoking user's HOME is unrelated to the source user's configuration.
	t.Setenv("HOME", t.TempDir())
	got, err := ReadFleet(dir, defaultHome)
	if err != nil {
		t.Fatal(err)
	}
	if got.DefaultHome.Directory != defaultHome || len(got.DefaultHome.Files) != 1 || got.DefaultHome.Files[0].Path != "juex.yaml" || string(got.DefaultHome.Files[0].Data) != content {
		t.Fatal("lost explicit default-home configuration layer")
	}
	if got.SourceHome != dir {
		t.Fatal("default Home replaced instance Home")
	}
}

func TestReadFleetRecordsMissingDefaultHomeWithoutCreatingIt(t *testing.T) {
	dir, _ := legacyFleetFixture(t)
	defaultHome := filepath.Join(t.TempDir(), ".juex")
	got, err := ReadFleet(dir, defaultHome)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.DefaultHome.Files) != 0 || !reflect.DeepEqual(got.DefaultHome.AbsentFiles, []string{"juex.yaml"}) {
		t.Fatal("missing default configuration was not recorded")
	}
	if _, err := os.Lstat(defaultHome); !os.IsNotExist(err) {
		t.Fatal("reader created the absent default Home", err)
	}
	if _, err := ReadFleet(dir, ""); err == nil {
		t.Fatal("reader silently guessed source default Home")
	}
}

func TestDefaultHomeConfigRejectsChangesDuringCapture(t *testing.T) {
	for _, initiallyPresent := range []bool{false, true} {
		directory := filepath.Join(t.TempDir(), ".juex")
		if initiallyPresent {
			if err := os.Mkdir(directory, 0700); err != nil {
				t.Fatal(err)
			}
			writeFixture(t, filepath.Join(directory, "juex.yaml"), []byte("model: before\n"))
		}
		_, reader, err := readDefaultHomeConfig(directory)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = reader.root.Close() })
		if err := reader.unchanged(); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
		writeFixture(t, filepath.Join(directory, "juex.yaml"), []byte("model: changed\n"))
		if err := reader.unchanged(); err == nil {
			t.Fatal("default configuration changed without rejection")
		}
	}
}

func TestConfigPublicationMustRemainAbsent(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	r := sourceReader{root: root, stats: make(map[string]os.FileInfo)}
	if err := captureConfigImports(&r, &Fleet{}); err != nil {
		t.Fatal(err)
	}
	if err := r.unchanged(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "cache/config-imports"), 0700); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(dir, "cache/config-imports/.publication-journal.json"), []byte(`{}`))
	if err := r.unchanged(); err == nil {
		t.Fatal("config publication appeared during capture")
	}
}

func legacyFleetFixture(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"agents", "services", "fleet", "logs", "workspaces"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	agent := legacyAgentFixture(t)
	if err := os.Rename(agent, filepath.Join(dir, "agents/abc234")); err != nil {
		t.Fatal(err)
	}
	memory := legacyMemoryFixture(t)
	if err := os.Rename(memory, filepath.Join(dir, "services/memory")); err != nil {
		t.Fatal(err)
	}
	writeFixtureJSON(t, filepath.Join(dir, "fleet.json"), map[string]any{"id": "fleet_fixture"})
	writeFixture(t, filepath.Join(dir, "juex.yaml"), []byte("model: fixture:home\n"))
	writeFixtureJSON(t, filepath.Join(dir, "fleet/supervisor.json"), map[string]any{"agent_id": "abc234"})
	writeFixture(t, filepath.Join(dir, "logs/live.log"), []byte("runtime log"))
	writeFixture(t, filepath.Join(dir, "workspaces/user-work.txt"), []byte("user-owned"))
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, ".juex"), 0700); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(workspace, ".juex/juex.yaml"), []byte("model: fixture:workspace\n"))
	mutateJSONFile(t, filepath.Join(dir, "agents/abc234/agent.json"), func(v map[string]any) { v["workspace"] = workspace })
	return dir, workspace
}
