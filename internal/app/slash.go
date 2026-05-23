package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/runtime"
)

const (
	SlashCompact = "/compact"
	SlashStatus  = "/status"
)

var slashCommandNames = []string{SlashCompact, SlashStatus}

type SlashCommand struct {
	Name string `json:"name"`
}

type SlashCommandResult struct {
	Name    string                    `json:"name"`
	Text    string                    `json:"text"`
	Compact *runtime.CompactionResult `json:"compact,omitempty"`
	Status  *StatusSnapshot           `json:"status,omitempty"`
}

type UnknownSlashCommandError struct {
	Input string
}

func (e *UnknownSlashCommandError) Error() string {
	return fmt.Sprintf("unknown slash command %q (available: %s)", e.Input, AvailableSlashCommandsText())
}

type SlashCommandArgumentsError struct {
	Name string
	Args string
}

func (e *SlashCommandArgumentsError) Error() string {
	return fmt.Sprintf("slash command %s does not accept arguments: %q", e.Name, e.Args)
}

func SlashCommandNames() []string {
	return append([]string(nil), slashCommandNames...)
}

func AvailableSlashCommandsText() string {
	return strings.Join(slashCommandNames, ", ")
}

func ParseSlashCommand(input string) (SlashCommand, bool, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" || !strings.HasPrefix(trimmed, "/") {
		return SlashCommand{}, false, nil
	}
	fields := strings.Fields(trimmed)
	commandName := fields[0]
	for _, name := range slashCommandNames {
		if commandName == name && len(fields) == 1 {
			return SlashCommand{Name: name}, true, nil
		}
		if commandName == name {
			args := strings.TrimSpace(strings.TrimPrefix(trimmed, commandName))
			return SlashCommand{}, true, &SlashCommandArgumentsError{Name: name, Args: strings.TrimSpace(args)}
		}
	}
	return SlashCommand{}, true, &UnknownSlashCommandError{Input: trimmed}
}

func (a *App) ExecuteSlashCommand(ctx context.Context, input string) (SlashCommandResult, bool, error) {
	cmd, handled, err := ParseSlashCommand(input)
	if err != nil || !handled {
		return SlashCommandResult{}, handled, err
	}
	result, err := a.ExecuteParsedSlashCommand(ctx, cmd)
	return result, true, err
}

func (a *App) ExecuteParsedSlashCommand(ctx context.Context, cmd SlashCommand) (SlashCommandResult, error) {
	switch cmd.Name {
	case SlashCompact:
		compact, err := a.Compact(ctx, "manual", false)
		if err != nil {
			return SlashCommandResult{}, err
		}
		text := "No eligible context to compact."
		if compact.MessageID != "" {
			text = fmt.Sprintf("Context compacted: %d -> %d tokens (%d summary chars).",
				compact.TokensBefore, compact.TokensAfter, compact.SummaryChars)
		}
		return SlashCommandResult{Name: cmd.Name, Text: text, Compact: &compact}, nil
	case SlashStatus:
		status := a.StatusSnapshot(time.Now().UTC())
		return SlashCommandResult{Name: cmd.Name, Text: status.Text(), Status: &status}, nil
	default:
		return SlashCommandResult{}, &UnknownSlashCommandError{Input: cmd.Name}
	}
}

type StatusSnapshot struct {
	SessionID    string                     `json:"session_id"`
	SessionDir   string                     `json:"session_dir,omitempty"`
	WorkDir      string                     `json:"work_dir"`
	Turns        int                        `json:"turns"`
	LastActiveAt time.Time                  `json:"last_active_at"`
	Provider     ProviderStatusSnapshot     `json:"provider"`
	MCP          MCPStatus                  `json:"mcp"`
	SkillCount   int                        `json:"skill_count"`
	TokenUsage   llm.Usage                  `json:"token_usage"`
	TokenTotal   int                        `json:"token_total"`
	ContextUsage *llm.ContextUsage          `json:"context_usage,omitempty"`
	PendingInput runtime.PendingInputStatus `json:"pending_input"`
}

