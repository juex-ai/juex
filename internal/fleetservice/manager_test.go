package fleetservice

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestPlansDeriveStableDistinctServiceNamesAndPaths(t *testing.T) {
	tests := []struct {
		name       string
		host       hostInfo
		home       string
		platform   Platform
		pathSuffix string
	}{
		{
			name:       "launchd",
			host:       hostInfo{goos: "darwin", userHome: "/Users/tester", uid: 501},
			home:       "/var/fleets/alpha",
			platform:   PlatformLaunchd,
			pathSuffix: ".plist",
		},
		{
			name:       "systemd",
			host:       hostInfo{goos: "linux", userHome: "/home/tester", xdgConfigHome: "/config"},
			home:       "/var/fleets/alpha",
			platform:   PlatformSystemd,
			pathSuffix: ".service",
		},
		{
			name:       "termux",
			host:       hostInfo{goos: "linux", userHome: "/home/tester", termuxPrefix: "/data/data/com.termux/files/usr"},
			home:       "/var/fleets/alpha",
			platform:   PlatformTermux,
			pathSuffix: filepath.Join("run"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first, err := buildPlan(testOptions(tt.home), tt.host)
			if err != nil {
				t.Fatal(err)
			}
			second, err := buildPlan(testOptions(tt.home), tt.host)
			if err != nil {
				t.Fatal(err)
			}
			other, err := buildPlan(testOptions("/other/alpha"), tt.host)
			if err != nil {
				t.Fatal(err)
			}
			if first.registration.Platform != tt.platform {
				t.Fatalf("platform = %q, want %q", first.registration.Platform, tt.platform)
			}
			if first.registration.Name != second.registration.Name || first.registration.Name == other.registration.Name {
				t.Fatalf("names first=%q second=%q other=%q", first.registration.Name, second.registration.Name, other.registration.Name)
			}
			if !regexp.MustCompile(`^[a-z0-9.-]+$`).MatchString(first.registration.Name) {
				t.Fatalf("unsafe service name %q", first.registration.Name)
			}
			if !strings.HasSuffix(first.registration.DefinitionPath, tt.pathSuffix) {
				t.Fatalf("definition path = %q, want suffix %q", first.registration.DefinitionPath, tt.pathSuffix)
			}
		})
	}
}

func TestLaunchdDefinitionPreservesDetachedAgents(t *testing.T) {
	home := filepath.Join(t.TempDir(), "fleet & home")
	plan, err := buildPlan(testOptions(home), hostInfo{
		goos:     "darwin",
		userHome: filepath.Join(t.TempDir(), "user home"),
		uid:      502,
	})
	if err != nil {
		t.Fatal(err)
	}
	body := string(plan.files[0].data)
	if err := validateXML(plan.files[0].data); err != nil {
		t.Fatalf("invalid plist XML: %v\n%s", err, body)
	}
	if runtime.GOOS == "darwin" {
		cmd := exec.Command("plutil", "-lint", "-")
		cmd.Stdin = strings.NewReader(body)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("plutil rejected plist: %v\n%s\n%s", err, output, body)
		}
	}
	for _, want := range []string{
		"<key>AbandonProcessGroup</key>",
		"<true/>",
		"<key>KeepAlive</key>",
		"<key>ProcessType</key>",
		"<string>Background</string>",
		"<key>JUEX_HOME</key>",
		"<string>" + strings.ReplaceAll(home, "&", "&amp;") + "</string>",
		"<string>fleet</string>",
		"<string>serve</string>",
		"<string>--addr</string>",
		"<string>127.0.0.1:8181</string>",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("plist missing %q:\n%s", want, body)
		}
	}
	if !strings.Contains(plan.registration.DefinitionPath, filepath.Join("Library", "LaunchAgents")) {
		t.Fatalf("definition path = %q", plan.registration.DefinitionPath)
	}
}

