package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/sandbox"
)

type fakeSearchRunner struct {
	request GrepRequest
	result  GrepResult
	err     error
	calls   int
}

func (r *fakeSearchRunner) Grep(_ context.Context, req GrepRequest) (GrepResult, error) {
	r.calls++
	r.request = req
	return r.result, r.err
}

func TestBuiltinsGrepUsesInjectedSearchRunner(t *testing.T) {
	workDir := t.TempDir()
	runner := &fakeSearchRunner{result: GrepResult{Matches: []GrepMatch{
		{Path: "nested/a.txt", Line: 3, Text: "alpha"},
		{Path: "b.txt", Line: 7, Text: "alphabet"},
	}}}
	registry := NewRegistry()
	RegisterBuiltins(registry, BuiltinOptions{
		WorkDir:      workDir,
		SearchRunner: runner,
	})

	out, err := registry.Call(context.Background(), "grep", map[string]any{
		"pattern": "alpha",
		"path":    ".",
	})
	if err != nil {
		t.Fatal(err)
	}
	if runner.calls != 1 {
		t.Fatalf("runner calls = %d, want 1", runner.calls)
	}
	if runner.request.Pattern != "alpha" || runner.request.Path != workDir {
		t.Fatalf("runner request = %+v", runner.request)
	}
	if want := "nested/a.txt:3:alpha\nb.txt:7:alphabet"; out != want {
		t.Fatalf("grep output = %q, want %q", out, want)
	}
}

func TestBuiltinsGrepFormatsPartialTermination(t *testing.T) {
	runner := &fakeSearchRunner{
		result: GrepResult{
			Matches:     []GrepMatch{{Path: "a.txt", Line: 1, Text: "alpha"}},
			Truncated:   true,
			Termination: "result limit reached (200 matches)",
		},
		err: context.DeadlineExceeded,
	}
	registry := NewRegistry()
	RegisterBuiltins(registry, BuiltinOptions{WorkDir: t.TempDir(), SearchRunner: runner})

	out, err := registry.Call(context.Background(), "grep", map[string]any{"pattern": "alpha"})
	if err == nil || !strings.Contains(err.Error(), "tools: grep timed out after 60s") {
		t.Fatalf("err = %v, want registry timeout error", err)
	}
	for _, want := range []string{"a.txt:1:alpha", "[search stopped: result limit reached (200 matches)]"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q: %q", want, out)
		}
	}
}

func TestRipgrepArgsPreserveLegacyTraversalContract(t *testing.T) {
	got := ripgrepArgs("a.*b", ".")
	want := []string{
		"--json",
		"--no-config",
		"--crlf",
		"--sort", "path",
		"--hidden",
		"--no-ignore",
		"--color", "never",
		"--line-number",
		"--glob", "!**/.git/**",
		"--glob", "!**/node_modules/**",
		"--glob", "!**/.agents/**",
		"-e", "a.*b",
		"--", ".",
	}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("rg args = %#v, want %#v", got, want)
	}
}

func TestEscapeRipgrepGlobPathTreatsFilesystemNamesLiterally(t *testing.T) {
	got := escapeRipgrepGlobPath(`private[*?]{old}\name`)
	want := `private\[\*\?\]\{old\}\\name`
	if got != want {
		t.Fatalf("escaped glob path = %q, want %q", got, want)
	}
}

func TestNormalizeGoRegexpUsesASCIIWordBoundariesWithoutChangingLiterals(t *testing.T) {
	tests := map[string]string{
		`\bfoo\B`: `(?-u:\b)foo(?-u:\B)`,
		`\\b`:     `\\b`,
	}
	for input, want := range tests {
		got, err := normalizeGoRegexpForRipgrep(input)
		if err != nil {
			t.Fatalf("normalize %q: %v", input, err)
		}
		if got != want {
			t.Fatalf("normalize %q = %q, want %q", input, got, want)
		}
	}
}

