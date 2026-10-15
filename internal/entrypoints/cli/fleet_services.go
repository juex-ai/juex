package cli

import (
	"encoding/json"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/app/config"
	"github.com/spf13/cobra"
)

func newFleetServicesCmd() *cobra.Command {
	root := &cobra.Command{Use: "services", Short: "Manage independent services in the owning JUEX_HOME"}
	for _, operation := range []string{"list", "status", "start", "stop", "restart", "logs"} {
		op := operation
		lines := 100
		cmd := &cobra.Command{Use: op + " <service>", Short: op + " a Fleet service", Args: usageArgs(cobra.ExactArgs(1))}
		if op == "list" {
			cmd.Use = "list"
			cmd.Short = "List Fleet services"
			cmd.Args = usageArgs(cobra.NoArgs)
		}
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			home, err := config.EffectiveHomeDir()
			if err != nil {
				return err
			}
			manager, err := app.NewFleetServices(home)
			if err != nil {
				return err
			}
			var result any
			switch op {
			case "list":
				result = manager.Status(cmd.Context())
			case "status":
				result, err = manager.Get(cmd.Context(), args[0])
			case "start":
				result, err = manager.Start(cmd.Context(), args[0])
			case "stop":
				result, err = manager.Stop(cmd.Context(), args[0])
			case "restart":
				result, err = manager.Restart(cmd.Context(), args[0])
			case "logs":
				data, err := manager.Logs(args[0], lines)
				if err != nil {
					return err
				}
				_, err = cmd.OutOrStdout().Write(data)
				return err
			}
			if err != nil {
				return err
			}
			encoder := json.NewEncoder(cmd.OutOrStdout())
			encoder.SetIndent("", "  ")
			return encoder.Encode(result)
		}
		if op == "logs" {
			cmd.Flags().IntVar(&lines, "lines", 100, "maximum log lines (1-10000)")
		}
		root.AddCommand(cmd)
	}
	return root
}
