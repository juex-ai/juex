package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/juex-ai/juex/internal/agentstate"
	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/fleet"
)

type agentSelectorFlags struct {
	agent string
	cwd   string
}

func bindAgentSelectorFlags(cmd *cobra.Command, flags *agentSelectorFlags) {
	cmd.Flags().StringVar(&flags.agent, "agent", "", "registered Agent id or unique exact name")
	cmd.Flags().StringVarP(&flags.cwd, "cwd", "C", "", "registered Workspace path (default current directory)")
}

func resolveManagedAgent(flags *agentSelectorFlags) (*fleet.Manager, fleet.AgentReference, error) {
	if flags == nil {
		flags = &agentSelectorFlags{}
	}
	agent := strings.TrimSpace(flags.agent)
	cwd := strings.TrimSpace(flags.cwd)
	if agent != "" && cwd != "" {
		return nil, fleet.AgentReference{}, &usageError{msg: "--agent and --cwd are mutually exclusive"}
	}
	manager, err := newFleetManager()
	if err != nil {
		return nil, fleet.AgentReference{}, err
	}
	if agent != "" {
		selected, err := manager.ResolveAgent(agent)
		var missing *fleet.NotFoundError
		if errors.As(err, &missing) {
			return nil, fleet.AgentReference{}, &notFoundError{msg: err.Error() + "; run juex agent list or juex agent add <workspace>"}
		}
		return manager, selected, mapFleetError(err)
	}
	if cwd == "" {
		cwd, err = os.Getwd()
		if err != nil {
			return nil, fleet.AgentReference{}, err
		}
	}
	resolution, err := agentstate.ResolveExisting(agentstate.Options{WorkDir: cwd})
	if err != nil {
		var missing *agentstate.NoAgentError
		if strings.TrimSpace(cwd) != "" && (os.IsNotExist(err) || errors.As(err, &missing)) {
			return nil, fleet.AgentReference{}, &notFoundError{msg: fmt.Sprintf("no managed Agent for Workspace %s; run juex agent add %s or select an existing Agent with --agent", cwd, cwd)}
		}
		return nil, fleet.AgentReference{}, err
	}
	selected, err := manager.ResolveAgent(resolution.Agent.ID)
	return manager, selected, mapFleetError(err)
}

func resolveSelectedAgent(flags *agentSelectorFlags) (*fleet.Manager, fleet.ReadOnlyAgentState, error) {
	manager, selected, err := resolveManagedAgent(flags)
	if err != nil {
		return nil, fleet.ReadOnlyAgentState{}, err
	}
	state, err := manager.ReadOnlyState(selected.ID)
	return manager, state, mapFleetError(err)
}

func loadSelectedAgentConfig(state fleet.ReadOnlyAgentState) (config.Config, error) {
	cfg, err := config.LoadWithOptions(config.LoadOptions{
		AgentID:    state.ID,
		AgentState: config.AgentStateExisting,
	})
	if err != nil {
		return config.Config{}, err
	}
	return cfg, nil
}

func newAgentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Register and manage workspace Agents",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(
		newAgentAddCmd(),
		newAgentListCmd(),
		newAgentShowCmd(),
		newAgentEnabledCmd(true),
		newAgentEnabledCmd(false),
		newAgentRemoveCmd(),
		newAgentLifecycleCmd("start"),
		newAgentLifecycleCmd("stop"),
		newAgentLifecycleCmd("restart"),
		newAgentLogsCmd(),
		newAgentConfigCmd(),
		newAgentSendCmd(),
	)
	return cmd
}

