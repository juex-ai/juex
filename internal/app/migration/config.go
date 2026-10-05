package migration

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/migration/legacy"
	"github.com/juex-ai/juex/internal/providers/profile"
)

// ConfigEvidence identifies the captured source load, not this process's HOME,
// cwd or environment. Identities use 281889e5's declaringConfigIdentity rule:
// canonical existing parents plus the original basename. LocalImports and
// ExplicitFiles contain captured bytes, never paths to read during conversion.
type ConfigEvidence struct {
	Contexts     map[string]ConfigContext
	Identities   map[string]string
	LocalImports []legacy.SourceFile
}

type ConfigContext struct {
	WorkingDirectory string
	ExplicitFiles    []legacy.SourceFile
	ModelRefs        []string
}

// ResolvedConfig is disk/startup-file configuration only. It cannot authorize
// cutover: inherited environment, authentication, resources and target bindings
// must still be verified. Private provider values are deliberately not JSON.
type ResolvedConfig struct {
	AgentID                   string          `json:"agent_id"`
	StartupModelOverride      bool            `json:"startup_model_override"`
	Models                    []ConfigModel   `json:"models"`
	Preset                    string          `json:"preset"`
	Modules                   map[string]bool `json:"modules"`
	MemoryProfile             string          `json:"memory_profile"`
	WorkerDepth               int             `json:"worker_depth"`
	UserResources             bool            `json:"user_resources"`
	ExtensionPolicyConfigured bool            `json:"extension_policy_configured"`
	ExtensionAllow            []string        `json:"extension_allow"`
	Sources                   []ConfigSource  `json:"sources"`
}

type ConfigModel struct {
	Ref           string         `json:"ref"`
	ContextWindow int            `json:"context_window"`
	Configuration profile.Config `json:"-"`
}

// Sources prove captured identity/content and historical cache time, not cache
// freshness or what a long-running source Agent previously loaded.
type ConfigSource struct {
	Kind           string    `json:"kind"`
	IdentitySHA256 string    `json:"identity_sha256"`
	SHA256         string    `json:"sha256"`
	FetchedAt      time.Time `json:"fetched_at,omitempty"`
}

type configLayer struct {
	path, identity, scope string
	file                  *legacy.SourceFile
}

// ResolveConfig uses the fixed source merge rules with no file/network access,
// cache recovery, environment lookup or live provider normalization. Unsupported
// fields fail explicitly rather than silently disappearing from the migration.
func ResolveConfig(source legacy.Fleet, evidence ConfigEvidence) ([]ResolvedConfig, error) {
	if !absoluteConfigPath(source.SourceHome) || !absoluteConfigPath(source.DefaultHome.Directory) {
		return nil, errors.New("configuration source Homes must be absolute")
	}
	result := make([]ResolvedConfig, 0, len(source.Agents))
	seen := map[string]bool{}
	for _, agent := range source.Agents {
		id := agent.Definition.ID
		if id == "" || seen[id] {
			return nil, errors.New("configuration Agent identity is missing or repeated")
		}
		seen[id] = true
		context, ok := evidence.Contexts[id]
		if !ok {
			return nil, fmt.Errorf("source Agent %s: startup configuration context is required", id)
		}
		value, err := resolveAgentConfig(source, agent, evidence, context)
		if err != nil {
			return nil, fmt.Errorf("source Agent %s configuration: %w", id, err)
		}
		result = append(result, value)
	}
	for id := range evidence.Contexts {
		if !seen[id] {
			return nil, errors.New("configuration context names an unknown Agent")
		}
	}
	slices.SortFunc(result, func(a, b ResolvedConfig) int { return strings.Compare(a.AgentID, b.AgentID) })
	return result, nil
}

