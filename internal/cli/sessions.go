package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/juex-ai/juex/internal/session"
)

// sessionsListOutput is the documented JSON shape for `sessions list`.
type sessionsListOutput struct {
	Sessions []session.Info `json:"sessions"`
}

func newSessionsCmd(flags *persistentFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sessions",
		Short: "List, show, and resume past sessions",
	}
	cmd.AddCommand(newSessionsListCmd(flags))
	return cmd
}

func newSessionsListCmd(flags *persistentFlags) *cobra.Command {
	var (
		format string
		limit  int
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List past sessions in the current WorkDir, newest activity first",
		Args:  cobra.NoArgs,
		Example: `  juex sessions list
  juex sessions list --format table
  juex sessions list --limit 5`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(flags)
			if err != nil {
				return err
			}
			infos, err := session.List(cfg.SessionsDir())
			if err != nil {
				return err
			}
			if limit > 0 && limit < len(infos) {
				infos = infos[:limit]
			}
			if infos == nil {
				infos = []session.Info{}
			}
			switch format {
			case "table":
				renderSessionsTable(cmd, infos)
			case "json", "":
				cmdPrintln(cmd, mustJSON(sessionsListOutput{Sessions: infos}))
			default:
				return &usageError{msg: "unknown --format value: " + format}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&format, "format", "json", "json|table")
	cmd.Flags().IntVar(&limit, "limit", 0, "max sessions to return; 0 = unlimited")
	return cmd
}

func renderSessionsTable(cmd *cobra.Command, infos []session.Info) {
	if len(infos) == 0 {
		cmdPrintln(cmd, "(no sessions)")
		return
	}
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "%-32s  %-20s  %-5s  %s\n", "ID", "LAST_ACTIVE", "TURNS", "PREVIEW")
	for _, s := range infos {
		fmt.Fprintf(w, "%-32s  %-20s  %5d  %s\n",
			s.ID, s.LastActiveAt.Format("2006-01-02 15:04:05"), s.Turns, truncateRunes(s.Preview, 60))
	}
}
