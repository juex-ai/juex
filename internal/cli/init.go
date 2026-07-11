package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/providerreadiness"
)

type initOptions struct {
	scope     string
	provider  string
	model     string
	apiKey    string
	baseURL   string
	protocol  string
	skipCheck bool
	yes       bool
}

type initProviderSpec struct {
	ID       string
	Protocol string
	BaseURL  string
	APIKey   string
	Model    string
}

func newInitCmd(flags *persistentFlags) *cobra.Command {
	opts := &initOptions{}
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create a first-run juex.yaml runtime config",
		Long: `Create or update a Juex runtime config. By default this writes
~/.juex/juex.yaml so provider settings can be shared across workspaces.
Use --scope workspace to write the current workspace .juex/juex.yaml.`,
		Example: `  juex init
  juex init --scope workspace --provider openai --model gpt-4.1 --api-key "$OPENAI_API_KEY" --skip-check --yes
  juex init --provider openai-codex --model gpt-5.5 --skip-check --yes`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			workDir, err := initWorkDir(flags)
			if err != nil {
				return err
			}
			spec, err := resolveInitProviderSpec(cmd, opts)
			if err != nil {
				return err
			}
			target, err := initTargetPath(opts.scope, workDir)
			if err != nil {
				return err
			}
			if initConfigExists(target) && !opts.yes {
				ok, err := promptYesNo(bufio.NewReader(cmd.InOrStdin()), cmd.ErrOrStderr(), fmt.Sprintf("Config exists at %s; merge without overwriting? [y/N]: ", target))
				if err != nil {
					return err
				}
				if !ok {
					return &usageError{msg: "init: existing config not modified; pass --yes to merge without prompting"}
				}
			}
			result, err := mergeInitConfigFile(target, spec)
			if err != nil {
				return err
			}
			if err := validateInitConfig(target, workDir); err != nil {
				return err
			}
			cmdPrintln(cmd, fmt.Sprintf("Wrote %s", target))
			cmdPrintln(cmd, result)
			if !opts.skipCheck {
				if err := runInitHelloCheck(cmd.Context(), target, workDir); err != nil {
					return err
				}
				cmdPrintln(cmd, "Provider hello check passed.")
			}
			cmdPrintln(cmd, "Next:")
			cmdPrintln(cmd, `  juex run "say hello"`)
			cmdPrintln(cmd, "  juex repl")
			cmdPrintln(cmd, "  juex serve")
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.scope, "scope", "user", "config scope: user or workspace")
	cmd.Flags().StringVar(&opts.provider, "provider", "", "provider id: openai, openai-codex, anthropic, deepseek, or a custom id")
	cmd.Flags().StringVar(&opts.model, "model", "", "model id to select for the provider")
	cmd.Flags().StringVar(&opts.apiKey, "api-key", "", "provider API key; optional for openai-codex when Codex auth is cached")
	cmd.Flags().StringVar(&opts.baseURL, "base-url", "", "provider base URL; required for custom providers")
	cmd.Flags().StringVar(&opts.protocol, "protocol", "", "custom provider protocol: anthropic/messages, openai/responses, or openai/chat")
	cmd.Flags().BoolVar(&opts.skipCheck, "skip-check", false, "skip the provider hello connectivity check")
	cmd.Flags().BoolVarP(&opts.yes, "yes", "y", false, "accept prompts and merge existing config without asking")
	return cmd
}

func initWorkDir(flags *persistentFlags) (string, error) {
	workDir := ""
	if flags != nil {
		workDir = flags.cwd
	}
	if workDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		workDir = cwd
	}
	st, err := os.Stat(workDir)
	if err != nil || !st.IsDir() {
		return "", &notFoundError{msg: "--cwd is not a valid directory: " + workDir}
	}
	if abs, err := filepath.Abs(workDir); err == nil {
		workDir = abs
	}
	return workDir, nil
}

