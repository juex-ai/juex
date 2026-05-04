package cli

import (
	"github.com/spf13/cobra"

	"github.com/juex-ai/juex/internal/app"
)

func newREPLCmd(flags *persistentFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "repl",
		Short: "Interactive REPL: read a prompt from stdin, print the answer, repeat",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(flags)
			if err != nil {
				return err
			}
			a, err := app.New(app.Options{
				Config:  cfg,
				Verbose: flags.verbose,
				WorkDir: cfg.WorkDir,
				Stderr:  cmd.ErrOrStderr(),
			})
			if err != nil {
				return err
			}
			defer a.Close()
			cmdPrintln(cmd, "juex repl - type your prompt (empty line + Ctrl-D to quit)")
			return a.REPL(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
}
