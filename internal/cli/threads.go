package cli

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/juex-ai/juex/internal/thread"
)

func newThreadsCmd(flags *persistentFlags) *cobra.Command {
	cmd := &cobra.Command{Use: "threads", Short: "Create, inspect, and manage Threads"}
	cmd.AddCommand(
		newThreadsCreateCmd(flags),
		newThreadsListCmd(flags),
		newThreadsShowCmd(flags),
		newThreadsRenameCmd(flags),
		newThreadMutationCmd(flags, "archive <thread>", "Archive an idle Worker Thread", "archive"),
		newThreadMutationCmd(flags, "unarchive <thread>", "Restore an archived Worker Thread", "unarchive"),
		newThreadMutationCmd(flags, "stop <thread>", "Cancel the active Turn in a Thread", "stop"),
		newThreadsDeleteCmd(flags),
	)
	declareAgentStatePolicy(cmd, agentStateExisting)
	return cmd
}

func withAgentClient(cmd *cobra.Command, flags *persistentFlags, run func(*agentClient) error) error {
	cfg, lifecycle, err := loadRuntimeConfigForCommand(cmd, flags)
	if err != nil {
		return err
	}
	defer func() { _ = lifecycle.finish(cmd, nil) }()
	client, err := connectAgent(cmd.Context(), cfg)
	if err != nil {
		return err
	}
	defer client.Close()
	return run(client)
}

