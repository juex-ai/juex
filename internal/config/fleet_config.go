package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const DefaultFleetAddr = "127.0.0.1:5839"

type FleetConfig struct {
	Addr           string
	AddrConfigured bool
	UnsafeBindAny  bool
}

type fleetFileConfig struct {
	Addr          string       `yaml:"addr"`
	UnsafeBindAny optionalBool `yaml:"unsafe_bind_any"`
}

func LoadHomeFleetConfig() (FleetConfig, error) {
	cfg := FleetConfig{Addr: DefaultFleetAddr}
	resolution, err := resolveHomeConfigSources()
	if err != nil {
		return cfg, err
	}
	loader := newConfigImportLoader(resolution.EffectiveHomeDir)
	for _, source := range resolution.Sources {
		if err := applyHomeFleetConfig(&cfg, source, loader); err != nil {
			return cfg, err
		}
	}
	return cfg, nil
}

func applyHomeFleetConfig(cfg *FleetConfig, source yamlConfigSource, loader *configImportLoader) error {
	_, root, _, err := readFleetConfigDocument(source.Path)
	if err != nil {
		return err
	}
	imports, err := fleetImports(root, source.Path)
	if err != nil {
		return err
	}
	documents := make([]configImportDocument, 0, len(imports))
	for i, item := range imports {
		document, loadErr := loader.load(source, item.Source)
		if loadErr != nil {
			return fmt.Errorf("config: %s imports[%d] %s: %w", source.Path, i, safeImportSource(item.Source), loadErr)
		}
		_, importedRoot, parseErr := parseFleetConfigDocument(document.data, document.source.Path)
		if parseErr != nil {
			return fmt.Errorf("config: %s imports[%d] %s: %w", source.Path, i, document.source.Path, parseErr)
		}
		if yamlMappingKeyPresent(importedRoot, "imports", map[*yaml.Node]bool{}) {
			return fmt.Errorf("config: %s imports[%d] %s: nested imports are not supported", source.Path, i, document.source.Path)
		}
		if _, parseErr := decodeFileConfig(document.data, document.source.Path); parseErr != nil {
			return fmt.Errorf("config: %s imports[%d] %s: %w", source.Path, i, document.source.Path, parseErr)
		}
		validation := Config{providerConfigs: map[string]providerConfig{}}
		if parseErr := applyYAMLData(&validation, document.data, document.source); parseErr != nil {
			return fmt.Errorf("config: %s imports[%d] %s: %w", source.Path, i, document.source.Path, parseErr)
		}
		documents = append(documents, document)
	}

	staged := *cfg
	for i, document := range documents {
		_, importedRoot, parseErr := parseFleetConfigDocument(document.data, document.source.Path)
		if parseErr != nil {
			return fmt.Errorf("config: %s imports[%d] %s: %w", source.Path, i, document.source.Path, parseErr)
		}
		if err := applyFleetConfigNode(&staged, importedRoot, document.source.Path); err != nil {
			return fmt.Errorf("config: %s imports[%d] %s: %w", source.Path, i, document.source.Path, err)
		}
	}
	if err := applyFleetConfigNode(&staged, root, source.Path); err != nil {
		return err
	}
	// Fleet-only loading may consume an existing runtime LKG, but it cannot
	// safely publish fresh content without the runtime's full semantic checks.
	*cfg = staged
	return nil
}

func applyFleetConfigNode(cfg *FleetConfig, root *yaml.Node, path string) error {
	fleetNode := yamlMappingValue(root, "fleet")
	if fleetNode == nil || fleetNode.Tag == "!!null" {
		return nil
	}
	if fleetNode.Kind != yaml.MappingNode {
		return fmt.Errorf("config: parse %s: fleet must be a mapping", path)
	}
	seen := map[string]struct{}{}
	for i := 0; i+1 < len(fleetNode.Content); i += 2 {
		key := strings.TrimSpace(fleetNode.Content[i].Value)
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("config: parse %s: duplicate fleet.%s", path, key)
		}
		seen[key] = struct{}{}
		value := fleetNode.Content[i+1]
		switch key {
		case "addr":
			if value.Kind != yaml.ScalarNode || value.Tag == "!!null" {
				return fmt.Errorf("config: parse %s: fleet.addr must be a host:port string", path)
			}
			addr := strings.TrimSpace(value.Value)
			if addr == "" {
				continue
			}
			if err := ValidateStableFleetAddr(addr); err != nil {
				return fmt.Errorf("config: parse %s: fleet.addr: %w", path, err)
			}
			cfg.Addr = addr
			cfg.AddrConfigured = true
		case "unsafe_bind_any":
			if value.Kind != yaml.ScalarNode || value.Tag == "!!null" {
				return fmt.Errorf("config: parse %s: fleet.unsafe_bind_any must be a boolean", path)
			}
			if err := value.Decode(&cfg.UnsafeBindAny); err != nil {
				return fmt.Errorf("config: parse %s: fleet.unsafe_bind_any must be a boolean", path)
			}
		default:
			return fmt.Errorf("config: parse %s: field fleet.%s not found", path, key)
		}
	}
	return nil
}

