package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/bundle"
)

func newThreadBundleCmd(selectors *agentSelectorFlags) *cobra.Command {
	var (
		outPath                string
		format                 string
		redact                 bool
		force                  bool
		includeMedia           bool
		includeWorktreeSummary bool
	)
	cmd := &cobra.Command{
		Use:   "bundle <thread> --out <file.tar.gz>",
		Short: "Create a portable debug bundle for one thread",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			if outPath == "" {
				return &usageError{msg: "juex thread bundle: --out required"}
			}
			_, state, err := resolveSelectedAgent(selectors)
			if err != nil {
				return err
			}
			cfg, err := loadSelectedAgentConfig(state)
			if err != nil {
				return err
			}
			agentRuntime, err := app.ResolveAgentRuntime(cfg)
			if err != nil {
				return err
			}
			result, err := bundle.Create(bundle.Options{
				WorkDir:                cfg.WorkDir,
				ThreadID:               args[0],
				OutPath:                outPath,
				Redact:                 redact,
				Force:                  force,
				IncludeMedia:           includeMedia,
				IncludeWorktreeSummary: includeWorktreeSummary,
				Config:                 cfg,
				Environment:            agentRuntime.Environment(),
			})
			if err != nil {
				switch {
				case errors.Is(err, bundle.ErrThreadNotFound):
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
				fmt.Fprintf(cmd.OutOrStdout(), "bundle: %s\nthread: %s\nfiles: %d\nbytes: %d\nredacted: %t\n", result.Path, result.ThreadID, result.Files, result.Bytes, result.Redacted)
			default:
				return &usageError{msg: "unknown --format value: " + format}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&outPath, "out", "", "output .tar.gz path")
	cmd.Flags().StringVar(&format, "format", "json", "json|text")
	cmd.Flags().BoolVar(&redact, "redact", true, "redact secret-like values from bundled text files")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing output path")
	cmd.Flags().BoolVar(&includeMedia, "include-media", false, "include Agent-managed media files")
	cmd.Flags().BoolVar(&includeWorktreeSummary, "include-worktree-summary", false, "include a worktree summary without file contents")
	return cmd
}