func newThreadsCreateCmd(flags *persistentFlags) *cobra.Command {
	var alias string
	var parent string
	cmd := &cobra.Command{
		Use:   "create [message...]",
		Short: "Create a Worker Thread",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return withAgentClient(cmd, flags, func(client *agentClient) error {
				parentEntry, err := client.resolveThread(cmd.Context(), parent, false)
				if err != nil {
					return err
				}
				var created thread.Info
				if err := client.doJSON(cmd.Context(), http.MethodPost, "/api/threads", map[string]any{
					"alias": strings.TrimSpace(alias), "parent_thread_id": parentEntry.ThreadID,
				}, &created); err != nil {
					return err
				}
				cmdPrintln(cmd, mustJSON(created))
				if message := strings.TrimSpace(strings.Join(args, " ")); message != "" {
					var receipt sendReceipt
					if err := client.doJSON(cmd.Context(), http.MethodPost, "/api/threads/"+created.ID+"/inputs", map[string]any{"prompt": message}, &receipt); err != nil {
						return err
					}
					cmdPrintln(cmd, compactJSON(receipt))
				}
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&alias, "alias", "", "Worker alias")
	cmd.Flags().StringVar(&parent, "parent", thread.MainID, "active parent Thread id or alias")
	declareAgentStatePolicy(cmd, agentStateMint)
	return cmd
}

func newThreadsListCmd(flags *persistentFlags) *cobra.Command {
	var activeOnly, archivedOnly, all bool
	var format string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List Threads from the Agent index",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if selected := countTrue(activeOnly, archivedOnly, all); selected > 1 {
				return &usageError{msg: "juex threads list: pass only one of --active, --archived, or --all"}
			}
			return withAgentClient(cmd, flags, func(client *agentClient) error {
				list, err := client.listThreads(cmd.Context())
				if err != nil {
					return err
				}
				if format == "json" {
					switch {
					case archivedOnly:
						cmdPrintln(cmd, mustJSON(map[string]any{"archived_threads": list.Archived}))
					case all:
						cmdPrintln(cmd, mustJSON(list))
					default:
						cmdPrintln(cmd, mustJSON(map[string]any{"active_threads": list.Active}))
					}
					return nil
				}
				if format != "table" {
					return &usageError{msg: "unknown --format value: " + format}
				}
				entries := list.Active
				if archivedOnly {
					entries = list.Archived
				} else if all {
					entries = append(append([]thread.IndexEntry(nil), list.Active...), list.Archived...)
				}
				renderThreadsTable(cmd, entries)
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&activeOnly, "active", false, "show active Threads (default)")
	cmd.Flags().BoolVar(&archivedOnly, "archived", false, "show archived Threads")
	cmd.Flags().BoolVar(&all, "all", false, "show active and archived Threads")
	cmd.Flags().StringVar(&format, "format", "table", "table|json")
	return cmd
}

func renderThreadsTable(cmd *cobra.Command, entries []thread.IndexEntry) {
	if len(entries) == 0 {
		cmdPrintln(cmd, "(no Threads)")
		return
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%-8s  %-18s  %-8s  %-9s  %-9s  %7s  %5s  %4s  %8s  %s\n", "TID", "ALIAS", "PARENT", "RETENTION", "EXECUTION", "PENDING", "TURNS", "GEN", "CONTEXT", "CREATED")
	for _, entry := range entries {
		parent := "-"
		if entry.ParentThreadID != "" {
			parent = "#" + entry.ParentThreadID
		}
		execution := string(entry.ExecutionState)
		if execution == "" {
			execution = "-"
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%-8s  %-18s  %-8s  %-9s  %-9s  %7d  %5d  %4d  %8d  %s\n",
			"#"+entry.ThreadID, truncateRunes(entry.Alias, 18), parent, entry.RetentionState, execution,
			entry.PendingInputCount, entry.TurnCount, entry.GenerationCount,
			entry.CurrentContextTokens, entry.CreatedAt.Format("2006-01-02"))
	}
}

func newThreadsShowCmd(flags *persistentFlags) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use: "show <thread>", Short: "Show Thread metadata and local paths", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withAgentClient(cmd, flags, func(client *agentClient) error {
				entry, err := client.resolveThread(cmd.Context(), args[0], true)
				if err != nil {
					return err
				}
				var response struct {
					thread.Info
				}
				if err := client.doJSON(cmd.Context(), http.MethodGet, "/api/threads/"+entry.ThreadID+"?limit=1", nil, &response); err != nil {
					return err
				}
				view := map[string]any{
					"thread":     response.Info,
					"journal":    filepath.Join(response.Dir, "journal.jsonl"),
					"scratchpad": filepath.Join(response.Dir, "scratchpad"),
				}
				if jsonOut {
					cmdPrintln(cmd, mustJSON(view))
					return nil
				}
				execution := string(response.ExecutionState)
				if execution == "" {
					execution = "-"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "thread_id:      %s\nalias:          %s\nparent:         %s\nretention:      %s\nexecution:      %s\ngeneration:     %s\nturns:          %d\npending:        %d\njournal:        %s\nscratchpad:     %s\n",
					response.ID, response.Alias, response.ParentThreadID, response.RetentionState, execution, response.GenerationID,
					response.TurnCount, response.PendingInputs, view["journal"], view["scratchpad"])
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newThreadsRenameCmd(flags *persistentFlags) *cobra.Command {
	return &cobra.Command{
		Use: "rename <thread> <alias>", Short: "Rename a Worker Thread", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withAgentClient(cmd, flags, func(client *agentClient) error {
				result, err := client.renameThread(cmd.Context(), args[0], args[1])
				if err != nil {
					return err
				}
				cmdPrintln(cmd, mustJSON(result))
				return nil
			})
		},
	}
}

func (client *agentClient) renameThread(ctx context.Context, selector, alias string) (thread.Info, error) {
	entry, err := client.resolveThread(ctx, selector, true)
	if err != nil {
		return thread.Info{}, err
	}
	var result thread.Info
	if err := client.doJSON(ctx, http.MethodPatch, "/api/threads/"+entry.ThreadID, map[string]string{"alias": alias}, &result); err != nil {
		return thread.Info{}, err
	}
	return result, nil
}

func newThreadMutationCmd(flags *persistentFlags, use, short, operation string) *cobra.Command {
	return &cobra.Command{
		Use: use, Short: short, Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withAgentClient(cmd, flags, func(client *agentClient) error {
				entry, err := client.resolveThread(cmd.Context(), args[0], operation == "unarchive")
				if err != nil {
					return err
				}
				var result map[string]any
				if err := client.doJSON(cmd.Context(), http.MethodPost, "/api/threads/"+entry.ThreadID+"/"+operation, map[string]any{}, &result); err != nil {
					return err
				}
				cmdPrintln(cmd, mustJSON(result))
				return nil
			})
		},
	}
}

func newThreadsDeleteCmd(flags *persistentFlags) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use: "delete <thread>", Short: "Permanently delete an archived Worker Thread", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				return &usageError{msg: "juex threads delete: --yes is required"}
			}
			return withAgentClient(cmd, flags, func(client *agentClient) error {
				entry, err := client.resolveThread(cmd.Context(), args[0], true)
				if err != nil {
					return err
				}
				var result map[string]any
				if err := client.doJSON(cmd.Context(), http.MethodDelete, "/api/threads/"+entry.ThreadID, nil, &result); err != nil {
					return err
				}
				cmdPrintln(cmd, mustJSON(result))
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm permanent deletion")
	return cmd
}

func countTrue(values ...bool) int {
	count := 0
	for _, value := range values {
		if value {
			count++
		}
	}
	return count
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit-1]) + "…"
}
