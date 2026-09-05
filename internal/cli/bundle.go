package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/bundle"
	"github.com/juex-ai/juex/internal/thread"
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
			selectedID, err := resolveBundleThreadID(thread.NewStore(cfg.RuntimePaths().StateDir), args[0])
			if err != nil {
				return err
			}
			result, err := bundle.Create(bundle.Options{
				WorkDir:                cfg.WorkDir,
				ThreadID:               selectedID,
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

func resolveBundleThreadID(store *thread.Store, selector string) (string, error) {
	selectedID := normalizeThreadSelector(selector)
	if thread.ValidID(selectedID) {
		exists, err := bundleThreadDirectoryExists(store, selectedID)
		if err != nil {
			return "", err
		}
		if exists {
			return selectedID, nil
		}
	}
	entries, err := store.List()
	if err != nil {
		return "", err
	}
	var threads threadList
	for _, entry := range entries {
		if entry.RetentionState == thread.RetentionArchived {
			threads.Archived = append(threads.Archived, entry)
		} else {
			threads.Active = append(threads.Active, entry)
		}
	}
	selected, err := resolveThreadEntry(selector, threads, true)
	if err != nil {
		return "", err
	}
	return selected.ThreadID, nil
}

func bundleThreadDirectoryExists(store *thread.Store, threadID string) (bool, error) {
	for _, root := range []string{store.ThreadsDir(), store.ArchiveDir()} {
		if _, err := os.Stat(filepath.Join(root, threadID)); err == nil {
			return true, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	return false, nil
}
