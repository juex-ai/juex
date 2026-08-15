package hooks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/environment"
)

func TestCommandHookMatchesEventAndTool(t *testing.T) {
	h := CommandHook{Name: "guard", Events: []EventName{EventPreToolUse}, Tools: []string{"exec_command"}}
	if !h.Matches(EventPreToolUse, "exec_command") {
		t.Fatal("hook should match configured event and tool")
	}
	if h.Matches(EventPostToolUse, "exec_command") {
		t.Fatal("hook should not match a different event")
	}
	if h.Matches(EventPreToolUse, "read") {
		t.Fatal("hook should not match a different tool")
	}
	withoutToolFilter := CommandHook{Name: "any", Events: []EventName{EventUserPromptSubmit}}
	if !withoutToolFilter.Matches(EventUserPromptSubmit, "") {
		t.Fatal("hook without tool filter should match event")
	}
}

func TestRunnerRunStableOrderAndStdout(t *testing.T) {
	r, err := NewRunner(Config{Commands: []CommandHook{
		{Name: "first", Events: []EventName{EventUserPromptSubmit}, Command: helperCommand("stdout:first")},
		{Name: "second", Events: []EventName{EventUserPromptSubmit}, Command: helperCommand("stdout:second")},
	}})
	if err != nil {
		t.Fatal(err)
	}

	results, err := r.Run(context.Background(), Request{EventName: EventUserPromptSubmit})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("results len = %d", len(results))
	}
	if results[0].Hook.Name != "first" || results[0].ExitCode != 0 || results[0].Stdout != "first" {
		t.Fatalf("first result = %+v", results[0])
	}
	if results[0].EventName != EventUserPromptSubmit {
		t.Fatalf("first event = %q", results[0].EventName)
	}
	if results[1].Hook.Name != "second" || results[1].ExitCode != 0 || results[1].Stdout != "second" {
		t.Fatalf("second result = %+v", results[1])
	}
}

