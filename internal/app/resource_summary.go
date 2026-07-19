package app

import (
	"fmt"
	"strings"

	juexruntime "github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/skills"
)

type ResourceSummary struct {
	SkillCount             int
	SkillPromptTokens      int
	SkillPromptBudgetChars int
	SkillPromptOmitted     int
	SkillFiltered          int
	MCPConfigured          int
	MCPConnected           int
	MCPToolCount           int
	MCPServers             []string
	AgentsSources          []string
}

func (a *App) ResourceSummary() ResourceSummary {
	if a == nil {
		return ResourceSummary{}
	}
	summary := ResourceSummary{SkillCount: len(a.skills)}
	if a.Engine != nil {
		sections := a.Engine.PromptSections()
		for _, section := range sections {
			switch section.Key {
			case "skills":
				summary.SkillPromptTokens = juexruntime.EstimateTextTokens(section.Text)
			case "agents":
				if section.Source != "" {
					summary.AgentsSources = appendIfMissing(summary.AgentsSources, section.Source)
				}
			}
		}
		if report, filtered, ok := a.Engine.PromptSkillStatus(); ok {
			summary.SkillPromptBudgetChars = report.BudgetChars
			summary.SkillPromptOmitted = len(report.Omitted)
			summary.SkillFiltered = filtered
		}
	}
	mcpStatus := a.MCPStatus()
	summary.MCPConfigured = mcpStatus.Configured
	summary.MCPConnected = mcpStatus.Connected
	for _, server := range mcpStatus.Servers {
		summary.MCPToolCount += server.ToolCount
		label := server.Name
		if server.ToolCount > 0 {
			label = fmt.Sprintf("%s:%d", server.Name, server.ToolCount)
		}
		summary.MCPServers = append(summary.MCPServers, label)
	}
	return summary
}

func (a *App) Skills() []skills.Skill {
	if a == nil {
		return nil
	}
	return append([]skills.Skill(nil), a.skills...)
}

func FormatResourceSummary(summary ResourceSummary) string {
	skills := fmt.Sprintf("%d skills (~%d tok", summary.SkillCount, summary.SkillPromptTokens)
	if summary.SkillPromptBudgetChars > 0 {
		skills += fmt.Sprintf(", budget %d chars", summary.SkillPromptBudgetChars)
	}
	if summary.SkillPromptOmitted > 0 {
		skills += fmt.Sprintf(", %d omitted", summary.SkillPromptOmitted)
	}
	if summary.SkillFiltered > 0 {
		skills += fmt.Sprintf(", %d filtered", summary.SkillFiltered)
	}
	skills += ")"
	mcp := fmt.Sprintf("%d MCP", summary.MCPConfigured)
	if len(summary.MCPServers) > 0 {
		mcp += " (" + strings.Join(summary.MCPServers, ", ") + ")"
	}
	agents := "none"
	if len(summary.AgentsSources) > 0 {
		agents = strings.Join(summary.AgentsSources, "+")
	}
	return fmt.Sprintf("resources: %s, %s, AGENTS.md: %s", skills, mcp, agents)
}

func appendIfMissing(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
