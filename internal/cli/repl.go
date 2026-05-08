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
			resumeDir, err := resolveSessionDir(rf, cfg.SessionsDir(), cmd.InOrStdin(), cmd.OutOrStdout(), stdinIsTTY())
			if err != nil {
				return err
			}
			a, err := app.New(app.Options{
				Config:    cfg,
				Verbose:   flags.verbose,
				WorkDir:   cfg.WorkDir,
				Stderr:    cmd.ErrOrStderr(),
				ResumeDir: resumeDir,
			})
			if err != nil {
				return err
			}
			defer func() { _ = a.Close() }()
			cmdPrintln(cmd, "juex repl - type your prompt (empty line + Ctrl-D to quit)")
			return a.REPL(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&rf.Resume, "resume", false, "interactively pick a past session to resume")
	cmd.Flags().StringVar(&rf.Session, "session", "", "resume a specific session id")
	return cmd
}