func fleetImports(root *yaml.Node, path string) ([]importConfig, error) {
	node := yamlMappingValue(root, "imports")
	if node == nil || node.Tag == "!!null" {
		return nil, nil
	}
	if node.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("config: parse %s: imports must be a sequence", path)
	}
	imports := make([]importConfig, 0, len(node.Content))
	for i, item := range node.Content {
		if item.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("config: parse %s: imports[%d] must be a mapping", path, i)
		}
		var source string
		seenSource := false
		for j := 0; j+1 < len(item.Content); j += 2 {
			key := strings.TrimSpace(item.Content[j].Value)
			if key != "source" {
				return nil, fmt.Errorf("config: parse %s: field imports[%d].%s not found", path, i, key)
			}
			if seenSource {
				return nil, fmt.Errorf("config: parse %s: duplicate imports[%d].source", path, i)
			}
			seenSource = true
			value := item.Content[j+1]
			if value.Kind != yaml.ScalarNode || value.Tag == "!!null" {
				return nil, fmt.Errorf("config: parse %s: imports[%d].source must be a string", path, i)
			}
			source = strings.TrimSpace(value.Value)
		}
		if source == "" {
			return nil, fmt.Errorf("config: parse %s: imports[%d].source is required", path, i)
		}
		imports = append(imports, importConfig{Source: source})
	}
	return imports, nil
}

func ValidateStableFleetAddr(addr string) error {
	host, portText, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return fmt.Errorf("must be a host:port TCP address (got %q)", addr)
	}
	if strings.TrimSpace(host) == "" {
		return fmt.Errorf("host is required")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	return nil
}

func SetHomeFleetSettings(addr string, unsafeBindAny bool) (string, error) {
	addr = strings.TrimSpace(addr)
	if err := ValidateStableFleetAddr(addr); err != nil {
		return "", fmt.Errorf("config: fleet.addr: %w", err)
	}
	home, err := EffectiveHomeDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(home, "juex.yaml")
	doc, root, _, err := readFleetConfigDocument(path)
	if err != nil {
		return "", err
	}
	fleetNode := yamlMappingValue(root, "fleet")
	if fleetNode == nil {
		fleetNode = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		root.Content = append(root.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "fleet"},
			fleetNode,
		)
	} else if fleetNode.Kind != yaml.MappingNode {
		return "", fmt.Errorf("config: parse %s: fleet must be a mapping", path)
	}
	setYAMLMappingScalar(fleetNode, "addr", addr)
	setYAMLMappingBool(fleetNode, "unsafe_bind_any", unsafeBindAny)
	if err := writeFleetConfigDocument(path, doc); err != nil {
		return "", err
	}
	return path, nil
}

func readFleetConfigDocument(path string) (*yaml.Node, *yaml.Node, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		root := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		return &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}, root, false, nil
	}
	if err != nil {
		return nil, nil, false, fmt.Errorf("config: read %s: %w", path, err)
	}
	doc, root, err := parseFleetConfigDocument(data, path)
	return doc, root, true, err
}

func parseFleetConfigDocument(data []byte, path string) (*yaml.Node, *yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	if len(doc.Content) == 0 {
		root := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		doc.Kind = yaml.DocumentNode
		doc.Content = []*yaml.Node{root}
		return &doc, root, nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, nil, fmt.Errorf("config: parse %s: top level must be a mapping", path)
	}
	return &doc, root, nil
}

func yamlMappingValue(node *yaml.Node, key string) *yaml.Node {
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

func setYAMLMappingScalar(node *yaml.Node, key, value string) {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			valueNode := node.Content[i+1]
			valueNode.Kind = yaml.ScalarNode
			valueNode.Tag = "!!str"
			valueNode.Value = value
			valueNode.Content = nil
			valueNode.Alias = nil
			return
		}
	}
	node.Content = append(node.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value},
	)
}

func setYAMLMappingBool(node *yaml.Node, key string, value bool) {
	text := strconv.FormatBool(value)
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			valueNode := node.Content[i+1]
			valueNode.Kind = yaml.ScalarNode
			valueNode.Tag = "!!bool"
			valueNode.Value = text
			valueNode.Content = nil
			valueNode.Alias = nil
			return
		}
	}
	node.Content = append(node.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: text},
	)
}

func writeFleetConfigDocument(path string, doc *yaml.Node) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("config: create home config directory: %w", err)
	}
	temp, err := os.CreateTemp(dir, ".juex-fleet-config-*.tmp")
	if err != nil {
		return fmt.Errorf("config: create home config temp file: %w", err)
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	encoder := yaml.NewEncoder(temp)
	encoder.SetIndent(2)
	if err := encoder.Encode(doc); err != nil {
		_ = temp.Close()
		return fmt.Errorf("config: encode home config: %w", err)
	}
	if err := encoder.Close(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("config: close home config encoder: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("config: sync home config: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("config: close home config: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("config: replace home config %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("config: chmod home config %s: %w", path, err)
	}
	return nil
}
