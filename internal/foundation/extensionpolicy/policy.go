// Package extensionpolicy defines portable resource snapshots without reading
// directories or running extension code in platform services.
package extensionpolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/hookpolicy"
)

const MaxCatalogBytes = 1 << 20

var ErrInvalid = errors.New("invalid extension resource declaration")
var variable = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`)

type SkillResource struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Path        string `json:"path"`
}

type SkillContent struct {
	ID      string `json:"id"`
	Content string `json:"content"`
}

type CommandResource struct {
	ID          string            `json:"id"`
	Description string            `json:"description,omitempty"`
	Command     []string          `json:"command"`
	Environment map[string]string `json:"environment,omitempty"`
}

type ObservableResource struct {
	CommandResource
	Options execprotocol.ObservableOptions `json:"options"`
}

type MCPResource struct {
	CommandResource
	execprotocol.MCPRemote
}

func (m MCPResource) Validate() error {
	if m.MCPRemote.Validate() != nil {
		return ErrInvalid
	}
	if m.Kind() == "stdio" {
		return m.CommandResource.Validate()
	}
	if !identifier.MatchString(m.ID) || len(m.Description) > 4096 || len(m.Command) != 0 || len(m.Environment) != 0 {
		return ErrInvalid
	}
	return nil
}

type Manifest struct {
	ManifestVersion int                      `json:"manifest_version"`
	Name            string                   `json:"name"`
	Version         string                   `json:"version"`
	Description     string                   `json:"description,omitempty"`
	Environment     map[string]string        `json:"environment,omitempty"`
	Skills          []SkillResource          `json:"skills"`
	Hooks           []hookpolicy.Declaration `json:"hooks"`
	MCP             []MCPResource            `json:"mcp"`
	Observables     []ObservableResource     `json:"observables"`
}

type Catalog struct {
	Manifest   Manifest        `json:"manifest"`
	Skills     []SkillContent  `json:"skills"`
	Revision   string          `json:"revision"`
	SourceKind string          `json:"source_kind,omitempty"`
	Skipped    []SkippedSource `json:"skipped,omitempty"`
}

type SkippedSource struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
	Target string `json:"target,omitempty"`
}

type Binding struct {
	ID                   string   `json:"id"`
	Enabled              bool     `json:"enabled"`
	EnvironmentID        string   `json:"environment_id"`
	AuthorizationVersion int64    `json:"authorization_version"`
	Directory            string   `json:"directory"`
	InspectionID         string   `json:"inspection_id"`
	Resources            []string `json:"resources"`
	Catalog              Catalog  `json:"catalog"`
}

func (b Binding) Selected(kind, id string) bool {
	return b.Enabled && slices.Contains(b.Resources, kind+"/"+id)
}
func (b Binding) Context() *execprotocol.ExtensionContext {
	return &execprotocol.ExtensionContext{BindingID: b.ID, Directory: b.Directory}
}
func (c Catalog) Digest() string {
	c.Revision = ""
	encoded, _ := json.Marshal(c)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func (m Manifest) Validate() error {
	return m.validate(32)
}

func (m Manifest) validate(skillLimit int) error {
	if m.ManifestVersion != 2 || !identifier.MatchString(m.Name) || len(m.Version) == 0 || len(m.Version) > 64 || len(m.Description) > 4096 || len(m.Skills) > skillLimit || len(m.Hooks) > 16 || len(m.MCP) > 16 || len(m.Observables) > 16 || ValidateEnvironment(m.Environment) != nil {
		return ErrInvalid
	}
	if hookpolicy.Validate(m.Hooks) != nil {
		return ErrInvalid
	}
	keys := map[string]bool{}
	add := func(kind, id string) bool {
		key := kind + "/" + id
		if !identifier.MatchString(id) || keys[key] {
			return false
		}
		keys[key] = true
		return true
	}
	for _, skill := range m.Skills {
		if !add("skill", skill.ID) || len(skill.Description) > 4096 || !fs.ValidPath(skill.Path) || skill.Path == "." || strings.Contains(skill.Path, `\`) {
			return ErrInvalid
		}
	}
	for _, hook := range m.Hooks {
		if !add("hook", hook.ID) || hook.EnvironmentID != "" || hook.WorkingDirectory != "" || hook.Source != "" || hook.Extension != nil || hook.AuthorizationVersion != 0 || ValidateEnvironment(hook.Environment) != nil {
			return ErrInvalid
		}
		if ValidateEnvironment((Binding{Catalog: Catalog{Manifest: m}}).Environment(hook.Environment)) != nil {
			return ErrInvalid
		}
	}
	for _, command := range m.MCP {
		if !add("mcp", command.ID) || command.Validate() != nil {
			return ErrInvalid
		}
		if ValidateEnvironment((Binding{Catalog: Catalog{Manifest: m}}).Environment(command.Environment)) != nil {
			return ErrInvalid
		}
	}
	for _, observable := range m.Observables {
		if !add("observable", observable.ID) || observable.Validate() != nil || observable.Options.Validate() != nil || ValidateEnvironment((Binding{Catalog: Catalog{Manifest: m}}).Environment(observable.Environment)) != nil {
			return ErrInvalid
		}
	}
	return nil
}
func (c CommandResource) Validate() error {
	if !identifier.MatchString(c.ID) || len(c.Description) > 4096 || ValidateEnvironment(c.Environment) != nil {
		return ErrInvalid
	}
	return (execprotocol.HookCommand{Command: c.Command, Input: json.RawMessage(`{}`), TimeoutMS: 1, MaxOutputBytes: 1}).Validate()
}
func ValidateEnvironment(values map[string]string) error {
	if len(values) > 64 {
		return ErrInvalid
	}
	for k, v := range values {
		if !variable.MatchString(k) || len(v) > 4096 || strings.ContainsRune(v, 0) || strings.HasPrefix(k, "JUEX_") {
			return ErrInvalid
		}
	}
	return nil
}
func (c Catalog) Validate() error {
	if len(c.Skipped) > 256 || c.SourceKind != "skills" && len(c.Skipped) > 0 {
		return ErrInvalid
	}
	for _, item := range c.Skipped {
		if !fs.ValidPath(item.Path) || len(item.Reason) > 256 || len(item.Target) > 4096 || strings.ContainsRune(item.Target, 0) {
			return ErrInvalid
		}
	}
	if c.SourceKind != "" && c.SourceKind != "skills" {
		return ErrInvalid
	}
	if c.SourceKind == "skills" && (len(c.Manifest.Skills) == 0 && len(c.Skipped) == 0 || len(c.Manifest.Environment) != 0 || len(c.Manifest.Hooks) != 0 || len(c.Manifest.MCP) != 0 || len(c.Manifest.Observables) != 0) {
		return ErrInvalid
	}
	skillLimit, byteLimit := 32, 256<<10
	if c.SourceKind == "skills" {
		skillLimit, byteLimit = 256, MaxCatalogBytes
	}
	if c.Manifest.validate(skillLimit) != nil || len(c.Skills) != len(c.Manifest.Skills) || c.Revision != c.Digest() {
		return ErrInvalid
	}
	for i, skill := range c.Skills {
		if skill.ID != c.Manifest.Skills[i].ID || len(skill.Content) > 64<<10 {
			return ErrInvalid
		}
	}
	encoded, err := json.Marshal(c)
	if err != nil || len(encoded) > byteLimit {
		return ErrInvalid
	}
	return nil
}
func Validate(bindings []Binding) error {
	if len(bindings) > 32 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, b := range bindings {
		if !validUUID(b.ID) || seen[b.ID] || !validUUID(b.EnvironmentID) || b.AuthorizationVersion < 1 || b.Directory == "" || !path.IsAbs(b.Directory) || len(b.Directory) > 4096 || strings.ContainsRune(b.Directory, 0) || len(b.InspectionID) == 0 || len(b.InspectionID) > 128 || b.Catalog.Validate() != nil {
			return ErrInvalid
		}
		seen[b.ID] = true
		allowed := map[string]bool{}
		for _, s := range b.Catalog.Manifest.Skills {
			allowed["skill/"+s.ID] = true
		}
		for _, h := range b.Catalog.Manifest.Hooks {
			allowed["hook/"+h.ID] = true
		}
		for _, c := range b.Catalog.Manifest.MCP {
			allowed["mcp/"+c.ID] = true
		}
		for _, c := range b.Catalog.Manifest.Observables {
			allowed["observable/"+c.ID] = true
		}
		selected := map[string]bool{}
		for _, key := range b.Resources {
			if !allowed[key] || selected[key] {
				return ErrInvalid
			}
			selected[key] = true
		}
	}
	encoded, err := json.Marshal(bindings)
	if err != nil || len(encoded) > 4<<20 || hookpolicy.Validate(Hooks(bindings)) != nil {
		return ErrInvalid
	}
	return nil
}
func validUUID(v string) bool {
	id, err := uuid.Parse(v)
	return err == nil && id != uuid.Nil && id.String() == v
}
func Revokes(before, after []Binding) bool {
	if hookpolicy.Revokes(Hooks(before), Hooks(after)) {
		return true
	}
	for _, old := range before {
		if !old.Enabled {
			continue
		}
		next := slices.IndexFunc(after, func(b Binding) bool { return b.ID == old.ID && b.Enabled })
		if next < 0 {
			return true
		}
		for _, resource := range old.Resources {
			if !slices.Contains(after[next].Resources, resource) {
				return true
			}
		}
	}
	return false
}

func (b Binding) Environment(extra map[string]string) map[string]string {
	values := map[string]string{}
	for k, v := range b.Catalog.Manifest.Environment {
		values[k] = v
	}
	for k, v := range extra {
		values[k] = v
	}
	return values
}

func Hooks(bindings []Binding) []hookpolicy.Declaration {
	result := []hookpolicy.Declaration{}
	for _, b := range bindings {
		for _, h := range b.Catalog.Manifest.Hooks {
			if !b.Selected("hook", h.ID) || !h.Enabled {
				continue
			}
			h.ID = uuid.NewSHA1(uuid.NameSpaceOID, []byte(b.ID+"/hook/"+h.ID)).String()
			h.Source = "extension:" + b.ID
			h.EnvironmentID, h.AuthorizationVersion = b.EnvironmentID, b.AuthorizationVersion
			h.Extension = b.Context()
			h.Environment = b.Environment(h.Environment)
			h.WorkingDirectory = b.Directory
			result = append(result, h)
		}
	}
	return result
}
