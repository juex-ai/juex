// Package promptcontext provides the concrete project-guidance and Session
// context Modules used to assemble provider system prompts.
package promptcontext

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/config"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/tools"
)

const (
	GuidanceModuleID       runtimemodule.ID = "project-guidance"
	SessionContextModuleID runtimemodule.ID = "session-context"
)

type GuidanceModule struct {
	GlobalAgentsMDPath string
	AgentsMDDirs       []string
}

func (*GuidanceModule) ID() runtimemodule.ID { return GuidanceModuleID }

func (m *GuidanceModule) Context(_ context.Context, request runtimemodule.ContextRequest) ([]runtimemodule.ContextSection, error) {
	if m == nil || request.Purpose != runtimemodule.ContextPurposeProviderIteration {
		return nil, nil
	}
	var sections []runtimemodule.ContextSection
	for _, agents := range loadAgentsMDFiles(m.GlobalAgentsMDPath, m.AgentsMDDirs) {
		sections = append(sections, runtimemodule.ContextSection{
			Key:        guidanceSectionKey(m.GlobalAgentsMDPath, agents.Path),
			Label:      agentsSectionLabel(agents.Path, m.GlobalAgentsMDPath),
			Source:     agentsSectionSource(agents.Path, m.GlobalAgentsMDPath),
			Path:       agents.Path,
			Text:       agents.Text,
			Projection: runtimemodule.ContextProjectionSystemPrompt,
			Budget:     runtimemodule.UnboundedContextBudget(),
		})
	}
	return sections, nil
}

func guidanceSectionKey(globalPath, path string) string {
	if sameCleanPath(path, globalPath) {
		return "agents_global"
	}
	cleaned := path
	if abs, err := filepath.Abs(path); err == nil {
		cleaned = abs
	}
	return "agents_project:" + filepath.ToSlash(filepath.Clean(cleaned))
}

type ShellProfile struct {
	Profile       string
	Family        string
	Binary        string
	Args          []string
	PathStyle     string
	HostPathStyle string
}

func ShellProfileFromConfig(profile config.ShellProfile) ShellProfile {
	return ShellProfile{
		Profile:       profile.Profile,
		Family:        profile.Family,
		Binary:        profile.Binary,
		Args:          append([]string(nil), profile.Args...),
		PathStyle:     profile.PathStyle,
		HostPathStyle: profile.HostPathStyle,
	}
}

type SessionContextModule struct {
	WorkDir       string
	Shell         ShellProfile
	ShellSessions *tools.ShellSessionManager
	Now           func() time.Time
}

func (*SessionContextModule) ID() runtimemodule.ID { return SessionContextModuleID }

func (m *SessionContextModule) Context(_ context.Context, request runtimemodule.ContextRequest) ([]runtimemodule.ContextSection, error) {
	if m == nil || request.Purpose != runtimemodule.ContextPurposeProviderIteration {
		return nil, nil
	}
	var sections []runtimemodule.ContextSection
	scratchpadDir := ""
	if request.Session != nil {
		scratchpadDir = request.Session.ScratchpadDir
	}
	if section, ok := scratchpadSection(m.WorkDir, scratchpadDir); ok {
		section.Projection = runtimemodule.ContextProjectionSystemPrompt
		section.Budget = runtimemodule.UnboundedContextBudget()
		sections = append(sections, section)
	}
	if m.ShellSessions != nil {
		if text := tools.FormatActiveShellSessionsPrompt(m.ShellSessions.List(false)); text != "" {
			sections = append(sections, runtimemodule.ContextSection{
				Key:        "active_shell_sessions",
				Label:      "Active Shell Sessions",
				Source:     "runtime",
				Text:       text,
				Projection: runtimemodule.ContextProjectionSystemPrompt,
				Budget:     runtimemodule.UnboundedContextBudget(),
			})
		}
	}
	sections = append(sections, runtimemodule.ContextSection{
		Key:        "operating_context",
		Label:      "Operating Context",
		Source:     "runtime",
		Text:       operatingContext(m.WorkDir, m.Shell, m.Now),
		Projection: runtimemodule.ContextProjectionSystemPrompt,
		Budget:     runtimemodule.UnboundedContextBudget(),
	})
	return sections, nil
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
	return append(files, agentsMDFile{Path: path, Text: fmt.Sprintf("# AGENTS.md (%s)\n\n%s", path, content)})
}

func scratchpadSection(workDir, scratchpadDir string) (runtimemodule.ContextSection, bool) {
	dir := strings.TrimSpace(scratchpadDir)
	if dir == "" {
		return runtimemodule.ContextSection{}, false
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	lines := []string{"## Session Scratchpad", fmt.Sprintf("- path: %s", dir)}
	if rel, ok := scratchpadRelativePath(workDir, dir); ok {
		lines = append(lines, fmt.Sprintf("- workspace-relative path for `write_begin`: %s", rel))
	}
	lines = append(lines,
		"- Use this directory for long drafts, intermediate files, and working material that exceeds the compact Notes budget.",
		"- Scratchpad contents are not automatically added to context. When needed, use `read` or `grep` to retrieve them.",
		"- Keep the current plan and short progress checkpoints in Notes; keep substantial working material here.",
		"- Save important intermediate conclusions here before compaction so a later turn can read them back.",
	)
	return runtimemodule.ContextSection{
		Key: "session_scratchpad", Label: "Session Scratchpad", Source: "runtime", Path: dir, Text: strings.Join(lines, "\n"),
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

func sameCleanPath(left, right string) bool {
	if left == "" || right == "" {
		return false
	}
	leftAbs, leftErr := filepath.Abs(left)
	rightAbs, rightErr := filepath.Abs(right)
	if leftErr == nil && rightErr == nil {
		return filepath.Clean(leftAbs) == filepath.Clean(rightAbs)
	}
	return filepath.Clean(left) == filepath.Clean(right)
}
