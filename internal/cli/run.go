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
	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/llm"
)

// runResult is the JSON shape emitted on success when --json is set.
type runResult struct {
	Text       string `json:"text"`
	SessionID  string `json:"session_id"`
	SessionDir string `json:"session_dir"`
	DurationMs int64  `json:"duration_ms"`
}

// errorJSON mirrors principle 9 (errors as guides):
// type, message, suggestion, retryable.
type errorJSON struct {
	Error      string `json:"error"`
	Message    string `json:"message"`
	Suggestion string `json:"suggestion,omitempty"`
	Retryable  bool   `json:"retryable"`
}

// dryRunPlan is the JSON shape emitted by `juex run --dry-run`.
//
// Derivable paths (memory_dir / sessions_dir under <work_dir>/.agents)
// are intentionally omitted — readers can reconstruct them from work_dir.
type dryRunPlan struct {
	ProviderType string         `json:"provider_type"`
	Model        string         `json:"model"`
	BaseURL      string         `json:"base_url"`
	WorkDir      string         `json:"work_dir"`
	EnvFile      string         `json:"env_file,omitempty"`
	Prompt       string         `json:"prompt"`
	PromptChars  int            `json:"prompt_chars"`
	SystemChars  int            `json:"system_prompt_chars"`
	ToolCount    int            `json:"tool_count"`
	Tools        []string       `json:"tools"`
	SkillCount   int            `json:"skill_count"`
	Skills       []skillSummary `json:"skills,omitempty"`
}

// skillSummary mirrors what the system prompt's "Available Skills" section
// shows: each skill's name + absolute SKILL.md path. Useful for agents
// that want to enumerate skills programmatically (no parsing the prompt).
type skillSummary struct {
	Name string `json:"name"`
	Path string `json:"path"`
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
		jsonOut bool
		dryRun  bool
		rf      resumeFlags
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
  juex run --env .env.local.openai "what is in scope.txt?"
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
			if flags.envPath != "" {
				if _, err := os.Stat(flags.envPath); err != nil {
					return emit(jsonOut, cmd.ErrOrStderr(), &notFoundError{
						msg: "--env file not found: " + flags.envPath,
					}, "verify the path exists; default search is ./.env and ~/.agents/.env", false)
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
					"set PROVIDER_API_TYPE/_BASE/_KEY/_MODEL in .env (see .env.example)", false)
			}

			prompt := strings.Join(args, " ")

			if dryRun {
				return runDryRun(cmd, flags, cfg, prompt, jsonOut)
			}

			resumeDir, err := resolveSessionDir(rf, cfg.SessionsDir(), cmd.InOrStdin(), cmd.OutOrStdout(), stdinIsTTY())
			if err != nil {
				return emit(jsonOut, cmd.ErrOrStderr(), err,
					"see 'juex sessions list' for valid ids", false)
			}

			a, err := app.New(app.Options{
				Config:    cfg,
				Verbose:   flags.verbose,
				WorkDir:   cfg.WorkDir,
				Stderr:    cmd.ErrOrStderr(),
				ResumeDir: resumeDir,
			})
			if err != nil {
				return emit(jsonOut, cmd.ErrOrStderr(), err,
					"check PROVIDER_API_TYPE / PROVIDER_API_KEY / PROVIDER_API_MODEL in your .env file", false)
			}
			defer a.Close()

			start := time.Now()
			out, err := a.Run(cmd.Context(), prompt)
			if err != nil {
				return emit(jsonOut, cmd.ErrOrStderr(), err,
					"see events.jsonl in the session dir for full lifecycle trace", true)
			}

			if jsonOut {
				cmdPrintln(cmd, mustJSON(runResult{
					Text:       out,
					SessionID:  a.Session.ID,
					SessionDir: a.Session.Dir,
					DurationMs: time.Since(start).Milliseconds(),
				}))
			} else {
				cmdPrintln(cmd, out)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit a JSON result on stdout (and JSON errors on stderr)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview what would execute (provider, model, prompt size, tool list); skip the LLM call; exit 10")
	cmd.Flags().BoolVar(&rf.Resume, "resume", false, "interactively pick a past session to resume")
	cmd.Flags().StringVar(&rf.Session, "session", "", "resume a specific session id")
	return cmd
}

// runDryRun wires everything but the LLM call so we can introspect the
// planned execution. Returns *dryRunOK so Execute() picks exit code 10.
func runDryRun(cmd *cobra.Command, flags *persistentFlags, cfg config.Config, userPrompt string, jsonOut bool) error {
	// Build the app with a noop provider — that's the only piece dry-run
	// can't reuse from the live wiring (no API key required for noop).
	a, err := app.New(app.Options{
		Config:   cfg,
		Provider: noopProvider{},
		WorkDir:  cfg.WorkDir,
		Stderr:   cmd.ErrOrStderr(),
	})
	if err != nil {
		return emit(jsonOut, cmd.ErrOrStderr(), err,
			"dry-run wiring failed; check skills/MCP/memory config", false)
	}
	defer a.Close()

	system := a.Engine.Prompt.Build()
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
	plan := dryRunPlan{
		ProviderType: cfg.ProviderType,
		Model:        cfg.Model,
		BaseURL:      cfg.BaseURL,
		WorkDir:      cfg.WorkDir,
		EnvFile:      flags.envPath,
		Prompt:       userPrompt,
		PromptChars:  len(userPrompt),
		SystemChars:  len(system),
		ToolCount:    len(tools),
		Tools:        tools,
		SkillCount:   len(skillSummaries),
		Skills:       skillSummaries,
	}

	if jsonOut {
		cmdPrintln(cmd, mustJSON(plan))
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), "DRY RUN — would execute:")
		fmt.Fprintln(cmd.OutOrStdout(), mustJSON(plan))
	}
	return &dryRunOK{msg: "dry run complete"}
}

// emit prints err in the right format and returns it (so cobra picks the
// exit code via Execute's switch). On --json: structured JSON on stderr.
// In plain mode we let cobra print its own "Error: ..." line.
func emit(jsonOut bool, stderr io.Writer, err error, suggestion string, retryable bool) error {
	if jsonOut {
		body := errorJSON{
			Error:      errorType(err),
			Message:    err.Error(),
			Suggestion: suggestion,
			Retryable:  retryable,
		}
		fmt.Fprintln(stderr, mustJSON(body))
	}
	return err
}

func errorType(err error) string {
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

func mustJSON(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf(`{"error":"json_marshal_failed","detail":%q}`, err.Error())
	}
	return string(b)
}
