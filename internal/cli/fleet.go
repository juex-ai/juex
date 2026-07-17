package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/fleet"
)

func newFleetCmd(flags *persistentFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fleet",
		Short: "Manage resident workspace agents in the effective JUEX_HOME",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newFleetServeCmd(flags))
	cmd.AddCommand(newFleetStatusCmd(flags))
	cmd.AddCommand(newFleetLifecycleCmd(flags, "start"))
	cmd.AddCommand(newFleetLifecycleCmd(flags, "stop"))
	cmd.AddCommand(newFleetLifecycleCmd(flags, "restart"))
	cmd.AddCommand(newFleetLogsCmd(flags))
	cmd.AddCommand(newFleetGCCmd(flags))
	return cmd
}

func newFleetManager() (*fleet.Manager, error) {
	homeDir, err := config.EffectiveHomeDir()
	if err != nil {
		return nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("juex fleet: resolve executable: %w", err)
	}
	return fleet.New(fleet.Options{HomeDir: homeDir, Executable: executable})
}

func newFleetServeCmd(_ *persistentFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run the resident fleet supervisor",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			manager, err := newFleetManager()
			if err != nil {
				return err
			}
			err = manager.Serve(cmd.Context(), func(action fleet.Action) {
				prefix := "fleet"
				if action.AgentID != "" {
					prefix += " " + action.AgentID
				}
				detail := action.Detail
				if action.Err != nil {
					detail = action.Err.Error()
				}
				if detail == "" {
					fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", prefix, action.Kind)
					return
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s: %s: %s\n", prefix, action.Kind, detail)
			})
			return mapFleetError(err)
		},
	}
}

func newFleetStatusCmd(_ *persistentFlags) *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show every registered agent and its runtime health",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if format != "table" && format != "json" {
				return &usageError{msg: "juex fleet status: --format must be table or json"}
			}
			manager, err := newFleetManager()
			if err != nil {
				return err
			}
			statuses, err := manager.Status(cmd.Context())
			if err != nil {
				return mapFleetError(err)
			}
			if format == "json" {
				encoder := json.NewEncoder(cmd.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(statuses)
			}
			renderFleetStatusTable(cmd, statuses)
			return nil
		},
	}
	cmd.Flags().StringVar(&format, "format", "table", "output format: table or json")
	return cmd
}

func renderFleetStatusTable(cmd *cobra.Command, statuses []fleet.AgentStatus) {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "ID\tNAME\tBINDING\tRUNTIME\tENABLED\tAUTOSTART\tPID\tSTARTED\tENDPOINT\tWORKSPACE\tPROBLEM")
	for _, status := range statuses {
		fmt.Fprintf(
			writer,
			"%s\t%s\t%s\t%s\t%t\t%t\t%s\t%s\t%s\t%s\t%s\n",
			status.ID,
			status.Name,
			status.Binding,
			status.RuntimeHealth,
			status.Enabled,
			status.Autostart,
			optionalPID(status.PID),
			optionalStartedAt(status),
			status.Endpoint,
			status.Workspace,
			status.Problem,
		)
	}
	_ = writer.Flush()
}

func optionalPID(pid int) string {
	if pid <= 0 {
		return ""
	}
	return strconv.Itoa(pid)
}

func optionalStartedAt(status fleet.AgentStatus) string {
	if status.StartedAt.IsZero() {
		return ""
	}
	return status.StartedAt.Format("2006-01-02T15:04:05Z07:00")
}

