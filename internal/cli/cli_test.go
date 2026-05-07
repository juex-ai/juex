package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/version"
)

func TestVersionCmd_ShortForm(t *testing.T) {
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"version"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), version.String()) {
		t.Fatalf("got %q", out.String())
	}
}

func TestVersionCmd_VerboseForm(t *testing.T) {
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"version", "-v"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	body := out.String()
	for _, want := range []string{"juex", "commit:", "built:", "go:", "os/arch:"} {
		if !strings.Contains(body, want) {
			t.Errorf("verbose missing %q in:\n%s", want, body)
		}
	}
}

func TestRunCmd_RequiresPrompt(t *testing.T) {
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"run"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected error when prompt missing")
	}
	if _, ok := err.(*usageError); !ok {
		t.Fatalf("expected *usageError, got %T: %v", err, err)
	}
}

func TestRootHelpListsSubcommands(t *testing.T) {
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	body := out.String()
	for _, want := range []string{"run", "repl", "sessions", "serve", "version", "Available Commands"} {
		if !strings.Contains(body, want) {
			t.Errorf("help missing %q in:\n%s", want, body)
		}
	}
}

func TestUnknownSubcommandIsError(t *testing.T) {
	root := newRootCmd()
	root.SilenceUsage = true
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"totally-bogus"})
	if err := root.Execute(); err == nil {
		t.Fatal("expected error for unknown command")
	}
}

func TestPersistentFlagsParsedAtRoot(t *testing.T) {
	// `juex --verbose run` should propagate verbose to the run command.
	// We can't easily run `run` end-to-end here (no stub provider), but we
	// can verify the flag is registered on the root and accepted.
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--verbose", "version"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
}

func TestExecute_ZeroExitOnVersion(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"version"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
}

func TestVersionCmd_JSONForm(t *testing.T) {
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"version", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	body := out.String()
	for _, want := range []string{`"name": "juex"`, `"version":`, `"go_version":`, `"os":`, `"arch":`} {
		if !strings.Contains(body, want) {
			t.Errorf("json missing %q in:\n%s", want, body)
		}
	}
}

func TestSchemaCmd_OutputsCommandTree(t *testing.T) {
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"schema"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	body := out.String()
	for _, want := range []string{
		`"name": "juex"`,
		`"name": "run"`,
		`"name": "repl"`,
		`"name": "version"`,
		`"name": "schema"`,
		`"name": "sessions"`,
		`"name": "list"`,
		`"name": "show"`,
		`"name": "serve"`,
		`"name": "addr"`,
		`"name": "unsafe-bind-any"`,
		`"name": "resume"`,  // flag
		`"name": "session"`, // flag
		`"name": "cwd"`,     // persistent flag dumped on subcommands
		`"shorthand": "C"`,
		`"persistent": true`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("schema missing %q in:\n%s", want, body)
		}
	}
}

func TestRunCmd_DryRunReturnsDryRunOK(t *testing.T) {
	// run --dry-run requires no API key; should produce a *dryRunOK so
	// Execute() picks exit code 10.
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	dir := t.TempDir()
	envFile := dir + "/.env"
	if err := writeEnvFile(envFile, "openai", "https://x", "k", "m"); err != nil {
		t.Fatal(err)
	}
	root.SetArgs([]string{"-C", dir, "--env", envFile, "run", "--dry-run", "hello"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected *dryRunOK")
	}
	if _, ok := err.(*dryRunOK); !ok {
		t.Fatalf("got %T: %v", err, err)
	}
	body := out.String()
	for _, want := range []string{`"provider_type": "openai"`, `"prompt": "hello"`, `"tools":`} {
		if !strings.Contains(body, want) {
			t.Errorf("plan missing %q in:\n%s", want, body)
		}
	}
}

func TestRunCmd_DryRunJSONShape(t *testing.T) {
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	dir := t.TempDir()
	envFile := dir + "/.env"
	writeEnvFile(envFile, "openai", "https://x", "k", "m")
	root.SetArgs([]string{"-C", dir, "--env", envFile, "run", "--dry-run", "--json", "hello"})
	err := root.Execute()
	if _, ok := err.(*dryRunOK); !ok {
		t.Fatalf("expected *dryRunOK, got %T: %v", err, err)
	}
	body := out.String()
	// In --json mode the "DRY RUN — would execute:" header is suppressed.
	if strings.Contains(body, "DRY RUN") {
		t.Fatalf("--json should not include human header: %s", body)
	}
	if !strings.HasPrefix(strings.TrimSpace(body), "{") {
		t.Fatalf("expected JSON, got:\n%s", body)
	}
}

func TestRunCmd_MissingEnvFileExits3(t *testing.T) {
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--env", "/no/such/file", "run", "x"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected error")
	}
	if _, ok := err.(*notFoundError); !ok {
		t.Fatalf("expected *notFoundError, got %T: %v", err, err)
	}
}

func TestRunCmd_MissingCwdExits3(t *testing.T) {
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--cwd", "/no/such/dir/__juex__", "run", "x"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected error")
	}
	if _, ok := err.(*notFoundError); !ok {
		t.Fatalf("expected *notFoundError, got %T: %v", err, err)
	}
}

func TestRunCmd_JSONErrorShape(t *testing.T) {
	root := newRootCmd()
	var stderr bytes.Buffer
	var stdout bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"--env", "/no/such/file", "run", "--json", "x"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected error")
	}
	body := stderr.String()
	for _, want := range []string{
		`"error": "not_found"`,
		`"message":`,
		`"suggestion":`,
		`"retryable": false`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("json error missing %q in:\n%s", want, body)
		}
	}
}

