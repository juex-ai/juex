package managementcli

import (
	"encoding/json"
	"github.com/juex-ai/juex/internal/app/managed"
	"github.com/spf13/cobra"
	"io"
	"os"
)

func recoveryCommand(out io.Writer) *cobra.Command {
	var tenant, user, device string
	cmd := &cobra.Command{Use: "recovery-revoke", Short: "Revoke restored authority while all services are offline", Args: cobra.NoArgs, Annotations: map[string]string{"maintenance": "true"}, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := managed.RevokeRecoveredAuthority(cmd.Context(), os.Getenv("JUEX_DATABASE_URL"), os.Getenv("JUEX_MAINTENANCE_DIR"), tenant, user, device); err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(map[string]string{"status": "revoked"})
	}}
	cmd.Flags().StringVar(&tenant, "tenant", "", "Restored tenant ID")
	cmd.Flags().StringVar(&user, "user", "", "Restored member ID to suspend")
	cmd.Flags().StringVar(&device, "device", "", "Restored device ID to revoke")
	return cmd
}
