// Package cli is the cobra-based CLI surface. cmd/juex's only job is to
// call Execute() and pass the exit code along.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/juex-ai/juex/internal/agentstate"
	"github.com/juex-ai/juex/internal/cancellation"
	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/errorclass"
	"github.com/juex-ai/juex/internal/observability"
	"github.com/juex-ai/juex/internal/session"
	"github.com/juex-ai/juex/internal/version"
)

// Exit code conventions (principle 6 from the agent-CLI guide). Stable
// across versions; treat as part of the public contract.
const (
	ExitSuccess       = 0
	ExitGeneralError  = 1
	ExitUsageError    = 2
	ExitNotFound      = 3
	ExitPermission    = 4
	ExitConflict      = 5
	ExitDoctorWarning = 6
	ExitDoctorFailure = 7
	ExitDryRun        = 10
)

const (
	workspaceCommandGroupID = "workspace"
	debugCommandGroupID     = "debug"
	fleetCommandGroupID     = "fleet"
	cliCommandGroupID       = "cli"
)

// Execute runs the root cobra command and returns the process exit code.
// We handle error printing ourselves (cobra is silenced) so we can suppress
// the message for dry-run sentinels and choose the appropriate exit code
// per error type (principle 6: stable exit codes).
func Execute() int {
	cmd := newRootCmd()
	ctx, stop := cancellation.NotifyContext(context.Background())
	defer stop()
	cmd.SetContext(ctx)
	err := cmd.ExecuteContext(ctx)
	if err == nil {
		return ExitSuccess
	}
	alreadyEmitted := false
	var emitted *emittedError
	if errors.As(err, &emitted) && emitted != nil && emitted.err != nil {
		err = emitted.err
		alreadyEmitted = true
	}
	err = cancellation.NormalizeErrorWithContext(ctx, err)
	var doctorErr *doctorExitError
	if errors.As(err, &doctorErr) {
		return doctorErr.ExitCode()
	}
	var lockErr *session.LockError
	if errors.As(err, &lockErr) {
		printErrorIfNeeded(alreadyEmitted, err)
		return ExitConflict
	}
	switch err.(type) {
	case *dryRunOK:
		// Dry run is a successful preview, not an error. No print.
		return ExitDryRun
	case *usageError:
		printErrorIfNeeded(alreadyEmitted, err)
		return ExitUsageError
	case *notFoundError:
		printErrorIfNeeded(alreadyEmitted, err)
		return ExitNotFound
	case *permissionError:
		printErrorIfNeeded(alreadyEmitted, err)
		return ExitPermission
	case *conflictError:
		printErrorIfNeeded(alreadyEmitted, err)
		return ExitConflict
	default:
		printErrorIfNeeded(alreadyEmitted, err)
		return ExitGeneralError
	}
}

func printErrorIfNeeded(alreadyEmitted bool, err error) {
	if alreadyEmitted {
		return
	}
	err = cancellation.NormalizeError(err)
	fmt.Fprintln(os.Stderr, "Error:", errorclass.PublicMessage(err, errorclass.MessageOptions{}))
}

// usageError marks an error caused by bad CLI usage (missing required arg,
// unknown subcommand, malformed flag). Mapped to exit code 2.
type usageError struct{ msg string }

func (u *usageError) Error() string { return u.msg }

// notFoundError marks a missing resource: file, env file, server, etc.
// Mapped to exit code 3.
type notFoundError struct{ msg string }

func (n *notFoundError) Error() string { return n.msg }

// permissionError marks an authentication / authorisation failure.
// Mapped to exit code 4. Reserved for future credential errors; the LLM
// SDKs already surface these as generic errors today.
type permissionError struct{ msg string }

func (p *permissionError) Error() string { return p.msg }

// conflictError marks a uniqueness / already-exists violation.
// Mapped to exit code 5. Not used in v0.0.1 commands; reserved for future
// noun-style write commands.
type conflictError struct{ msg string }

func (c *conflictError) Error() string { return c.msg }

// dryRunOK signals a successful dry-run from a side-effecting command.
// Mapped to exit code 10 so agents can distinguish "preview ok" from
// "really executed ok".
type dryRunOK struct{ msg string }