type ProviderStatusSnapshot struct {
	ID       string `json:"id,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	Model    string `json:"model,omitempty"`
	BaseURL  string `json:"base_url,omitempty"`
}

func (a *App) StatusSnapshot(now time.Time) StatusSnapshot {
	if a == nil {
		return StatusSnapshot{}
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	var (
		sessionID    string
		sessionDir   string
		turns        int
		lastActiveAt time.Time
		tokenUsage   llm.Usage
		contextUsage *llm.ContextUsage
	)
	if a.Session != nil {
		info := a.Session.Info(now)
		sessionID = info.ID
		sessionDir = info.Dir
		turns = info.Turns
		lastActiveAt = info.LastActiveAt
		tokenUsage = info.TokenUsage
		if info.ContextUsage != nil {
			copied := *info.ContextUsage
			copied.Breakdown = append([]llm.ContextUsagePart(nil), info.ContextUsage.Breakdown...)
			contextUsage = &copied
		}
	}
	pending := runtime.PendingInputStatus{}
	if a.Engine != nil {
		pending = a.Engine.PendingInputStatus()
	}
	return StatusSnapshot{
		SessionID:    sessionID,
		SessionDir:   sessionDir,
		WorkDir:      a.workDir(),
		Turns:        turns,
		LastActiveAt: lastActiveAt,
		Provider:     a.providerStatusSnapshot(),
		MCP:          a.MCPStatus(),
		SkillCount:   len(a.skills),
		TokenUsage:   tokenUsage,
		TokenTotal:   tokenUsage.TotalTokens(),
		ContextUsage: contextUsage,
		PendingInput: pending,
	}
}

func (a *App) workDir() string {
	if a == nil {
		return ""
	}
	if a.cfg.WorkDir != "" {
		return a.cfg.WorkDir
	}
	return ""
}

func (a *App) providerStatusSnapshot() ProviderStatusSnapshot {
	if a == nil {
		return ProviderStatusSnapshot{}
	}
	cfg := a.cfg
	if cfg.ProviderID == "" && cfg.ProviderProtocol == "" {
		return ProviderStatusSnapshot{Model: cfg.Model, BaseURL: cfg.BaseURL}
	}
	profile, err := cfg.ProviderProfile()
	if err != nil {
		return ProviderStatusSnapshot{
			ID:       cfg.ProviderID,
			Protocol: cfg.ProviderProtocol,
			Model:    cfg.Model,
			BaseURL:  cfg.BaseURL,
		}
	}
	return ProviderStatusSnapshot{
		ID:       profile.ID,
		Protocol: string(profile.Protocol),
		Model:    profile.Model,
		BaseURL:  profile.BaseURL,
	}
}

func (s StatusSnapshot) Text() string {
	var lines []string
	lines = append(lines, "Juex status")
	if s.SessionID != "" {
		lines = append(lines, fmt.Sprintf("session: %s (%d turns)", s.SessionID, s.Turns))
	}
	if s.WorkDir != "" {
		lines = append(lines, "workdir: "+s.WorkDir)
	}
	lines = append(lines, "provider: "+formatProviderSnapshot(s.Provider))
	lines = append(lines, fmt.Sprintf("mcp: %d/%d connected, %d errors", s.MCP.Connected, s.MCP.Configured, s.MCP.Errors))
	lines = append(lines, fmt.Sprintf("skills: %d", s.SkillCount))
	lines = append(lines, FormatTokenUsage(s.TokenUsage))
	if s.ContextUsage != nil {
		lines = append(lines, fmt.Sprintf("context: %d/%d tokens (%s)", s.ContextUsage.TotalTokens, s.ContextUsage.ContextWindow, s.ContextUsage.Model))
	} else {
		lines = append(lines, "context: not measured yet")
	}
	pendingState := "idle"
	if s.PendingInput.TurnID != "" {
		pendingState = "running"
	}
	lines = append(lines, fmt.Sprintf("pending input: %s (%d/%d)", pendingState, s.PendingInput.PendingCount, s.PendingInput.MaxPendingInputs))
	return strings.Join(lines, "\n")
}

func formatProviderSnapshot(p ProviderStatusSnapshot) string {
	var parts []string
	if p.ID != "" {
		parts = append(parts, p.ID)
	}
	if p.Protocol != "" {
		parts = append(parts, p.Protocol)
	}
	if p.Model != "" {
		parts = append(parts, p.Model)
	}
	if p.BaseURL != "" {
		parts = append(parts, p.BaseURL)
	}
	if len(parts) == 0 {
		return "not configured"
	}
	return strings.Join(parts, " / ")
}
