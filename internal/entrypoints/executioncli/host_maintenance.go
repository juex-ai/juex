package executioncli

import (
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/juex-ai/juex/internal/app/managed"
	"github.com/spf13/cobra"
)

func stopHostsCommand(out io.Writer) *cobra.Command {
	var descriptor int
	var configuration string
	cmd := &cobra.Command{Use: "stop-hosts", Short: "Stop owned Host executors after platform writers are offline", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		var inherited *os.File
		if descriptor >= 0 {
			if descriptor < 3 {
				return errors.New("maintenance lock cannot use a standard stream")
			}
			inherited = os.NewFile(uintptr(descriptor), "operator-admission-lock")
			defer func() { _ = inherited.Close() }()
		}
		stopped, err := managed.StopManagedHosts(cmd.Context(), os.Getenv("JUEX_DATABASE_URL"), configuration, os.Getenv("JUEX_MAINTENANCE_DIR"), inherited)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(map[string]any{"stopped": stopped})
	}}
	cmd.Flags().IntVar(&descriptor, "lock-fd", -1, "Inherited operator admission lock; otherwise acquire an exclusive offline lock")
	cmd.Flags().StringVar(&configuration, "host-config", os.Getenv("JUEX_HOST_CONFIG"), "Existing owned Host configuration")
	return cmd
}
