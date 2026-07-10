package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/cancellation"
	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/errorclass"
	"github.com/juex-ai/juex/internal/llm"
	juexruntime "github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/sandbox"
	"github.com/juex-ai/juex/internal/session"
)

// runResult is the JSON shape emitted on success when --json is set.
type runResult struct {
	Text        string    `json:"text"`
	SessionID   string    `json:"session_id"`
	SessionDir  string    `json:"session_dir"`
	SessionKind string    `json:"session_kind"`
	Active      bool      `json:"active"`
	DurationMs  int64     `json:"duration_ms"`
	TokenUsage  llm.Usage `json:"token_usage"`
	TokenTotal  int       `json:"token_total"`
}

// errorJSON mirrors principle 9 (errors as guides):
// type, message, suggestion, retryable.
type errorJSON struct {
	Error      string         `json:"error"`
	Message    string         `json:"message"`
	Suggestion string         `json:"suggestion,omitempty"`
	Retryable  bool           `json:"retryable"`
	SessionID  string         `json:"session_id,omitempty"`
	SessionDir string         `json:"session_dir,omitempty"`
	WorkDir    string         `json:"work_dir,omitempty"`
	Details    map[string]any `json:"details,omitempty"`
}

const externalStopSuggestion = "The run was stopped externally; check the terminal, parent process, benchmark runner, or supervisor logs for the reason."

// dryRunPlan is the JSON shape emitted by `juex run --dry-run`.
//
// Derivable paths (memory_dir / sessions_dir under <work_dir>/.juex)
// are intentionally omitted — readers can reconstruct them from work_dir.
type dryRunPlan struct {
	ProviderID  string              `json:"provider_id,omitempty"`
	Protocol    string              `json:"protocol,omitempty"`
	Model       string              `json:"model"`
	BaseURL     string              `json:"base_url"`
	WorkDir     string              `json:"work_dir"`
	ConfigFile  string              `json:"config_file,omitempty"`
	Prompt      string              `json:"prompt"`
	PromptChars int                 `json:"prompt_chars"`
	SystemChars int                 `json:"system_prompt_chars"`
	ToolCount   int                 `json:"tool_count"`
	Tools       []string            `json:"tools"`
	Shell       config.ShellProfile `json:"shell"`
	Sandbox     sandbox.Policy      `json:"sandbox"`
	SkillCount  int                 `json:"skill_count"`
	Skills      []skillSummary      `json:"skills,omitempty"`
	Resources   string              `json:"resources"`
	Sections    []promptSectionPlan `json:"system_prompt_sections,omitempty"`
	MCP         app.MCPStatus       `json:"mcp"`
}

// skillSummary mirrors what the system prompt's "Available Skills" section
// shows: each skill's name + absolute SKILL.md path. Useful for agents
// that want to enumerate skills programmatically (no parsing the prompt).
type skillSummary struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type promptSectionPlan struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Source string `json:"source"`
	Path   string `json:"path,omitempty"`
	Chars  int    `json:"chars"`
	Tokens int    `json:"tokens"`
}

// noopProvider stands in for the real LLM provider during a dry run so
// app.New can wire everything without requiring an API key. Calling
// Complete returns a sentinel — but dry-run never reaches that point.
type noopProvider struct{}

func (noopProvider) Name() string { return "dry-run" }
func (noopProvider) Complete(ctx context.Context, sys string, h []llm.Message, t []llm.ToolSpec) (llm.Response, error) {
	return llm.Response{}, errors.New("noopProvider: dry run should not call the LLM")
}