func newAgentAddCmd() *cobra.Command {
	var name string
	var autostart bool
	cmd := &cobra.Command{
		Use:   "add <workspace>",
		Short: "Register an existing Workspace as an Agent",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			workspace, err := filepath.Abs(args[0])
			if err != nil {
				return fmt.Errorf("juex agent add: resolve Workspace: %w", err)
			}
			manager, err := newFleetManager()
			if err != nil {
				return err
			}
			var nameOption *string
			if cmd.Flags().Changed("name") {
				nameOption = &name
			}
			var autostartOption *bool
			if cmd.Flags().Changed("autostart") {
				autostartOption = &autostart
			}
			result, err := manager.Add(cmd.Context(), fleet.AddOptions{
				Workspace: workspace, Name: nameOption, Autostart: autostartOption,
			})
			if err != nil {
				return mapFleetError(err)
			}
			action := "Registered"
			if result.Created {
				action = "Added"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s %s: %s\n", action, result.Agent.ID, result.Agent.Name, result.Agent.RuntimeHealth)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Agent display name")
	cmd.Flags().BoolVar(&autostart, "autostart", false, "start the Agent during Fleet reconciliation")
	return cmd
}

func newAgentListCmd() *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List registered Agents and runtime health",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if format != "table" && format != "json" {
				return &usageError{msg: "juex agent list: --format must be table or json"}
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
				if err := encoder.Encode(statuses); err != nil {
					return err
				}
			} else {
				renderFleetStatusTable(cmd, statuses)
			}
			reportFleetVersionSkew(cmd, statuses)
			return nil
		},
	}
	cmd.Flags().StringVar(&format, "format", "table", "output format: table or json")
	return cmd
}

