package cli

import (
	"github.com/spf13/cobra"

	"github.com/juex-ai/juex/internal/app"
)

func newREPLCmd(flags *persistentFlags) *cobra.Command {
	var rf resumeFlags
	cmd := &cobra.Command{
		Use:   "repl",
		Short: "Interactive REPL: read a prompt from stdin, print the answer, repeat",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(flags)
			if err != nil {
				return err
			}
			resumeDir, err := resolveSessionDir(rf, cfg.SessionsDir(), cfg.HistoryPath(), cmd.InOrStdin(), cmd.OutOrStdout(), stdinIsTTY())
			if err != nil {
				return err
			}
			a, err := app.New(app.Options{
				Config:    cfg,
				Verbose:   flags.verbose,
				WorkDir:   cfg.WorkDir,
				Stderr:    cmd.ErrOrStderr(),
				ResumeDir: resumeDir,
				Alias:     rf.Alias,
			})
			if err != nil {
				return err
			}
			defer a.Close()
			cmdPrintln(cmd, "juex repl - type your prompt (empty line + Ctrl-D to quit)")
			return a.REPL(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&rf.Resume, "resume", "", "resume a past session by id, alias, or 'last'; omit value for interactive picker")
	cmd.Flags().Lookup("resume").NoOptDefVal = resumePick
	cmd.Flags().StringVar(&rf.Session, "session", "", "resume a specific session id")
	cmd.Flags().StringVar(&rf.Alias, "alias", "", "set or update the session alias")
	return cmd
}
