package managementcli

import (
	"encoding/json"
	"io"
	"os"

	"github.com/juex-ai/juex/internal/app/managed"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/management"
	"github.com/spf13/cobra"
)

func modelCommand(open func(*cobra.Command) (*managed.Management, error), out io.Writer) *cobra.Command {
	root := &cobra.Command{Use: "model", Short: "Manage deployment-provided model access"}
	var provider, name, endpoint, protocol, keyEnv string
	var contextWindow, maxOutput int
	put := &cobra.Command{Use: "put", Short: "Create or update a model; read its credential from an environment variable", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		app, err := open(cmd)
		if err != nil {
			return err
		}
		defer app.Close()
		model, err := app.Directory.ConfigureModel(cmd.Context(), management.ModelConfiguration{Provider: provider, Name: name, Endpoint: endpoint, Protocol: llm.Protocol(protocol), APIKey: os.Getenv(keyEnv), ContextWindow: contextWindow, MaxOutput: maxOutput, Enabled: true})
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(model)
	}}
	put.Flags().StringVar(&provider, "provider", "", "Operator-defined provider name")
	put.Flags().StringVar(&name, "name", "", "Provider model name")
	put.Flags().StringVar(&endpoint, "endpoint", "", "Provider API base URL")
	put.Flags().StringVar(&protocol, "protocol", "openai/chat", "openai/chat, openai/responses, or anthropic/messages")
	put.Flags().StringVar(&keyEnv, "api-key-env", "JUEX_MODEL_API_KEY", "Environment variable holding the provider credential")
	put.Flags().IntVar(&contextWindow, "context-window", 32768, "Model context window in tokens")
	put.Flags().IntVar(&maxOutput, "max-output", 4096, "Maximum output tokens per request")
	root.AddCommand(put)
	for _, enabled := range []bool{true, false} {
		use := "disable"
		if enabled {
			use = "enable"
		}
		root.AddCommand(&cobra.Command{Use: use + " <model-id>", Short: use + " a model for new provider calls", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			app, err := open(cmd)
			if err != nil {
				return err
			}
			defer app.Close()
			if err := app.Directory.SetModelEnabled(cmd.Context(), args[0], enabled); err != nil {
				return err
			}
			return json.NewEncoder(out).Encode(map[string]bool{"enabled": enabled})
		}})
	}
	return root
}