func TestResolveRipgrepPrefersPackageAndRejectsMissingManagedBinary(t *testing.T) {
	packageRoot := t.TempDir()
	executable := filepath.Join(packageRoot, "bin", "juex")
	if err := os.MkdirAll(filepath.Dir(executable), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"schema_version":1,"juex_version":"1.2.3","platform":{"os":"linux","arch":"amd64"},"ripgrep":{"version":"15.1.0","path":"juex-path/rg","sha256":"` + strings.Repeat("0", 64) + `"}}`
	if err := os.WriteFile(filepath.Join(packageRoot, "juex-package.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	lookPathCalled := false
	_, err := resolveRipgrep(ripgrepResolveOptions{
		ExecutablePath: executable,
		RuntimeOS:      "linux",
		RuntimeArch:    "amd64",
		JuexVersion:    "1.2.3",
		LookPath: func(string) (string, error) {
			lookPathCalled = true
			return "/usr/bin/rg", nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "managed ripgrep") {
		t.Fatalf("err = %v, want missing managed ripgrep", err)
	}
	if lookPathCalled {
		t.Fatal("system PATH fallback was used for a marked package")
	}
}

func TestResolveRipgrepUsesSystemPathForSourceBuild(t *testing.T) {
	want := filepath.Join(t.TempDir(), "rg")
	if err := os.WriteFile(want, []byte("fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := resolveRipgrep(ripgrepResolveOptions{
		ExecutablePath: filepath.Join(t.TempDir(), "juex"),
		RuntimeOS:      "linux",
		RuntimeArch:    "amd64",
		JuexVersion:    "dev",
		LookPath: func(name string) (string, error) {
			if name != "rg" {
				t.Fatalf("LookPath name = %q", name)
			}
			return want, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != want || got.Source != RipgrepSourceSystem {
		t.Fatalf("resolved = %+v, want system %q", got, want)
	}
}

func TestResolveRipgrepIgnoresStaleManagedHomeForSourceBuild(t *testing.T) {
	prefix := t.TempDir()
	executable := filepath.Join(prefix, "bin", "juex")
	staleHome := filepath.Join(prefix, "lib", "juex", "releases", "0.0.1-linux-amd64")
	if err := os.MkdirAll(filepath.Dir(executable), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(staleHome, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("source build"), 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(t.TempDir(), "rg")
	if err := os.WriteFile(want, []byte("system rg"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := resolveRipgrep(ripgrepResolveOptions{
		ExecutablePath: executable,
		RuntimeOS:      "linux",
		RuntimeArch:    "amd64",
		JuexVersion:    "dev",
		LookPath: func(string) (string, error) {
			return want, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != want || got.Source != RipgrepSourceSystem {
		t.Fatalf("resolved = %+v, want stale package ignored", got)
	}
}

func TestResolveRipgrepValidatesPackagedBinaryDigest(t *testing.T) {
	packageRoot := t.TempDir()
	executable := filepath.Join(packageRoot, "bin", "juex")
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}
	if err := os.MkdirAll(filepath.Dir(executable), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	rgRelative := "juex-path/rg"
	if runtime.GOOS == "windows" {
		rgRelative += ".exe"
	}
	rgPath := filepath.Join(packageRoot, filepath.FromSlash(rgRelative))
	if err := os.MkdirAll(filepath.Dir(rgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rgPath, []byte("pinned rg"), 0o755); err != nil {
		t.Fatal(err)
	}
	digest, err := fileSHA256(rgPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest := `{"schema_version":1,"juex_version":"1.2.3","platform":{"os":"` + runtime.GOOS + `","arch":"` + runtime.GOARCH + `"},"ripgrep":{"version":"15.1.0","path":"` + rgRelative + `","sha256":"` + digest + `"}}`
	if err := os.WriteFile(filepath.Join(packageRoot, "juex-package.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	resolved, err := resolveRipgrep(ripgrepResolveOptions{
		ExecutablePath: executable,
		RuntimeOS:      runtime.GOOS,
		RuntimeArch:    runtime.GOARCH,
		JuexVersion:    "1.2.3",
		LookPath: func(string) (string, error) {
			t.Fatal("system PATH fallback should not run")
			return "", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	resolvedInfo, statErr := os.Stat(resolved.Path)
	if statErr != nil {
		t.Fatal(statErr)
	}
	wantInfo, statErr := os.Stat(rgPath)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if !os.SameFile(resolvedInfo, wantInfo) || resolved.Version != "15.1.0" || resolved.Source != RipgrepSourcePackage {
		t.Fatalf("resolved = %+v", resolved)
	}
}

func TestResolveRipgrepUsesWindowsManagedPackagePointer(t *testing.T) {
	prefix := t.TempDir()
	executable := filepath.Join(prefix, "bin", "juex.exe")
	releaseKey := "1.2.3-windows-amd64"
	packageRoot := filepath.Join(prefix, "lib", "juex", "releases", releaseKey)
	rgPath := filepath.Join(packageRoot, "juex-path", "rg.exe")
	if err := os.MkdirAll(filepath.Dir(executable), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(rgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("copied package binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rgPath, []byte("pinned windows rg"), 0o755); err != nil {
		t.Fatal(err)
	}
	digest, err := fileSHA256(rgPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest := `{"schema_version":1,"juex_version":"1.2.3","platform":{"os":"windows","arch":"amd64"},"ripgrep":{"version":"15.1.0","path":"juex-path/rg.exe","sha256":"` + digest + `"}}`
	if err := os.WriteFile(filepath.Join(packageRoot, "juex-package.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prefix, "lib", "juex", "current.txt"), []byte(releaseKey), 0o644); err != nil {
		t.Fatal(err)
	}

	resolved, err := resolveRipgrep(ripgrepResolveOptions{
		ExecutablePath: executable,
		RuntimeOS:      "windows",
		RuntimeArch:    "amd64",
		JuexVersion:    "1.2.3",
		LookPath: func(string) (string, error) {
			t.Fatal("system PATH fallback should not run")
			return "", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	resolvedInfo, err := os.Stat(resolved.Path)
	if err != nil {
		t.Fatal(err)
	}
	wantInfo, err := os.Stat(rgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(resolvedInfo, wantInfo) || resolved.Version != "15.1.0" || resolved.Source != RipgrepSourcePackage {
		t.Fatalf("resolved = %+v, want Windows managed package", resolved)
	}
}

func TestRipgrepRunnerSearchesHiddenIgnoredFilesAndCapsGlobally(t *testing.T) {
	rg, err := exec.LookPath("rg")
	if err != nil {
		t.Skip("system rg is unavailable")
	}
	root := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "ripgreprc")
	if err := os.WriteFile(configPath, []byte("--glob=!*.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RIPGREP_CONFIG_PATH", configPath)
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("ignored.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		".hidden.txt": "needle hidden\n",
		"ignored.txt": "needle ignored\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{".git", "node_modules", ".agents"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, dir, "secret.txt"), []byte("needle excluded\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fileWithPunctuation := filepath.Join(root, "- spaced,a.txt")
	if err := os.WriteFile(fileWithPunctuation, []byte("needle punctuation\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := NewRipgrepRunner(RipgrepRunnerOptions{
		RipgrepPath: rg,
		WorkDir:     root,
		Sandbox:     sandbox.DefaultPolicy(),
	})
	result, err := runner.Grep(context.Background(), GrepRequest{Pattern: "needle", Path: root})
	if err != nil {
		t.Fatal(err)
	}
	joined := formatGrepResult(result)
	for _, want := range []string{".hidden.txt", "ignored.txt", "- spaced,a.txt"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("search result missing %q:\n%s", want, joined)
		}
	}
	for _, forbidden := range []string{".git/secret.txt", "node_modules/secret.txt", ".agents/secret.txt", "excluded"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("excluded search result contains %q:\n%s", forbidden, joined)
		}
	}
	for _, dir := range []string{".git", "node_modules", ".agents"} {
		result, err := runner.Grep(context.Background(), GrepRequest{
			Pattern: "needle",
			Path:    filepath.Join(root, dir),
		})
		if err != nil {
			t.Fatalf("search excluded root %s: %v", dir, err)
		}
		if len(result.Matches) != 0 {
			t.Fatalf("excluded root %s matches = %+v, want none", dir, result.Matches)
		}
	}

	capRoot := t.TempDir()
	for i := 0; i < 205; i++ {
		name := filepath.Join(capRoot, "hit-"+strconv.Itoa(i)+".txt")
		if err := os.WriteFile(name, []byte("needle bulk\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	result, err = runner.Grep(context.Background(), GrepRequest{Pattern: "needle", Path: capRoot})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 200 || !result.Truncated {
		t.Fatalf("result matches=%d truncated=%t, want 200/true", len(result.Matches), result.Truncated)
	}
	for i := 1; i < len(result.Matches); i++ {
		if result.Matches[i-1].Path > result.Matches[i].Path {
			t.Fatalf("capped result order is not deterministic: %q before %q", result.Matches[i-1].Path, result.Matches[i].Path)
		}
	}
}

func TestRipgrepRunnerPreservesSingleFileOutputPath(t *testing.T) {
	rg, err := exec.LookPath("rg")
	if err != nil {
		t.Skip("system rg is unavailable")
	}
	root := t.TempDir()
	path := filepath.Join(root, "one.txt")
	if err := os.WriteFile(path, []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := NewRipgrepRunner(RipgrepRunnerOptions{RipgrepPath: rg, WorkDir: root})
	result, err := runner.Grep(context.Background(), GrepRequest{Pattern: "needle", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 1 || result.Matches[0].Path != "." {
		t.Fatalf("single-file matches = %+v, want legacy path .", result.Matches)
	}
}

func TestRipgrepRunnerPreservesGoRegexpDialect(t *testing.T) {
	rg, err := exec.LookPath("rg")
	if err != nil {
		t.Skip("system rg is unavailable")
	}
	root := t.TempDir()
	for name, body := range map[string]string{
		"quoted.txt":   "literal.*\n",
		"expanded.txt": "literalXYZ\n",
		"ascii.txt":    "42abc\n",
		"unicode.txt":  "٤abc\n",
		"boundary.txt": "éfooé\n",
		"crlf.txt":     "foo\r\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runner := NewRipgrepRunner(RipgrepRunnerOptions{RipgrepPath: rg, WorkDir: root})
	quoted, err := runner.Grep(context.Background(), GrepRequest{Pattern: `\Qliteral.*\E`, Path: root})
	if err != nil {
		t.Fatal(err)
	}
	quotedOutput := formatGrepResult(quoted)
	if !strings.Contains(quotedOutput, "quoted.txt") || strings.Contains(quotedOutput, "expanded.txt") {
		t.Fatalf("quoted Go regexp output = %q", quotedOutput)
	}
	digits, err := runner.Grep(context.Background(), GrepRequest{Pattern: `^\d+`, Path: root})
	if err != nil {
		t.Fatal(err)
	}
	digitOutput := formatGrepResult(digits)
	if !strings.Contains(digitOutput, "ascii.txt") || strings.Contains(digitOutput, "unicode.txt") {
		t.Fatalf("Go ASCII digit regexp output = %q", digitOutput)
	}
	boundary, err := runner.Grep(context.Background(), GrepRequest{Pattern: `\bfoo\b`, Path: root})
	if err != nil {
		t.Fatal(err)
	}
	if output := formatGrepResult(boundary); !strings.Contains(output, "boundary.txt") {
		t.Fatalf("Go ASCII word-boundary regexp output = %q", output)
	}
	crlf, err := runner.Grep(context.Background(), GrepRequest{Pattern: `foo$`, Path: root})
	if err != nil {
		t.Fatal(err)
	}
	if output := formatGrepResult(crlf); !strings.Contains(output, "crlf.txt") {
		t.Fatalf("Go CRLF line-anchor regexp output = %q", output)
	}
}

func TestRipgrepRunnerUsesSandboxRunnerAndExcludesBlockedDescendant(t *testing.T) {
	rg, err := exec.LookPath("rg")
	if err != nil {
		t.Skip("system rg is unavailable")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "public.txt"), []byte("needle public\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(root, "private[")
	if err := os.MkdirAll(blocked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocked, "secret.txt"), []byte("needle secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	policy := sandbox.DefaultPolicy()
	policy.Enabled = true
	policy.FileSystem.BlockedPaths = []string{"private["}
	sandboxRunner := &fakeSandboxRunner{}
	runner := NewRipgrepRunner(RipgrepRunnerOptions{
		RipgrepPath:   rg,
		WorkDir:       root,
		Sandbox:       policy,
		SandboxRunner: sandboxRunner,
	})
	result, err := runner.Grep(context.Background(), GrepRequest{Pattern: "needle", Path: root})
	if err != nil {
		t.Fatal(err)
	}
	if sandboxRunner.calls != 1 {
		t.Fatalf("sandbox runner calls = %d, want 1", sandboxRunner.calls)
	}
	output := formatGrepResult(result)
	if !strings.Contains(output, "public") || strings.Contains(output, "secret") {
		t.Fatalf("blocked descendant leaked into grep result: %q", output)
	}
}

func TestParseRipgrepJSONStopsAtRecordLimit(t *testing.T) {
	cancelled := false
	stop := newSearchStop(func() { cancelled = true })
	input := `{"type":"match","data":{"path":{"text":"a.txt"},"lines":{"text":"` + strings.Repeat("x", 300) + `"},"line_number":1}}` + "\n"
	result := parseRipgrepJSON(strings.NewReader(input), 1024, 128, 200, stop)
	if result.err != nil {
		t.Fatal(result.err)
	}
	if !cancelled || stop.reason() != "ripgrep record limit reached" {
		t.Fatalf("cancelled=%t reason=%q", cancelled, stop.reason())
	}
}

func TestParseRipgrepJSONStopsAtStdoutLimitAfterPartialMatch(t *testing.T) {
	cancelled := false
	stop := newSearchStop(func() { cancelled = true })
	match := `{"type":"match","data":{"path":{"text":"a.txt"},"lines":{"text":"needle\n"},"line_number":1}}` + "\n"
	input := match + strings.Repeat("x", 256)
	result := parseRipgrepJSON(strings.NewReader(input), len(match)+32, 512, 200, stop)
	if result.err != nil {
		t.Fatal(result.err)
	}
	if len(result.matches) != 1 || !cancelled || stop.reason() != "stdout output limit reached" {
		t.Fatalf("result=%+v cancelled=%t reason=%q", result, cancelled, stop.reason())
	}
}

func TestDrainBoundedStopsAtStderrLimit(t *testing.T) {
	cancelled := false
	stop := newSearchStop(func() { cancelled = true })
	result := drainBounded(strings.NewReader(strings.Repeat("x", 256)), 64, "stderr output limit reached", stop)
	if result.err != nil {
		t.Fatal(result.err)
	}
	if len(result.text) != 64 || !cancelled || stop.reason() != "stderr output limit reached" {
		t.Fatalf("bytes=%d cancelled=%t reason=%q", len(result.text), cancelled, stop.reason())
	}
}