func (d *dryRunOK) Error() string { return d.msg }

type emittedError struct{ err error }

func (e *emittedError) Error() string {
	if e == nil || e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e *emittedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

type persistentFlags struct {
	configPath                string
	cwd                       string
	models                    string
	enableUserAgentsResources string
	debug                     bool
	logLevel                  string
	verbose                   bool
}

type agentStatePolicy uint8

const (
	agentStateNone agentStatePolicy = iota
	agentStateExisting
	agentStateMint
	agentStateEphemeral
)

const agentStatePolicyAnnotation = "juex.ai/agent-state-policy"

func declareAgentStatePolicy(cmd *cobra.Command, policy agentStatePolicy) {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	switch policy {
	case agentStateNone:
		cmd.Annotations[agentStatePolicyAnnotation] = "none"
	case agentStateExisting:
		cmd.Annotations[agentStatePolicyAnnotation] = "existing"
	case agentStateMint:
		cmd.Annotations[agentStatePolicyAnnotation] = "mint"
	default:
		panic(fmt.Sprintf("unsupported declared agent-state policy %d", policy))
	}
}

func commandAgentStatePolicy(cmd *cobra.Command) (agentStatePolicy, error) {
	for current := cmd; current != nil && current != cmd.Root(); current = current.Parent() {
		value, ok := current.Annotations[agentStatePolicyAnnotation]
		if !ok {
			continue
		}
		switch value {
		case "none":
			return agentStateNone, nil
		case "existing":
			return agentStateExisting, nil
		case "mint":
			ephemeral, err := current.Flags().GetBool("ephemeral")
			if err == nil && ephemeral {
				return agentStateEphemeral, nil
			}
			if current.Name() == "run" {
				dryRun, dryRunErr := current.Flags().GetBool("dry-run")
				if dryRunErr == nil && dryRun {
					return agentStateEphemeral, nil
				}
			}
			return agentStateMint, nil
		default:
			return agentStateNone, fmt.Errorf("juex: command %s declares unknown agent-state policy %q", cmd.CommandPath(), value)
		}
	}
	return agentStateNone, fmt.Errorf("juex: command %s has no agent-state policy declaration", cmd.CommandPath())
}

func newRootCmd() *cobra.Command {
	flags := &persistentFlags{}
	var showVersion bool
	cmd := &cobra.Command{
		Use:   "juex",
		Short: "Juex agent runtime",
		Long: `Juex agent runtime.

Agent, session, and troubleshooting commands resolve the workspace agent from
the current directory or --cwd. juex fleet commands manage all agents
registered under the effective $JUEX_HOME. CLI information commands do not
operate on an agent.`,
		SilenceUsage:  true,
		SilenceErrors: true, // Execute() prints errors itself so it can suppress dry-run sentinels
		RunE: func(cmd *cobra.Command, args []string) error {
			if showVersion {
				cmdPrintln(cmd, version.String())
				return nil
			}
			return cmd.Help()
		},
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if isFleetCommand(cmd) {
				for _, name := range []string{"cwd", "config", "models"} {
					flag := cmd.Root().PersistentFlags().Lookup(name)
					if flag != nil && flag.Changed {
						return &usageError{msg: "juex fleet: --" + name + " is not supported; fleet commands use the effective JUEX_HOME registry"}
					}
				}
			}
			if flag := cmd.Root().PersistentFlags().Lookup("models"); flag != nil && flag.Changed && strings.TrimSpace(flags.models) == "" {
				return &usageError{msg: "--models: at least one provider:model is required"}
			}
			if _, err := observability.ParseLevel(flags.logLevel); err != nil {
				return &usageError{msg: "--log-level: " + err.Error()}
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&showVersion, "version", "v", false, "print version and exit")
	cmd.PersistentFlags().StringVar(&flags.configPath, "config", "", "path to juex.yaml override")
	cmd.PersistentFlags().StringVarP(&flags.cwd, "cwd", "C", "", "working directory (default $PWD)")
	cmd.PersistentFlags().StringVar(&flags.models, "models", "", "ordered model override as comma-separated provider:model refs")
	cmd.PersistentFlags().StringVar(&flags.enableUserAgentsResources, "enable-user-agents-resources", "", "enable personal ~/.agents resources (true/false or 1/0; default from config)")
	if flag := cmd.PersistentFlags().Lookup("enable-user-agents-resources"); flag != nil {
		flag.NoOptDefVal = "true"
	}
	// --verbose has no short form at root level so each subcommand can use
	// -v locally (see version.go); cobra would otherwise conflict.
	cmd.PersistentFlags().BoolVar(&flags.debug, "debug", false, "write detailed session logs, traces, spans, and tool summaries")
	cmd.PersistentFlags().StringVar(&flags.logLevel, "log-level", "", "minimum session log level: debug, info, warn, or error (default info)")
	cmd.PersistentFlags().BoolVar(&flags.verbose, "verbose", false, "stream runtime lifecycle events to stderr")

	cmd.AddGroup(
		&cobra.Group{ID: workspaceCommandGroupID, Title: "Workspace agent (current directory)"},
		&cobra.Group{ID: debugCommandGroupID, Title: "Troubleshooting (current directory)"},
		&cobra.Group{ID: fleetCommandGroupID, Title: "Fleet (all agents under $JUEX_HOME)"},
		&cobra.Group{ID: cliCommandGroupID, Title: "About this CLI"},
	)
	cmd.SetHelpCommandGroupID(cliCommandGroupID)
	cmd.SetCompletionCommandGroupID(cliCommandGroupID)
	addGrouped := func(groupID string, commands ...*cobra.Command) {
		for _, command := range commands {
			command.GroupID = groupID
		}
		cmd.AddCommand(commands...)
	}
	addGrouped(
		workspaceCommandGroupID,
		newRunCmd(flags),
		newREPLCmd(flags),
		newListenCmd(flags),
		newSessionsCmd(flags),
		newInitCmd(flags),
	)
	addGrouped(
		debugCommandGroupID,
		newDoctorCmd(flags),
		newBundleCmd(flags),
	)
	addGrouped(fleetCommandGroupID, newFleetCmd(flags))
	addGrouped(
		cliCommandGroupID,
		newVersionCmd(flags),
	)
	return cmd
}

func isFleetCommand(cmd *cobra.Command) bool {
	for current := cmd; current != nil; current = current.Parent() {
		if current.Name() == "fleet" {
			return true
		}
	}
	return false
}

// loadConfig preserves the historical durable-mint behavior for internal
// callers that are not tied to a Cobra command.
func loadConfig(flags *persistentFlags) (config.Config, error) {
	return loadConfigWithPolicy(flags, agentStateMint)
}

func loadConfigWithPolicy(flags *persistentFlags, policy agentStatePolicy) (config.Config, error) {
	var (
		cfg config.Config
		err error
	)
	configPath := explicitConfigPath(flags)
	var mode config.AgentStateMode
	switch policy {
	case agentStateMint:
		mode = config.AgentStateMint
	case agentStateExisting:
		mode = config.AgentStateExisting
	case agentStateNone, agentStateEphemeral:
		mode = config.AgentStateNone
	default:
		return cfg, fmt.Errorf("juex: unsupported agent-state policy %d", policy)
	}
	cfg, err = config.LoadWithOptions(config.LoadOptions{
		WorkDir:    flags.cwd,
		ConfigPath: configPath,
		ModelRefs:  modelsOverride(flags),
		AgentState: mode,
	})
	if err != nil {
		var modelErr *config.ModelsOverrideError
		if errors.As(err, &modelErr) {
			return cfg, &usageError{msg: "--models: " + err.Error()}
		}
		var noAgent *agentstate.NoAgentError
		if errors.As(err, &noAgent) {
			return cfg, &notFoundError{msg: noAgent.Error()}
		}
		return cfg, err
	}
	if flags != nil && flags.enableUserAgentsResources != "" {
		enabled, err := config.ParseBoolValue(flags.enableUserAgentsResources)
		if err != nil {
			return cfg, &usageError{msg: "--enable-user-agents-resources: " + err.Error()}
		}
		cfg.EnableUserAgentsResources = enabled
	}
	return cfg, nil
}

func loadConfigForCommand(cmd *cobra.Command, flags *persistentFlags) (config.Config, error) {
	policy, err := commandAgentStatePolicy(cmd)
	if err != nil {
		return config.Config{}, err
	}
	if policy == agentStateEphemeral {
		return config.Config{}, fmt.Errorf("juex: command %s requires the ephemeral runtime loader", cmd.CommandPath())
	}
	cfg, err := loadConfigWithPolicy(flags, policy)
	if err != nil {
		return cfg, err
	}
	writeConfigMessages(cmd, cfg)
	return cfg, nil
}

func writeConfigMessages(cmd *cobra.Command, cfg config.Config) {
	for _, notice := range cfg.AgentStateNotices {
		fmt.Fprintf(cmd.ErrOrStderr(), "juex: notice: %s\n", notice)
	}
}

type runtimeConfigLifecycle struct {
	state              *agentstate.Ephemeral
	keep               bool
	path               string
	restoreEnvironment func() error
}

func loadRuntimeConfigForCommand(cmd *cobra.Command, flags *persistentFlags, keep bool) (config.Config, *runtimeConfigLifecycle, error) {
	policy, err := commandAgentStatePolicy(cmd)
	if err != nil {
		return config.Config{}, nil, err
	}
	cfg, err := loadConfigWithPolicy(flags, policy)
	if err != nil {
		return cfg, nil, err
	}
	writeConfigMessages(cmd, cfg)
	lifecycle := &runtimeConfigLifecycle{}
	if policy == agentStateEphemeral {
		state, err := agentstate.CreateEphemeral(cfg.WorkDir)
		if err != nil {
			return cfg, nil, err
		}
		cfg.AgentID = state.Resolution.Agent.ID
		cfg.AgentName = state.Resolution.Agent.Name
		cfg.AgentStateDir = state.Resolution.Address.StateDir()
		cfg.AgentAddress = state.Resolution.Address
		lifecycle.state = state
		lifecycle.keep = keep
		lifecycle.path = state.Resolution.Address.StateDir()
	}
	restore, err := cfg.EnvironmentSnapshot().Activate()
	if err != nil {
		if lifecycle.state != nil {
			_ = lifecycle.state.Remove()
		}
		return cfg, nil, err
	}
	lifecycle.restoreEnvironment = restore
	return cfg, lifecycle, nil
}

func (lifecycle *runtimeConfigLifecycle) finish(cmd *cobra.Command, primary error) error {
	if lifecycle == nil {
		return primary
	}
	if lifecycle.state != nil {
		if lifecycle.keep {
			fmt.Fprintln(cmd.ErrOrStderr(), "juex: kept ephemeral state at "+lifecycle.path)
		} else if err := lifecycle.state.Remove(); err != nil {
			if primary != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "juex: warning: "+err.Error())
			} else {
				primary = err
			}
		}
	}
	if lifecycle.restoreEnvironment != nil {
		if err := lifecycle.restoreEnvironment(); err != nil {
			if primary != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "juex: warning: restore environment: "+err.Error())
			} else {
				primary = fmt.Errorf("juex: restore environment: %w", err)
			}
		}
	}
	return primary
}

func validateEphemeralFlags(ephemeral, keep, dryRun bool) error {
	switch {
	case dryRun && ephemeral:
		return &usageError{msg: "juex run: --dry-run cannot be combined with --ephemeral; dry-run is already isolated"}
	case dryRun && keep:
		return &usageError{msg: "juex run: --dry-run cannot be combined with --keep"}
	case keep && !ephemeral:
		return &usageError{msg: "--keep requires --ephemeral"}
	default:
		return nil
	}
}

func explicitConfigPath(flags *persistentFlags) string {
	if flags == nil {
		return ""
	}
	return flags.configPath
}

func modelsOverride(flags *persistentFlags) []string {
	if flags == nil || strings.TrimSpace(flags.models) == "" {
		return nil
	}
	return strings.Split(flags.models, ",")
}

// cmdPrintln is a small helper so subcommands always write to the cobra
// command's stdout (which tests can capture via cmd.SetOut).
func cmdPrintln(c *cobra.Command, s string) {
	fmt.Fprintln(c.OutOrStdout(), s)
}