func newFleetLifecycleCmd(_ *persistentFlags, action string) *cobra.Command {
	return &cobra.Command{
		Use:   action + " <agent>",
		Short: strings.ToUpper(action[:1]) + action[1:] + " one resident agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			manager, err := newFleetManager()
			if err != nil {
				return err
			}
			var status fleet.AgentStatus
			switch action {
			case "start":
				status, err = manager.Start(cmd.Context(), args[0])
			case "stop":
				status, err = manager.Stop(cmd.Context(), args[0])
			case "restart":
				status, err = manager.Restart(cmd.Context(), args[0])
			}
			if err != nil {
				return mapFleetError(err)
			}
			fmt.Fprintf(
				cmd.OutOrStdout(),
				"%s %s: %s",
				status.ID,
				status.Name,
				status.RuntimeHealth,
			)
			if status.Endpoint != "" {
				fmt.Fprintf(cmd.OutOrStdout(), " at %s", status.Endpoint)
			}
			fmt.Fprintln(cmd.OutOrStdout())
			return nil
		},
	}
}

func newFleetLogsCmd(_ *persistentFlags) *cobra.Command {
	var lines int
	cmd := &cobra.Command{
		Use:   "logs <agent>",
		Short: "Print a bounded tail of one agent's fleet log",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if lines < 1 || lines > 10_000 {
				return &usageError{msg: "juex fleet logs: --lines must be between 1 and 10000"}
			}
			manager, err := newFleetManager()
			if err != nil {
				return err
			}
			body, err := manager.Logs(args[0], lines)
			if err != nil {
				return mapFleetError(err)
			}
			_, err = cmd.OutOrStdout().Write(body)
			return err
		},
	}
	cmd.Flags().IntVar(&lines, "lines", 200, "number of trailing log lines (1-10000)")
	return cmd
}

func newFleetGCCmd(_ *persistentFlags) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "gc",
		Short: "Review and delete definitely orphaned agent state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			manager, err := newFleetManager()
			if err != nil {
				return err
			}
			candidates, err := manager.GCCandidates(cmd.Context())
			if err != nil {
				return mapFleetError(err)
			}
			if len(candidates) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No definite orphan candidates.")
				return nil
			}
			renderGCCandidates(cmd, candidates)
			if !yes {
				fmt.Fprint(cmd.OutOrStdout(), "Delete these orphaned agent directories? [y/N] ")
				line, readErr := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
				if readErr != nil && strings.TrimSpace(line) == "" {
					return readErr
				}
				answer := strings.ToLower(strings.TrimSpace(line))
				if answer != "y" && answer != "yes" {
					fmt.Fprintln(cmd.OutOrStdout(), "Cancelled; no agent state was deleted.")
					return nil
				}
			}
			ids := make([]string, 0, len(candidates))
			for _, candidate := range candidates {
				ids = append(ids, candidate.AgentID)
			}
			if err := manager.DeleteOrphans(cmd.Context(), ids); err != nil {
				return mapFleetError(err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Deleted %d orphaned agent director", len(ids))
			if len(ids) == 1 {
				fmt.Fprintln(cmd.OutOrStdout(), "y.")
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "ies.")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "delete all listed candidates without prompting")
	return cmd
}

func renderGCCandidates(cmd *cobra.Command, candidates []fleet.GCCandidate) {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "ID\tWORKSPACE\tSIZE\tLAST ACTIVITY\tRUNNING\tREASON")
	for _, candidate := range candidates {
		lastActivity := ""
		if !candidate.LastActivity.IsZero() {
			lastActivity = candidate.LastActivity.Format("2006-01-02T15:04:05Z07:00")
		}
		fmt.Fprintf(
			writer,
			"%s\t%s\t%d\t%s\t%t\t%s\n",
			candidate.AgentID,
			candidate.Workspace,
			candidate.SizeBytes,
			lastActivity,
			candidate.Running,
			candidate.Reason,
		)
	}
	_ = writer.Flush()
}

func mapFleetError(err error) error {
	if err == nil {
		return nil
	}
	var missing *fleet.NotFoundError
	if errors.As(err, &missing) {
		return &notFoundError{msg: err.Error()}
	}
	var ambiguous *fleet.AmbiguousSelectorError
	var conflict *fleet.ConflictError
	if errors.As(err, &ambiguous) || errors.As(err, &conflict) {
		return &conflictError{msg: err.Error()}
	}
	return err
}
