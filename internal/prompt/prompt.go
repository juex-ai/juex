// Package prompt assembles the system prompt that opens every turn.
//
// Layout (in order, matching design doc §4.1):
//
//  1. AGENTS.md hierarchy (user-global -> project root -> cwd subdir)
//  2. Runtime Module context, including the Skills index
//  3. Session scratchpad guidance
//  4. Tool list (auto-supplied to the provider, not duplicated here)
//  5. Operating context (cwd, time, OS)
//
// The builder is rebuilt from scratch every turn so that Module context and
// skill changes propagate immediately.
package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/config"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
)

const SectionSeparator = "\n\n---\n\n"

type Builder struct {
	ModulePromptContext func() ([]runtimemodule.ContextSection, error)
}

type ShellProfile struct {
	Profile       string
	Family        string
	Binary        string
	Args          []string
	PathStyle     string
	HostPathStyle string
}

// ShellProfileFromConfig converts the resolved config shell profile into the
// prompt-facing value object used by Builder.
func ShellProfileFromConfig(p config.ShellProfile) ShellProfile {
	return ShellProfile{
		Profile:       p.Profile,
		Family:        p.Family,
		Binary:        p.Binary,
		Args:          append([]string(nil), p.Args...),
		PathStyle:     p.PathStyle,
		HostPathStyle: p.HostPathStyle,
	}
}

type Section = runtimemodule.ContextSection

// Build is the compatibility helper for callers that cannot return an error.
// Runtime request paths use BuildWithError so Module failures stay visible.
func (b *Builder) Build() string {
	text, _ := b.BuildWithError()
	return text
}

func (b *Builder) BuildWithError() (string, error) {
	sections, err := b.SectionsWithError()
	if err != nil {
		return "", err
	}
	return JoinSections(sections), nil
}

func (b *Builder) Sections() []Section {
	sections, _ := b.SectionsWithError()
	return sections
}

func (b *Builder) SectionsWithError() ([]Section, error) {
	if b == nil || b.ModulePromptContext == nil {
		return nil, nil
	}
	moduleSections, err := b.ModulePromptContext()
	if err != nil {
		return nil, fmt.Errorf("prompt: module context: %w", err)
	}
	return runtimemodule.SectionsForProjection(moduleSections, runtimemodule.ContextProjectionSystemPrompt), nil
}

type agentsMDFile struct {
	Path string
	Text string
}

func loadAgentsMDFiles(globalPath string, dirs []string) []agentsMDFile {
	var files []agentsMDFile
	files = appendAgentsMDFile(files, globalPath)
	for _, dir := range dirs {
		files = appendAgentsMDFile(files, filepath.Join(dir, "AGENTS.md"))
	}
	return files
}

func appendAgentsMDFile(files []agentsMDFile, path string) []agentsMDFile {
	if path == "" {
		return files
	}
	for _, existing := range files {
		if sameCleanPath(existing.Path, path) {
			return files
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return files
	}
	content := strings.TrimSpace(string(data))
	if content == "" {
		return files
	}
	return append(files, agentsMDFile{
		Path: path,
		Text: fmt.Sprintf("# AGENTS.md (%s)\n\n%s", path, content),
	})
}

func scratchpadSection(workDir, scratchpadDir string) (Section, bool) {
	dir := strings.TrimSpace(scratchpadDir)
	if dir == "" {
		return Section{}, false
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	lines := []string{
		"## Session Scratchpad",
		fmt.Sprintf("- path: %s", dir),
	}
	if rel, ok := scratchpadRelativePath(workDir, dir); ok {
		lines = append(lines, fmt.Sprintf("- workspace-relative path for `write_begin`: %s", rel))
	}
	lines = append(lines,
		"- Use this directory for long drafts, intermediate files, and working material that exceeds the compact Notes budget.",
		"- Scratchpad contents are not automatically added to context. When needed, use `read` or `grep` to retrieve them.",
		"- Keep the current plan and short progress checkpoints in Notes; keep substantial working material here.",
		"- Save important intermediate conclusions here before compaction so a later turn can read them back.",
	)
	text := strings.Join(lines, "\n")
	return Section{
		Key:    "session_scratchpad",
		Label:  "Session Scratchpad",
		Source: "runtime",
		Path:   dir,
		Text:   text,
	}, true
}

func scratchpadRelativePath(workDir, dir string) (string, bool) {
	workDir = strings.TrimSpace(workDir)
	if workDir == "" {
		return "", false
	}
	root, err := filepath.Abs(workDir)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == "." || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func JoinSections(sections []Section) string {
	parts := make([]string, 0, len(sections))
	for _, section := range sections {
		if section.Text != "" {
			parts = append(parts, section.Text)
		}
	}
	return strings.Join(parts, SectionSeparator)
}

func operatingContext(workDir string, shell ShellProfile, nowFn func() time.Time) string {
	now := time.Now
	if nowFn != nil {
		now = nowFn
	}
	cwd := workDir
	if cwd == "" {
		cwd, _ = os.Getwd()
	} else if abs, err := filepath.Abs(cwd); err == nil {
		cwd = abs
	}
	lines := []string{
		"## Operating Context",
		fmt.Sprintf("- cwd: %s", cwd),
		fmt.Sprintf("- os: %s/%s", runtime.GOOS, runtime.GOARCH),
		fmt.Sprintf("- time: %s", now().UTC().Format(time.RFC3339)),
	}
	if shell.Binary != "" || shell.Profile != "" || shell.Family != "" {
		profile := shell.Profile
		if profile == "" {
			profile = shell.Family
		}
		binary := shell.Binary
		if binary == "" {
			binary = "shell"
		}
		family := shell.Family
		if family == "" {
			family = profile
		}
		pathStyle := shell.PathStyle
		if pathStyle == "" {
			pathStyle = "platform"
		}
		lines = append(lines,
			fmt.Sprintf("- shell: %s (%s)", profile, binary),
			fmt.Sprintf("- shell_family: %s", family),
			fmt.Sprintf("- shell_path_style: %s", pathStyle),
			"",
			fmt.Sprintf("Use the `exec_command` tool with %s syntax.", family),
		)
		if family == "powershell" {
			lines = append(lines, "For powershell, do not use POSIX heredocs, rm -rf, grep-only assumptions, or bash-specific expansion.")
		}
	}
	return strings.Join(lines, "\n")
}

func agentsSectionLabel(path, globalPath string) string {
	if sameCleanPath(path, globalPath) {
		return "Global AGENTS.md"
	}
	if filepath.Base(filepath.Dir(path)) == ".agents" {
		return ".agents/AGENTS.md"
	}
	return "Workspace AGENTS.md"
}

func agentsSectionSource(path, globalPath string) string {
	if sameCleanPath(path, globalPath) {
		return "user"
	}
	return "project"
}

func sameCleanPath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA == nil && errB == nil {
		return filepath.Clean(absA) == filepath.Clean(absB)
	}
	return filepath.Clean(a) == filepath.Clean(b)
}