func newAgentShowCmd() *cobra.Command {
	selector := &agentSelectorFlags{}
	var jsonOut bool
	cmd := &cobra.Command{
		Use: "show", Short: "Show one registered Agent and runtime health", Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			manager, state, err := resolveManagedAgent(selector)
			if err != nil {
				return err
			}
			status, err := manager.StatusOne(cmd.Context(), state.ID)
			if err != nil {
				return mapFleetError(err)
			}
			if jsonOut {
				cmdPrintln(cmd, mustJSON(status))
			} else {
				renderFleetStatusTable(cmd, []fleet.AgentStatus{status})
			}
			return nil
		},
	}
	bindAgentSelectorFlags(cmd, selector)
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newAgentEnabledCmd(enabled bool) *cobra.Command {
	selector := &agentSelectorFlags{}
	action := "disable"
	if enabled {
		action = "enable"
	}
	cmd := &cobra.Command{
		Use: action, Short: strings.ToUpper(action[:1]) + action[1:] + " the selected Agent", Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			manager, state, err := resolveManagedAgent(selector)
			if err != nil {
				return err
			}
			status, err := manager.SetEnabled(cmd.Context(), state.ID, enabled)
			if err != nil {
				return mapFleetError(err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s: enabled=%t runtime=%s\n", status.ID, status.Name, status.Enabled, status.RuntimeHealth)
			return nil
		},
	}
	bindAgentSelectorFlags(cmd, selector)
	return cmd
}

func newAgentRemoveCmd() *cobra.Command {
	selector := &agentSelectorFlags{}
	var yes bool
	cmd := &cobra.Command{
		Use: "remove", Short: "Permanently delete the selected Agent and its state", Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			manager, state, err := resolveManagedAgent(selector)
			if err != nil {
				return err
			}
			if !yes {
				fmt.Fprintf(cmd.OutOrStdout(), "Permanently remove Agent %q and delete all of its runtime state? [y/N] ", state.Name)
				line, readErr := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
				if readErr != nil && strings.TrimSpace(line) == "" {
					return readErr
				}
				answer := strings.ToLower(strings.TrimSpace(line))
				if answer != "y" && answer != "yes" {
					cmdPrintln(cmd, "Cancelled; no Agent state was deleted.")
					return nil
				}
			}
			removed, err := manager.Remove(cmd.Context(), state.ID, fleet.RemoveOptions{SkipConfirmation: true})
			if err != nil {
				return mapFleetError(err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Removed %s %s from %s.\n", removed.ID, removed.Name, removed.Workspace)
			return nil
		},
	}
	bindAgentSelectorFlags(cmd, selector)
	cmd.Flags().BoolVar(&yes, "yes", false, "remove without prompting")
	return cmd
}

func newAgentLifecycleCmd(action string) *cobra.Command {
	selector := &agentSelectorFlags{}
	cmd := &cobra.Command{
		Use: action, Short: strings.ToUpper(action[:1]) + action[1:] + " the selected Agent Runtime", Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			manager, state, err := resolveManagedAgent(selector)
			if err != nil {
				return err
			}
			var status fleet.AgentStatus
			var restart fleet.RestartResult
			switch action {
			case "start":
				status, err = manager.Start(cmd.Context(), state.ID)
			case "stop":
				status, err = manager.Stop(cmd.Context(), state.ID)
			case "restart":
				restart, err = manager.Restart(cmd.Context(), state.ID)
				status = restart.AgentStatus
			default:
				return fmt.Errorf("unsupported Agent lifecycle action %q", action)
			}
			if err != nil {
				return mapFleetError(err)
			}
			renderAgentLifecycleResult(cmd, action, status, restart.Resume)
			return nil
		},
	}
	bindAgentSelectorFlags(cmd, selector)
	return cmd
}

func renderAgentLifecycleResult(cmd *cobra.Command, action string, status fleet.AgentStatus, resume fleet.RestartResume) {
	fmt.Fprintf(cmd.OutOrStdout(), "%s %s: %s", status.ID, status.Name, status.RuntimeHealth)
	if status.Endpoint != "" {
		fmt.Fprintf(cmd.OutOrStdout(), " at %s", status.Endpoint)
	}
	if action == "restart" {
		switch {
		case resume.Sent:
			fmt.Fprint(cmd.OutOrStdout(), " resume=sent")
		case resume.Required:
			fmt.Fprint(cmd.OutOrStdout(), " resume=failed")
		case resume.Error != "":
			fmt.Fprint(cmd.OutOrStdout(), " resume=unknown")
		default:
			fmt.Fprint(cmd.OutOrStdout(), " resume=not-needed")
		}
		if resume.Error != "" {
			fmt.Fprintf(cmd.OutOrStdout(), " warning=%s", resume.Error)
		}
	}
	fmt.Fprintln(cmd.OutOrStdout())
}

func newAgentLogsCmd() *cobra.Command {
	selector := &agentSelectorFlags{}
	var lines int
	cmd := &cobra.Command{
		Use: "logs", Short: "Print a bounded tail of the selected Agent Fleet log", Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if lines < 1 || lines > 10_000 {
				return &usageError{msg: "juex agent logs: --lines must be between 1 and 10000"}
			}
			manager, state, err := resolveManagedAgent(selector)
			if err != nil {
				return err
			}
			body, err := manager.Logs(state.ID, lines)
			if err != nil {
				return mapFleetError(err)
			}
			_, err = cmd.OutOrStdout().Write(body)
			return err
		},
	}
	bindAgentSelectorFlags(cmd, selector)
	cmd.Flags().IntVar(&lines, "lines", 200, "number of trailing log lines (1-10000)")
	return cmd
}

func newAgentConfigCmd() *cobra.Command {
	selector := &agentSelectorFlags{}
	var jsonOut bool
	cmd := &cobra.Command{
		Use: "config", Short: "Show the selected Agent config path", Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			manager, state, err := resolveManagedAgent(selector)
			if err != nil {
				return err
			}
			configFile, err := manager.Config(state.ID)
			if err != nil {
				return mapFleetError(err)
			}
			if jsonOut {
				cmdPrintln(cmd, mustJSON(map[string]any{
					"agent_id": state.ID, "name": state.Name, "path": configFile.Path, "exists": configFile.Exists,
				}))
			} else {
				cmdPrintln(cmd, configFile.Path)
			}
			return nil
		},
	}
	bindAgentSelectorFlags(cmd, selector)
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit config locator JSON")
	return cmd
}
