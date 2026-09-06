package shelltools

import (
	"context"
	"fmt"
	"strings"

	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/tools"
)

func (m *Module) Context(_ context.Context, request runtimemodule.ContextRequest) ([]runtimemodule.ContextSection, error) {
	if m == nil || request.Purpose != runtimemodule.ContextPurposeProviderIteration {
		return nil, nil
	}
	m.mu.RLock()
	shell, sessions := m.options.Shell, m.options.ShellSessions
	m.mu.RUnlock()
	if shell.Binary == "" {
		shell = tools.DefaultShellProfile()
	}
	sections := []runtimemodule.ContextSection{{
		Key: "shell_guidance", Label: "Shell", Source: "runtime", Text: shellGuidance(shell),
		Projection: runtimemodule.ContextProjectionSystemPrompt, Budget: runtimemodule.UnboundedContextBudget(),
	}}
	if sessions != nil {
		if text := tools.FormatActiveShellSessionsPrompt(sessions.List(false)); text != "" {
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

	return sections, nil
}

func shellGuidance(shell tools.ShellProfile) string {
	lines := []string{"## Shell"}
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
