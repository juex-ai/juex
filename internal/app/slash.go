package app

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/observable"
	"github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/runtime/workmem"
	"github.com/juex-ai/juex/internal/thread"
)

const (
	SlashCompact = "/compact"
	SlashGoal    = "/goal"
	SlashNew     = "/new"
	SlashStatus  = "/status"
)

const newThreadGreetingPrompt = "Please greet me briefly, introduce what you can help with in one concise sentence, and ask what I want to do next. You may suggest a concrete place to start."

var slashCommandNames = []string{SlashCompact, SlashGoal, SlashNew, SlashStatus}

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

func NewThreadGreetingPrompt() string {
	return newThreadGreetingPrompt
}

func NewThreadGreetingMessage() llm.Message {
	msg := llm.TextMessage(llm.RoleUser, newThreadGreetingPrompt)
	msg.Kind = llm.MessageKindSystemNotice
	return msg
}

func GoalInstructionPrompt(args string) string {
	args = strings.TrimSpace(args)
	if args == "" {
		return "The user wants to inspect or update the Thread goal. Use get_goal first, then create_goal or update_goal if a goal should be created, changed, marked success, or marked failure. Do not treat this slash command text itself as the goal description."
	}
	return "The user wants to create or update the Thread goal. Use get_goal first, then call create_goal or update_goal as appropriate. Do not write goal state directly; use the goal tools only.\n\nUser goal request:\n" + args
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
	if commandName == SlashCompact || commandName == SlashGoal {
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
		return a.executeCompactSlashCommand(ctx, cmd, "")
	case SlashStatus:
		status := a.StatusSnapshot()
		return SlashCommandResult{Name: cmd.Name, Text: status.Text(), Status: &status}, nil
	case SlashNew:
		if err := a.NewContext(ctx); err != nil {
			return SlashCommandResult{}, err
		}
		status := a.StatusSnapshot()
		text := fmt.Sprintf("New context generation: %s", status.GenerationID)
		return SlashCommandResult{Name: cmd.Name, Text: text, Status: &status}, nil
	default:
		return SlashCommandResult{}, &UnknownSlashCommandError{Input: cmd.Name}
	}
}

func (a *App) executeCompactSlashCommand(ctx context.Context, cmd SlashCommand, admittedTurnID string) (SlashCommandResult, error) {
	var (
		compact runtime.CompactionResult
		err     error
	)
	if admittedTurnID == "" {
		compact, err = a.CompactWithInstructions(ctx, "manual", false, cmd.Args)
	} else {
		compact, err = a.CompactAdmittedWithInstructions(ctx, admittedTurnID, "manual", false, cmd.Args)
	}
	if err != nil {
		return SlashCommandResult{}, err
	}
	text := "No eligible context to compact."
	if compact.MessageID != "" {
		text = fmt.Sprintf("Context compacted: %d -> %d tokens (%d summary chars).",
			compact.TokensBefore, compact.TokensAfter, compact.SummaryChars)
	}
	return SlashCommandResult{Name: cmd.Name, Text: text, Compact: &compact}, nil
}

