package tools

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/juex-ai/juex/internal/sandbox"
)

const (
	defaultGrepMaxMatches  = 200
	defaultGrepStdoutBytes = 1 << 20
	defaultGrepStderrBytes = 64 << 10
	defaultGrepRecordBytes = 256 << 10
)

type GrepRequest struct {
	Pattern string
	Path    string
}

type GrepMatch struct {
	Path string
	Line int
	Text string
}

type GrepResult struct {
	Matches     []GrepMatch
	Truncated   bool
	Termination string
}

type SearchRunner interface {
	Grep(context.Context, GrepRequest) (GrepResult, error)
}

type RipgrepRunnerOptions struct {
	RipgrepPath   string
	WorkDir       string
	Sandbox       sandbox.Policy
	SandboxRunner sandbox.Runner
	MaxMatches    int
	MaxStdout     int
	MaxStderr     int
	MaxRecord     int
}

type RipgrepRunner struct {
	opts         RipgrepRunnerOptions
	resolveOnce  sync.Once
	resolvedPath string
	resolveErr   error
}

func NewRipgrepRunner(opts RipgrepRunnerOptions) *RipgrepRunner {
	if opts.MaxMatches <= 0 {
		opts.MaxMatches = defaultGrepMaxMatches
	}
	if opts.MaxStdout <= 0 {
		opts.MaxStdout = defaultGrepStdoutBytes
	}
	if opts.MaxStderr <= 0 {
		opts.MaxStderr = defaultGrepStderrBytes
	}
	if opts.MaxRecord <= 0 {
		opts.MaxRecord = defaultGrepRecordBytes
	}
	return &RipgrepRunner{opts: opts}
}