func newRunCmd(flags *persistentFlags) *cobra.Command {
	var (
		jsonOut     bool
		dryRun      bool
		newSession  bool
		sideSession bool
		rf          resumeFlags
	)
	cmd := &cobra.Command{
		Use:   "run [flags] <prompt>",
		Short: "Run one turn and print the answer",
		Long: `Single-shot agent invocation. The prompt is the joined positional args.
With --json the result is a JSON object on stdout (text + session metadata);
errors emit a structured JSON object on stderr.
With --dry-run no LLM call is made; instead a JSON preview of the planned
execution is printed and the process exits with code 10.`,
		Example: `  juex run "summarise README.md"
  juex run --config .juex/juex.yaml "what is in scope.txt?"
  juex -C /path/to/project run "do thing"
  juex run --json "do thing" | jq -r .text
  juex run --dry-run "do thing"     # exits 10 with a JSON plan`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) < 1 {
				return &usageError{msg: "juex run: prompt required (positional argument)"}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			// Validate paths BEFORE calling loadConfig so we surface the
			// right exit code (3 not found) instead of a generic error.
			configPath := explicitConfigPath(flags)
			if configPath != "" {
				if _, err := os.Stat(configPath); err != nil {
					return emit(jsonOut, cmd.ErrOrStderr(), &notFoundError{
						msg: "config file not found: " + configPath,
					}, "verify the path exists; default search is ./.juex/juex.yaml", false)
				}
			}
			if flags.cwd != "" {
				if st, err := os.Stat(flags.cwd); err != nil || !st.IsDir() {
					return emit(jsonOut, cmd.ErrOrStderr(), &notFoundError{
						msg: "--cwd is not a valid directory: " + flags.cwd,
					}, "pass an existing directory path", false)
				}
			}
			cfg, err := loadConfig(flags)
			if err != nil {
				return emit(jsonOut, cmd.ErrOrStderr(), err,
					"set top-level model and providers[] entries in .juex/juex.yaml (copy from juex.yaml.example)", false)
			}

			prompt := strings.Join(args, " ")
			if newSession && sideSession {
				return emit(jsonOut, cmd.ErrOrStderr(), &usageError{msg: "pass --new or --side, not both"},
					"use --new for a new primary session or --side for a side session", false)
			}
			if (newSession || sideSession) && (rf.Resume != "" || rf.Session != "") {
				return emit(jsonOut, cmd.ErrOrStderr(), &usageError{msg: "pass --new/--side or --resume/--session, not both"},
					"use 'juex sessions activate <id>' before the default run path", false)
			}

			if dryRun {
				return runDryRun(cmd, flags, cfg, prompt, jsonOut)
			}

			resumeDir, err := resolveSessionDir(rf, cfg.SessionsDir(), cfg.HistoryPath(), cmd.InOrStdin(), cmd.OutOrStdout(), stdinIsTTY())
			if err != nil {
				return emit(jsonOut, cmd.ErrOrStderr(), err,
					"see 'juex sessions list' for valid ids", false)
			}

			mode := app.SessionModeAttachActive
			if newSession {
				mode = app.SessionModeNewPrimary
			}
			if sideSession {
				mode = app.SessionModeNewSide
			}
			a, err := app.New(app.Options{
				Config:      cfg,
				Verbose:     flags.verbose,
				Debug:       flags.debug,
				LogLevel:    flags.logLevel,
				WorkDir:     cfg.WorkDir,
				Stderr:      cmd.ErrOrStderr(),
				ResumeDir:   resumeDir,
				Alias:       rf.Alias,
				SessionMode: mode,
			})
			if err != nil {
				return emit(jsonOut, cmd.ErrOrStderr(), err,
					"check top-level model plus providers[].id/providers[].protocol/providers[].api_key in .juex/juex.yaml", false)
			}
			defer a.Close()
			if flags.verbose {
				fmt.Fprintln(cmd.ErrOrStderr(), app.FormatResourceSummary(a.ResourceSummary()))
			}

			start := time.Now()
			out, err := a.Run(cmd.Context(), prompt)
			if err != nil {
				var slashErr *app.UnknownSlashCommandError
				if errors.As(err, &slashErr) {
					return emit(jsonOut, cmd.ErrOrStderr(), err,
						"available slash commands: "+app.AvailableSlashCommandsText(), false)
				}
				return emitRunError(jsonOut, cmd.ErrOrStderr(), err, a, cfg.WorkDir)
			}
			usage := a.TokenUsage()

			if jsonOut {
				info := a.Session.Info(time.Now().UTC())
				cmdPrintln(cmd, mustJSON(runResult{
					Text:        out,
					SessionID:   a.Session.ID,
					SessionDir:  a.Session.Dir,
					SessionKind: info.Kind,
					Active:      info.Active,
					DurationMs:  time.Since(start).Milliseconds(),
					TokenUsage:  usage,
					TokenTotal:  usage.TotalTokens(),
				}))
			} else {
				cmdPrintln(cmd, out)
				cmdPrintln(cmd, app.FormatTokenUsage(usage))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit a JSON result on stdout (and JSON errors on stderr)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview what would execute (provider, model, prompt size, tool list); skip the LLM call; exit 10")
	cmd.Flags().BoolVar(&newSession, "new", false, "create a new primary session and make it active")
	cmd.Flags().BoolVar(&sideSession, "side", false, "create a side session without changing the active primary")
	cmd.Flags().StringVar(&rf.Resume, "resume", "", "deprecated: resume a past session by id, alias, or 'last'; use sessions activate")
	cmd.Flags().Lookup("resume").NoOptDefVal = resumePick
	cmd.Flags().StringVar(&rf.Session, "session", "", "resume a specific session id")
	cmd.Flags().StringVar(&rf.Alias, "alias", "", "set or update the session alias")
	return cmd
}

// runDryRun wires everything but the LLM call so we can introspect the
// planned execution. Returns *dryRunOK so Execute() picks exit code 10.
func runDryRun(cmd *cobra.Command, flags *persistentFlags, cfg config.Config, userPrompt string, jsonOut bool) error {
	// Build the app with a noop provider — that's the only piece dry-run
	// can't reuse from the live wiring (no API key required for noop).
	a, err := app.New(app.Options{
		Config:              cfg,
		Provider:            noopProvider{},
		Debug:               flags.debug,
		LogLevel:            flags.logLevel,
		WorkDir:             cfg.WorkDir,
		Stderr:              cmd.ErrOrStderr(),
		SuppressMCPWarnings: jsonOut,
	})
	if err != nil {
		return emit(jsonOut, cmd.ErrOrStderr(), err,
			"dry-run wiring failed; check skills/MCP/memory config", false)
	}
	defer a.Close()

	system := a.Engine.Prompt.Build()
	sections := a.Engine.Prompt.Sections()
	toolList := a.Engine.Tools.List()
	tools := make([]string, len(toolList))
	for i, t := range toolList {
		tools[i] = t.Name
	}
	var skillSummaries []skillSummary
	if pb := a.Engine.Prompt; pb != nil && pb.Skills != nil {
		for _, s := range pb.Skills.All() {
			skillSummaries = append(skillSummaries, skillSummary{Name: s.Name, Path: s.Path})
		}
	}
	sectionPlans := make([]promptSectionPlan, 0, len(sections))
	for _, section := range sections {
		sectionPlans = append(sectionPlans, promptSectionPlan{
			Key:    section.Key,
			Label:  section.Label,
			Source: section.Source,
			Path:   section.Path,
			Chars:  len(section.Text),
			Tokens: juexruntime.EstimateTextTokens(section.Text),
		})
	}
	providerID := cfg.ProviderID
	protocol := cfg.ProviderProtocol
	if cfg.ProviderID != "" || cfg.ProviderProtocol != "" {
		profile, err := cfg.ProviderProfile()
		if err != nil {
			return emit(jsonOut, cmd.ErrOrStderr(), err,
				"check top-level model and providers[] entries in .juex/juex.yaml", false)
		}
		providerID = profile.ID
		protocol = string(profile.Protocol)
	}
	plan := dryRunPlan{
		ProviderID:  providerID,
		Protocol:    protocol,
		Model:       cfg.Model,
		BaseURL:     cfg.BaseURL,
		WorkDir:     cfg.WorkDir,
		ConfigFile:  configFileForPlan(flags),
		Prompt:      userPrompt,
		PromptChars: len(userPrompt),
		SystemChars: len(system),
		ToolCount:   len(tools),
		Tools:       tools,
		Shell:       cfg.Shell,
		Sandbox:     cfg.SandboxPolicy(),
		SkillCount:  len(skillSummaries),
		Skills:      skillSummaries,
		Resources:   app.FormatResourceSummary(a.ResourceSummary()),
		Sections:    sectionPlans,
		MCP:         a.MCPStatus(),
	}

	if jsonOut {
		cmdPrintln(cmd, mustJSON(plan))
	} else {
		cmdPrintln(cmd, "DRY RUN — would execute:")
		cmdPrintln(cmd, mustJSON(plan))
	}
	return &dryRunOK{msg: "dry run complete"}
}

func configFileForPlan(flags *persistentFlags) string {
	return explicitConfigPath(flags)
}

// emit prints err in the right format and returns it (so cobra picks the
// exit code via Execute's switch). On --json: structured JSON on stderr.
// In plain mode we let cobra print its own "Error: ..." line.
func emit(jsonOut bool, stderr io.Writer, err error, suggestion string, retryable bool) error {
	err = cancellation.NormalizeError(err)
	if cancellation.IsUserCancelled(err) {
		retryable = false
	}
	if jsonOut {
		body := errorJSON{
			Error:      errorType(err),
			Message:    errorclass.PublicMessage(err, errorclass.MessageOptions{}),
			Suggestion: suggestion,
			Retryable:  retryable,
		}
		fmt.Fprintln(stderr, mustJSON(body))
		return &emittedError{err: err}
	}
	return err
}

func emitRunError(jsonOut bool, stderr io.Writer, err error, a *app.App, workDir string) error {
	suggestion := "see events.jsonl in the session dir for full lifecycle trace"
	err = cancellation.NormalizeError(err)
	retryable := true
	details := signalDetails(err)
	if details != nil {
		suggestion = externalStopSuggestion
		retryable = false
	} else if cancellation.IsUserCancelled(err) {
		suggestion = "rerun the command when ready"
		retryable = false
	}
	if jsonOut {
		body := errorJSON{
			Error:      errorType(err),
			Message:    errorclass.PublicMessage(err, errorclass.MessageOptions{}),
			Suggestion: suggestion,
			Retryable:  retryable,
			Details:    details,
		}
		if a != nil && a.Session != nil {
			body.SessionID = a.Session.ID
			body.SessionDir = a.Session.Dir
		}
		body.WorkDir = workDir
		fmt.Fprintln(stderr, mustJSON(body))
		return &emittedError{err: err}
	}
	return err
}

func errorType(err error) string {
	if signalErr, ok := cancellation.AsSignalError(err); ok {
		return string(signalErr.Kind)
	}
	if cancellation.IsUserCancelled(err) {
		return "cancelled"
	}
	if errorclass.IsTimeout(err) {
		return "timeout"
	}
	var lockErr *session.LockError
	if errors.As(err, &lockErr) {
		return "conflict"
	}
	switch err.(type) {
	case *usageError:
		return "usage_error"
	case *notFoundError:
		return "not_found"
	case *permissionError:
		return "permission_denied"
	case *conflictError:
		return "conflict"
	case *dryRunOK:
		return "dry_run_ok"
	default:
		return "general_error"
	}
}

func signalDetails(err error) map[string]any {
	signalErr, ok := cancellation.AsSignalError(err)
	if !ok {
		return nil
	}
	return map[string]any{
		"signal":        signalErr.Signal,
		"signal_number": signalErr.SignalNumber,
		"interrupted":   true,
	}
}

func mustJSON(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf(`{"error":"json_marshal_failed","detail":%q}`, err.Error())
	}
	return string(b)
}