type StatusSnapshot struct {
	ThreadID     string                      `json:"thread_id"`
	ThreadDir    string                      `json:"thread_dir,omitempty"`
	ThreadAlias  string                      `json:"thread_alias,omitempty"`
	GenerationID string                      `json:"generation_id"`
	State        string                      `json:"state"`
	WorkDir      string                      `json:"work_dir"`
	Turns        int                         `json:"turns"`
	StartedAt    time.Time                   `json:"started_at"`
	LastActiveAt time.Time                   `json:"last_active_at"`
	Provider     ProviderStatusSnapshot      `json:"provider"`
	MCP          MCPStatus                   `json:"mcp"`
	Observables  StatusObservablesSnapshot   `json:"observables"`
	SkillCount   int                         `json:"skill_count"`
	TokenUsage   llm.Usage                   `json:"token_usage"`
	TokenTotal   int                         `json:"token_total"`
	ContextUsage *llm.ContextUsage           `json:"context_usage,omitempty"`
	Compaction   StatusCompactionSnapshot    `json:"compaction"`
	PendingInput runtime.PendingInputStatus  `json:"pending_input"`
	Goal         *workmem.GoalStatusSnapshot `json:"goal,omitempty"`
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

type StatusObservablesSnapshot struct {
	Configured int `json:"configured"`
	Running    int `json:"running"`
	Errors     int `json:"errors"`
}

type StatusSuccessRatesSnapshot struct {
	LLMRequests   int `json:"llm_requests"`
	LLMSuccesses  int `json:"llm_successes"`
	ToolRequests  int `json:"tool_requests"`
	ToolSuccesses int `json:"tool_successes"`
}

const (
	statusIconThread      = "\U0001F4AC"
	statusIconGeneration  = "\U0001F4CC"
	statusIconWorkDir     = "\U0001F4C1"
	statusIconProvider    = "\U0001F916"
	statusIconMCP         = "\U0001F50C"
	statusIconObservable  = "\U0001F52D"
	statusIconSkills      = "\U0001F9E9"
	statusIconTokens      = "\U0001F522"
	statusIconContext     = "\U0001F9E0"
	statusIconCompact     = "\U0001F5DC\ufe0f"
	statusIconSuccess     = "\U0001F4C8"
	statusIconTurn        = "\u2699\ufe0f"
	statusIconQueuedInput = "\U0001F4E5"
	statusIconGoal        = "\U0001F3AF"
)

func (a *App) StatusSnapshot() StatusSnapshot {
	if a == nil {
		return StatusSnapshot{}
	}
	a.threadMu.RLock()
	defer a.threadMu.RUnlock()
	var (
		threadID     string
		threadDir    string
		threadAlias  string
		generationID string
		threadState  string
		turns        int
		startedAt    time.Time
		lastActiveAt time.Time
		tokenUsage   llm.Usage
		contextUsage *llm.ContextUsage
		compaction   StatusCompactionSnapshot
	)
	if a.Thread != nil {
		info := a.Thread.Info()
		replay := a.Thread.ReplaySnapshot()
		threadID = info.ID
		threadDir = info.Dir
		threadAlias = info.Alias
		generationID = info.GenerationID
		threadState = string(info.ExecutionState)
		turns = info.TurnCount
		startedAt = info.CreatedAt.Time
		lastActiveAt = replay.Projection.LastActivityAt.Time
		tokenUsage = info.TokenUsage
		if info.ContextUsage != nil {
			copied := *info.ContextUsage
			copied.Breakdown = append([]llm.ContextUsagePart(nil), info.ContextUsage.Breakdown...)
			contextUsage = &copied
		}
		compaction = compactionStatusFromReplay(replay)
	}
	observables := observablesStatusFromManager(a.obsv)
	pending := runtime.PendingInputStatus{}
	var goal *workmem.GoalStatusSnapshot
	if a.Engine != nil {
		pending = a.Engine.PendingInputStatus()
		goal, _ = a.Engine.ThreadStateStatus()
	}
	return StatusSnapshot{
		ThreadID:     threadID,
		ThreadDir:    threadDir,
		ThreadAlias:  threadAlias,
		GenerationID: generationID,
		State:        threadState,
		WorkDir:      a.cfg.WorkDir,
		Turns:        turns,
		StartedAt:    startedAt,
		LastActiveAt: lastActiveAt,
		Provider:     a.providerStatusSnapshot(),
		MCP:          a.MCPStatus(),
		Observables:  observables,
		SkillCount:   len(a.skills),
		TokenUsage:   tokenUsage,
		TokenTotal:   tokenUsage.TotalTokens(),
		ContextUsage: contextUsage,
		Compaction:   compaction,
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
	if s.ThreadID != "" {
		lines = append(lines, statusLabel(statusIconThread, formatThreadStatus(s.ThreadID, s.ThreadAlias, s.Turns, s.StartedAt)))
	}
	if s.GenerationID != "" {
		lines = append(lines, statusLabel(statusIconGeneration, fmt.Sprintf("generation: %s (%s)", s.GenerationID, s.State)))
	}
	if s.WorkDir != "" {
		lines = append(lines, statusLabel(statusIconWorkDir, "workdir: "+s.WorkDir))
	}
	lines = append(lines, statusLabel(statusIconProvider, "model: "+formatModelSnapshot(s.Provider)))
	lines = append(lines, statusLabel(statusIconMCP, fmt.Sprintf("mcp: %d/%d connected, %d errors", s.MCP.Connected, s.MCP.Configured, s.MCP.Errors)))
	lines = append(lines, statusLabel(statusIconObservable, formatObservablesStatus(s.Observables)))
	lines = append(lines, statusLabel(statusIconSkills, fmt.Sprintf("skills: %d", s.SkillCount)))
	lines = append(lines, statusLabel(statusIconTokens, FormatTokenUsage(s.TokenUsage)))
	if s.ContextUsage != nil {
		lines = append(lines, statusLabel(statusIconContext, "context: "+formatContextUsage(*s.ContextUsage)))
	} else {
		lines = append(lines, statusLabel(statusIconContext, "context: not measured yet"))
	}
	lines = append(lines, statusLabel(statusIconCompact, formatCompactionStatus(s.Compaction)))
	if s.Goal != nil {
		lines = append(lines, statusLabel(statusIconGoal, formatGoalStatus(s.Goal)))
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

func formatGoalStatus(goal *workmem.GoalStatusSnapshot) string {
	if goal == nil {
		return "goal: none"
	}
	status := string(goal.Status)
	if status == "" {
		status = "unknown"
	}
	description := strings.TrimSpace(goal.Description)
	if description == "" {
		return "goal: " + status
	}
	return fmt.Sprintf("goal: %s - %s", status, description)
}

func statusLabel(icon, text string) string {
	return icon + " " + text
}

func observablesStatusFromManager(manager *observable.Manager) StatusObservablesSnapshot {
	if manager == nil {
		return StatusObservablesSnapshot{}
	}
	counts := manager.Counts()
	return StatusObservablesSnapshot{
		Configured: counts.Configured,
		Running:    counts.Running,
		Errors:     counts.Errors,
	}
}

func formatThreadStatus(threadID, alias string, turns int, startedAt time.Time) string {
	label := threadID
	if alias != "" {
		label += " (" + alias + ")"
	}
	if startedAt.IsZero() {
		return fmt.Sprintf("thread: %s (%d turns)", label, turns)
	}
	return fmt.Sprintf("thread: %s (started %s, %d turns)", label, formatStatusLocalTime(startedAt), turns)
}

func formatStatusLocalTime(t time.Time) string {
	return t.Local().Format("2006-01-02 15:04:05")
}

func compactionStatusFromReplay(replay thread.ReplayState) StatusCompactionSnapshot {
	status := StatusCompactionSnapshot{Count: replay.CompactionCount}
	for _, activity := range replay.Activities {
		if activity.Type != thread.FactContextCompacted || activity.Summary == nil {
			continue
		}
		status.MemoryTokens = compactMemoryTokens(*activity.Summary)
	}
	return status
}

func compactMemoryTokens(msg llm.Message) int {
	return runtime.EstimateTextTokens(msg.FirstText())
}

func formatModelSnapshot(p ProviderStatusSnapshot) string {
	switch {
	case p.ID != "" && p.Model != "":
		return p.ID + ":" + p.Model
	case p.Model != "":
		return p.Model
	case p.ID != "":
		return p.ID
	default:
		return "not configured"
	}
}

func formatContextUsage(usage llm.ContextUsage) string {
	tokens := fmt.Sprintf("~%s tokens", FormatCompactTokenCount(usage.TotalTokens))
	if usage.ContextWindow > 0 {
		tokens = fmt.Sprintf("~%s/%s tokens", FormatCompactTokenCount(usage.TotalTokens), FormatCompactTokenCount(usage.ContextWindow))
	}
	return fmt.Sprintf("%s, cache hit %s", tokens, percent(usage.CachedInputTokens, usage.InputTokens))
}

func formatObservablesStatus(status StatusObservablesSnapshot) string {
	return fmt.Sprintf("observables: %d/%d running, %d errors", status.Running, status.Configured, status.Errors)
}

func formatCompactionStatus(status StatusCompactionSnapshot) string {
	memory := "0 tokens"
	if status.MemoryTokens > 0 {
		memory = fmt.Sprintf("~%s tokens", FormatCompactTokenCount(status.MemoryTokens))
	}
	return fmt.Sprintf("compact: %d, memory: %s", status.Count, memory)
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

func FormatCompactTokenCount(value int) string {
	if value <= 0 {
		return "0"
	}
	units := []struct {
		suffix string
		value  float64
	}{
		{"b", 1_000_000_000},
		{"m", 1_000_000},
		{"k", 1_000},
	}
	for _, unit := range units {
		if value >= int(unit.value) {
			formatted := trimCompactFloat(float64(value)/unit.value) + unit.suffix
			switch formatted {
			case "1000k":
				return "1m"
			case "1000m":
				return "1b"
			default:
				return formatted
			}
		}
	}
	return fmt.Sprintf("%d", value)
}

func trimCompactFloat(value float64) string {
	rounded := math.Round(value*10) / 10
	if rounded == math.Trunc(rounded) {
		return fmt.Sprintf("%.0f", rounded)
	}
	return fmt.Sprintf("%.1f", rounded)
}