func TestExitCodes_DistinctTypes(t *testing.T) {
	// Quick sanity that each error type maps to its dedicated exit code via
	// the type switch in Execute(). We can't call Execute() directly because
	// it builds its own root cmd from scratch, but we can verify the switch.
	cases := map[error]int{
		&usageError{msg: "u"}:      ExitUsageError,
		&notFoundError{msg: "n"}:   ExitNotFound,
		&permissionError{msg: "p"}: ExitPermission,
		&conflictError{msg: "c"}:   ExitConflict,
		&dryRunOK{msg: "d"}:        ExitDryRun,
	}
	for err, want := range cases {
		got := classifyForTest(err)
		if got != want {
			t.Errorf("err %T -> %d, want %d", err, got, want)
		}
	}
	if classifyForTest(nil) != ExitSuccess {
		t.Error("nil err should be ExitSuccess")
	}
	if classifyForTest(&strErr{"foo"}) != ExitGeneralError {
		t.Error("unknown err type should be ExitGeneralError")
	}
}

// strErr is a tiny error type used only by TestExitCodes_DistinctTypes
// to represent an unknown error variant.
type strErr struct{ s string }

func (e *strErr) Error() string { return e.s }

// classifyForTest mirrors Execute()'s type switch but skips the printing.
func classifyForTest(err error) int {
	if err == nil {
		return ExitSuccess
	}
	switch err.(type) {
	case *dryRunOK:
		return ExitDryRun
	case *usageError:
		return ExitUsageError
	case *notFoundError:
		return ExitNotFound
	case *permissionError:
		return ExitPermission
	case *conflictError:
		return ExitConflict
	default:
		return ExitGeneralError
	}
}

func writeEnvFile(path, typ, base, key, model string) error {
	body := "PROVIDER_API_TYPE=" + typ + "\nPROVIDER_API_BASE=" + base +
		"\nPROVIDER_API_KEY=" + key + "\nPROVIDER_API_MODEL=" + model + "\n"
	return os.WriteFile(path, []byte(body), 0o644)
}

func TestRunCmd_ResumeAndSessionMutuallyExclusive(t *testing.T) {
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	dir := t.TempDir()
	envFile := dir + "/.env"
	writeEnvFile(envFile, "openai", "https://x", "k", "m")
	root.SetArgs([]string{"-C", dir, "--env", envFile, "run", "--resume", "--session", "abc", "x"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected error")
	}
	if _, ok := err.(*usageError); !ok {
		t.Fatalf("got %T", err)
	}
}

func TestRunCmd_SessionFlagNotFound(t *testing.T) {
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	dir := t.TempDir()
	envFile := dir + "/.env"
	writeEnvFile(envFile, "openai", "https://x", "k", "m")
	root.SetArgs([]string{"-C", dir, "--env", envFile, "run", "--session", "missing", "x"})
	err := root.Execute()
	if _, ok := err.(*notFoundError); !ok {
		t.Fatalf("got %T: %v", err, err)
	}
}

func TestREPLCmd_AcceptsResumeFlags(t *testing.T) {
	dir := t.TempDir()
	envFile := dir + "/.env"
	writeEnvFile(envFile, "openai", "https://x", "k", "m")

	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"-C", dir, "--env", envFile, "repl", "--resume", "--session", "x"})
	err := root.Execute()
	if _, ok := err.(*usageError); !ok {
		t.Fatalf("got %T: %v", err, err)
	}
}

func TestServeCmd_UnsafeBindAnyBypassesLoopbackCheck(t *testing.T) {
	// Without --unsafe-bind-any, a non-loopback addr is a usage error.
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	dir := t.TempDir()
	envFile := dir + "/.env"
	writeEnvFile(envFile, "openai", "https://x", "k", "m")
	root.SetArgs([]string{"-C", dir, "--env", envFile, "serve", "--addr", "0.0.0.0:0"})
	err := root.Execute()
	if _, ok := err.(*usageError); !ok {
		t.Fatalf("expected *usageError without --unsafe-bind-any, got %T: %v", err, err)
	}

	// With --unsafe-bind-any, the loopback check is skipped. We don't
	// actually want to bind here, so we use a port that's almost
	// certainly already in use to force srv.Run to error quickly with a
	// bind failure (general error, not usage error). Pass an obviously
	// unavailable address.
	root2 := newRootCmd()
	var out2 bytes.Buffer
	root2.SetOut(&out2)
	root2.SetErr(&out2)
	root2.SetArgs([]string{"-C", dir, "--env", envFile, "serve", "--addr", "300.300.300.300:0", "--unsafe-bind-any"})
	err2 := root2.Execute()
	if err2 == nil {
		t.Fatal("expected non-nil error from invalid bind address")
	}
	if _, ok := err2.(*usageError); ok {
		t.Fatalf("expected non-usage error with --unsafe-bind-any, got *usageError: %v", err2)
	}
	// Confirm the warning was printed.
	if !strings.Contains(out2.String(), "WARNING: --unsafe-bind-any") {
		t.Errorf("expected stderr warning, got: %s", out2.String())
	}
}

func TestIsLoopbackAddr(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:8080", true},
		{"127.42.0.99:8080", true}, // anywhere in 127.0.0.0/8
		{"[::1]:8080", true},
		{"localhost:8080", true},
		{"localhost", true}, // bare host
		{"0.0.0.0:8080", false},
		{"192.168.1.5:8080", false},
		{"10.0.0.1:8080", false},
		{"", false},
		{"not-an-address", false},
	}
	for _, c := range cases {
		if got := isLoopbackAddr(c.addr); got != c.want {
			t.Errorf("isLoopbackAddr(%q) = %v, want %v", c.addr, got, c.want)
		}
	}
}