func initTargetPath(scope, workDir string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "", "user", "global", "user-global":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("init: resolve home directory: %w", err)
		}
		return filepath.Join(home, ".juex", "juex.yaml"), nil
	case "workspace", "project", "local":
		paths := (config.Config{WorkDir: workDir}).RuntimePaths()
		return paths.RuntimeConfigPath, nil
	default:
		return "", &usageError{msg: "--scope must be user or workspace"}
	}
}

func resolveInitProviderSpec(cmd *cobra.Command, opts *initOptions) (initProviderSpec, error) {
	reader := bufio.NewReader(cmd.InOrStdin())
	provider, err := promptIfEmpty(reader, cmd.ErrOrStderr(), strings.TrimSpace(opts.provider), "Provider [openai]: ", "openai")
	if err != nil {
		return initProviderSpec{}, err
	}
	provider = strings.TrimSpace(provider)
	model, err := promptIfEmpty(reader, cmd.ErrOrStderr(), strings.TrimSpace(opts.model), "Model id: ", defaultInitModel(provider))
	if err != nil {
		return initProviderSpec{}, err
	}
	model = strings.TrimSpace(model)
	if provider == "" || model == "" {
		return initProviderSpec{}, &usageError{msg: "init: --provider and --model are required for non-interactive use"}
	}

	protocol := strings.TrimSpace(opts.protocol)
	if isKnownInitProvider(provider) {
		if protocol != "" {
			return initProviderSpec{}, &usageError{msg: "init: known provider presets own their protocol; omit --protocol"}
		}
	} else {
		if protocol == "" {
			protocol, err = promptIfEmpty(reader, cmd.ErrOrStderr(), "", "Protocol [openai/chat]: ", string(llm.ProtocolOpenAIChat))
			if err != nil {
				return initProviderSpec{}, err
			}
		}
		if protocol == "" {
			return initProviderSpec{}, &usageError{msg: "init: custom providers require --protocol"}
		}
	}

	baseURL := strings.TrimSpace(opts.baseURL)
	if !isKnownInitProvider(provider) && baseURL == "" {
		baseURL, err = promptIfEmpty(reader, cmd.ErrOrStderr(), "", "Base URL: ", "")
		if err != nil {
			return initProviderSpec{}, err
		}
		if strings.TrimSpace(baseURL) == "" {
			return initProviderSpec{}, &usageError{msg: "init: custom providers require --base-url"}
		}
	}

	apiKey := strings.TrimSpace(opts.apiKey)
	if apiKey == "" && provider != "openai-codex" {
		apiKey, err = promptIfEmpty(reader, cmd.ErrOrStderr(), "", "API key: ", "")
		if err != nil {
			return initProviderSpec{}, err
		}
		if strings.TrimSpace(apiKey) == "" {
			return initProviderSpec{}, &usageError{msg: "init: --api-key is required for provider " + provider}
		}
	}

	return initProviderSpec{
		ID:       provider,
		Protocol: strings.TrimSpace(protocol),
		BaseURL:  strings.TrimSpace(baseURL),
		APIKey:   strings.TrimSpace(apiKey),
		Model:    model,
	}, nil
}

func promptIfEmpty(reader *bufio.Reader, stderr io.Writer, value, prompt, fallback string) (string, error) {
	if strings.TrimSpace(value) != "" {
		return value, nil
	}
	fmt.Fprint(stderr, prompt)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		if strings.TrimSpace(fallback) == "" {
			return "", err
		}
		return fallback, nil
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return fallback, nil
	}
	return line, nil
}

