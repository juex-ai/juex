package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/fleet"
	"github.com/juex-ai/juex/internal/fleetservice"
	"github.com/juex-ai/juex/internal/fleetweb"
	"github.com/juex-ai/juex/internal/version"
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
	cmd.AddCommand(newFleetAddCmd(flags))
	cmd.AddCommand(newFleetEnabledCmd(flags, true))
	cmd.AddCommand(newFleetEnabledCmd(flags, false))
	cmd.AddCommand(newFleetRemoveCmd(flags))
	cmd.AddCommand(newFleetLifecycleCmd(flags, "start"))
	cmd.AddCommand(newFleetLifecycleCmd(flags, "stop"))
	cmd.AddCommand(newFleetLifecycleCmd(flags, "restart"))
	cmd.AddCommand(newFleetLogsCmd(flags))
	cmd.AddCommand(newFleetGCCmd(flags))
	cmd.AddCommand(newFleetInstallCmd(flags))
	cmd.AddCommand(newFleetUninstallCmd(flags))
	cmd.AddCommand(newFleetServiceInstalledCmd())
	declareAgentStatePolicy(cmd, agentStateNone)
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
	var (
		addr          string
		unsafeBindAny bool
	)
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the resident fleet supervisor and browser API",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolvedAddr, _, err := resolveFleetAddr(cmd, addr, false)
			if err != nil {
				return err
			}
			addr = resolvedAddr
			if !isTCPListenAddr(addr) {
				return &usageError{msg: "juex fleet serve: --addr must be a host:port TCP address (got " + addr + ")"}
			}
			if !unsafeBindAny && !isLoopbackAddr(addr) {
				return &usageError{msg: "juex fleet serve: --addr must bind to loopback (got " + addr + "). Pass --unsafe-bind-any if you have your own network protection."}
			}
			if unsafeBindAny {
				fmt.Fprintln(cmd.ErrOrStderr(), "WARNING: --unsafe-bind-any in use; juex has no authentication. Anyone who can reach this address can run shell commands.")
			}
			manager, err := newFleetManager()
			if err != nil {
				return err
			}
			ctx, stopSignals := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stopSignals()
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()

			ready := make(chan struct{})
			supervisorErr := make(chan error, 1)
			var readyOnce sync.Once
			go func() {
				supervisorErr <- manager.Serve(ctx, func(action fleet.Action) {
					reportFleetAction(cmd, action)
					if action.Kind == "ready" {
						readyOnce.Do(func() { close(ready) })
					}
				})
			}()
			select {
			case <-ready:
			case err := <-supervisorErr:
				return mapFleetError(err)
			case <-ctx.Done():
				cancel()
				return mapFleetError(<-supervisorErr)
			}

			server, err := fleetweb.New(fleetweb.Options{
				Manager:      manager,
				Addr:         addr,
				AllowAnyBind: unsafeBindAny,
				OnReady: func(actual string) {
					fmt.Fprintln(cmd.OutOrStdout(), "juex fleet listening on http://"+actual)
				},
			})
			if err != nil {
				cancel()
				<-supervisorErr
				return err
			}
			webErr := server.Run(ctx)
			cancel()
			fleetErr := <-supervisorErr
			return errors.Join(webErr, mapFleetError(fleetErr))
		},
	}
	cmd.Flags().StringVar(&addr, "addr", config.DefaultFleetAddr, "loopback address (host:port)")
	cmd.Flags().BoolVar(&unsafeBindAny, "unsafe-bind-any", false, "allow --addr to bind beyond loopback (no auth — use only on trusted networks)")
	return cmd
}

func isTCPListenAddr(addr string) bool {
	_, portText, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	port, err := strconv.Atoi(portText)
	return err == nil && port >= 0 && port <= 65535
}

func newFleetServiceManager(unsafeBindAny bool) (*fleetservice.Manager, error) {
	homeDir, err := config.EffectiveHomeDir()
	if err != nil {
		return nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("juex fleet: resolve executable: %w", err)
	}
	return fleetservice.New(fleetservice.Options{
		HomeDir:       homeDir,
		Executable:    executable,
		UnsafeBindAny: unsafeBindAny,
	})
}

func resolveFleetAddr(cmd *cobra.Command, flagAddr string, stable bool) (string, bool, error) {
	explicit := cmd.Flags().Changed("addr")
	addr := strings.TrimSpace(flagAddr)
	if !explicit {
		fleetCfg, err := config.LoadHomeFleetConfig()
		if err != nil {
			return "", false, err
		}
		addr = fleetCfg.Addr
	}
	if stable {
		if err := config.ValidateStableFleetAddr(addr); err != nil {
			return "", explicit, &usageError{msg: "juex fleet: --addr " + err.Error()}
		}
	} else if !isTCPListenAddr(addr) {
		return "", explicit, &usageError{msg: "juex fleet: --addr must be a host:port TCP address (got " + addr + ")"}
	}
	return addr, explicit, nil
}

