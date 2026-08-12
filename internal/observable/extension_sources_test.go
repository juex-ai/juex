package observable_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/juex-ai/juex/internal/environment"
	"github.com/juex-ai/juex/internal/observable"
	"github.com/juex-ai/juex/internal/sandbox"
)

func TestManagerLoadsReadOnlyExtensionDefinitionsWithSource(t *testing.T) {
	dir := t.TempDir()
	project := validSpec("project-observable")
	writeObservableConfig(t, dir, project)
	extensionPath := filepath.Join(dir, "extension", "observables.json")
	writeObservableConfigPath(t, extensionPath, validSpec("extension-observable"))

	mgr, err := observable.NewManager(observable.ManagerOptions{
		ConfigPath: configPath(dir),
		StateDir:   stateDir(dir),
		WorkDir:    dir,
		ReadOnlyConfigSources: []observable.ReadOnlyConfigSource{{
			Path:   extensionPath,
			Source: "ext:demo",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mgr.Close() }()

	projectStatus, ok := mgr.Status().ByID("project-observable")
	if !ok || projectStatus.Source != "project" {
		t.Fatalf("project status = %+v ok=%v", projectStatus, ok)
	}
	extensionStatus, ok := mgr.Status().ByID("extension-observable")
	if !ok || extensionStatus.Source != "ext:demo" {
		t.Fatalf("extension status = %+v ok=%v", extensionStatus, ok)
	}
	before, err := os.ReadFile(configPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	err = mgr.Delete(context.Background(), "extension-observable")
	var readOnlyErr *observable.ReadOnlyDefinitionError
	if !errors.As(err, &readOnlyErr) || readOnlyErr.Source != "ext:demo" {
		t.Fatalf("Delete() err = %v, want ext:demo read-only error", err)
	}
	after, err := os.ReadFile(configPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("project config changed after extension delete\nbefore=%s\nafter=%s", before, after)
	}
	if _, ok := mgr.Status().ByID("extension-observable"); !ok {
		t.Fatal("extension status disappeared after rejected delete")
	}
	if _, err := mgr.Create(context.Background(), validSpec("extension-observable")); !errors.As(err, &readOnlyErr) {
		t.Fatalf("Create() err = %v, want read-only conflict", err)
	}
}

func TestManagerRejectsCrossSourceDuplicateDefinitions(t *testing.T) {
	dir := t.TempDir()
	writeObservableConfig(t, dir, validSpec("duplicate"))
	extensionPath := filepath.Join(dir, "extension", "observables.json")
	writeObservableConfigPath(t, extensionPath, validSpec("duplicate"))

	_, err := observable.NewManager(observable.ManagerOptions{
		ConfigPath: configPath(dir),
		StateDir:   stateDir(dir),
		WorkDir:    dir,
		ReadOnlyConfigSources: []observable.ReadOnlyConfigSource{{
			Path:   extensionPath,
			Source: "ext:demo",
		}},
	})
	if err == nil ||
		!strings.Contains(err.Error(), `"duplicate"`) ||
		!strings.Contains(err.Error(), "project") ||
		!strings.Contains(err.Error(), "ext:demo") {
		t.Fatalf("NewManager() err = %v, want both duplicate sources", err)
	}
}

func TestExtensionConfigIssuesAreDistinctAndDoNotBlockProjectCreate(t *testing.T) {
	dir := t.TempDir()
	extensionPath := filepath.Join(dir, "extension", "observables.json")
	writeRawObservableConfig(t, extensionPath, `{
  "observables": [
    {"id":"broken","type":"command","command_config":{}},
    {"id":"broken","type":"command","command_config":{}}
  ]
}`)
	mgr, err := observable.NewManager(observable.ManagerOptions{
		ConfigPath: configPath(dir),
		StateDir:   stateDir(dir),
		WorkDir:    dir,
		ReadOnlyConfigSources: []observable.ReadOnlyConfigSource{{
			Path:   extensionPath,
			Source: "ext:demo",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mgr.Close() }()

	statuses := mgr.Status().Observables
	if len(statuses) != 2 ||
		statuses[0].ID == statuses[1].ID ||
		statuses[0].Source != "ext:demo" ||
		statuses[1].Source != "ext:demo" {
		t.Fatalf("issue statuses = %+v, want two distinct ext:demo issues", statuses)
	}
	if _, err := mgr.Create(context.Background(), validSpec("created")); err != nil {
		t.Fatalf("Create() blocked by extension issues: %v", err)
	}
}

func TestReadOnlyConflictWinsBeforeProjectIssueEditGate(t *testing.T) {
	dir := t.TempDir()
	writeRawObservableConfig(t, configPath(dir), `{"observables":[{"id":"project-broken","type":"command","command_config":{}}]}`)
	extensionPath := filepath.Join(dir, "extension", "observables.json")
	writeObservableConfigPath(t, extensionPath, validSpec("extension-observable"))
	mgr, err := observable.NewManager(observable.ManagerOptions{
		ConfigPath: configPath(dir),
		StateDir:   stateDir(dir),
		WorkDir:    dir,
		ReadOnlyConfigSources: []observable.ReadOnlyConfigSource{{
			Path:   extensionPath,
			Source: "ext:demo",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mgr.Close() }()
	var readOnlyErr *observable.ReadOnlyDefinitionError
	if err := mgr.Delete(context.Background(), "extension-observable"); !errors.As(err, &readOnlyErr) {
		t.Fatalf("Delete() err = %v, want read-only conflict before project edit gate", err)
	}
	if _, err := mgr.Create(context.Background(), validSpec("extension-observable")); !errors.As(err, &readOnlyErr) {
		t.Fatalf("Create() err = %v, want read-only conflict before project edit gate", err)
	}
}

func TestExtensionDataPreparationIsDeferredUntilCommandStart(t *testing.T) {
	dir := t.TempDir()
	extensionDir := filepath.Join(dir, "extension")
	dataDir := filepath.Join(dir, "agent-state", "extensions", "demo")
	extensionPath := filepath.Join(extensionDir, "observables.json")
	spec := validSpec("extension-command")
	spec = mutateCommandSpec(spec, func(command *observable.CommandSourceSpec) {
		command.Command = "$JUEX_EXT_DIR/bin/helper"
		command.Args = []string{"$JUEX_EXT_DATA_DIR", "${WORKDIR}"}
	})
	writeObservableConfigPath(t, extensionPath, spec)
	var prepares atomic.Int32
	mgr, err := observable.NewManager(observable.ManagerOptions{
		ConfigPath: configPath(dir),
		StateDir:   stateDir(dir),
		WorkDir:    dir,
		ReadOnlyConfigSources: []observable.ReadOnlyConfigSource{{
			Path:   extensionPath,
			Source: "ext:demo",
			Runtime: observable.RuntimeContext{
				ExtensionDir:     extensionDir,
				ExtensionDataDir: dataDir,
				PrepareExtensionDataDir: func() error {
					prepares.Add(1)
					return os.MkdirAll(dataDir, 0o700)
				},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mgr.Close() }()
	if prepares.Load() != 0 {
		t.Fatalf("prepare count after load = %d, want 0", prepares.Load())
	}
	if err := mgr.Start(context.Background(), "extension-command"); err == nil {
		t.Fatal("Start() err = nil, want missing expanded helper")
	}
	if prepares.Load() != 0 {
		t.Fatalf("prepare count after command lookup failure = %d, want 0", prepares.Load())
	}
}

func TestExtensionCommandExpandsRuntimeAndGetsAgentStateSandboxRoot(t *testing.T) {
	dir := t.TempDir()
	extensionDir := filepath.Join(dir, "extension with spaces")
	if err := os.MkdirAll(extensionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	helper := copyObservableHelperExecutable(t, extensionDir)
	dataDir := filepath.Join(t.TempDir(), "agent state", "extensions", "demo")
	extensionPath := filepath.Join(extensionDir, "observables.json")
	spec := helperSpec("extension-runtime", "json-once")
	spec = mutateCommandSpec(spec, func(command *observable.CommandSourceSpec) {
		command.Command = "$JUEX_EXT_DIR/" + filepath.Base(helper)
		command.Args = []string{
			"-test.run=TestObservableHelperProcess",
			"--",
			"$JUEX_EXT_DATA_DIR",
			"${WORKDIR}",
			"json-once",
		}
		command.CWD = "${JUEX_WORKDIR}"
		command.Env["JUEX_EXT_DIR"] = "spoofed"
		command.Env["JUEX_EXT_DATA_DIR"] = "spoofed"
		command.Env["OBSERVABLE_RUNTIME_PATHS"] = "$JUEX_EXT_DIR|${JUEX_EXT_DATA_DIR}|$JUEX_EXT_DIR_SUFFIX"
	})
	writeObservableConfigPath(t, extensionPath, spec)
	snapshot, err := environment.Resolve(environment.Options{
		Inherited: []string{
			"PATH=" + os.Getenv("PATH"),
			"JUEX_EXT_DIR=inherited-spoof",
			"JUEX_EXT_DATA_DIR=inherited-spoof",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := &recordingSandboxRunner{}
	var prepares atomic.Int32
	agentStateDir := filepath.Dir(filepath.Dir(dataDir))
	mgr, err := observable.NewManager(observable.ManagerOptions{
		ConfigPath:    configPath(dir),
		StateDir:      stateDir(dir),
		WorkDir:       dir,
		AgentStateDir: agentStateDir,
		Environment:   snapshot,
		Sandbox: sandbox.Policy{
			Enabled: true,
			FileSystem: sandbox.FileSystemPolicy{
				OutsideWorkspace: sandbox.OutsideWorkspaceReadOnly,
			},
			Network: sandbox.NetworkPolicy{Enabled: true},
		},
		SandboxRunner: runner,
		ReadOnlyConfigSources: []observable.ReadOnlyConfigSource{{
			Path:   extensionPath,
			Source: "ext:demo",
			Runtime: observable.RuntimeContext{
				ExtensionDir:     extensionDir,
				ExtensionDataDir: dataDir,
				PrepareExtensionDataDir: func() error {
					prepares.Add(1)
					return os.MkdirAll(dataDir, 0o700)
				},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mgr.Close() }()
	if prepares.Load() != 0 {
		t.Fatalf("prepare count after load = %d, want 0", prepares.Load())
	}
	if err := mgr.Start(context.Background(), "extension-runtime"); err != nil {
		t.Fatal(err)
	}
	if prepares.Load() != 1 {
		t.Fatalf("prepare count = %d, want 1", prepares.Load())
	}
	if got := runner.last.WritableRoots; len(got) != 2 || got[0] != dir || got[1] != agentStateDir {
		t.Fatalf("writable roots = %v, want Workspace and AgentStateDir", got)
	}
	gotBinary, err := os.Stat(runner.last.Spec.Binary)
	if err != nil {
		t.Fatalf("stat expanded extension helper %q: %v", runner.last.Spec.Binary, err)
	}
	wantBinaryPath := filepath.Join(extensionDir, filepath.Base(helper))
	wantBinary, err := os.Stat(wantBinaryPath)
	if err != nil {
		t.Fatalf("stat expected extension helper %q: %v", wantBinaryPath, err)
	}
	if !os.SameFile(gotBinary, wantBinary) {
		t.Fatalf("binary = %q, want expanded extension helper %q", runner.last.Spec.Binary, wantBinaryPath)
	}
	if len(runner.last.Spec.Args) < 4 ||
		runner.last.Spec.Args[2] != dataDir ||
		runner.last.Spec.Args[3] != dir {
		t.Fatalf("args = %#v, want expanded data/work paths", runner.last.Spec.Args)
	}
	env := environmentMap(runner.last.Spec.Env)
	if env["JUEX_EXT_DIR"] != extensionDir || env["JUEX_EXT_DATA_DIR"] != dataDir {
		t.Fatalf("authoritative extension env = %#v", env)
	}
	wantPaths := extensionDir + "|" + dataDir + "|$JUEX_EXT_DIR_SUFFIX"
	if env["OBSERVABLE_RUNTIME_PATHS"] != wantPaths {
		t.Fatalf("runtime paths = %q, want %q", env["OBSERVABLE_RUNTIME_PATHS"], wantPaths)
	}
}

func TestProjectCommandGetsWorkspaceAndAgentStateSandboxRoots(t *testing.T) {
	dir := t.TempDir()
	writeObservableConfig(t, dir, helperSpec("project-runtime", "json-once"))
	agentStateDir := filepath.Join(t.TempDir(), "agents", "abcdef")
	runner := &recordingSandboxRunner{}
	mgr, err := observable.NewManager(observable.ManagerOptions{
		ConfigPath:    configPath(dir),
		StateDir:      stateDir(dir),
		WorkDir:       dir,
		AgentStateDir: agentStateDir,
		Environment:   environment.FromEnviron(os.Environ()),
		Sandbox: sandbox.Policy{
			Enabled:    true,
			FileSystem: sandbox.FileSystemPolicy{OutsideWorkspace: sandbox.OutsideWorkspaceReadOnly},
		},
		SandboxRunner: runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mgr.Close() }()
	if err := mgr.Start(context.Background(), "project-runtime"); err != nil {
		t.Fatal(err)
	}
	if got := runner.last.WritableRoots; len(got) != 2 || got[0] != dir || got[1] != agentStateDir {
		t.Fatalf("writable roots = %#v, want Workspace and AgentStateDir", got)
	}
}

func TestProjectCommandDoesNotCreateAgentStateDirWithoutSandbox(t *testing.T) {
	dir := t.TempDir()
	writeObservableConfig(t, dir, helperSpec("project-unsandboxed", "json-once"))
	agentStateDir := filepath.Join(t.TempDir(), "agents", "abcdef")
	mgr, err := observable.NewManager(observable.ManagerOptions{
		ConfigPath:    configPath(dir),
		StateDir:      stateDir(dir),
		WorkDir:       dir,
		AgentStateDir: agentStateDir,
		Environment:   environment.FromEnviron(os.Environ()),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mgr.Close() }()
	if err := mgr.Start(context.Background(), "project-unsandboxed"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(agentStateDir); !os.IsNotExist(err) {
		t.Fatalf("unsandboxed command created AgentStateDir: %v", err)
	}
}

func TestProjectCommandRejectsAndStripsExtensionEnvironment(t *testing.T) {
	dir := t.TempDir()
	spec := helperSpec("project-env", "json-once")
	writeObservableConfig(t, dir, spec)
	snapshot, err := environment.Resolve(environment.Options{
		Inherited: []string{
			"PATH=" + os.Getenv("PATH"),
			"JUEX_EXT_DIR=inherited-spoof",
			"JUEX_EXT_DATA_DIR=inherited-spoof",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := &recordingSandboxRunner{}
	mgr, err := observable.NewManager(observable.ManagerOptions{
		ConfigPath:    configPath(dir),
		StateDir:      stateDir(dir),
		WorkDir:       dir,
		Environment:   snapshot,
		Sandbox:       sandbox.Policy{Enabled: true},
		SandboxRunner: runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mgr.Close() }()
	if err := mgr.Start(context.Background(), spec.ID); err != nil {
		t.Fatal(err)
	}
	env := environmentMap(runner.last.Spec.Env)
	if _, ok := env["JUEX_EXT_DIR"]; ok {
		t.Fatalf("project inherited JUEX_EXT_DIR leaked: %#v", env)
	}
	if _, ok := env["JUEX_EXT_DATA_DIR"]; ok {
		t.Fatalf("project inherited JUEX_EXT_DATA_DIR leaked: %#v", env)
	}
	if got := runner.last.WritableRoots; len(got) != 1 || got[0] != dir {
		t.Fatalf("project writable roots = %v, want only Workspace without AgentStateDir", got)
	}

	rejectDir := t.TempDir()
	rejected := validSpec("project-reject")
	rejected = mutateCommandSpec(rejected, func(command *observable.CommandSourceSpec) {
		command.Args = []string{"${JUEX_EXT_DIR}"}
	})
	writeObservableConfig(t, rejectDir, rejected)
	rejectManager, err := observable.NewManager(observable.ManagerOptions{
		ConfigPath: configPath(rejectDir),
		StateDir:   stateDir(rejectDir),
		WorkDir:    rejectDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rejectManager.Close() }()
	if err := rejectManager.Start(context.Background(), rejected.ID); err == nil || !strings.Contains(err.Error(), "only available to extension definitions") {
		t.Fatalf("Start() err = %v, want explicit project extension variable rejection", err)
	}
}

func TestExtensionDataDirPreparesIndependentlyFromSandboxRoots(t *testing.T) {
	dir := t.TempDir()
	extensionDir := filepath.Join(dir, "extension")
	if err := os.MkdirAll(extensionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	helper := copyObservableHelperExecutable(t, extensionDir)
	dataDir := filepath.Join(t.TempDir(), "agent-state", "extensions", "demo")
	extensionPath := filepath.Join(extensionDir, "observables.json")
	spec := helperSpec("blocked-extension", "json-once")
	spec = mutateCommandSpec(spec, func(command *observable.CommandSourceSpec) {
		command.Command = "$JUEX_EXT_DIR/" + filepath.Base(helper)
	})
	writeObservableConfigPath(t, extensionPath, spec)
	var prepares atomic.Int32
	runner := &recordingSandboxRunner{}
	agentStateDir := filepath.Dir(filepath.Dir(dataDir))
	mgr, err := observable.NewManager(observable.ManagerOptions{
		ConfigPath:    configPath(dir),
		StateDir:      stateDir(dir),
		WorkDir:       dir,
		AgentStateDir: agentStateDir,
		Sandbox: sandbox.Policy{
			Enabled: true,
			FileSystem: sandbox.FileSystemPolicy{
				OutsideWorkspace: sandbox.OutsideWorkspaceReadOnly,
				BlockedPaths:     []string{dataDir},
			},
			Network: sandbox.NetworkPolicy{Enabled: true},
		},
		SandboxRunner: runner,
		ReadOnlyConfigSources: []observable.ReadOnlyConfigSource{{
			Path:   extensionPath,
			Source: "ext:demo",
			Runtime: observable.RuntimeContext{
				ExtensionDir:     extensionDir,
				ExtensionDataDir: dataDir,
				PrepareExtensionDataDir: func() error {
					prepares.Add(1)
					return os.MkdirAll(dataDir, 0o700)
				},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mgr.Close() }()
	err = mgr.Start(context.Background(), spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if prepares.Load() != 1 {
		t.Fatalf("prepare count = %d, want 1", prepares.Load())
	}
	if info, statErr := os.Stat(dataDir); statErr != nil || !info.IsDir() {
		t.Fatalf("prepared data dir stat = %+v err=%v", info, statErr)
	}
	if got := runner.last.WritableRoots; len(got) != 2 || got[0] != dir || got[1] != agentStateDir {
		t.Fatalf("writable roots = %v, want Workspace and AgentStateDir", got)
	}
	if got := runner.last.Policy.FileSystem.BlockedPaths; len(got) != 1 || got[0] != dataDir {
		t.Fatalf("blocked paths = %v, want %q", got, dataDir)
	}
}

func writeObservableConfigPath(t *testing.T, path string, specs ...observable.Spec) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := observable.SaveConfig(path, observable.FileConfig{Observables: specs}); err != nil {
		t.Fatal(err)
	}
}

func writeRawObservableConfig(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func environmentMap(entries []string) map[string]string {
	out := make(map[string]string, len(entries))
	for _, entry := range entries {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			out[key] = value
		}
	}
	return out
}
