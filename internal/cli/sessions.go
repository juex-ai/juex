package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/session"
)

// sessionsListOutput is the documented JSON shape for `sessions list`.
type sessionsListOutput struct {
	Sessions []session.Info `json:"sessions"`
}

func newSessionsCmd(flags *persistentFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sessions",
		Short: "List, show, delete, and resume past sessions",
	}
	cmd.AddCommand(newSessionsListCmd(flags))
	cmd.AddCommand(newSessionsShowCmd(flags))
	cmd.AddCommand(newSessionsContextCmd(flags))
	cmd.AddCommand(newSessionsCompactCmd(flags))
	cmd.AddCommand(newSessionsActivateCmd(flags))
	cmd.AddCommand(newSessionsDeleteCmd(flags))
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
			cfg, err := loadConfigForCommand(cmd, flags)
			if err != nil {
				return err
			}
			infos, err := session.List(cfg.SessionsDir())
			if err != nil {
				return err
			}
			infos, err = session.MarkActive(cfg.HistoryPath(), infos)
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
	fmt.Fprintf(w, "%-32s  %-8s  %-6s  %-16s  %-20s  %-5s  %s\n", "ID", "KIND", "ACTIVE", "ALIAS", "LAST_ACTIVE", "TURNS", "PREVIEW")
	for _, s := range infos {
		active := ""
		if s.Active {
			active = "yes"
		}
		fmt.Fprintf(w, "%-32s  %-8s  %-6s  %-16s  %-20s  %5d  %s\n",
			s.ID, s.Kind, active, truncateRunes(s.Alias, 16), s.LastActiveAt.Format("2006-01-02 15:04:05"), s.Turns, truncateRunes(s.Preview, 60))
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
			cfg, err := loadConfigForCommand(cmd, flags)
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
	fmt.Fprintf(w, "kind:           %s\n", info.Kind)
	fmt.Fprintf(w, "active:         %t\n", info.Active)
	fmt.Fprintf(w, "started_at:     %s\n", info.StartedAt.Format(time.RFC3339))
	fmt.Fprintf(w, "last_active_at: %s\n", info.LastActiveAt.Format(time.RFC3339))
	fmt.Fprintf(w, "turns:          %d\n\n", info.Turns)
	for _, m := range msgs {
		role := string(m.Role)
		for _, b := range m.Blocks {
			switch b.Type {
			case llm.BlockText:
				fmt.Fprintf(w, "%s> %s\n", role, b.Text)
			case llm.BlockImage:
				fmt.Fprintf(w, "%s> %s\n", role, llm.FormatImagePlaceholder(b.Media))
			case llm.BlockReasoning:
				if b.Redacted {
					fmt.Fprintln(w, "thinking> [redacted]")
				} else {
					fmt.Fprintf(w, "thinking> %s\n", b.Text)
				}
			case llm.BlockToolUse:
				fmt.Fprintf(w, "tool> %s(%v)\n", b.ToolName, b.Input)
			case llm.BlockToolResult:
				if b.Content != "" {
					fmt.Fprintf(w, "tool< %s\n", b.Content)
				}
				if b.Media != nil {
					fmt.Fprintf(w, "tool< %s\n", llm.FormatImagePlaceholder(b.Media))
				}
				if b.Content == "" && b.Media == nil {
					fmt.Fprintln(w, "tool< ")
				}
			}
		}
	}
}

func newSessionsActivateCmd(flags *persistentFlags) *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "activate <id>",
		Short: "Make a primary session the active workspace session",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return &usageError{msg: "juex sessions activate: <id> required"}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfigForCommand(cmd, flags)
			if err != nil {
				return err
			}
			id := args[0]
			info, err := session.Activate(cfg.SessionsDir(), cfg.HistoryPath(), id)
			if err != nil {
				if os.IsNotExist(err) {
					return &notFoundError{msg: "session not found: " + id}
				}
				if errors.Is(err, session.ErrCannotActivateSide) {
					return &usageError{msg: "side sessions cannot become active: " + id}
				}
				return err
			}
			switch format {
			case "json", "":
				cmdPrintln(cmd, mustJSON(info))
			case "text":
				fmt.Fprintf(cmd.OutOrStdout(), "active session: %s\n", info.ID)
			default:
				return &usageError{msg: "unknown --format value: " + format}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&format, "format", "json", "json|text")
	return cmd
}

func newSessionsContextCmd(flags *persistentFlags) *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "context <id>",
		Short: "Print the active provider context for one session",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return &usageError{msg: "juex sessions context: <id> required"}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfigForCommand(cmd, flags)
			if err != nil {
				return err
			}
			id := args[0]
			dir := filepath.Join(cfg.SessionsDir(), id)
			msgs, err := session.LoadActiveMessages(dir)
			if err != nil {
				if os.IsNotExist(err) {
					return &notFoundError{msg: "session not found: " + id}
				}
				return err
			}
			snap := runtime.ActiveContextFromHistory(msgs)
			switch format {
			case "json", "":
				cmdPrintln(cmd, mustJSON(snap))
			case "text":
				renderActiveContextText(cmd, snap)
			default:
				return &usageError{msg: "unknown --format value: " + format}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&format, "format", "json", "json|text")
	return cmd
}