func (r *RipgrepRunner) Grep(ctx context.Context, req GrepRequest) (GrepResult, error) {
	if _, err := regexp.Compile(req.Pattern); err != nil {
		return GrepResult{}, fmt.Errorf("grep: bad pattern: %w", err)
	}
	info, err := os.Stat(req.Path)
	if err != nil {
		return GrepResult{}, fmt.Errorf("grep: %w", err)
	}
	cwd := req.Path
	target := "."
	singleFile := !info.IsDir()
	if singleFile {
		cwd = filepath.Dir(req.Path)
		target = filepath.Base(req.Path)
	}

	rgPath, err := r.ripgrepPath()
	if err != nil {
		return GrepResult{}, err
	}

	args := ripgrepArgs(req.Pattern, target)
	insertAt := len(args) - 4
	blockedArgs := blockedPathGlobArgs(cwd, r.opts.WorkDir, r.opts.Sandbox)
	combinedArgs := make([]string, 0, len(args)+len(blockedArgs))
	combinedArgs = append(combinedArgs, args[:insertAt]...)
	combinedArgs = append(combinedArgs, blockedArgs...)
	combinedArgs = append(combinedArgs, args[insertAt:]...)
	args = combinedArgs
	spec := sandbox.ExecSpec{Binary: rgPath, Args: args, Dir: cwd}
	if r.opts.Sandbox.Enabled {
		runner := r.opts.SandboxRunner
		if runner == nil {
			runner = sandbox.DefaultRunner{}
		}
		spec, err = runner.Prepare(ctx, sandbox.Request{
			Policy:         r.opts.Sandbox,
			WorkspaceRoots: []string{r.opts.WorkDir},
			Spec:           spec,
		})
		if err != nil {
			return GrepResult{}, fmt.Errorf("grep: prepare sandbox: %w", err)
		}
	}

	procCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(procCtx, spec.Binary, spec.Args...)
	cmd.Dir = spec.Dir
	if len(spec.Env) > 0 {
		cmd.Env = append([]string(nil), spec.Env...)
	}
	configureCommandForContext(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return GrepResult{}, fmt.Errorf("grep: stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return GrepResult{}, fmt.Errorf("grep: stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return GrepResult{}, fmt.Errorf("grep: start ripgrep: %w", err)
	}

	stop := newSearchStop(cancel)
	stdoutDone := make(chan ripgrepParseResult, 1)
	stderrDone := make(chan boundedDrainResult, 1)
	go func() {
		stdoutDone <- parseRipgrepJSON(stdout, r.opts.MaxStdout, r.opts.MaxRecord, r.opts.MaxMatches, stop)
	}()
	go func() {
		stderrDone <- drainBounded(stderr, r.opts.MaxStderr, "stderr output limit reached", stop)
	}()
	parsed := <-stdoutDone
	diagnostics := <-stderrDone
	// StdoutPipe and StderrPipe require their readers to finish before Wait:
	// Wait closes the descriptors after observing process exit and can otherwise
	// race a final JSON record into an artificial short read.
	waitErr := cmd.Wait()
	if singleFile {
		for i := range parsed.matches {
			// The former in-process walker rendered an explicitly selected
			// file relative to itself, which is ".". Preserve that public
			// output contract even though ripgrep reports the basename.
			parsed.matches[i].Path = "."
		}
	}

	result := GrepResult{Matches: parsed.matches}
	if reason := stop.reason(); reason != "" {
		result.Truncated = true
		result.Termination = reason
	}
	if ctx.Err() != nil {
		result.Truncated = true
		result.Termination = contextTermination(ctx.Err())
		return result, ctx.Err()
	}
	if parsed.err != nil {
		return result, fmt.Errorf("grep: parse ripgrep output: %w", parsed.err)
	}
	if diagnostics.err != nil && stop.reason() == "" {
		return result, fmt.Errorf("grep: read ripgrep diagnostics: %w", diagnostics.err)
	}
	if stop.reason() != "" {
		return result, nil
	}
	if waitErr == nil {
		return result, nil
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		switch exitErr.ExitCode() {
		case 1:
			return result, nil
		case 2:
			// A completed JSON stream proves that ripgrep accepted the
			// invocation and searched every readable descendant. It can still
			// exit 2 for an unreadable sibling; preserve those accessible
			// matches as the former in-process walker did. Argument/startup
			// failures do not emit a summary and remain fatal below.
			if parsed.summary {
				return result, nil
			}
		}
	}
	detail := strings.TrimSpace(diagnostics.text)
	if detail != "" {
		return result, fmt.Errorf("grep: ripgrep failed: %s", detail)
	}
	return result, fmt.Errorf("grep: ripgrep failed: %w", waitErr)
}

func (r *RipgrepRunner) ripgrepPath() (string, error) {
	r.resolveOnce.Do(func() {
		explicit := strings.TrimSpace(r.opts.RipgrepPath)
		if explicit != "" {
			r.resolvedPath, r.resolveErr = validateExecutableFile(explicit)
			if r.resolveErr != nil {
				r.resolveErr = fmt.Errorf("grep: configured ripgrep: %w", r.resolveErr)
			}
			return
		}
		resolved, err := ResolveRipgrep()
		if err != nil {
			r.resolveErr = err
			return
		}
		r.resolvedPath = resolved.Path
	})
	return r.resolvedPath, r.resolveErr
}

func ripgrepArgs(pattern, target string) []string {
	return []string{
		"--json",
		"--hidden",
		"--no-ignore",
		"--color", "never",
		"--line-number",
		"--glob", "!**/.git/**",
		"--glob", "!**/node_modules/**",
		"--glob", "!**/.agents/**",
		"-e", pattern,
		"--", target,
	}
}

func blockedPathGlobArgs(searchRoot, workDir string, policy sandbox.Policy) []string {
	if !policy.Enabled {
		return nil
	}
	if workDir == "" {
		workDir = searchRoot
	}
	var args []string
	for _, raw := range policy.FileSystem.BlockedPaths {
		path := strings.TrimSpace(raw)
		if path == "" {
			continue
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(workDir, path)
		}
		rel, err := filepath.Rel(searchRoot, filepath.Clean(path))
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		glob := filepath.ToSlash(rel)
		args = append(args, "--glob", "!"+glob, "--glob", "!"+glob+"/**")
	}
	return args
}

func formatGrepResult(result GrepResult) string {
	lines := make([]string, 0, len(result.Matches)+1)
	for _, match := range result.Matches {
		path := strings.ReplaceAll(strings.ToValidUTF8(match.Path, "�"), "\n", "\\n")
		text := strings.TrimSuffix(strings.TrimSuffix(strings.ToValidUTF8(match.Text, "�"), "\n"), "\r")
		lines = append(lines, fmt.Sprintf("%s:%d:%s", path, match.Line, text))
	}
	if result.Termination != "" {
		lines = append(lines, "[search stopped: "+result.Termination+"]")
	}
	if len(lines) == 0 {
		return "(no matches)"
	}
	return strings.Join(lines, "\n")
}

type searchStop struct {
	once   sync.Once
	cancel context.CancelFunc
	mu     sync.Mutex
	why    string
}

func newSearchStop(cancel context.CancelFunc) *searchStop {
	return &searchStop{cancel: cancel}
}

func (s *searchStop) stop(reason string) {
	s.once.Do(func() {
		s.mu.Lock()
		s.why = reason
		s.mu.Unlock()
		s.cancel()
	})
}

func (s *searchStop) reason() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.why
}

