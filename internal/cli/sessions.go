package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/juex-ai/juex/internal/llm"
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
	cmd.AddCommand(newSessionsShowCmd(flags))
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
	fmt.Fprintf(w, "%-32s  %-16s  %-20s  %-5s  %s\n", "ID", "ALIAS", "LAST_ACTIVE", "TURNS", "PREVIEW")
	for _, s := range infos {
		fmt.Fprintf(w, "%-32s  %-16s  %-20s  %5d  %s\n",
			s.ID, truncateRunes(s.Alias, 16), s.LastActiveAt.Format("2006-01-02 15:04:05"), s.Turns, truncateRunes(s.Preview, 60))
	}
}

type sessionsShowOutput struct {
	session.Info
	Messages []llm.Message `json:"messages"`
}

func newSessionsShowCmd(flags *persistentFlags) *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Print one session's metadata and transcript",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return &usageError{msg: "juex sessions show: <id> required"}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(flags)
			if err != nil {
				return err
			}
			id := args[0]
			dir := filepath.Join(cfg.SessionsDir(), id)
			info, msgs, err := session.LoadInfo(dir)
			if err != nil {
				if os.IsNotExist(err) {
					return &notFoundError{msg: "session not found: " + id}
				}
				return err
			}
			switch format {
			case "json", "":
				cmdPrintln(cmd, mustJSON(sessionsShowOutput{Info: info, Messages: msgs}))
			case "text":
				renderSessionText(cmd, info, msgs)
			default:
				return &usageError{msg: "unknown --format value: " + format}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&format, "format", "json", "json|text")
	return cmd
}

func renderSessionText(cmd *cobra.Command, info session.Info, msgs []llm.Message) {
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "id:             %s\n", info.ID)
	fmt.Fprintf(w, "alias:          %s\n", info.Alias)
	fmt.Fprintf(w, "started_at:     %s\n", info.StartedAt.Format(time.RFC3339))
	fmt.Fprintf(w, "last_active_at: %s\n", info.LastActiveAt.Format(time.RFC3339))
	fmt.Fprintf(w, "turns:          %d\n\n", info.Turns)
	for _, m := range msgs {
		role := string(m.Role)
		for _, b := range m.Blocks {
			switch b.Type {
			case llm.BlockText:
				fmt.Fprintf(w, "%s> %s\n", role, b.Text)
			case llm.BlockReasoning:
				if b.Redacted {
					fmt.Fprintln(w, "thinking> [redacted]")
				} else {
					fmt.Fprintf(w, "thinking> %s\n", b.Text)
				}
			case llm.BlockToolUse:
				fmt.Fprintf(w, "tool> %s(%v)\n", b.ToolName, b.Input)
			case llm.BlockToolResult:
				fmt.Fprintf(w, "tool< %s\n", b.Content)
			}
		}
	}
}