func promptYesNo(reader *bufio.Reader, stderr io.Writer, prompt string) (bool, error) {
	fmt.Fprint(stderr, prompt)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

func initConfigExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

func isKnownInitProvider(id string) bool {
	switch strings.TrimSpace(id) {
	case "anthropic", "openai", "openai-codex", "deepseek":
		return true
	default:
		return false
	}
}

func defaultInitModel(provider string) string {
	switch strings.TrimSpace(provider) {
	case "anthropic":
		return "claude-sonnet-4-5"
	case "openai-codex":
		return "gpt-5.5"
	case "deepseek":
		return "deepseek-chat"
	default:
		return "gpt-4.1"
	}
}

func mergeInitConfigFile(path string, spec initProviderSpec) (string, error) {
	doc, root, exists, err := loadInitYAML(path)
	if err != nil {
		return "", err
	}
	changed := false
	if scalarValue(mappingValue(root, "model")) == "" {
		setMappingScalar(root, "model", spec.ID+":"+spec.Model)
		changed = true
	}
	providers := ensureMappingSequence(root, "providers")
	if providers == nil {
		return "", fmt.Errorf("init: providers must be a YAML sequence in %s", path)
	}
	provider := findProviderNode(providers, spec.ID)
	if provider == nil {
		providers.Content = append(providers.Content, newProviderNode(spec))
		changed = true
	} else {
		if mergeMissingProviderFields(provider, spec) {
			changed = true
		}
		models := ensureMappingSequence(provider, "models")
		if models == nil {
			return "", fmt.Errorf("init: provider %q models must be a YAML sequence in %s", spec.ID, path)
		}
		if findModelNode(models, spec.Model) == nil {
			models.Content = append(models.Content, newModelNode(spec.Model))
			changed = true
		}
	}
	if !changed && exists {
		return "Config already contained the selected provider and model.", nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	enc := yaml.NewEncoder(f)
	enc.SetIndent(2)
	encodeErr := enc.Encode(doc)
	closeEncErr := enc.Close()
	closeErr := f.Close()
	if encodeErr != nil {
		return "", encodeErr
	}
	if closeEncErr != nil {
		return "", closeEncErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", err
	}
	if exists {
		return "Merged provider settings without overwriting existing provider fields.", nil
	}
	return "Created runtime config.", nil
}

func loadInitYAML(path string) (*yaml.Node, *yaml.Node, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			root := &yaml.Node{Kind: yaml.MappingNode}
			return &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}, root, false, nil
		}
		return nil, nil, false, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, nil, true, fmt.Errorf("init: parse %s: %w", path, err)
	}
	if len(doc.Content) == 0 {
		root := &yaml.Node{Kind: yaml.MappingNode}
		doc.Content = []*yaml.Node{root}
		return &doc, root, true, nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, nil, true, fmt.Errorf("init: %s must contain a YAML mapping", path)
	}
	return &doc, root, true, nil
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func setMappingScalar(node *yaml.Node, key, value string) {
	if existing := mappingValue(node, key); existing != nil {
		existing.Kind = yaml.ScalarNode
		existing.Tag = "!!str"
		existing.Value = value
		return
	}
	node.Content = append(node.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value},
	)
}

func ensureMappingSequence(node *yaml.Node, key string) *yaml.Node {
	if existing := mappingValue(node, key); existing != nil {
		if existing.Kind != yaml.SequenceNode {
			return nil
		}
		return existing
	}
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	node.Content = append(node.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		seq,
	)
	return seq
}

func findProviderNode(providers *yaml.Node, id string) *yaml.Node {
	if providers == nil || providers.Kind != yaml.SequenceNode {
		return nil
	}
	for _, item := range providers.Content {
		if scalarValue(mappingValue(item, "id")) == id {
			return item
		}
	}
	return nil
}

func findModelNode(models *yaml.Node, id string) *yaml.Node {
	if models == nil || models.Kind != yaml.SequenceNode {
		return nil
	}
	for _, item := range models.Content {
		if scalarValue(mappingValue(item, "id")) == id {
			return item
		}
	}
	return nil
}

func scalarValue(node *yaml.Node) string {
	if node == nil || node.Kind != yaml.ScalarNode {
		return ""
	}
	return strings.TrimSpace(node.Value)
}

