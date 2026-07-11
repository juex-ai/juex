package cli

import (
	"github.com/spf13/cobra"

	"github.com/juex-ai/juex/internal/app"
)

func newREPLCmd(flags *persistentFlags) *cobra.Command {
	var rf resumeFlags
	var newSession bool
	cmd := &cobra.Command{
		Use:   "repl",
		Short: "Interactive REPL: read a prompt from stdin, print the answer, repeat",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(flags)
			if err != nil {
				return err
			}
			if err := ensureSelectedRuntimeConfig(cfg); err != nil {
				return err
			}
			resumeDir, err := resolveSessionDir(rf, cfg.SessionsDir(), cfg.HistoryPath(), cmd.InOrStdin(), cmd.OutOrStdout(), stdinIsTTY())
			if err != nil {
				return err
			}
			if newSession && (rf.Resume != "" || rf.Session != "") {
				return &usageError{msg: "pass --new or --resume/--session, not both"}
			}
			mode := app.SessionModeAttachActive
			if newSession {
				mode = app.SessionModeNewPrimary
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
				return err
			}
			defer a.Close()
			cmdPrintln(cmd, "juex repl - type your prompt; /attach <path> stages an image (empty line + Ctrl-D to quit)")
			cmdPrintln(cmd, app.FormatResourceSummary(a.ResourceSummary()))
			return a.REPL(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&newSession, "new", false, "create a new primary session and make it active")
	cmd.Flags().StringVar(&rf.Resume, "resume", "", "deprecated: resume a past session by id, alias, or 'last'; use sessions activate")
	cmd.Flags().Lookup("resume").NoOptDefVal = resumePick
	cmd.Flags().StringVar(&rf.Session, "session", "", "resume a specific session id")
	cmd.Flags().StringVar(&rf.Alias, "alias", "", "set or update the session alias")
	return cmd
}
