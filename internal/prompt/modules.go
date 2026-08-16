package prompt

import (
	"context"
	"path/filepath"
	"time"

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
