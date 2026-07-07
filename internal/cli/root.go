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

	"github.com/juex-ai/juex/internal/cancellation"
	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/errorclass"
	"github.com/juex-ai/juex/internal/observability"
	"github.com/juex-ai/juex/internal/session"
)

// Exit code conventions (principle 6 from the agent-CLI guide). Stable
// across versions; treat as part of the public contract.
const (
	ExitSuccess      = 0
	ExitGeneralError = 1
	ExitUsageError   = 2
	ExitNotFound     = 3
	ExitPermission   = 4
	ExitConflict     = 5
	ExitDryRun       = 10
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
	model                     string
	enableUserGlobalResources string
	debug                     bool
	logLevel                  string
	verbose                   bool
}

func newRootCmd() *cobra.Command {
	flags := &persistentFlags{}
	cmd := &cobra.Command{
		Use:           "juex",
		Short:         "Juex agent runtime",
		SilenceUsage:  true,
		SilenceErrors: true, // Execute() prints errors itself so it can suppress dry-run sentinels
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if flag := cmd.Root().PersistentFlags().Lookup("model"); flag != nil && flag.Changed && strings.TrimSpace(flags.model) == "" {
				_, err := config.ParseModelRef("")
				return &usageError{msg: "--model: " + err.Error()}
			}
			if _, err := observability.ParseLevel(flags.logLevel); err != nil {
				return &usageError{msg: "--log-level: " + err.Error()}
			}
			return nil
		},
	}
	cmd.PersistentFlags().StringVar(&flags.configPath, "config", "", "path to juex.yaml override")
	cmd.PersistentFlags().StringVarP(&flags.cwd, "cwd", "C", "", "working directory (default $PWD)")
	cmd.PersistentFlags().StringVar(&flags.model, "model", "", "model override in provider_id:model_id form")
	cmd.PersistentFlags().StringVar(&flags.enableUserGlobalResources, "enable-user-global-resources", "", "enable user-global ~/.agents resources (true/false or 1/0; default from config)")
	if flag := cmd.PersistentFlags().Lookup("enable-user-global-resources"); flag != nil {
		flag.NoOptDefVal = "true"
	}
	// --verbose has no short form at root level so each subcommand can use
	// -v locally (see version.go); cobra would otherwise conflict.
	cmd.PersistentFlags().BoolVar(&flags.debug, "debug", false, "write detailed session logs, traces, spans, and tool summaries")
	cmd.PersistentFlags().StringVar(&flags.logLevel, "log-level", "", "minimum session log level: debug, info, warn, or error (default info)")
	cmd.PersistentFlags().BoolVar(&flags.verbose, "verbose", false, "stream runtime lifecycle events to stderr")

	cmd.AddCommand(newRunCmd(flags))
	cmd.AddCommand(newREPLCmd(flags))
	cmd.AddCommand(newVersionCmd(flags))
	cmd.AddCommand(newSchemaCmd(flags))
	cmd.AddCommand(newSessionsCmd(flags))
	cmd.AddCommand(newBundleCmd(flags))
	cmd.AddCommand(newServeCmd(flags))
	return cmd
}

// loadConfig returns the resolved config, with --config and --cwd applied.
func loadConfig(flags *persistentFlags) (config.Config, error) {
	var (
		cfg config.Config
		err error
	)
	configPath := explicitConfigPath(flags)
	if configPath != "" {
		cfg, err = config.LoadFromFileForWorkDirWithModelOverride(configPath, flags.cwd, modelOverride(flags))
	} else {
		cfg, err = config.LoadForWorkDirWithModelOverride(flags.cwd, modelOverride(flags))
	}
	if err != nil {
		var modelErr *config.ModelOverrideError
		if errors.As(err, &modelErr) {
			return cfg, &usageError{msg: "--model: " + err.Error()}
		}
		return cfg, err
	}
	if flags != nil && flags.enableUserGlobalResources != "" {
		enabled, err := config.ParseBoolValue(flags.enableUserGlobalResources)
		if err != nil {
			return cfg, &usageError{msg: "--enable-user-global-resources: " + err.Error()}
		}
		cfg.EnableUserGlobalResources = enabled
	}
	return cfg, nil
}

func explicitConfigPath(flags *persistentFlags) string {
	if flags == nil {
		return ""
	}
	return flags.configPath
}

func modelOverride(flags *persistentFlags) string {
	if flags == nil {
		return ""
	}
	return flags.model
}

// cmdPrintln is a small helper so subcommands always write to the cobra
// command's stdout (which tests can capture via cmd.SetOut).
func cmdPrintln(c *cobra.Command, s string) {
	fmt.Fprintln(c.OutOrStdout(), s)
}
