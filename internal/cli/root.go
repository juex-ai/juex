// Package cli is the cobra-based CLI surface. cmd/juex's only job is to
// call Execute() and pass the exit code along.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/juex-ai/juex/internal/agentstate"
	"github.com/juex-ai/juex/internal/cancellation"
	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/errorclass"
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
	resourceCommandGroupID = "resources"
	adminCommandGroupID    = "administration"
	cliCommandGroupID      = "cli"
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
	if strings.HasPrefix(err.Error(), "unknown command ") {
		err = &usageError{msg: err.Error()}
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

func usageArgs(validate cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := validate(cmd, args); err != nil {
			return &usageError{msg: err.Error()}
		}
		return nil
	}
}

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
	cwd        string
	agentID    string
	configPath string
}

type agentStatePolicy uint8

const (
	agentStateNone agentStatePolicy = iota
	agentStateExisting
)

func newRootCmd() *cobra.Command {
	var showVersion bool
	cmd := &cobra.Command{
		Use:   "juex",
		Short: "Juex agent runtime",
		Long: `Juex agent runtime.

Fleet manages the resident supervisor. Agent commands manage registered
workspace Agents. Thread commands operate through the selected Agent Runtime.`,
		SilenceUsage:  true,
		SilenceErrors: true, // Execute() prints errors itself so it can suppress dry-run sentinels
		RunE: func(cmd *cobra.Command, args []string) error {
			if showVersion {
				cmdPrintln(cmd, version.String())
				return nil
			}
			return cmd.Help()
		},
	}
	cmd.Flags().BoolVarP(&showVersion, "version", "v", false, "print version and exit")
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return &usageError{msg: err.Error()}
	})

	cmd.AddGroup(
		&cobra.Group{ID: resourceCommandGroupID, Title: "Managed resources"},
		&cobra.Group{ID: adminCommandGroupID, Title: "Administration"},
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
	addGrouped(resourceCommandGroupID, newAgentCmd(), newThreadCmd())
	addGrouped(adminCommandGroupID, newFleetCmd(nil), newConfigCmd(), newDiagnoseCmd(), newListenCmd(&persistentFlags{}))
	addGrouped(cliCommandGroupID, newVersionCmd(nil))
	cmd.InitDefaultHelpCmd()
	cmd.InitDefaultCompletionCmd()
	return cmd
}

func loadConfigWithPolicy(flags *persistentFlags, policy agentStatePolicy) (config.Config, error) {
	var (
		cfg config.Config
		err error
	)
	var mode config.AgentStateMode
	switch policy {
	case agentStateExisting:
		mode = config.AgentStateExisting
	case agentStateNone:
		mode = config.AgentStateNone
	default:
		return cfg, fmt.Errorf("juex: unsupported agent-state policy %d", policy)
	}
	cfg, err = config.LoadWithOptions(config.LoadOptions{
		WorkDir:    flags.cwd,
		AgentID:    flags.agentID,
		ConfigPath: flags.configPath,
		AgentState: mode,
	})
	if err != nil {
		var noAgent *agentstate.NoAgentError
		if errors.As(err, &noAgent) {
			return cfg, &notFoundError{msg: noAgent.Error()}
		}
		return cfg, err
	}
	return cfg, nil
}

type runtimeConfigLifecycle struct {
	restoreEnvironment func() error
}

func loadRuntimeConfigForCommand(cmd *cobra.Command, flags *persistentFlags) (config.Config, *runtimeConfigLifecycle, error) {
	cfg, err := loadConfigWithPolicy(flags, agentStateExisting)
	if err != nil {
		return cfg, nil, err
	}
	lifecycle := &runtimeConfigLifecycle{}
	restore, err := cfg.EnvironmentSnapshot().Activate()
	if err != nil {
		return cfg, nil, err
	}
	lifecycle.restoreEnvironment = restore
	return cfg, lifecycle, nil
}

func (lifecycle *runtimeConfigLifecycle) finish(cmd *cobra.Command, primary error) error {
	if lifecycle == nil {
		return primary
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

// cmdPrintln is a small helper so subcommands always write to the cobra
// command's stdout (which tests can capture via cmd.SetOut).
func cmdPrintln(c *cobra.Command, s string) {
	fmt.Fprintln(c.OutOrStdout(), s)
}

func mustJSON(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}