func TestRunnerPropagatesResolvedEnvironment(t *testing.T) {
	snapshot, err := environment.Resolve(environment.Options{Layers: []environment.Layer{{
		Source: environment.SourceDotenv,
		Path:   "/work/.env",
		Values: map[string]string{"HOOK_RUNTIME_MARKER": "from-snapshot"},
		Strict: true,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewRunnerWithOptions(Config{Commands: []CommandHook{{
		Name:    "environment",
		Events:  []EventName{EventSessionStart},
		Command: helperCommand("environment"),
	}}}, RunnerOptions{Environment: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	results, err := r.Run(context.Background(), Request{EventName: EventSessionStart})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Stdout != "from-snapshot" {
		t.Fatalf("results = %+v", results)
	}
}

func TestRunnerResolvesRelativeCommandFromRequestCWD(t *testing.T) {
	workDir := t.TempDir()
	command := copyHookHelperExecutable(t, workDir)
	r, err := NewRunner(Config{Commands: []CommandHook{{
		Name:    "relative",
		Events:  []EventName{EventSessionStart},
		Command: []string{command, "-test.run=TestHookHelperProcess", "--", "stdout:relative"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	results, err := r.Run(context.Background(), Request{
		EventName: EventSessionStart,
		CWD:       workDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Stdout != "relative" {
		t.Fatalf("results = %+v", results)
	}
}

func TestRunnerRunExitTwoReturnsTextResult(t *testing.T) {
	r, err := NewRunner(Config{Commands: []CommandHook{
		{Name: "guard", Events: []EventName{EventPreToolUse}, Command: helperCommand("block")},
	}})
	if err != nil {
		t.Fatal(err)
	}

	results, err := r.Run(context.Background(), Request{EventName: EventPreToolUse, ToolName: "exec_command"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].ExitCode != 2 {
		t.Fatalf("results = %+v", results)
	}
	if results[0].Stdout != "policy denied" || results[0].Stderr != "guard diagnostic" {
		t.Fatalf("result streams = %+v", results[0])
	}
}

func TestRunnerRunOtherExitErrorsAndContinues(t *testing.T) {
	observer := &recordingObserver{}
	r, err := NewRunner(Config{Commands: []CommandHook{
		{Name: "first", Events: []EventName{EventUserPromptSubmit}, Command: helperCommand("stdout:first")},
		{Name: "broken", Events: []EventName{EventUserPromptSubmit}, Command: helperCommand("exit-seven")},
		{Name: "last", Events: []EventName{EventUserPromptSubmit}, Command: helperCommand("stdout:last")},
	}})
	if err != nil {
		t.Fatal(err)
	}

	results, err := r.Run(context.Background(), Request{EventName: EventUserPromptSubmit, Observer: observer})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Hook.Name != "first" || results[1].Hook.Name != "last" {
		t.Fatalf("results = %+v", results)
	}
	if len(observer.errors) != 1 || observer.errors[0].result.ExitCode != 7 || !strings.Contains(observer.errors[0].err.Error(), "exited with code 7") {
		t.Fatalf("observer errors = %+v", observer.errors)
	}
}

func TestRunnerRunResultCarriesTriggeredTool(t *testing.T) {
	r, err := NewRunner(Config{Commands: []CommandHook{
		{Name: "multi", Events: []EventName{EventPreToolUse, EventPostToolUse}, Tools: []string{"exec_command"}, Command: helperCommand("allow")},
	}})
	if err != nil {
		t.Fatal(err)
	}

	results, err := r.Run(context.Background(), Request{EventName: EventPostToolUse, ToolName: "exec_command"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("results len = %d", len(results))
	}
	if results[0].EventName != EventPostToolUse || results[0].ToolName != "exec_command" {
		t.Fatalf("result trigger = %q/%q", results[0].EventName, results[0].ToolName)
	}
}

func TestRunnerRunTimeout(t *testing.T) {
	observer := &recordingObserver{}
	r, err := NewRunner(Config{Commands: []CommandHook{
		{Name: "slow", Events: []EventName{EventPreToolUse}, Command: helperCommand("sleep"), TimeoutSeconds: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}

	results, err := r.Run(context.Background(), Request{EventName: EventPreToolUse, ToolName: "exec_command", Observer: observer})
	if err != nil {
		t.Fatalf("optional timeout error = %v", err)
	}
	if len(results) != 0 || len(observer.errors) != 1 || !strings.Contains(observer.errors[0].err.Error(), "timed out") {
		t.Fatalf("results = %+v, observer errors = %+v", results, observer.errors)
	}
}

func TestRunnerRunRequiredTimeoutFails(t *testing.T) {
	r, err := NewRunner(Config{Commands: []CommandHook{{
		Name: "slow", Events: []EventName{EventPreToolUse}, Command: helperCommand("sleep"), TimeoutSeconds: 1, Required: true,
	}}})
	if err != nil {
		t.Fatal(err)
	}

	_, err = r.Run(context.Background(), Request{EventName: EventPreToolUse, ToolName: "exec_command"})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want required timeout", err)
	}
}

func TestRunnerRunRequiredExitErrorFails(t *testing.T) {
	r, err := NewRunner(Config{Commands: []CommandHook{
		{Name: "broken", Events: []EventName{EventUserPromptSubmit}, Command: helperCommand("exit-seven"), Required: true},
		{Name: "unreached", Events: []EventName{EventUserPromptSubmit}, Command: helperCommand("stdout:unreached")},
	}})
	if err != nil {
		t.Fatal(err)
	}

	results, err := r.Run(context.Background(), Request{EventName: EventUserPromptSubmit})
	if err == nil || !strings.Contains(err.Error(), "exited with code 7") {
		t.Fatalf("err = %v, want required exit error", err)
	}
	if len(results) != 0 {
		t.Fatalf("results = %+v, want no completed hooks", results)
	}
}

func TestRunnerRunExtensionRuntimeExpandsPathsAndPreparesData(t *testing.T) {
	extensionDir := t.TempDir()
	dataDir := filepath.Join(t.TempDir(), "agent", "extensions", "demo")
	helper := copyHookHelperExecutable(t, extensionDir)
	prepared := false
	snapshot := environment.FromEnviron(append(os.Environ(),
		"JUEX_EXT_DIR=/spoofed-extension",
		"JUEX_EXT_DATA_DIR=/spoofed-data",
	))
	r, err := NewRunnerWithOptions(Config{Commands: []CommandHook{{
		Name:     "extension",
		Events:   []EventName{EventSessionStart},
		Command:  []string{"${JUEX_EXT_DIR}/" + filepath.Base(helper), "-test.run=TestHookHelperProcess", "--", "extension-environment"},
		Required: true,
		Runtime: RuntimeContext{
			ExtensionDir:     extensionDir,
			ExtensionDataDir: dataDir,
			PrepareExtensionDataDir: func() error {
				prepared = true
				return os.MkdirAll(dataDir, 0o700)
			},
		},
	}}}, RunnerOptions{Environment: snapshot})
	if err != nil {
		t.Fatal(err)
	}

	results, err := r.Run(context.Background(), Request{EventName: EventSessionStart, CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if !prepared {
		t.Fatal("extension data directory was not prepared")
	}
	if len(results) != 1 || results[0].Stdout != extensionDir+"|"+dataDir {
		t.Fatalf("results = %+v", results)
	}
}

func TestRunnerRunNonExtensionStripsExtensionEnvironment(t *testing.T) {
	snapshot := environment.FromEnviron(append(os.Environ(),
		"JUEX_EXT_DIR=/spoofed-extension",
		"JUEX_EXT_DATA_DIR=/spoofed-data",
	))
	r, err := NewRunnerWithOptions(Config{Commands: []CommandHook{{
		Name:    "workspace",
		Events:  []EventName{EventSessionStart},
		Command: helperCommand("extension-environment"),
	}}}, RunnerOptions{Environment: snapshot})
	if err != nil {
		t.Fatal(err)
	}

	results, err := r.Run(context.Background(), Request{EventName: EventSessionStart})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Stdout != "|" {
		t.Fatalf("results = %+v, want stripped extension environment", results)
	}
}

func TestRunnerRunOptionalExtensionPrepareFailureContinues(t *testing.T) {
	observer := &recordingObserver{}
	r, err := NewRunner(Config{Commands: []CommandHook{
		{
			Name: "broken-extension", Events: []EventName{EventSessionStart}, Command: helperCommand("stdout:unused"),
			Runtime: RuntimeContext{
				ExtensionDir:            t.TempDir(),
				ExtensionDataDir:        filepath.Join(t.TempDir(), "data"),
				PrepareExtensionDataDir: func() error { return errors.New("prepare failed") },
			},
		},
		{Name: "last", Events: []EventName{EventSessionStart}, Command: helperCommand("stdout:last")},
	}})
	if err != nil {
		t.Fatal(err)
	}

	results, err := r.Run(context.Background(), Request{EventName: EventSessionStart, Observer: observer})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Hook.Name != "last" {
		t.Fatalf("results = %+v", results)
	}
	if len(observer.errors) != 1 || !strings.Contains(observer.errors[0].err.Error(), "prepare failed") {
		t.Fatalf("observer errors = %+v", observer.errors)
	}
}

func TestRunnerRunPropagatesParentCancellation(t *testing.T) {
	r, err := NewRunner(Config{Commands: []CommandHook{
		{Name: "slow", Events: []EventName{EventPreToolUse}, Command: helperCommand("sleep"), TimeoutSeconds: 5},
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	defer cancel()

	_, err = r.Run(ctx, Request{EventName: EventPreToolUse, ToolName: "exec_command"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context canceled", err)
	}
}

func TestNewRunnerRejectsTimeoutAboveMax(t *testing.T) {
	_, err := NewRunner(Config{Commands: []CommandHook{
		{Name: "too-slow", Events: []EventName{EventPreToolUse}, Command: helperCommand("allow"), TimeoutSeconds: MaxTimeoutSeconds + 1},
	}})
	if err == nil || !strings.Contains(err.Error(), "timeout_seconds cannot exceed 300 seconds") {
		t.Fatalf("err = %v, want max timeout error", err)
	}
}

func TestRunnerRunOutputLimit(t *testing.T) {
	observer := &recordingObserver{}
	r, err := NewRunner(Config{Commands: []CommandHook{
		{Name: "large", Events: []EventName{EventPostToolUse}, Command: helperCommand("large"), MaxOutputBytes: 8},
	}})
	if err != nil {
		t.Fatal(err)
	}

	results, err := r.Run(context.Background(), Request{EventName: EventPostToolUse, ToolName: "read", Observer: observer})
	if err != nil {
		t.Fatalf("optional output limit error = %v", err)
	}
	if len(results) != 0 || len(observer.errors) != 1 || !strings.Contains(observer.errors[0].err.Error(), "stdout exceeded") {
		t.Fatalf("results = %+v, observer errors = %+v", results, observer.errors)
	}
}

func TestLoadFileConfigEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.yaml")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFileConfig(path, "ext:empty", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Commands) != 0 {
		t.Fatalf("commands = %+v, want empty config", cfg.Commands)
	}
}

func helperCommand(mode string) []string {
	return []string{os.Args[0], "-test.run=TestHookHelperProcess", "--", mode}
}

func copyHookHelperExecutable(t *testing.T, workDir string) string {
	t.Helper()
	source, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	name := "hook-helper"
	if filepath.Ext(source) != "" {
		name += filepath.Ext(source)
	}
	body, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, name), body, 0o755); err != nil {
		t.Fatal(err)
	}
	return "." + string(filepath.Separator) + name
}

func TestHookHelperProcess(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "--" {
		return
	}
	mode := os.Args[len(os.Args)-1]
	switch {
	case strings.HasPrefix(mode, "stdout:"):
		_, _ = os.Stdout.WriteString(strings.TrimPrefix(mode, "stdout:"))
	case mode == "block":
		_, _ = os.Stdout.WriteString("policy denied")
		_, _ = os.Stderr.WriteString("guard diagnostic")
		os.Exit(2)
	case mode == "exit-seven":
		_, _ = os.Stderr.WriteString("hook failed")
		os.Exit(7)
	case mode == "sleep":
		time.Sleep(5 * time.Second)
	case mode == "large":
		_, _ = os.Stdout.WriteString(strings.Repeat("x", 32))
	case mode == "environment":
		_, _ = os.Stdout.WriteString(os.Getenv("HOOK_RUNTIME_MARKER"))
	case mode == "extension-environment":
		_, _ = os.Stdout.WriteString(os.Getenv("JUEX_EXT_DIR") + "|" + os.Getenv("JUEX_EXT_DATA_DIR"))
	}
	os.Exit(0)
}

type observedHookError struct {
	result Result
	err    error
}

type recordingObserver struct {
	errors []observedHookError
}

func (*recordingObserver) HookStarted(CommandHook, Request) {}

func (*recordingObserver) HookCompleted(Result) {}

func (o *recordingObserver) HookErrored(result Result, err error) {
	o.errors = append(o.errors, observedHookError{result: result, err: err})
}
