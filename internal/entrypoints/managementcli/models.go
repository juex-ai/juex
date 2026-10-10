package managementcli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/juex-ai/juex/internal/app/managed"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/management"
	"github.com/spf13/cobra"
)

func modelCommand(open func(*cobra.Command) (*managed.Management, error), out io.Writer) *cobra.Command {
	root := &cobra.Command{Use: "model", Short: "Manage deployment-provided model access"}
	var provider, name, endpoint, protocol, keyEnv, optionsFile, authentication string
	var contextWindow, maxOutput, outputReserve int
	put := &cobra.Command{Use: "put", Short: "Create or update a model from explicit operator configuration", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		reserve := outputReserve
		if !cmd.Flags().Changed("output-reserve") {
			reserve = maxOutput
		}
		if contextWindow < 1024 || maxOutput < 0 || reserve <= 0 || maxOutput > reserve || reserve >= contextWindow {
			return errors.New("model limits require 0 <= max-output <= output-reserve < context-window, with a positive reserve and context-window >= 1024")
		}
		if llm.Protocol(protocol) == llm.ProtocolAnthropicMessages && maxOutput == 0 && reserve < llm.AnthropicDefaultOutputTokens {
			return errors.New("anthropic/messages requires an output reserve of at least 4096 when max-output is zero")
		}
		options, err := readModelOptions(optionsFile)
		if err != nil {
			return err
		}
		if cmd.Flags().Changed("authentication") {
			options.Authentication = authentication
		}
		options = options.Normalized()
		var key string
		switch options.Authentication {
		case "api_key":
			key = os.Getenv(keyEnv)
		case "none":
			if cmd.Flags().Changed("api-key-env") {
				return errors.New("--api-key-env cannot be used with authentication none")
			}
		default:
			return errors.New("authentication must be api_key or none")
		}
		app, err := open(cmd)
		if err != nil {
			return err
		}
		defer app.Close()
		model, err := managed.ConfigureModel(cmd.Context(), app.Directory, management.ModelConfiguration{Provider: provider, Name: name, Endpoint: endpoint, Protocol: llm.Protocol(protocol), APIKey: key, ContextWindow: contextWindow, MaxOutput: maxOutput, OutputReserve: reserve, Enabled: true, Options: options})
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(model)
	}}
	put.Flags().StringVar(&provider, "provider", "", "Operator-defined provider name; Codex requires openai-codex")
	put.Flags().StringVar(&name, "name", "", "Provider model name")
	put.Flags().StringVar(&endpoint, "endpoint", "", "Provider API base URL")
	put.Flags().StringVar(&protocol, "protocol", "openai/chat", "openai/chat, openai/responses, openai-codex/responses, or anthropic/messages")
	put.Flags().StringVar(&keyEnv, "api-key-env", "JUEX_MODEL_API_KEY", "Environment variable holding the provider credential")
	put.Flags().StringVar(&authentication, "authentication", "", "api_key (default) or none for an explicitly unauthenticated provider")
	put.Flags().StringVar(&optionsFile, "options-file", "", "Private JSON file with thinking_effort, headers, query, capabilities and compat options")
	put.Flags().IntVar(&contextWindow, "context-window", 32768, "Model context window in tokens")
	put.Flags().IntVar(&maxOutput, "max-output", 4096, "Normal request output cap; zero uses the provider default and requires --output-reserve")
	put.Flags().IntVar(&outputReserve, "output-reserve", 0, "Positive context reservation for output; defaults to max-output when omitted")
	root.AddCommand(put)
	var inherit bool
	var allowed []string
	access := &cobra.Command{Use: "access <tenant-id>", Short: "Set tenant model access; an empty allow list denies all models", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if inherit == cmd.Flags().Changed("allow") {
			return errors.New("choose --inherit or --allow; --allow='' denies all models")
		}
		app, err := open(cmd)
		if err != nil {
			return err
		}
		defer app.Close()
		if err := app.Directory.SetTenantModels(cmd.Context(), args[0], inherit, allowed); err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(map[string]any{"tenant_id": args[0], "inherit": inherit, "allowed_model_ids": allowed})
	}}
	access.Flags().BoolVar(&inherit, "inherit", false, "Inherit the deployment model catalog")
	access.Flags().StringSliceVar(&allowed, "allow", nil, "Comma-separated permitted model IDs; empty denies all")
	root.AddCommand(access)
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

func readModelOptions(path string) (management.ModelOptions, error) {
	var options management.ModelOptions
	if path == "" {
		return options, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return options, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return options, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 64<<10 {
		return options, errors.New("model options must be a private regular JSON file of at most 64 KiB")
	}
	data, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return options, errors.New("model options must be readable and at most 64 KiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var decoded *management.ModelOptions
	if err := decoder.Decode(&decoded); err != nil || decoded == nil {
		return management.ModelOptions{}, errors.New("invalid model options JSON")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return management.ModelOptions{}, errors.New("model options must contain exactly one JSON object")
	}
	return *decoded, nil
}