func TestSystemdDefinitionEscapesPathsAndUsesProcessKillMode(t *testing.T) {
	home := filepath.Join(t.TempDir(), `fleet $HOME %i "quoted"`)
	executable := filepath.Join(t.TempDir(), `juex $bin %h "quoted"`)
	opts := testOptions(home)
	opts.Executable = executable
	plan, err := buildPlan(opts, hostInfo{
		goos:          "linux",
		userHome:      filepath.Join(t.TempDir(), "user"),
		xdgConfigHome: filepath.Join(t.TempDir(), "xdg config"),
	})
	if err != nil {
		t.Fatal(err)
	}
	body := string(plan.files[0].data)
	for _, want := range []string{
		"Type=exec",
		"KillMode=process",
		"Restart=on-failure",
		"WantedBy=default.target",
		`Environment="JUEX_HOME=`,
		`$$bin`,
		`$HOME`,
		`%%i`,
		`ExecStart=`,
		`fleet serve --addr 127.0.0.1:8181`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("unit missing %q:\n%s", want, body)
		}
	}
	if !strings.Contains(plan.registration.DefinitionPath, filepath.Join("xdg config", "systemd", "user")) {
		t.Fatalf("definition path = %q", plan.registration.DefinitionPath)
	}
}

func TestTermuxDefinitionUsesDirectExecAndStandardLogging(t *testing.T) {
	prefix := filepath.Join(t.TempDir(), "usr")
	home := filepath.Join(t.TempDir(), "fleet home")
	opts := testOptions(home)
	opts.UnsafeBindAny = true
	opts.Addr = "0.0.0.0:8182"
	plan, err := buildPlan(opts, hostInfo{goos: "linux", userHome: t.TempDir(), termuxPrefix: prefix})
	if err != nil {
		t.Fatal(err)
	}
	if plan.registration.Platform != PlatformTermux || len(plan.files) != 2 {
		t.Fatalf("plan = %+v files=%d", plan.registration, len(plan.files))
	}
	run := string(plan.files[0].data)
	logRun := string(plan.files[1].data)
	for _, want := range []string{
		"#!" + filepath.Join(prefix, "bin", "sh"),
		"export JUEX_HOME=",
		"exec ",
		" 'fleet' 'serve' '--addr' '0.0.0.0:8182' '--unsafe-bind-any'",
	} {
		if !strings.Contains(run, want) {
			t.Fatalf("run script missing %q:\n%s", want, run)
		}
	}
	if strings.Contains(run, "kill") || strings.Contains(run, "pkill") {
		t.Fatalf("run script group-kills descendants:\n%s", run)
	}
	wantLogger := filepath.Join(prefix, "share", "termux-services", "svlogger")
	if !strings.Contains(logRun, "exec "+shellQuote(wantLogger)) {
		t.Fatalf("unexpected log script:\n%s", logRun)
	}
	if strings.Contains(logRun, filepath.Join(prefix, "bin", "svlogger")) {
		t.Fatalf("log script used the wrong Termux svlogger path:\n%s", logRun)
	}
	for _, file := range plan.files {
		if file.mode != 0o700 {
			t.Fatalf("%s mode = %o", file.path, file.mode)
		}
	}
}

