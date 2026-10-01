package executioncli

import (
	"encoding/json"
	"github.com/juex-ai/juex/internal/app/managed"
	"github.com/spf13/cobra"
	"io"
	"os"
)

func recoveryCommand(out io.Writer) *cobra.Command {
	var config, previous string
	var initialize bool
	cmd := &cobra.Command{Use: "restore-storage", Short: "Restore hosted quota metadata offline after restoring all backup files", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := managed.RecoverHostedStorage(cmd.Context(), os.Getenv("JUEX_DATABASE_URL"), config, previous, os.Getenv("JUEX_MAINTENANCE_DIR"), initialize); err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(map[string]string{"status": "storage_restored"})
	}}
	cmd.Flags().StringVar(&config, "hosted-config", os.Getenv("JUEX_HOSTED_CONFIG"), "Restored operator configuration using the destination XFS UUID")
	cmd.Flags().StringVar(&previous, "previous-storage", "", "Original XFS UUID from the verified backup")
	cmd.Flags().BoolVar(&initialize, "initialize", false, "Initialize empty quota trees before extracting Workspace/Home files")
	return cmd
}