func resolveAgentConfig(source legacy.Fleet, agent legacy.Agent, evidence ConfigEvidence, context ConfigContext) (ResolvedConfig, error) {
	r := configResolver{evidence: evidence, files: source.Files}
	workDir, err := r.identity(context.WorkingDirectory)
	if err != nil {
		return ResolvedConfig{}, err
	}
	identities := []string{"work_dir=" + workDir}
	for _, file := range context.ExplicitFiles {
		identity, err := r.identity(file.Path)
		if err != nil {
			return ResolvedConfig{}, err
		}
		identities = append(identities, "explicit="+identity)
	}
	r.context = configDigest([]byte(strings.Join(identities, "\n")))
	var layers []configLayer
	add := func(scope, directory, name string, files []legacy.SourceFile, absent []string) error {
		p := filepath.Join(directory, name)
		identity, err := r.identity(p)
		if err != nil {
			return err
		}
		file, err := capturedConfigFile(name, files, absent)
		if err != nil {
			return err
		}
		layers = append(layers, configLayer{path: p, identity: identity, scope: scope, file: file})
		return nil
	}
	if err := add("default-home", source.DefaultHome.Directory, "juex.yaml", source.DefaultHome.Files, source.DefaultHome.AbsentFiles); err != nil {
		return ResolvedConfig{}, err
	}
	if err := add("instance-home", source.SourceHome, "juex.yaml", source.Files, source.AbsentFiles); err != nil {
		return ResolvedConfig{}, err
	}
	if layers[0].identity == layers[1].identity {
		if !sameCapturedConfig(layers[0].file, layers[1].file) {
			return ResolvedConfig{}, errors.New("same Home has inconsistent captured configuration")
		}
		layers = layers[:1]
	}
	var workspace *legacy.Workspace
	for i := range source.Workspaces {
		if source.Workspaces[i].AgentID == agent.Definition.ID {
			if workspace != nil {
				return ResolvedConfig{}, errors.New("repeated Workspace capture")
			}
			workspace = &source.Workspaces[i]
		}
	}
	if workspace == nil || workspace.Path != agent.Definition.Workspace {
		return ResolvedConfig{}, errors.New("matching Workspace capture is required")
	}
	workspaceIdentity, err := r.identity(workspace.Path)
	if err != nil {
		return ResolvedConfig{}, err
	}
	if workDir != workspaceIdentity {
		return ResolvedConfig{}, errors.New("startup working directory does not match the captured Agent Workspace")
	}
	name := ".juex/juex.yaml"
	if filepath.Base(filepath.Clean(workspace.Path)) == ".juex" {
		name = "juex.yaml"
	}
	if err := add("workspace", workspace.Path, name, workspace.Files, workspace.AbsentFiles); err != nil {
		return ResolvedConfig{}, err
	}
	if err := add("agent", filepath.Join(source.SourceHome, "agents", agent.Definition.ID), "juex.yaml", agent.Files, agent.AbsentFiles); err != nil {
		return ResolvedConfig{}, err
	}
	s := configSettings{value: ResolvedConfig{AgentID: agent.Definition.ID, Preset: "standard", WorkerDepth: 1, UserResources: true}, providers: map[string]configProvider{}, modules: map[string]bool{}, fleetProfile: "agent"}
	loaded := map[string]configLayer{}
	for _, layer := range layers {
		if err := r.apply(&s, layer, false); err != nil {
			return ResolvedConfig{}, err
		}
		loaded[layer.identity] = layer
	}
	for i := range context.ExplicitFiles {
		file := &context.ExplicitFiles[i]
		identity, _ := r.identity(file.Path)
		layer := configLayer{file.Path, identity, "explicit", file}
		replay := false
		if prior, ok := loaded[identity]; ok {
			if !sameCapturedConfig(prior.file, file) {
				return ResolvedConfig{}, errors.New("explicit configuration contradicts its captured persistent layer")
			}
			if prior.scope == "agent" {
				continue
			}
			layer, replay = prior, true
		}
		if err := r.apply(&s, layer, replay); err != nil {
			return ResolvedConfig{}, err
		}
	}
	if len(context.ModelRefs) > 0 {
		s.models = slices.Clone(context.ModelRefs)
	}
	s.value.StartupModelOverride = len(context.ModelRefs) > 0
	return s.resolve()
}

func absoluteConfigPath(p string) bool {
	return filepath.IsAbs(p) && filepath.Clean(p) == p && !strings.ContainsRune(p, 0)
}

func sameCapturedConfig(a, b *legacy.SourceFile) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return verifyConfigFile(*a) == nil && verifyConfigFile(*b) == nil && a.SHA256 == b.SHA256
}
