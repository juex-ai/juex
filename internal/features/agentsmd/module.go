// Package agentsmd owns automatic AGENTS.md guidance loading.
package agentsmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
)

const ModuleID runtimemodule.ID = "agents-md"

type Module struct {
	GlobalAgentsMDPath string
	AgentsMDDirs       []string
}

func (*Module) ID() runtimemodule.ID { return ModuleID }

func (m *Module) Context(_ context.Context, request runtimemodule.ContextRequest) ([]runtimemodule.ContextSection, error) {
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