type fleetInstallSettings struct {
	Addr                string
	UnsafeBindAny       bool
	ConfigPath          string
	MigratedLegacyAddr  bool
	PreservedLegacyBind bool
}

type fleetServiceInstaller interface {
	ExistingServeOptions() (fleetservice.InstalledServeOptions, bool, error)
	Install(context.Context) (fleetservice.Registration, error)
}

type fleetAgentRestarter interface {
	RestartRunningAgents(context.Context) (fleet.RestartAgentsResult, error)
}

type fleetInstallCommandDeps struct {
	newServiceManager func(bool) (fleetServiceInstaller, error)
	newAgentManager   func() (fleetAgentRestarter, error)
}

func defaultFleetInstallCommandDeps() fleetInstallCommandDeps {
	return fleetInstallCommandDeps{
		newServiceManager: func(unsafeBindAny bool) (fleetServiceInstaller, error) {
			return newFleetServiceManager(unsafeBindAny)
		},
		newAgentManager: func() (fleetAgentRestarter, error) {
			return newFleetManager()
		},
	}
}

func resolveFleetInstallSettings(
	cmd *cobra.Command,
	flagAddr string,
	unsafeBindAny bool,
	fleetCfg config.FleetConfig,
	existing fleetservice.InstalledServeOptions,
	existingFound bool,
) (fleetInstallSettings, error) {
	explicitAddr := cmd.Flags().Changed("addr")
	addr := strings.TrimSpace(flagAddr)
	if !explicitAddr {
		addr = fleetCfg.Addr
	}
	settings := fleetInstallSettings{Addr: addr, UnsafeBindAny: unsafeBindAny}
	if !explicitAddr &&
		!fleetCfg.AddrConfigured &&
		existingFound &&
		existing.Addr != "" &&
		existing.Addr != config.LegacyDefaultFleetAddr {
		settings.Addr = existing.Addr
		settings.MigratedLegacyAddr = true
	}
	if !explicitAddr &&
		!cmd.Flags().Changed("unsafe-bind-any") &&
		existingFound &&
		existing.UnsafeBindAny {
		settings.UnsafeBindAny = true
		settings.PreservedLegacyBind = true
	}
	if err := config.ValidateStableFleetAddr(settings.Addr); err != nil {
		return fleetInstallSettings{}, &usageError{msg: "juex fleet: --addr " + err.Error()}
	}
	if !settings.UnsafeBindAny && !isLoopbackAddr(settings.Addr) {
		return fleetInstallSettings{}, &usageError{
			msg: "juex fleet install: --addr must bind to loopback (got " + settings.Addr + "). Pass --unsafe-bind-any if you have your own network protection.",
		}
	}
	if explicitAddr || settings.MigratedLegacyAddr {
		configPath, err := config.SetHomeFleetAddr(settings.Addr)
		if err != nil {
			return fleetInstallSettings{}, err
		}
		settings.ConfigPath = configPath
	}
	return settings, nil
}

func newFleetInstallCmd(_ *persistentFlags) *cobra.Command {
	return newFleetInstallCmdWithDeps(defaultFleetInstallCommandDeps())
}

