package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/juex-ai/juex/internal/bundle"
)

func newBundleCmd(flags *persistentFlags) *cobra.Command {
	var (
		sessionID              string
		outPath                string
		format                 string
		redact                 bool
		force                  bool
		includeArtifacts       bool
		includeWorktreeSummary bool
	)
	cmd := &cobra.Command{
		Use:   "bundle --session <id> --out <file.tar.gz>",
		Short: "Create a portable debug bundle for one session",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if sessionID == "" {
				return &usageError{msg: "juex bundle: --session required"}
			}
			if outPath == "" {
				return &usageError{msg: "juex bundle: --out required"}
			}
			cfg, err := loadConfigForCommand(cmd, flags)
			if err != nil {
				return err
			}
			result, err := bundle.Create(bundle.Options{
				WorkDir:                cfg.WorkDir,
				SessionID:              sessionID,
				OutPath:                outPath,
				Redact:                 redact,
				Force:                  force,
				IncludeArtifacts:       includeArtifacts,
				IncludeWorktreeSummary: includeWorktreeSummary,
				Config:                 cfg,
				Env:                    os.Environ(),
			})
			if err != nil {
				switch {
				case errors.Is(err, bundle.ErrSessionNotFound):
					return &notFoundError{msg: err.Error()}
				case errors.Is(err, bundle.ErrOutputExists):
					return &conflictError{msg: err.Error()}
				default:
					return err
				}
			}
			switch format {
			case "json", "":
				cmdPrintln(cmd, mustJSON(result))
			case "text":
				fmt.Fprintf(cmd.OutOrStdout(), "bundle: %s\nsession: %s\nfiles: %d\nbytes: %d\nredacted: %t\n", result.Path, result.SessionID, result.Files, result.Bytes, result.Redacted)
			default:
				return &usageError{msg: "unknown --format value: " + format}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&sessionID, "session", "", "session id to bundle")
	cmd.Flags().StringVar(&outPath, "out", "", "output .tar.gz path")
	cmd.Flags().StringVar(&format, "format", "json", "json|text")
	cmd.Flags().BoolVar(&redact, "redact", true, "redact secret-like values from bundled text files")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing output path")
	cmd.Flags().BoolVar(&includeArtifacts, "include-artifacts", false, "include .juex/artifacts files")
	cmd.Flags().BoolVar(&includeWorktreeSummary, "include-worktree-summary", false, "include a worktree summary without file contents")
	declareAgentStatePolicy(cmd, agentStateExisting)
	return cmd
}
