package cli

import (
	"github.com/spf13/cobra"

	"github.com/juex-ai/juex/internal/app"
)

func newREPLCmd(flags *persistentFlags) *cobra.Command {
	var alias string
	var newSession bool
	var ephemeral bool
	var keep bool
	cmd := &cobra.Command{
		Use:   "repl",
		Short: "Interactive REPL: read a prompt from stdin, print the answer, repeat",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) (runErr error) {
			if err := validateEphemeralFlags(ephemeral, keep, false); err != nil {
				return err
			}
			cfg, lifecycle, err := loadRuntimeConfigForCommand(cmd, flags, keep)
			if err != nil {
				return err
			}
			if lifecycle != nil {
				defer func() {
					runErr = lifecycle.finish(cmd, runErr)
				}()
			}
			if err := ensureSelectedRuntimeConfig(cfg); err != nil {
				return err
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
				Alias:       alias,
				SessionMode: mode,
			})
			if err != nil {
				return err
			}
			defer func() { _ = a.CloseAndWait() }()
			cmdPrintln(cmd, "juex repl - type your prompt; /attach <path> stages an image (empty line + Ctrl-D to quit)")
			cmdPrintln(cmd, app.FormatResourceSummary(a.ResourceSummary()))
			return a.REPL(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	cmd.Flags().BoolVar(&newSession, "new", false, "create a new primary session and make it active")
	cmd.Flags().BoolVar(&ephemeral, "ephemeral", false, "use isolated temporary agent state and remove it on exit")
	cmd.Flags().BoolVar(&keep, "keep", false, "retain and print ephemeral agent state after exit (requires --ephemeral)")
	cmd.Flags().StringVar(&alias, "alias", "", "set or update the session alias")
	declareAgentStatePolicy(cmd, agentStateMint)
	return cmd
}