func renderActiveContextText(cmd *cobra.Command, snap runtime.ActiveContextSnapshot) {
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "estimated_tokens: %d\n\n", snap.EstimatedTokens)
	for _, m := range snap.Messages {
		role := string(m.Role)
		if m.Kind != "" {
			role += "[" + m.Kind + "]"
		}
		for _, b := range m.Blocks {
			switch b.Type {
			case llm.BlockText:
				fmt.Fprintf(w, "%s> %s\n", role, b.Text)
			case llm.BlockImage:
				fmt.Fprintf(w, "%s> %s\n", role, llm.FormatImagePlaceholder(b.Media))
			case llm.BlockReasoning:
				fmt.Fprintf(w, "%s thinking> %s\n", role, b.Text)
			case llm.BlockToolUse:
				fmt.Fprintf(w, "%s tool> %s(%v)\n", role, b.ToolName, b.Input)
			case llm.BlockToolResult:
				if b.Content != "" {
					fmt.Fprintf(w, "%s tool< %s\n", role, b.Content)
				}
				if b.Media != nil {
					fmt.Fprintf(w, "%s tool< %s\n", role, llm.FormatImagePlaceholder(b.Media))
				}
				if b.Content == "" && b.Media == nil {
					fmt.Fprintf(w, "%s tool< \n", role)
				}
			}
		}
	}
}

func newSessionsCompactCmd(flags *persistentFlags) *cobra.Command {
	var (
		format       string
		reason       string
		instructions string
	)
	cmd := &cobra.Command{
		Use:   "compact <id>",
		Short: "Append a compact summary marker to one session",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return &usageError{msg: "juex sessions compact: <id> required"}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfigForCommand(cmd, flags)
			if err != nil {
				return err
			}
			result, err := compactSession(cmd.Context(), cfg, args[0], reason, instructions, nil, flags.debug, flags.logLevel)
			if err != nil {
				if os.IsNotExist(err) {
					return &notFoundError{msg: "session not found: " + args[0]}
				}
				return err
			}
			switch format {
			case "json", "":
				cmdPrintln(cmd, mustJSON(result))
			case "text":
				renderCompactResultText(cmd, result)
			default:
				return &usageError{msg: "unknown --format value: " + format}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&format, "format", "json", "json|text")
	cmd.Flags().StringVar(&reason, "reason", "manual", "reason stored in compaction metadata")
	cmd.Flags().StringVar(&instructions, "instructions", "", "focus instructions for the compaction summary")
	return cmd
}

func compactSession(ctx context.Context, cfg config.Config, id, reason, instructions string, provider llm.Provider, debug bool, logLevel string) (runtime.CompactionResult, error) {
	if reason == "" {
		reason = "manual"
	}
	dir := filepath.Join(cfg.SessionsDir(), id)
	a, err := app.New(app.Options{
		Config:     cfg,
		Provider:   provider,
		Debug:      debug,
		LogLevel:   logLevel,
		ResumeDir:  dir,
		DisableMCP: true,
	})
	if err != nil {
		return runtime.CompactionResult{}, err
	}
	defer func() { _ = a.CloseAndWait() }()
	return a.CompactWithInstructions(ctx, reason, false, instructions)
}

func renderCompactResultText(cmd *cobra.Command, result runtime.CompactionResult) {
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "message_id:      %s\n", result.MessageID)
	fmt.Fprintf(w, "reason:          %s\n", result.Reason)
	fmt.Fprintf(w, "tokens_before:   %d\n", result.TokensBefore)
	fmt.Fprintf(w, "tokens_after:    %d\n", result.TokensAfter)
	fmt.Fprintf(w, "summary_chars:   %d\n", result.SummaryChars)
	if result.TailStartMessageID != "" {
		fmt.Fprintf(w, "tail_start_id:   %s\n", result.TailStartMessageID)
	}
}

func newSessionsDeleteCmd(flags *persistentFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete one session and remove it from history",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return &usageError{msg: "juex sessions delete: <id> required"}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfigForCommand(cmd, flags)
			if err != nil {
				return err
			}
			id := args[0]
			if err := session.Delete(cfg.SessionsDir(), cfg.HistoryPath(), id); err != nil {
				if os.IsNotExist(err) {
					return &notFoundError{msg: "session not found: " + id}
				}
				return err
			}
			cmdPrintln(cmd, mustJSON(map[string]any{"deleted": true, "id": id}))
			return nil
		},
	}
	return cmd
}