func newFleetInstallCmdWithDeps(deps fleetInstallCommandDeps) *cobra.Command {
	var (
		addr          string
		unsafeBindAny bool
		restartAgents bool
	)
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install and start the fleet as a per-user system service",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			explicitAddr := cmd.Flags().Changed("addr")
			fleetCfg, err := config.LoadHomeFleetConfig()
			if err != nil {
				return err
			}
			selectedAddr := strings.TrimSpace(addr)
			if !explicitAddr {
				selectedAddr = fleetCfg.Addr
			}
			if err := config.ValidateStableFleetAddr(selectedAddr); err != nil {
				return &usageError{msg: "juex fleet: --addr " + err.Error()}
			}
			if explicitAddr && !unsafeBindAny && !isLoopbackAddr(selectedAddr) {
				return &usageError{
					msg: "juex fleet install: --addr must bind to loopback (got " + selectedAddr + "). Pass --unsafe-bind-any if you have your own network protection.",
				}
			}
			probeManager, err := deps.newServiceManager(false)
			if err != nil {
				return err
			}
			existing, existingFound, err := probeManager.ExistingServeOptions()
			if err != nil {
				return err
			}
			settings, err := resolveFleetInstallSettings(
				cmd,
				addr,
				unsafeBindAny,
				fleetCfg,
				existing,
				existingFound,
			)
			if err != nil {
				return err
			}
			if settings.UnsafeBindAny {
				fmt.Fprintln(cmd.ErrOrStderr(), "WARNING: --unsafe-bind-any in use; juex has no authentication. Anyone who can reach this address can run shell commands.")
			}
			manager, err := deps.newServiceManager(settings.UnsafeBindAny)
			if err != nil {
				return err
			}
			if settings.MigratedLegacyAddr {
				fmt.Fprintf(
					cmd.OutOrStdout(),
					"Migrated existing fleet address %s to %s.\n",
					settings.Addr,
					settings.ConfigPath,
				)
			}
			if settings.PreservedLegacyBind {
				fmt.Fprintln(cmd.OutOrStdout(), "Preserved existing fleet --unsafe-bind-any option.")
			}
			registration, err := manager.Install(cmd.Context())
			if err != nil {
				if settings.ConfigPath != "" {
					return fmt.Errorf("%w; fleet.addr remains written to %s", err, settings.ConfigPath)
				}
				return err
			}
			renderFleetServiceResult(cmd, "Installed", registration)
			if !restartAgents {
				return nil
			}
			agentManager, err := deps.newAgentManager()
			if err != nil {
				return err
			}
			result, restartErr := agentManager.RestartRunningAgents(cmd.Context())
			renderRestartAgentsResult(cmd, result)
			return restartErr
		},
	}
	cmd.Flags().StringVar(&addr, "addr", config.DefaultFleetAddr, "stable fleet browser address (host:port)")
	cmd.Flags().BoolVar(&unsafeBindAny, "unsafe-bind-any", false, "allow --addr to bind beyond loopback (no auth — use only on trusted networks)")
	cmd.Flags().BoolVar(&restartAgents, "restart-agents", false, "restart currently healthy resident agents after installing the service")
	return cmd
}

func renderRestartAgentsResult(cmd *cobra.Command, result fleet.RestartAgentsResult) {
	for _, item := range result.Items {
		resume := "not-needed"
		switch {
		case item.Resume.Sent:
			resume = "sent"
		case item.Resume.Required:
			resume = "failed"
		case item.Resume.Error != "":
			resume = "unknown"
		}
		fmt.Fprintf(
			cmd.OutOrStdout(),
			"Agent %s %s: %s runtime=%s resume=%s",
			item.Agent.ID,
			item.Agent.Name,
			item.Outcome,
			item.Agent.RuntimeHealth,
			resume,
		)
		if item.Reason != "" {
			fmt.Fprintf(cmd.OutOrStdout(), " reason=%s", item.Reason)
		}
		if item.Resume.Error != "" {
			fmt.Fprintf(cmd.OutOrStdout(), " warning=%s", item.Resume.Error)
		}
		fmt.Fprintln(cmd.OutOrStdout())
	}
	fmt.Fprintf(
		cmd.OutOrStdout(),
		"Agent refresh: %d restarted, %d skipped, %d failed.\n",
		result.Restarted,
		result.Skipped,
		result.Failed,
	)
}

func newFleetUninstallCmd(_ *persistentFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Stop and remove the fleet per-user system service",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			manager, err := newFleetServiceManager(false)
			if err != nil {
				return err
			}
			registration, err := manager.Uninstall(cmd.Context())
			if err != nil {
				return err
			}
			renderFleetServiceResult(cmd, "Uninstalled", registration)
			return nil
		},
	}
}

func newFleetServiceInstalledCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "service-installed",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			manager, err := newFleetServiceManager(false)
			if err != nil {
				return err
			}
			installed, err := manager.Installed(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), strconv.FormatBool(installed))
			return nil
		},
	}
}

func renderFleetServiceResult(cmd *cobra.Command, action string, registration fleetservice.Registration) {
	fmt.Fprintf(
		cmd.OutOrStdout(),
		"%s %s fleet service %s (%s).\n",
		action,
		registration.Platform,
		registration.Name,
		registration.DefinitionPath,
	)
	for _, note := range registration.Notes {
		fmt.Fprintln(cmd.OutOrStdout(), "Note:", note)
	}
}

