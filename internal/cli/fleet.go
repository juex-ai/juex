package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/fleet"
	"github.com/juex-ai/juex/internal/fleetservice"
	"github.com/juex-ai/juex/internal/fleetweb"
	"github.com/juex-ai/juex/internal/processmetrics"
	"github.com/juex-ai/juex/internal/version"
)

func newFleetCmd(flags *persistentFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fleet",
		Short: "Manage resident Workspace Agents in the effective JUEX_HOME",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newFleetServeCmd(flags))
	cmd.AddCommand(newFleetStatusCmd(flags))
	cmd.AddCommand(newFleetGCCmd(flags))
	cmd.AddCommand(newFleetInstallCmd(flags))
	cmd.AddCommand(newFleetUninstallCmd(flags))
	cmd.AddCommand(newFleetServiceInstalledCmd())
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
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			settings, err := resolveFleetServeSettings(cmd, addr, unsafeBindAny)
			if err != nil {
				return err
			}
			addr = settings.Addr
			unsafeBindAny = settings.UnsafeBindAny
			if !isTCPListenAddr(addr) {
				return &usageError{msg: "juex fleet serve: --addr must be a host:port TCP address (got " + addr + ")"}
			}
			if !unsafeBindAny && !isLoopbackAddr(addr) {
				return &usageError{msg: "juex fleet serve: --addr must bind to loopback (got " + addr + "). Pass --unsafe-bind-any if you have your own network protection."}
			}
			if unsafeBindAny && !isLoopbackAddr(addr) {
				fmt.Fprintln(cmd.ErrOrStderr(), "WARNING: non-loopback fleet binding is enabled; juex has no authentication. Anyone who can reach this address can run shell commands.")
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

func newFleetServiceManager() (*fleetservice.Manager, error) {
	homeDir, err := config.EffectiveHomeDir()
	if err != nil {
		return nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("juex fleet: resolve executable: %w", err)
	}
	return fleetservice.New(fleetservice.Options{
		HomeDir:    homeDir,
		Executable: executable,
	})
}

type fleetServeSettings struct {
	Addr          string
	UnsafeBindAny bool
}

func resolveFleetServeSettings(
	cmd *cobra.Command,
	flagAddr string,
	flagUnsafeBindAny bool,
) (fleetServeSettings, error) {
	explicitAddr := cmd.Flags().Changed("addr")
	addr := strings.TrimSpace(flagAddr)
	unsafeBindAny := flagUnsafeBindAny
	if !explicitAddr {
		fleetCfg, err := config.LoadHomeFleetConfig()
		if err != nil {
			return fleetServeSettings{}, err
		}
		addr = fleetCfg.Addr
		unsafeBindAny = effectiveFleetUnsafeBindAny(
			cmd,
			flagUnsafeBindAny,
			explicitAddr,
			fleetCfg,
		)
	}
	if !isTCPListenAddr(addr) {
		return fleetServeSettings{}, &usageError{msg: "juex fleet: --addr must be a host:port TCP address (got " + addr + ")"}
	}
	return fleetServeSettings{Addr: addr, UnsafeBindAny: unsafeBindAny}, nil
}

func effectiveFleetUnsafeBindAny(
	cmd *cobra.Command,
	flagUnsafeBindAny bool,
	explicitAddr bool,
	fleetCfg config.FleetConfig,
) bool {
	if !explicitAddr && !cmd.Flags().Changed("unsafe-bind-any") {
		return fleetCfg.UnsafeBindAny
	}
	return flagUnsafeBindAny
}

type fleetInstallSettings struct {
	Addr          string
	UnsafeBindAny bool
	ConfigPath    string
}

type fleetServiceInstaller interface {
	ExistingServeOptions() (fleetservice.InstalledServeOptions, bool, error)
	Install(context.Context) (fleetservice.Registration, error)
}

type fleetInstallCommandDeps struct {
	newServiceManager func() (fleetServiceInstaller, error)
}

func defaultFleetInstallCommandDeps() fleetInstallCommandDeps {
	return fleetInstallCommandDeps{
		newServiceManager: func() (fleetServiceInstaller, error) {
			return newFleetServiceManager()
		},
	}
}

func resolveFleetInstallSettings(
	cmd *cobra.Command,
	flagAddr string,
	unsafeBindAny bool,
	fleetCfg config.FleetConfig,
) (fleetInstallSettings, error) {
	explicitAddr := cmd.Flags().Changed("addr")
	addr := strings.TrimSpace(flagAddr)
	if !explicitAddr {
		addr = fleetCfg.Addr
	}
	explicitUnsafeBindAny := cmd.Flags().Changed("unsafe-bind-any")
	effectiveUnsafeBindAny := effectiveFleetUnsafeBindAny(
		cmd,
		unsafeBindAny,
		explicitAddr,
		fleetCfg,
	)
	settings := fleetInstallSettings{Addr: addr, UnsafeBindAny: effectiveUnsafeBindAny}
	if err := config.ValidateStableFleetAddr(settings.Addr); err != nil {
		return fleetInstallSettings{}, &usageError{msg: "juex fleet: --addr " + err.Error()}
	}
	if !settings.UnsafeBindAny && !isLoopbackAddr(settings.Addr) {
		return fleetInstallSettings{}, &usageError{
			msg: "juex fleet install: --addr must bind to loopback (got " + settings.Addr + "). Pass --unsafe-bind-any if you have your own network protection.",
		}
	}
	if explicitAddr || explicitUnsafeBindAny {
		configPath, err := config.SetHomeFleetSettings(
			settings.Addr,
			settings.UnsafeBindAny,
		)
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
	)
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install and start the fleet as a per-user system service",
		Args:  usageArgs(cobra.NoArgs),
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
			selectedUnsafeBindAny := effectiveFleetUnsafeBindAny(
				cmd,
				unsafeBindAny,
				explicitAddr,
				fleetCfg,
			)
			if !selectedUnsafeBindAny && !isLoopbackAddr(selectedAddr) {
				return &usageError{
					msg: "juex fleet install: --addr must bind to loopback (got " + selectedAddr + "). Pass --unsafe-bind-any if you have your own network protection.",
				}
			}
			manager, err := deps.newServiceManager()
			if err != nil {
				return err
			}
			// Parse an existing definition so a malformed or foreign service is
			// not overwritten. Its baked-in options are not configuration inputs.
			_, _, err = manager.ExistingServeOptions()
			if err != nil {
				return err
			}
			settings, err := resolveFleetInstallSettings(
				cmd,
				addr,
				unsafeBindAny,
				fleetCfg,
			)
			if err != nil {
				return err
			}
			if settings.UnsafeBindAny && !isLoopbackAddr(settings.Addr) {
				fmt.Fprintln(cmd.ErrOrStderr(), "WARNING: non-loopback fleet binding is enabled; juex has no authentication. Anyone who can reach this address can run shell commands.")
			}
			registration, err := manager.Install(cmd.Context())
			if err != nil {
				if settings.ConfigPath != "" {
					return fmt.Errorf("%w; fleet.addr remains written to %s", err, settings.ConfigPath)
				}
				return err
			}
			renderFleetServiceResult(cmd, "Installed", registration)
			return nil
		},
	}
	cmd.Flags().StringVar(&addr, "addr", config.DefaultFleetAddr, "stable fleet browser address (host:port)")
	cmd.Flags().BoolVar(&unsafeBindAny, "unsafe-bind-any", false, "allow --addr to bind beyond loopback (no auth — use only on trusted networks)")
	return cmd
}

