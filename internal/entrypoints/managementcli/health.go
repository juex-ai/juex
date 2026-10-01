package managementcli

import (
	"context"
	"encoding/json"
	"github.com/juex-ai/juex/internal/app/managed"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/spf13/cobra"
	"io"
	"os"
	"time"
)

func healthCommand(credentials *string, out io.Writer) *cobra.Command {
	return &cobra.Command{Use: "check", Short: "Check all private service connections", Args: cobra.NoArgs, Annotations: map[string]string{"maintenance": "true"}, RunE: func(cmd *cobra.Command, _ []string) error {
		ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
		defer cancel()
		if err := managed.CheckServices(ctx, os.Getenv("JUEX_DATABASE_URL"), platformrpc.CredentialsAt(*credentials, "management"), map[string]string{"runtime": os.Getenv("JUEX_RUNTIME_RPC"), "execution": os.Getenv("JUEX_EXECUTION_RPC"), "memory": os.Getenv("JUEX_MEMORY_RPC"), "calendar": os.Getenv("JUEX_CALENDAR_RPC")}); err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(map[string]string{"status": "ready"})
	}}
}