func reportFleetAction(cmd *cobra.Command, action fleet.Action) {
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

func renderFleetStatusTable(cmd *cobra.Command, statuses []fleet.AgentStatus) {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "ID\tNAME\tBINDING\tRUNTIME\tVERSION\tENABLED\tAUTOSTART\tPID\tSTARTED\tENDPOINT\tWORKSPACE\tPROBLEM")
	for _, status := range statuses {
		fmt.Fprintf(
			writer,
			"%s\t%s\t%s\t%s\t%s\t%t\t%t\t%s\t%s\t%s\t%s\t%s\n",
			status.ID,
			status.Name,
			status.Binding,
			status.RuntimeHealth,
			optionalBinaryVersion(status.BinaryVersion),
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

func optionalBinaryVersion(binaryVersion string) string {
	if strings.TrimSpace(binaryVersion) == "" {
		return "unknown"
	}
	return binaryVersion
}

func reportFleetVersionSkew(cmd *cobra.Command, statuses []fleet.AgentStatus) {
	var skewed []string
	for _, status := range statuses {
		if !status.ProcessAlive || status.BinaryVersion == version.Version {
			continue
		}
		skewed = append(skewed, fmt.Sprintf("%s(%s)", status.ID, optionalBinaryVersion(status.BinaryVersion)))
	}
	if len(skewed) == 0 {
		return
	}
	fmt.Fprintf(
		cmd.ErrOrStderr(),
		"WARNING: running agents use a different JueX binary version than installed %s: %s. Restart them when safe; agents were not restarted automatically.\n",
		version.Version,
		strings.Join(skewed, ", "),
	)
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

func newFleetAddCmd(_ *persistentFlags) *cobra.Command {
	var (
		name      string
		autostart bool
		start     bool
	)
	cmd := &cobra.Command{
		Use:   "add <path>",
		Short: "Register an existing workspace as a resident agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
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
				Workspace: args[0],
				Name:      nameOption,
				Autostart: autostartOption,
				Start:     start,
			})
			if err != nil {
				return mapFleetError(err)
			}
			action := "Registered"
			if result.Created {
				action = "Added"
			}
			fmt.Fprintf(
				cmd.OutOrStdout(),
				"%s %s %s: %s\n",
				action,
				result.Agent.ID,
				result.Agent.Name,
				result.Agent.RuntimeHealth,
			)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "agent display name")
	cmd.Flags().BoolVar(&autostart, "autostart", false, "start the agent during fleet reconciliation")
	cmd.Flags().BoolVar(&start, "start", false, "start the agent immediately after registration")
	return cmd
}

func newFleetEnabledCmd(_ *persistentFlags, enabled bool) *cobra.Command {
	action := "disable"
	if enabled {
		action = "enable"
	}
	return &cobra.Command{
		Use:   action + " <agent>",
		Short: strings.ToUpper(action[:1]) + action[1:] + " one resident agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			manager, err := newFleetManager()
			if err != nil {
				return err
			}
			status, err := manager.SetEnabled(cmd.Context(), args[0], enabled)
			if err != nil {
				return mapFleetError(err)
			}
			fmt.Fprintf(
				cmd.OutOrStdout(),
				"%s %s: enabled=%t runtime=%s\n",
				status.ID,
				status.Name,
				status.Enabled,
				status.RuntimeHealth,
			)
			return nil
		},
	}
}

func newFleetRemoveCmd(_ *persistentFlags) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "remove <agent>",
		Short: "Permanently delete one registered agent and its state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				fmt.Fprintf(
					cmd.OutOrStdout(),
					"Permanently remove agent %q and delete its sessions and memory? [y/N] ",
					args[0],
				)
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
			manager, err := newFleetManager()
			if err != nil {
				return err
			}
			removed, err := manager.Remove(cmd.Context(), args[0], fleet.RemoveOptions{
				SkipConfirmation: true,
			})
			if err != nil {
				return mapFleetError(err)
			}
			fmt.Fprintf(
				cmd.OutOrStdout(),
				"Removed %s %s from %s.\n",
				removed.ID,
				removed.Name,
				removed.Workspace,
			)
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "remove the agent without prompting")
	return cmd
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
			var resume *fleet.RestartResume
			switch action {
			case "start":
				status, err = manager.Start(cmd.Context(), args[0])
			case "stop":
				status, err = manager.Stop(cmd.Context(), args[0])
			case "restart":
				var result fleet.RestartResult
				result, err = manager.Restart(cmd.Context(), args[0])
				status = result.AgentStatus
				resume = &result.Resume
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
			if resume != nil {
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
	var unavailable *fleet.LogUnavailableError
	if errors.As(err, &unavailable) {
		return &notFoundError{msg: err.Error()}
	}
	var ambiguous *fleet.AmbiguousSelectorError
	var conflict *fleet.ConflictError
	if errors.As(err, &ambiguous) || errors.As(err, &conflict) {
		return &conflictError{msg: err.Error()}
	}
	var invalid *fleet.ValidationError
	if errors.As(err, &invalid) {
		return &usageError{msg: err.Error()}
	}
	return err
}