func newFleetUninstallCmd(_ *persistentFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Stop and remove the fleet per-user system service",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			manager, err := newFleetServiceManager()
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
		Args:   usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			manager, err := newFleetServiceManager()
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

type fleetStatusService interface {
	Installed(context.Context) (bool, error)
}

type fleetStatusCommandDeps struct {
	loadHome          func() (string, error)
	loadConfig        func() (config.FleetConfig, error)
	newServiceManager func() (fleetStatusService, error)
	httpClient        *http.Client
}

type fleetServiceStatus struct {
	EffectiveHome    string                `json:"effective_home"`
	Address          string                `json:"address"`
	ServiceInstalled bool                  `json:"service_installed"`
	Running          bool                  `json:"running"`
	Reachable        bool                  `json:"reachable"`
	Process          *processmetrics.Usage `json:"process,omitempty"`
	Problem          string                `json:"problem,omitempty"`
}

func defaultFleetStatusCommandDeps() fleetStatusCommandDeps {
	return fleetStatusCommandDeps{
		loadHome:   config.EffectiveHomeDir,
		loadConfig: config.LoadHomeFleetConfig,
		newServiceManager: func() (fleetStatusService, error) {
			return newFleetServiceManager()
		},
		httpClient: &http.Client{Timeout: time.Second},
	}
}

func newFleetStatusCmd(_ *persistentFlags) *cobra.Command {
	return newFleetStatusCmdWithDeps(defaultFleetStatusCommandDeps())
}

func newFleetStatusCmdWithDeps(deps fleetStatusCommandDeps) *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show Fleet service configuration and process health",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if format != "table" && format != "json" {
				return &usageError{msg: "juex fleet status: --format must be table or json"}
			}
			home, err := deps.loadHome()
			if err != nil {
				return err
			}
			fleetConfig, err := deps.loadConfig()
			if err != nil {
				return err
			}
			serviceManager, err := deps.newServiceManager()
			if err != nil {
				return err
			}
			installed, err := serviceManager.Installed(cmd.Context())
			if err != nil {
				return err
			}
			status := fleetServiceStatus{EffectiveHome: home, Address: fleetConfig.Addr, ServiceInstalled: installed}
			probeFleetStatus(cmd.Context(), deps.httpClient, &status)
			if format == "json" {
				cmdPrintln(cmd, mustJSON(status))
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "effective_home:    %s\naddress:           %s\nservice_installed: %t\nrunning:           %t\nreachable:         %t\n", status.EffectiveHome, status.Address, status.ServiceInstalled, status.Running, status.Reachable)
				if status.Process != nil {
					fmt.Fprintf(cmd.OutOrStdout(), "rss_bytes:         %d\n", status.Process.RSSBytes)
				}
				if status.Problem != "" {
					fmt.Fprintln(cmd.OutOrStdout(), "problem:           "+status.Problem)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&format, "format", "table", "output format: table or json")
	return cmd
}

func probeFleetStatus(ctx context.Context, client *http.Client, status *fleetServiceStatus) {
	if status == nil {
		return
	}
	addr := fleetProbeAddress(status.Address)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/api/fleet/status", nil)
	if err != nil {
		status.Problem = err.Error()
		return
	}
	response, err := client.Do(request)
	if err != nil {
		status.Problem = err.Error()
		return
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		status.Problem = err.Error()
		return
	}
	if response.StatusCode != http.StatusOK {
		status.Problem = fmt.Sprintf("Fleet API returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(data)))
		return
	}
	var payload struct {
		Process processmetrics.Usage `json:"process"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		status.Problem = "decode Fleet status: " + err.Error()
		return
	}
	status.Running = true
	status.Reachable = true
	status.Process = &payload.Process
}

func fleetProbeAddress(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
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

func newFleetGCCmd(_ *persistentFlags) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "gc",
		Short: "Review and delete definitely orphaned agent state",
		Args:  usageArgs(cobra.NoArgs),
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
