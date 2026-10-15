package cli

import (
	"encoding/json"
	"github.com/spf13/cobra"
)

func newFleetSupervisorCmd() *cobra.Command {
	root := &cobra.Command{Use: "supervisor", Short: "Manage the Fleet's bound Supervisor Agent", Long: "Manage the ordinary Agent bound to the Supervisor role. Reset creates a new identity; reset and remove disable the old Agent and retain its workspace, configuration, history, and shared service data."}
	for _, action := range []string{"status", "init", "start", "stop", "enable", "disable", "repair", "reset", "remove"} {
		root.AddCommand(&cobra.Command{Use: action, Short: action + " the Supervisor Agent", Args: usageArgs(cobra.NoArgs), RunE: func(cmd *cobra.Command, _ []string) error {
			manager, err := newFleetManager()
			if err != nil {
				return err
			}
			result, err := manager.SupervisorAction(cmd.Context(), action)
			if err != nil {
				return mapFleetError(err)
			}
			encoder := json.NewEncoder(cmd.OutOrStdout())
			encoder.SetIndent("", "  ")
			return encoder.Encode(result)
		}})
	}
	return root
}