func mergeMissingProviderFields(provider *yaml.Node, spec initProviderSpec) bool {
	changed := false
	if spec.Protocol != "" && scalarValue(mappingValue(provider, "protocol")) == "" {
		setMappingScalar(provider, "protocol", spec.Protocol)
		changed = true
	}
	if spec.BaseURL != "" && scalarValue(mappingValue(provider, "base_url")) == "" {
		setMappingScalar(provider, "base_url", spec.BaseURL)
		changed = true
	}
	if spec.APIKey != "" && scalarValue(mappingValue(provider, "api_key")) == "" {
		setMappingScalar(provider, "api_key", spec.APIKey)
		changed = true
	}
	return changed
}

func newProviderNode(spec initProviderSpec) *yaml.Node {
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	setMappingScalar(node, "id", spec.ID)
	if spec.Protocol != "" {
		setMappingScalar(node, "protocol", spec.Protocol)
	}
	if spec.BaseURL != "" {
		setMappingScalar(node, "base_url", spec.BaseURL)
	}
	if spec.APIKey != "" {
		setMappingScalar(node, "api_key", spec.APIKey)
	}
	models := ensureMappingSequence(node, "models")
	models.Content = append(models.Content, newModelNode(spec.Model))
	return node
}

func newModelNode(id string) *yaml.Node {
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	setMappingScalar(node, "id", id)
	return node
}

func validateInitConfig(path, workDir string) error {
	_, err := loadInitConfigForCheck(path, workDir)
	if err != nil {
		return fmt.Errorf("init: wrote %s but config validation failed: %w", path, err)
	}
	return nil
}

func initConfigCheckWorkDir(path, workDir string) (string, func()) {
	if initConfigTargetScope(path, workDir) == "workspace" {
		return workDir, func() {}
	}
	tmp, err := os.MkdirTemp("", "juex-init-validate-")
	if err != nil {
		return workDir, func() {}
	}
	return tmp, func() {
		_ = os.RemoveAll(tmp)
	}
}

func loadInitConfigForCheck(path, workDir string) (config.Config, error) {
	scope := initConfigTargetScope(path, workDir)
	checkWorkDir, cleanup := initConfigCheckWorkDir(path, workDir)
	defer cleanup()
	if scope == "workspace" || scope == "user" {
		return config.LoadForWorkDir(checkWorkDir)
	}
	return config.LoadFromFileForWorkDir(path, checkWorkDir)
}

func initConfigTargetScope(path, workDir string) string {
	workspaceConfig := (config.Config{WorkDir: workDir}).RuntimePaths().RuntimeConfigPath
	if cleanPath(path) == cleanPath(workspaceConfig) {
		return "workspace"
	}
	if home, err := os.UserHomeDir(); err == nil {
		userConfig := filepath.Join(home, ".juex", "juex.yaml")
		if cleanPath(path) == cleanPath(userConfig) {
			return "user"
		}
	}
	return "explicit"
}

func cleanPath(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return filepath.Clean(path)
}

func runInitHelloCheck(ctx context.Context, path, workDir string) error {
	cfg, err := loadInitConfigForCheck(path, workDir)
	if err != nil {
		return err
	}
	if err := ensureSelectedRuntimeConfig(cfg); err != nil {
		return err
	}
	result := providerreadiness.CheckConnectivity(ctx, cfg, providerreadiness.ConnectivityOptions{})
	if result.Status != providerreadiness.StatusOK {
		return initHelloCheckError(result)
	}
	return nil
}

func initHelloCheckError(result providerreadiness.Result) error {
	if result.Err != nil {
		if result.Suggestion != "" {
			return fmt.Errorf("init: provider hello check failed: %w; %s", result.Err, result.Suggestion)
		}
		return fmt.Errorf("init: provider hello check failed: %w", result.Err)
	}
	if result.Suggestion != "" {
		return fmt.Errorf("init: provider hello check failed: %s; %s", result.Message, result.Suggestion)
	}
	return fmt.Errorf("init: provider hello check failed: %s", result.Message)
}
