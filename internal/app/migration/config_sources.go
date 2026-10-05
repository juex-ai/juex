package migration

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/migration/legacy"
	"gopkg.in/yaml.v3"
)

type configResolver struct {
	evidence ConfigEvidence
	files    []legacy.SourceFile
	context  string
}

func configDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func verifyConfigFile(file legacy.SourceFile) error {
	if int64(len(file.Data)) != file.Size || configDigest(file.Data) != file.SHA256 {
		return errors.New("captured configuration size/hash mismatch")
	}
	return nil
}

func capturedConfigFile(name string, files []legacy.SourceFile, absent []string) (*legacy.SourceFile, error) {
	var found *legacy.SourceFile
	count := 0
	for i := range files {
		if files[i].Path == name {
			count++
			found = &files[i]
		}
	}
	for _, path := range absent {
		if path == name {
			count++
		}
	}
	if count != 1 {
		return nil, errors.New("configuration presence is missing or ambiguous")
	}
	if found != nil {
		if err := verifyConfigFile(*found); err != nil {
			return nil, err
		}
	}
	return found, nil
}

func (r configResolver) identity(p string) (string, error) {
	identity, ok := r.evidence.Identities[p]
	if !ok || !absoluteConfigPath(p) || !absoluteConfigPath(identity) {
		return "", errors.New("captured canonical configuration identity is required")
	}
	return identity, nil
}

func (r configResolver) apply(s *configSettings, layer configLayer, replay bool) error {
	if layer.file == nil {
		return nil
	}
	if err := verifyConfigFile(*layer.file); err != nil {
		return err
	}
	value, _, err := decodeConfig(layer.file.Data)
	if err != nil {
		return err
	}
	for _, item := range value.Imports {
		data, provenance, err := r.imported(layer, strings.TrimSpace(item.Source))
		if err != nil {
			return err
		}
		imported, nested, err := decodeConfig(data)
		if err != nil {
			return err
		}
		if nested {
			return errors.New("nested configuration imports are unsupported")
		}
		if err := s.apply(imported, layer.scope, replay); err != nil {
			return err
		}
		if !replay {
			s.value.Sources = append(s.value.Sources, provenance)
		}
	}
	if err := s.apply(value, layer.scope, replay); err != nil {
		return err
	}
	if !replay {
		s.value.Sources = append(s.value.Sources, ConfigSource{Kind: layer.scope, IdentitySHA256: configDigest([]byte(layer.identity)), SHA256: layer.file.SHA256})
	}
	return nil
}

type configImportCache struct {
	Version         int       `json:"version"`
	Source          string    `json:"source"`
	SourceSHA256    string    `json:"source_sha256"`
	DeclaringSHA256 string    `json:"declaring_sha256"`
	ContextSHA256   string    `json:"context_sha256"`
	ETag            string    `json:"etag"`
	LastModified    string    `json:"last_modified"`
	FetchedAt       time.Time `json:"fetched_at"`
	ContentSHA256   string    `json:"content_sha256"`
	Content         string    `json:"content"`
}

func (r configResolver) imported(layer configLayer, source string) ([]byte, ConfigSource, error) {
	if source == "" {
		return nil, ConfigSource{}, errors.New("configuration import source is empty")
	}
	if filepath.IsAbs(source) || !strings.Contains(source, "://") {
		if !filepath.IsAbs(source) {
			source = filepath.Join(filepath.Dir(layer.path), source)
		}
		source = filepath.Clean(source)
		identity, err := r.identity(source)
		if err != nil {
			return nil, ConfigSource{}, err
		}
		file, err := capturedConfigFile(source, r.evidence.LocalImports, nil)
		if err != nil {
			return nil, ConfigSource{}, err
		}
		return file.Data, ConfigSource{Kind: "local-import", IdentitySHA256: configDigest([]byte(identity)), SHA256: file.SHA256}, nil
	}
	u, err := url.Parse(source)
	if err != nil || (!strings.EqualFold(u.Scheme, "https") && !strings.EqualFold(u.Scheme, "http")) || u.Host == "" || u.User != nil || u.Fragment != "" {
		return nil, ConfigSource{}, errors.New("configuration import URL is invalid")
	}
	sourceSHA, declaringSHA := configDigest([]byte(u.String())), configDigest([]byte(layer.identity))
	name := "cache/config-imports/" + sourceSHA + "-" + declaringSHA + "-" + r.context + ".json"
	file, err := capturedConfigFile(name, r.files, nil)
	if err != nil {
		return nil, ConfigSource{}, errors.New("exact configuration import cache is missing or invalid")
	}
	var cache configImportCache
	decoder := json.NewDecoder(bytes.NewReader(file.Data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cache); err != nil {
		return nil, ConfigSource{}, errors.New("configuration import cache cannot be decoded")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return nil, ConfigSource{}, errors.New("configuration import cache has trailing data")
	}
	u.RawQuery, u.ForceQuery = "", false
	if cache.Version != 3 || cache.Source != u.String() || cache.SourceSHA256 != sourceSHA || cache.DeclaringSHA256 != declaringSHA || cache.ContextSHA256 != r.context || cache.FetchedAt.IsZero() || len(cache.Content) > 1<<20 || cache.ContentSHA256 != "sha256:"+configDigest([]byte(cache.Content)) {
		return nil, ConfigSource{}, errors.New("configuration import cache identity/content mismatch")
	}
	return []byte(cache.Content), ConfigSource{Kind: "remote-import", IdentitySHA256: sourceSHA, SHA256: strings.TrimPrefix(cache.ContentSHA256, "sha256:"), FetchedAt: cache.FetchedAt}, nil
}

func decodeConfig(data []byte) (configDocument, bool, error) {
	var node yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&node); err != nil {
		return configDocument{}, false, errors.New("configuration YAML cannot be decoded")
	}
	var trailing yaml.Node
	if decoder.Decode(&trailing) != io.EOF {
		return configDocument{}, false, errors.New("configuration YAML must contain one document")
	}
	if err := plainConfigYAML(&node); err != nil {
		return configDocument{}, false, err
	}
	var value configDocument
	decoder = yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&value); err != nil {
		return configDocument{}, false, errors.New("configuration YAML has an unknown field or invalid value")
	}
	nested := false
	if len(node.Content) == 1 && node.Content[0].Kind == yaml.MappingNode {
		for i := 0; i < len(node.Content[0].Content); i += 2 {
			if node.Content[0].Content[i].Value == "imports" {
				nested = true
			}
		}
	}
	return value, nested, nil
}

func plainConfigYAML(node *yaml.Node) error {
	// Aliases/merges require their own source proof; rejecting them also keeps
	// nested imports from hiding behind an inherited mapping key.
	if node.Kind == yaml.AliasNode || node.Anchor != "" || node.Tag == "!!merge" {
		return errors.New("configuration YAML aliases and merges require explicit conversion")
	}
	for _, child := range node.Content {
		if err := plainConfigYAML(child); err != nil {
			return err
		}
	}
	return nil
}
