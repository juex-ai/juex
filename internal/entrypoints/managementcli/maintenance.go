package managementcli

import (
	"encoding/json"
	"errors"
	"github.com/juex-ai/juex/internal/app/managed"
	"github.com/spf13/cobra"
	"io"
	"os"
)

func maintenanceCommand(out io.Writer) *cobra.Command {
	var offline bool
	cmd := &cobra.Command{Use: "maintenance-report", Short: "Inspect all service-owned recovery inventories without changing data", Args: cobra.NoArgs, Annotations: map[string]string{"maintenance": "true"}, RunE: func(cmd *cobra.Command, _ []string) error {
		reports, err := managed.InspectMaintenance(cmd.Context(), os.Getenv("JUEX_DATABASE_URL"), offline)
		if err != nil {
			return err
		}
		ready := true
		for _, r := range reports {
			ready = ready && r.Ready()
		}
		if err = json.NewEncoder(out).Encode(map[string]any{"ready": ready, "services": reports}); err != nil {
			return err
		}
		if !ready {
			return errors.New("maintenance not ready: inspect busy work; no operation was cancelled")
		}
		return nil
	}}
	cmd.Flags().BoolVar(&offline, "offline", false, "Require all business database clients to be disconnected")
	return cmd
}
