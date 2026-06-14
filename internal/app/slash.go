package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/session"
)

const (
	SlashCompact = "/compact"
	SlashNew     = "/new"
	SlashStatus  = "/status"
)

var slashCommandNames = []string{SlashCompact, SlashNew, SlashStatus}

type SlashCommand struct {
	Name string `json:"name"`
	Args string `json:"args,omitempty"`
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
	if !isSlashCommandName(commandName) {
		return SlashCommand{}, false, nil
	}
	if commandName == SlashCompact {
		args := strings.TrimSpace(strings.TrimPrefix(trimmed, commandName))
		return SlashCommand{Name: commandName, Args: args}, true, nil
	}
	if len(fields) == 1 {
		return SlashCommand{Name: commandName}, true, nil
	}
	args := strings.TrimSpace(strings.TrimPrefix(trimmed, commandName))
	return SlashCommand{}, true, &SlashCommandArgumentsError{Name: commandName, Args: args}
}

func isSlashCommandName(commandName string) bool {
	for _, name := range slashCommandNames {
		if commandName == name {
			return true
		}
	}
	return false
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
		compact, err := a.CompactWithInstructions(ctx, "manual", false, cmd.Args)
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
	case SlashNew:
		if err := a.SwitchToNewPrimarySession(); err != nil {
			return SlashCommandResult{}, err
		}
		status := a.StatusSnapshot(time.Now().UTC())
		text := fmt.Sprintf("New primary session: %s", status.SessionID)
		return SlashCommandResult{Name: cmd.Name, Text: text, Status: &status}, nil
	default:
		return SlashCommandResult{}, &UnknownSlashCommandError{Input: cmd.Name}
	}
}

type StatusSnapshot struct {
	SessionID    string                      `json:"session_id"`
	SessionDir   string                      `json:"session_dir,omitempty"`
	SessionKind  string                      `json:"session_kind,omitempty"`
	Active       bool                        `json:"active"`
	WorkDir      string                      `json:"work_dir"`
	Turns        int                         `json:"turns"`
	LastActiveAt time.Time                   `json:"last_active_at"`
	Provider     ProviderStatusSnapshot      `json:"provider"`
	MCP          MCPStatus                   `json:"mcp"`
	SkillCount   int                         `json:"skill_count"`
	TokenUsage   llm.Usage                   `json:"token_usage"`
	TokenTotal   int                         `json:"token_total"`
	ContextUsage *llm.ContextUsage           `json:"context_usage,omitempty"`
	Compaction   StatusCompactionSnapshot    `json:"compaction"`
	SuccessRates StatusSuccessRatesSnapshot  `json:"success_rates"`
	PendingInput runtime.PendingInputStatus  `json:"pending_input"`
	Goal         *runtime.GoalStatusSnapshot `json:"goal,omitempty"`
}

