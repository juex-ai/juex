// Package cli is the cobra-based CLI surface. cmd/juex's only job is to
// call Execute() and pass the exit code along.
package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/juex-ai/juex/internal/config"
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
	err := cmd.Execute()
	if err == nil {
		return ExitSuccess
	}
	switch err.(type) {
	case *dryRunOK:
		// Dry run is a successful preview, not an error. No print.
		return ExitDryRun
	case *usageError:
		fmt.Fprintln(os.Stderr, "Error:", err.Error())
		return ExitUsageError
	case *notFoundError:
		fmt.Fprintln(os.Stderr, "Error:", err.Error())
		return ExitNotFound
	case *permissionError:
		fmt.Fprintln(os.Stderr, "Error:", err.Error())
		return ExitPermission
	case *conflictError:
		fmt.Fprintln(os.Stderr, "Error:", err.Error())
		return ExitConflict
	default:
		fmt.Fprintln(os.Stderr, "Error:", err.Error())
		return ExitGeneralError
	}
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

type persistentFlags struct {
	configPath string
	envPath    string
	cwd        string
	verbose    bool
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
	}
	cmd.PersistentFlags().StringVar(&flags.configPath, "config", "", "path to juex.yaml override")
	cmd.PersistentFlags().StringVarP(&flags.envPath, "env", "e", "", "legacy path to explicit .env override")
	cmd.PersistentFlags().StringVarP(&flags.cwd, "cwd", "C", "", "working directory (default $PWD)")
	// --verbose has no short form at root level so each subcommand can use
	// -v locally (see version.go); cobra would otherwise conflict.
	cmd.PersistentFlags().BoolVar(&flags.verbose, "verbose", false, "stream runtime lifecycle events to stderr")

	cmd.AddCommand(newRunCmd(flags))
	cmd.AddCommand(newREPLCmd(flags))
	cmd.AddCommand(newVersionCmd(flags))
	cmd.AddCommand(newSchemaCmd(flags))
	cmd.AddCommand(newSessionsCmd(flags))
	cmd.AddCommand(newServeCmd(flags))
	return cmd
}

// loadConfig returns the resolved config, with --config/--env and --cwd applied.
func loadConfig(flags *persistentFlags) (config.Config, error) {
	var (
		cfg config.Config
		err error
	)
	configPath, err := explicitConfigPath(flags)
	if err != nil {
		return cfg, err
	}
	if configPath != "" {
		cfg, err = config.LoadFromFileForWorkDir(configPath, flags.cwd)
	} else {
		cfg, err = config.LoadForWorkDir(flags.cwd)
	}
	if err != nil {
		return cfg, err
	}
	return cfg, nil
}

func explicitConfigPath(flags *persistentFlags) (string, error) {
	if flags == nil {
		return "", nil
	}
	if flags.configPath != "" && flags.envPath != "" {
		return "", &usageError{msg: "--config and --env are mutually exclusive"}
	}
	if flags.configPath != "" {
		return flags.configPath, nil
	}
	return flags.envPath, nil
}

// cmdPrintln is a small helper so subcommands always write to the cobra
// command's stdout (which tests can capture via cmd.SetOut).
func cmdPrintln(c *cobra.Command, s string) {
	fmt.Fprintln(c.OutOrStdout(), s)
}
