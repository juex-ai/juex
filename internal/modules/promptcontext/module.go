// Package promptcontext provides the concrete AGENTS.md and Thread
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

	"github.com/juex-ai/juex/internal/modulecatalog"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
)

const (
	GuidanceModuleID      runtimemodule.ID = modulecatalog.AgentsMD
	ThreadContextModuleID runtimemodule.ID = "thread-context"
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

type ThreadContextModule struct {
	WorkDir                 string
	OperatingContextEnabled bool
	ScratchpadEnabled       bool
	Now                     func() time.Time
}

func (*ThreadContextModule) ID() runtimemodule.ID { return ThreadContextModuleID }

func (m *ThreadContextModule) Context(_ context.Context, request runtimemodule.ContextRequest) ([]runtimemodule.ContextSection, error) {
	if m == nil || request.Purpose != runtimemodule.ContextPurposeProviderIteration {
		return nil, nil
	}
	var sections []runtimemodule.ContextSection
	scratchpadDir := ""
	if m.ScratchpadEnabled && request.Thread != nil {
		scratchpadDir = request.Thread.ScratchpadDir
	}
	if section, ok := scratchpadSection(m.WorkDir, scratchpadDir); ok {
		section.Projection = runtimemodule.ContextProjectionSystemPrompt
		section.Budget = runtimemodule.UnboundedContextBudget()
		sections = append(sections, section)
	}
	if m.OperatingContextEnabled {
		sections = append(sections, runtimemodule.ContextSection{
			Key:        "operating_context",
			Label:      "Operating Context",
			Source:     "runtime",
			Text:       operatingContext(m.WorkDir, m.Now),
			Projection: runtimemodule.ContextProjectionSystemPrompt,
			Budget:     runtimemodule.UnboundedContextBudget(),
		})
	}
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
	lines := []string{"## Thread Scratchpad", fmt.Sprintf("- path: %s", dir)}
	if rel, ok := scratchpadRelativePath(workDir, dir); ok {
		lines = append(lines, fmt.Sprintf("- workspace-relative path: %s", rel))
	}
	lines = append(lines,
		"- Use this directory for long drafts, intermediate files, and working material.",
		"- Scratchpad contents are not automatically added to context. Retrieve needed contents with the available file tools.",
		"- Save important intermediate conclusions here before compaction so a later turn can read them back.",
	)
	return runtimemodule.ContextSection{
		Key: "thread_scratchpad", Label: "Thread Scratchpad", Source: "runtime", Path: dir, Text: strings.Join(lines, "\n"),
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

func operatingContext(workDir string, nowFn func() time.Time) string {
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