type ProviderStatusSnapshot struct {
	ID       string `json:"id,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	Model    string `json:"model,omitempty"`
	BaseURL  string `json:"base_url,omitempty"`
}

type StatusCompactionSnapshot struct {
	Count        int `json:"count"`
	MemoryTokens int `json:"memory_tokens"`
}

type StatusSuccessRatesSnapshot struct {
	LLMRequests   int `json:"llm_requests"`
	LLMSuccesses  int `json:"llm_successes"`
	ToolRequests  int `json:"tool_requests"`
	ToolSuccesses int `json:"tool_successes"`
}

const (
	statusIconHeading     = "\U0001F4CA"
	statusIconSession     = "\U0001F4AC"
	statusIconSessionKind = "\U0001F4CC"
	statusIconWorkDir     = "\U0001F4C1"
	statusIconProvider    = "\U0001F916"
	statusIconMCP         = "\U0001F50C"
	statusIconSkills      = "\U0001F9E9"
	statusIconTokens      = "\U0001F522"
	statusIconContext     = "\U0001F9E0"
	statusIconCompact     = "\U0001F5DC\ufe0f"
	statusIconSuccess     = "\U0001F4C8"
	statusIconTurn        = "\u2699\ufe0f"
	statusIconQueuedInput = "\U0001F4E5"
	statusIconGoal        = "\U0001F3AF"
)

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
		sessionKind  string
		active       bool
		tokenUsage   llm.Usage
		contextUsage *llm.ContextUsage
		compaction   StatusCompactionSnapshot
		successRates StatusSuccessRatesSnapshot
	)
	if a.Session != nil {
		info, history := a.Session.Snapshot(now)
		sessionID = info.ID
		sessionDir = info.Dir
		sessionKind = info.Kind
		active = info.Active
		turns = info.Turns
		lastActiveAt = info.LastActiveAt
		tokenUsage = info.TokenUsage
		if info.ContextUsage != nil {
			copied := *info.ContextUsage
			copied.Breakdown = append([]llm.ContextUsagePart(nil), info.ContextUsage.Breakdown...)
			contextUsage = &copied
		}
		compaction = compactionStatusFromHistory(history)
		successRates = successRatesFromSessionStats(a.Session.RuntimeStats())
	}
	pending := runtime.PendingInputStatus{}
	var goal *runtime.GoalStatusSnapshot
	if a.Engine != nil {
		pending = a.Engine.PendingInputStatus()
		if snapshot, err := a.Engine.GoalStatusSnapshot(); err == nil {
			goal = snapshot
		}
	}
	return StatusSnapshot{
		SessionID:    sessionID,
		SessionDir:   sessionDir,
		SessionKind:  sessionKind,
		Active:       active,
		WorkDir:      a.cfg.WorkDir,
		Turns:        turns,
		LastActiveAt: lastActiveAt,
		Provider:     a.providerStatusSnapshot(),
		MCP:          a.MCPStatus(),
		SkillCount:   len(a.skills),
		TokenUsage:   tokenUsage,
		TokenTotal:   tokenUsage.TotalTokens(),
		ContextUsage: contextUsage,
		Compaction:   compaction,
		SuccessRates: successRates,
		PendingInput: pending,
		Goal:         goal,
	}
}

func (a *App) providerStatusSnapshot() ProviderStatusSnapshot {
	if a == nil {
		return ProviderStatusSnapshot{}
	}
	status := providerRuntimeStatusFromConfig(a.cfg)
	return ProviderStatusSnapshot{
		ID:       status.ID,
		Protocol: status.Protocol,
		Model:    status.Model,
		BaseURL:  status.BaseURL,
	}
}

func (s StatusSnapshot) Text() string {
	var lines []string
	lines = append(lines, statusLabel(statusIconHeading, "Juex status"))
	if s.SessionID != "" {
		lines = append(lines, statusLabel(statusIconSession, fmt.Sprintf("session: %s (%d turns)", s.SessionID, s.Turns)))
	}
	if s.SessionKind != "" {
		state := "inactive"
		if s.Active {
			state = "active"
		}
		lines = append(lines, statusLabel(statusIconSessionKind, fmt.Sprintf("session kind: %s (%s)", s.SessionKind, state)))
	}
	if s.WorkDir != "" {
		lines = append(lines, statusLabel(statusIconWorkDir, "workdir: "+s.WorkDir))
	}
	lines = append(lines, statusLabel(statusIconProvider, "model: "+formatModelSnapshot(s.Provider)))
	lines = append(lines, statusLabel(statusIconMCP, fmt.Sprintf("mcp: %d/%d connected, %d errors", s.MCP.Connected, s.MCP.Configured, s.MCP.Errors)))
	lines = append(lines, statusLabel(statusIconSkills, fmt.Sprintf("skills: %d", s.SkillCount)))
	lines = append(lines, statusLabel(statusIconTokens, FormatTokenUsage(s.TokenUsage)))
	if s.ContextUsage != nil {
		lines = append(lines, statusLabel(statusIconContext, "context: "+formatContextUsage(*s.ContextUsage)))
	} else {
		lines = append(lines, statusLabel(statusIconContext, "context: not measured yet"))
	}
	lines = append(lines, statusLabel(statusIconCompact, formatCompactionStatus(s.Compaction)))
	lines = append(lines, statusLabel(statusIconSuccess, formatSuccessRates(s.SuccessRates)))
	if s.Goal != nil {
		lines = append(lines, statusLabel(statusIconGoal, formatGoalStatus(s.Goal)))
		if s.Goal.LastCheck != nil {
			lines = append(lines, statusLabel(statusIconGoal, formatCompletionCheckStatus(s.Goal.LastCheck)))
		}
	}
	turnState := "idle"
	if s.PendingInput.TurnID != "" {
		turnState = "running"
	}
	lines = append(lines, statusLabel(statusIconTurn, "turn: "+turnState))
	if s.PendingInput.MaxPendingInputs > 0 {
		lines = append(lines, statusLabel(statusIconQueuedInput, fmt.Sprintf("queued input: %d/%d", s.PendingInput.PendingCount, s.PendingInput.MaxPendingInputs)))
	} else {
		lines = append(lines, statusLabel(statusIconQueuedInput, fmt.Sprintf("queued input: %d", s.PendingInput.PendingCount)))
	}
	return strings.Join(lines, "\n")
}

func formatGoalStatus(goal *runtime.GoalStatusSnapshot) string {
	if goal == nil {
		return "goal: none"
	}
	status := string(goal.Status)
	if status == "" {
		status = "unknown"
	}
	objective := strings.TrimSpace(goal.Objective)
	if objective == "" {
		return "goal: " + status
	}
	return fmt.Sprintf("goal: %s - %s", status, objective)
}

func formatCompletionCheckStatus(check *runtime.CompletionCheck) string {
	if check == nil {
		return "completion: none"
	}
	status := string(check.Status)
	if status == "" {
		status = "unknown"
	}
	summary := strings.TrimSpace(check.Summary)
	if summary == "" {
		return "completion: " + status
	}
	return fmt.Sprintf("completion: %s - %s", status, summary)
}

func statusLabel(icon, text string) string {
	return icon + " " + text
}

func compactionStatusFromHistory(history []llm.Message) StatusCompactionSnapshot {
	var status StatusCompactionSnapshot
	for _, msg := range history {
		if msg.Kind != llm.MessageKindCompact {
			continue
		}
		status.Count++
		status.MemoryTokens = compactMemoryTokens(msg)
	}
	return status
}

func compactMemoryTokens(msg llm.Message) int {
	if msg.Compaction != nil && msg.Compaction.SummaryChars > 0 {
		return runtime.EstimateCharsAsTokens(msg.Compaction.SummaryChars)
	}
	return runtime.EstimateTextTokens(msg.FirstText())
}

func successRatesFromSessionStats(stats session.RuntimeStats) StatusSuccessRatesSnapshot {
	return StatusSuccessRatesSnapshot{
		LLMRequests:   stats.LLMRequests,
		LLMSuccesses:  stats.LLMSuccesses,
		ToolRequests:  stats.ToolRequests,
		ToolSuccesses: stats.ToolSuccesses,
	}
}

func formatModelSnapshot(p ProviderStatusSnapshot) string {
	switch {
	case p.ID != "" && p.Model != "":
		return p.ID + "/" + p.Model
	case p.Model != "":
		return p.Model
	case p.ID != "":
		return p.ID
	default:
		return "not configured"
	}
}

func formatContextUsage(usage llm.ContextUsage) string {
	tokens := fmt.Sprintf("%d tokens", usage.TotalTokens)
	if usage.ContextWindow > 0 {
		tokens = fmt.Sprintf("%d/%d tokens", usage.TotalTokens, usage.ContextWindow)
	}
	return fmt.Sprintf("%s, cache hit %s", tokens, percent(usage.CachedInputTokens, usage.InputTokens))
}

func formatCompactionStatus(status StatusCompactionSnapshot) string {
	memory := "0 tokens"
	if status.MemoryTokens > 0 {
		memory = fmt.Sprintf("~%d tokens", status.MemoryTokens)
	}
	return fmt.Sprintf("compact: %d, memory: %s", status.Count, memory)
}

func formatSuccessRates(status StatusSuccessRatesSnapshot) string {
	return fmt.Sprintf("success: llm %s, tools %s",
		formatSuccessRate(status.LLMSuccesses, status.LLMRequests),
		formatSuccessRate(status.ToolSuccesses, status.ToolRequests))
}

func formatSuccessRate(successes, requests int) string {
	if requests <= 0 {
		return "n/a"
	}
	return fmt.Sprintf("%d/%d (%s)", successes, requests, percent(successes, requests))
}

func percent(numerator, denominator int) string {
	if denominator <= 0 {
		return "n/a"
	}
	rate := float64(numerator) * 100 / float64(denominator)
	if rate == float64(int(rate)) {
		return fmt.Sprintf("%.0f%%", rate)
	}
	return fmt.Sprintf("%.1f%%", rate)
}