type ripgrepParseResult struct {
	matches []GrepMatch
	summary bool
	err     error
}

type ripgrepJSONEvent struct {
	Type string `json:"type"`
	Data struct {
		Path       ripgrepJSONText `json:"path"`
		Lines      ripgrepJSONText `json:"lines"`
		LineNumber int             `json:"line_number"`
	} `json:"data"`
}

type ripgrepJSONText struct {
	Text  string `json:"text"`
	Bytes string `json:"bytes"`
}

func (t ripgrepJSONText) value() string {
	if t.Bytes == "" {
		return t.Text
	}
	decoded, err := base64.StdEncoding.DecodeString(t.Bytes)
	if err != nil {
		return ""
	}
	return strings.ToValidUTF8(string(decoded), "�")
}

func parseRipgrepJSON(src io.Reader, maxBytes, maxRecord, maxMatches int, stop *searchStop) ripgrepParseResult {
	reader := bufio.NewReaderSize(src, 32<<10)
	var result ripgrepParseResult
	var record []byte
	total := 0
	for {
		fragment, readErr := reader.ReadSlice('\n')
		total += len(fragment)
		if total > maxBytes {
			stop.stop("stdout output limit reached")
			_, _ = io.Copy(io.Discard, reader)
			return result
		}
		if len(record)+len(fragment) > maxRecord {
			stop.stop("ripgrep record limit reached")
			_, _ = io.Copy(io.Discard, reader)
			return result
		}
		record = append(record, fragment...)
		if errors.Is(readErr, bufio.ErrBufferFull) {
			continue
		}
		if len(record) > 0 {
			var event ripgrepJSONEvent
			if err := json.Unmarshal(bytesTrimSpace(record), &event); err != nil {
				result.err = err
				stop.stop("invalid ripgrep output")
				_, _ = io.Copy(io.Discard, reader)
				return result
			}
			if event.Type == "match" {
				path := strings.TrimPrefix(filepath.ToSlash(event.Data.Path.value()), "./")
				result.matches = append(result.matches, GrepMatch{
					Path: path,
					Line: event.Data.LineNumber,
					Text: event.Data.Lines.value(),
				})
				if len(result.matches) >= maxMatches {
					stop.stop(fmt.Sprintf("result limit reached (%d matches)", maxMatches))
					_, _ = io.Copy(io.Discard, reader)
					return result
				}
			}
			if event.Type == "summary" {
				result.summary = true
			}
			record = record[:0]
		}
		if errors.Is(readErr, io.EOF) {
			return result
		}
		if readErr != nil {
			result.err = readErr
			stop.stop("ripgrep output read failed")
			return result
		}
	}
}

func bytesTrimSpace(value []byte) []byte {
	return []byte(strings.TrimSpace(string(value)))
}

type boundedDrainResult struct {
	text string
	err  error
}

func drainBounded(src io.Reader, maxBytes int, reason string, stop *searchStop) boundedDrainResult {
	var b strings.Builder
	buf := make([]byte, 32<<10)
	total := 0
	for {
		n, err := src.Read(buf)
		if n > 0 {
			remaining := maxBytes - total
			if remaining > 0 {
				keep := n
				if keep > remaining {
					keep = remaining
				}
				b.Write(buf[:keep])
			}
			total += n
			if total > maxBytes {
				stop.stop(reason)
			}
		}
		if errors.Is(err, io.EOF) {
			return boundedDrainResult{text: b.String()}
		}
		if err != nil {
			return boundedDrainResult{text: b.String(), err: err}
		}
	}
}

func contextTermination(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "cancelled"
}
