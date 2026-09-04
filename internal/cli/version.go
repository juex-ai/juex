package cli

import (
	"github.com/spf13/cobra"

	"github.com/juex-ai/juex/internal/version"
)

func newVersionCmd(flags *persistentFlags) *cobra.Command {
	var (
		verbose bool
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print Juex CLI build information",
		Args:  usageArgs(cobra.NoArgs),
		Example: `  juex version                  # short: "juex 0.0.1"
	  juex version --verbose        # multi-line build metadata
	  juex version --json           # machine-readable`,
		RunE: func(cmd *cobra.Command, args []string) error {
			showBuild := verbose
			info := version.Build()
			switch {
			case jsonOut:
				cmdPrintln(cmd, info.JSON())
			case showBuild:
				cmdPrintln(cmd, info.Verbose())
			default:
				cmdPrintln(cmd, version.String())
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "include commit, build time, Go version, and platform")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "output build information as JSON")
	return cmd
}
