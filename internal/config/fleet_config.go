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

const (
	DefaultFleetAddr       = "127.0.0.1:5839"
	LegacyDefaultFleetAddr = "127.0.0.1:8080"
)

type FleetConfig struct {
	Addr           string
	AddrConfigured bool
}

type fleetFileConfig struct {
	Addr string `yaml:"addr"`
}

func LoadHomeFleetConfig() (FleetConfig, error) {
	cfg := FleetConfig{Addr: DefaultFleetAddr}
	home, err := EffectiveHomeDir()
	if err != nil {
		return cfg, err
	}
	path := filepath.Join(home, "juex.yaml")
	doc, root, _, err := readFleetConfigDocument(path)
	if err != nil {
		return cfg, err
	}
	_ = doc
	fleetNode := yamlMappingValue(root, "fleet")
	if fleetNode == nil || fleetNode.Tag == "!!null" {
		return cfg, nil
	}
	if fleetNode.Kind != yaml.MappingNode {
		return cfg, fmt.Errorf("config: parse %s: fleet must be a mapping", path)
	}
	seen := map[string]struct{}{}
	for i := 0; i+1 < len(fleetNode.Content); i += 2 {
		key := strings.TrimSpace(fleetNode.Content[i].Value)
		if _, duplicate := seen[key]; duplicate {
			return cfg, fmt.Errorf("config: parse %s: duplicate fleet.%s", path, key)
		}
		seen[key] = struct{}{}
		if key != "addr" {
			return cfg, fmt.Errorf("config: parse %s: field fleet.%s not found", path, key)
		}
		value := fleetNode.Content[i+1]
		if value.Kind != yaml.ScalarNode || value.Tag == "!!null" {
			return cfg, fmt.Errorf("config: parse %s: fleet.addr must be a host:port string", path)
		}
		cfg.Addr = strings.TrimSpace(value.Value)
		cfg.AddrConfigured = cfg.Addr != ""
	}
	if cfg.Addr == "" {
		cfg.Addr = DefaultFleetAddr
	}
	if err := ValidateStableFleetAddr(cfg.Addr); err != nil {
		return cfg, fmt.Errorf("config: parse %s: fleet.addr: %w", path, err)
	}
	return cfg, nil
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

func SetHomeFleetAddr(addr string) (string, error) {
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
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, nil, true, fmt.Errorf("config: parse %s: %w", path, err)
	}
	if len(doc.Content) == 0 {
		root := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		doc.Kind = yaml.DocumentNode
		doc.Content = []*yaml.Node{root}
		return &doc, root, true, nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, nil, true, fmt.Errorf("config: parse %s: top level must be a mapping", path)
	}
	return &doc, root, true, nil
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