func TestPublishFilesRollsBackEarlierDefinitionOnFailure(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first")
	if err := os.WriteFile(first, []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("block"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := publishFiles([]definitionFile{
		{path: first, data: []byte("new"), mode: 0o600},
		{path: filepath.Join(blocker, "second"), data: []byte("new"), mode: 0o600},
	})
	if err == nil {
		t.Fatal("publishFiles succeeded despite invalid second definition path")
	}
	data, readErr := os.ReadFile(first)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "old" {
		t.Fatalf("first definition = %q after rollback", data)
	}
	info, statErr := os.Stat(first)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o640 {
		t.Fatalf("first definition mode = %o after rollback", info.Mode().Perm())
	}
}

func TestSystemdInstallPublishesAndRestartsUnit(t *testing.T) {
	host := hostInfo{goos: "linux", userHome: t.TempDir(), xdgConfigHome: t.TempDir()}
	runner := &fakeRunner{}
	manager, err := newManagerForHost(testOptions(t.TempDir()), host, runner)
	if err != nil {
		t.Fatal(err)
	}
	registration, err := manager.Install(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantVerbs := []string{"daemon-reload", "enable", "restart"}
	if got := runner.verbs(); !reflect.DeepEqual(got, wantVerbs) {
		t.Fatalf("verbs = %v, want %v", got, wantVerbs)
	}
	info, err := os.Stat(registration.DefinitionPath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("unit mode = %o", info.Mode().Perm())
	}
}

func TestTermuxInstallRequiresManagerAndEnablesService(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows filesystems do not expose the executable mode bits required by Termux")
	}
	prefix := t.TempDir()
	if err := os.MkdirAll(filepath.Join(prefix, "var", "service"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(prefix, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(prefix, "share", "termux-services"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"sh", "sv", "sv-enable", "sv-disable"} {
		if err := os.WriteFile(filepath.Join(prefix, "bin", name), []byte("#!/bin/sh\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(prefix, "share", "termux-services", "svlogger"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	manager, err := newManagerForHost(testOptions(t.TempDir()), hostInfo{goos: "linux", userHome: t.TempDir(), termuxPrefix: prefix}, runner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := runner.verbs(); !reflect.DeepEqual(got, []string{"sv-enable"}) {
		t.Fatalf("verbs = %v", got)
	}
	if got := runner.commands[0].env["SVDIR"]; got != filepath.Join(prefix, "var", "service") {
		t.Fatalf("SVDIR = %q", got)
	}
}

func TestUnknownLaunchdStateErrorFailsLoudly(t *testing.T) {
	runner := &fakeRunner{results: []fakeCommandResult{{output: "not permitted", err: errors.New("exit status 1")}}}
	manager, err := newManagerForHost(testOptions(t.TempDir()), hostInfo{goos: "darwin", userHome: t.TempDir(), uid: 501}, runner)
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.Uninstall(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not permitted") {
		t.Fatalf("error = %v", err)
	}
}

func TestManagerValidatesInstalledAddressAndPlatform(t *testing.T) {
	host := hostInfo{goos: "linux", userHome: t.TempDir(), xdgConfigHome: t.TempDir()}
	tests := []struct {
		name string
		opts Options
		host hostInfo
		want string
	}{
		{name: "zero port", opts: Options{HomeDir: t.TempDir(), Executable: "/bin/juex", Addr: "127.0.0.1:0"}, host: host, want: "non-zero"},
		{name: "malformed", opts: Options{HomeDir: t.TempDir(), Executable: "/bin/juex", Addr: "127.0.0.1"}, host: host, want: "host:port"},
		{name: "unsafe bind", opts: Options{HomeDir: t.TempDir(), Executable: "/bin/juex", Addr: "0.0.0.0:8080"}, host: host, want: "UnsafeBindAny"},
		{name: "windows", opts: testOptions(t.TempDir()), host: hostInfo{goos: "windows", userHome: t.TempDir()}, want: "not supported"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := newManagerForHost(tt.opts, tt.host, &fakeRunner{})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestLaunchdInstallAndUninstallQueryManagerState(t *testing.T) {
	host := hostInfo{goos: "darwin", userHome: t.TempDir(), uid: 501}
	runner := &fakeRunner{results: []fakeCommandResult{
		{output: "service not found", err: errors.New("exit status 113")},
		{},
		{},
		{},
		{},
		{output: "service not found", err: errors.New("exit status 113")},
	}}
	manager, err := newManagerForHost(testOptions(t.TempDir()), host, runner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Uninstall(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantVerbs := []string{"print", "bootstrap", "kickstart", "print", "bootout", "print"}
	if got := runner.verbs(); !reflect.DeepEqual(got, wantVerbs) {
		t.Fatalf("verbs = %v, want %v\ncommands=%v", got, wantVerbs, runner.commands)
	}
	if _, err := os.Stat(manager.plan.registration.DefinitionPath); !os.IsNotExist(err) {
		t.Fatalf("plist still exists: %v", err)
	}
}

func TestLaunchdUninstallWaitsForManagerStateToDisappear(t *testing.T) {
	host := hostInfo{goos: "darwin", userHome: t.TempDir(), uid: 501}
	runner := &fakeRunner{results: []fakeCommandResult{
		{},
		{},
		{},
		{output: "service not found", err: errors.New("exit status 113")},
	}}
	manager, err := newManagerForHost(testOptions(t.TempDir()), host, runner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Uninstall(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantVerbs := []string{"print", "bootout", "print", "print"}
	if got := runner.verbs(); !reflect.DeepEqual(got, wantVerbs) {
		t.Fatalf("verbs = %v, want %v", got, wantVerbs)
	}
}

func TestSystemdUninstallStopsCachedUnitWithoutDefinition(t *testing.T) {
	host := hostInfo{goos: "linux", userHome: t.TempDir(), xdgConfigHome: t.TempDir()}
	runner := &fakeRunner{results: []fakeCommandResult{
		{output: "LoadState=loaded\nActiveState=active\n"},
		{},
		{},
		{output: "LoadState=not-found\nActiveState=inactive\n"},
	}}
	manager, err := newManagerForHost(testOptions(t.TempDir()), host, runner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Uninstall(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantVerbs := []string{"show", "disable", "daemon-reload", "show"}
	if got := runner.verbs(); !reflect.DeepEqual(got, wantVerbs) {
		t.Fatalf("verbs = %v, want %v", got, wantVerbs)
	}
}

func TestTermuxUninstallRequiresDefinitionAndConfirmedDown(t *testing.T) {
	prefix := t.TempDir()
	host := hostInfo{goos: "linux", userHome: t.TempDir(), termuxPrefix: prefix}
	manager, err := newManagerForHost(testOptions(t.TempDir()), host, &fakeRunner{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Uninstall(context.Background()); err == nil || !strings.Contains(err.Error(), "cannot confirm") {
		t.Fatalf("missing service dir error = %v", err)
	}

	if err := os.MkdirAll(filepath.Dir(manager.plan.registration.DefinitionPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manager.plan.registration.DefinitionPath, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{results: []fakeCommandResult{{}, {}, {output: "down: juex: 0s", err: errors.New("exit status 3")}}}
	manager.runner = runner
	if _, err := manager.Uninstall(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantVerbs := []string{"sv-disable", "sv", "sv"}
	if got := runner.verbs(); !reflect.DeepEqual(got, wantVerbs) {
		t.Fatalf("verbs = %v, want %v", got, wantVerbs)
	}
	if _, err := os.Stat(filepath.Dir(manager.plan.registration.DefinitionPath)); !os.IsNotExist(err) {
		t.Fatalf("service directory remains: %v", err)
	}
}

func testOptions(home string) Options {
	return Options{
		HomeDir:    home,
		Executable: filepath.Join(home, "bin", "juex"),
		Addr:       "127.0.0.1:8181",
	}
}

func validateXML(data []byte) error {
	decoder := xml.NewDecoder(strings.NewReader(string(data)))
	for {
		if _, err := decoder.Token(); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

type fakeCommandResult struct {
	output string
	err    error
}

type fakeRunner struct {
	commands []command
	results  []fakeCommandResult
}

func (r *fakeRunner) Run(_ context.Context, cmd command) (string, error) {
	r.commands = append(r.commands, cmd)
	if len(r.results) == 0 {
		return "", nil
	}
	result := r.results[0]
	r.results = r.results[1:]
	return result.output, result.err
}

func (r *fakeRunner) verbs() []string {
	verbs := make([]string, 0, len(r.commands))
	for _, cmd := range r.commands {
		base := filepath.Base(cmd.name)
		switch base {
		case "launchctl":
			if len(cmd.args) > 0 {
				verbs = append(verbs, cmd.args[0])
			} else {
				verbs = append(verbs, base)
			}
		case "systemctl":
			verb := base
			for _, arg := range cmd.args {
				if !strings.HasPrefix(arg, "-") {
					verb = arg
					break
				}
			}
			verbs = append(verbs, verb)
		default:
			verbs = append(verbs, base)
		}
	}
	return verbs
}

func (r *fakeRunner) String() string {
	return fmt.Sprint(r.commands)
}
