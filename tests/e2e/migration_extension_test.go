//go:build linux || darwin

package e2e

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/app/migration"
	"github.com/juex-ai/juex/internal/execution/native"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/extensionpolicy"
	"github.com/juex-ai/juex/internal/migration/legacy"
)

type migratedMCPProcess struct {
	CWD          string
	WorkDir      string
	JuexWorkDir  string
	ExtensionDir string
	DataDir      string
	State        string
	Args         []string
	PID          int
	ChildPID     int
}

func TestMigrationStdioExtensionNativeProtocol(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cwd, workspace, installation := filepath.Join(base, "process 'cwd'"), filepath.Join(base, "runtime workspace"), filepath.Join(base, "installation")
	for _, p := range []string{cwd, workspace, installation} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(base, "startup-should-not-run")
	startup := filepath.Join(base, "startup.sh")
	if err := os.WriteFile(startup, []byte("printf unexpected > '"+marker+"'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"-test.run=^TestMigrationStdioMCPHelper$", "--", "two words", "quote'\"", "", "$HOME", "$(touch marker)", ";literal", "${WORKDIR}/file", "$WORKDIR_X"}
	source := migrationExtensionCapture(t, args, map[string]string{"MIGRATION_EXTENSION_HELPER": "1", "STATE": "$JUEX_EXT_DATA_DIR/private", "WORKDIR": "ignored", "JUEX_WORKDIR": "ignored", "JUEX_EXT_DIR": "ignored", "juex_ext_data_dir": "ignored", "BASH_ENV": startup, "ENV": startup})
	manifest, err := migration.ConvertMCPExtension(source, map[string]migration.MCPProcessBinding{"wire": {Executable: executable, WorkingDirectory: cwd, RuntimeWorkDir: workspace}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installation, "juex.extension.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	config := nativeConfig(t)
	agentID := uuid.NewString()
	config.EnvironmentID = uuid.NewString()
	config.WorkingDirectory = workspace
	config.Grants = map[string][]execprotocol.Capability{agentID: {execprotocol.Files, execprotocol.Shell, execprotocol.MCP}}
	engine := openNative(t, config)
	request := func(id, kind string, args any) execprotocol.Request {
		v := nativeRequest(t, id, kind, args)
		v.AgentID = agentID
		return v
	}
	wait := func(id string, predicate func(execprotocol.Snapshot) bool) execprotocol.Snapshot {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			v, err := engine.Snapshot(agentID, id, 0, 256<<10)
			if err != nil {
				t.Fatal(err)
			}
			if predicate(v) {
				return v
			}
			time.Sleep(10 * time.Millisecond)
		}
		v, _ := engine.Snapshot(agentID, id, 0, 256<<10)
		t.Fatal("migration MCP did not reach expected state", v)
		return execprotocol.Snapshot{}
	}
	run := func(r execprotocol.Request) execprotocol.Snapshot {
		t.Helper()
		if _, err := engine.Submit(r); err != nil {
			t.Fatal(err)
		}
		return wait(r.ID, func(s execprotocol.Snapshot) bool { return s.State.Terminal() })
	}
	inspected := run(request("inspect", "inspect_extension", native.FileArguments{Path: installation}))
	var catalog extensionpolicy.Catalog
	if inspected.State != execprotocol.Completed || json.Unmarshal([]byte(inspected.Text()), &catalog) != nil || catalog.Validate() != nil {
		t.Fatal(inspected)
	}
	resource := catalog.Manifest.MCP[0]
	connect := request("connect", "mcp_connect", native.MCPArguments{Command: resource.Command[0], Args: resource.Command[1:], WorkingDirectory: installation, Environment: resource.Environment, Extension: &execprotocol.ExtensionContext{BindingID: uuid.NewString(), Directory: installation}})
	if _, err := engine.Submit(connect); err != nil {
		t.Fatal(err)
	}
	wait(connect.ID, func(s execprotocol.Snapshot) bool { return strings.Contains(s.Text(), `"type":"connected"`) })
	list := run(request("list", "mcp_list", native.MCPArguments{ConnectionID: connect.ID}))
	if list.State != execprotocol.Completed || !strings.Contains(list.Text(), `"name":"inspect"`) {
		t.Fatal(list)
	}
	call := run(request("call", "mcp_call", native.MCPArguments{ConnectionID: connect.ID, Name: "inspect", Arguments: map[string]any{}}))
	var reply struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if call.State != execprotocol.Completed || json.Unmarshal([]byte(call.Text()), &reply) != nil || len(reply.Content) != 1 {
		t.Fatal(call)
	}
	var observed migratedMCPProcess
	if json.Unmarshal([]byte(reply.Content[0].Text), &observed) != nil {
		t.Fatal(reply)
	}
	wantArgs := []string{"two words", "quote'\"", "", "$HOME", "$(touch marker)", ";literal", workspace + "/file", "$WORKDIR_X"}
	if observed.CWD != cwd || observed.WorkDir != workspace || observed.JuexWorkDir != workspace || observed.ExtensionDir != installation || !reflect.DeepEqual(observed.Args, wantArgs) {
		t.Fatal(observed)
	}
	if !filepath.IsAbs(observed.DataDir) || observed.DataDir == installation || observed.DataDir == cwd || observed.DataDir == workspace || observed.State != filepath.Join(observed.DataDir, "private") {
		t.Fatal(observed)
	}
	if data, err := os.ReadFile(filepath.Join(observed.DataDir, "protocol-proof")); err != nil || string(data) != "called" {
		t.Fatal(string(data), err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("shell startup file executed", err)
	}
	if _, err := os.Stat(filepath.Join(cwd, "marker")); !os.IsNotExist(err) {
		t.Fatal("literal argument was evaluated", err)
	}
	event := wait(connect.ID, func(s execprotocol.Snapshot) bool {
		return strings.Contains(s.Text(), "migrated-notification") && strings.Contains(s.Text(), "migration-fixture-diagnostic")
	})
	if event.State != execprotocol.Running {
		t.Fatal(event)
	}
	if err := engine.Cancel(agentID, connect.ID); err != nil {
		t.Fatal(err)
	}
	terminal := wait(connect.ID, func(s execprotocol.Snapshot) bool { return s.State.Terminal() })
	if terminal.State != execprotocol.Cancelled {
		t.Fatal(terminal)
	}
	for _, pid := range []int{observed.PID, observed.ChildPID} {
		migrationProcessStopped(t, pid)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	engine = openNative(t, config)
	retained, err := engine.Snapshot(agentID, connect.ID, 0, 256<<10)
	if err != nil || retained.State != execprotocol.Cancelled || !strings.Contains(retained.Text(), "migrated-notification") || !strings.Contains(retained.Text(), "migration-fixture-diagnostic") {
		t.Fatal(retained, err)
	}

	for _, failure := range []string{"cwd", "executable"} {
		binding := migration.MCPProcessBinding{Executable: executable, WorkingDirectory: cwd, RuntimeWorkDir: workspace}
		if failure == "cwd" {
			binding.WorkingDirectory = filepath.Join(base, "missing cwd")
		} else {
			binding.Executable = filepath.Join(base, "missing executable")
		}
		manifest, err := migration.ConvertMCPExtension(source, map[string]migration.MCPProcessBinding{"wire": binding}, nil)
		if err != nil {
			t.Fatal(err)
		}
		c := manifest.MCP[0]
		failed := run(request("missing-"+failure, "mcp_connect", native.MCPArguments{Command: c.Command[0], Args: c.Command[1:], WorkingDirectory: installation, Environment: c.Environment}))
		if failed.State != execprotocol.Failed || strings.Contains(failed.Text(), `"type":"connected"`) {
			t.Fatal(failed)
		}
	}
}

func migrationExtensionCapture(t *testing.T, args []string, environment map[string]string) legacy.ExtensionSnapshot {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "wire")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "juex.extension.json"), []byte(`{"manifest_version":1,"name":"wire","version":"1.0.0"}`), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]any{"mcpServers": map[string]any{"wire": map[string]any{"command": "source-wire", "args": args, "env": environment}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "mcp.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := legacy.ReadExtension(directory, legacy.ExtensionResources{MCP: true, Hooks: true, Observables: true, Skills: true})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func migrationProcessStopped(t *testing.T, pid int) {
	t.Helper()
	if pid <= 0 {
		t.Fatal("missing process identity")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
		state := strings.TrimSpace(string(data))
		// Linux PID 1 may retain an orphan zombie after group cancellation. It has
		// exited and cannot run code; do not mistake delayed reaping for a live child.
		if state == "" && err != nil || strings.HasPrefix(state, "Z") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("MCP process %d remains live", pid)
}

func TestMigrationStdioMCPHelper(t *testing.T) {
	if os.Getenv("MIGRATION_EXTENSION_ENV_PROBE") == "1" {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"IFS": os.Getenv("IFS"), "PWD": os.Getenv("PWD"), "SHELLOPTS": os.Getenv("SHELLOPTS")})
		os.Exit(0)
	}
	if os.Getenv("MIGRATION_EXTENSION_HELPER") != "1" {
		return
	}
	// MCP servers may log diagnostics to stderr without corrupting JSON-RPC stdout.
	fmt.Fprintln(os.Stderr, "migration-fixture-diagnostic")
	child := exec.Command("/bin/sh", "-c", "exec sleep 120")
	if err := child.Start(); err != nil {
		os.Exit(2)
	}
	reader, writer := bufio.NewScanner(os.Stdin), json.NewEncoder(os.Stdout)
	for reader.Scan() {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params map[string]any  `json:"params"`
		}
		if json.Unmarshal(reader.Bytes(), &request) != nil {
			os.Exit(3)
		}
		if len(request.ID) == 0 {
			continue
		}
		var result any
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": request.Params["protocolVersion"], "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "migration-fixture", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "inspect", "inputSchema": map[string]any{"type": "object"}}}}
		case "tools/call":
			cwd, err := os.Getwd()
			if err != nil {
				os.Exit(4)
			}
			var args []string
			for i, arg := range os.Args {
				if arg == "--" {
					args = os.Args[i+1:]
					break
				}
			}
			value := migratedMCPProcess{CWD: cwd, WorkDir: os.Getenv("WORKDIR"), JuexWorkDir: os.Getenv("JUEX_WORKDIR"), ExtensionDir: os.Getenv("JUEX_EXT_DIR"), DataDir: os.Getenv("JUEX_EXT_DATA_DIR"), State: os.Getenv("STATE"), Args: args, PID: os.Getpid(), ChildPID: child.Process.Pid}
			if os.WriteFile(filepath.Join(value.DataDir, "protocol-proof"), []byte("called"), 0600) != nil {
				os.Exit(5)
			}
			encoded, err := json.Marshal(value)
			if err != nil {
				os.Exit(6)
			}
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": string(encoded)}}}
		default:
			result = map[string]any{}
		}
		if writer.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}) != nil {
			os.Exit(7)
		}
		if request.Method == "tools/call" {
			if writer.Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/claude/channel", "params": map[string]any{"content": "migrated-notification"}}) != nil {
				os.Exit(8)
			}
		}
	}
	_ = child.Process.Kill()
	_ = child.Wait()
	if reader.Err() != nil {
		fmt.Fprintln(os.Stderr, reader.Err())
		os.Exit(9)
	}
	os.Exit(0)
}

func TestMigrationStdioExtensionRejectsChangedProcessEnvironment(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{"IFS": ":", "PWD": "/synthetic/source", "SHELLOPTS": "xtrace"}
	command := exec.Command(executable, "-test.run=^TestMigrationStdioMCPHelper$")
	command.Env = append(os.Environ(), "MIGRATION_EXTENSION_ENV_PROBE=1")
	for key, value := range values {
		command.Env = append(command.Env, key+"="+value)
	}
	data, err := command.Output()
	var observed map[string]string
	if err != nil || json.Unmarshal(data, &observed) != nil || !reflect.DeepEqual(observed, values) {
		t.Fatal(string(data), err)
	}
	for key, value := range values {
		t.Run(key, func(t *testing.T) {
			source := migrationExtensionCapture(t, nil, map[string]string{key: value})
			_, err := migration.ConvertMCPExtension(source, map[string]migration.MCPProcessBinding{"wire": {Executable: executable, WorkingDirectory: t.TempDir(), RuntimeWorkDir: t.TempDir()}}, nil)
			if err == nil {
				t.Fatal("accepted a launcher that changes the observed source environment")
			}
		})
	}
}
