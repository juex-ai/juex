// Package importproof reads the immutable v1 offline import format. It is only
// used to recover already committed identities, never to create new resources.
package importproof

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/hookpolicy"
	"github.com/juex-ai/juex/internal/foundation/instructionpolicy"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/management"
)

type AgentConfigV1 struct {
	DynamicInstructions *instructionpolicy.DynamicInstructions `json:"dynamic_instructions,omitempty"`
	Capabilities        *agentpolicy.Policy                    `json:"capabilities,omitempty"`
	Hooks               []hookpolicy.Declaration               `json:"hooks,omitempty"`
	WorkerDepth         int                                    `json:"worker_depth,omitempty"`
	Name                string                                 `json:"name"`
	Instructions        string                                 `json:"instructions"`
	ModelID             string                                 `json:"model_id"`
}

type AgentV1 struct {
	SourceAgentID string        `json:"source_agent_id"`
	Config        AgentConfigV1 `json:"config"`
}

type AgentsV1 struct {
	ExpectedFleetID string    `json:"expected_fleet_id"`
	Source          string    `json:"source"`
	SourceSHA256    string    `json:"source_sha256"`
	Agents          []AgentV1 `json:"agents"`
}

// This list is part of the stored proof, not the current capability defaults.
func ModulesV1() []agentpolicy.Capability {
	return []agentpolicy.Capability{"files", "shell", "workers", "collaboration", "mcp", "observations", "memory", "calendar", "hooks", "extensions", "notes", "tasks", "context-control", "working-files"}
}

func (v AgentsV1) Prepare() (management.AgentsImport, string, error) {
	current := management.AgentsImport{ExpectedFleetID: v.ExpectedFleetID, Source: v.Source, SourceSHA256: v.SourceSHA256}
	for _, item := range v.Agents {
		c := item.Config
		if c.ModelID != "" {
			id, err := uuid.Parse(c.ModelID)
			if err != nil || id == uuid.Nil || id.String() != c.ModelID {
				return current, "", management.ErrInvalid
			}
		}
		declaration := management.Configuration{Modules: map[agentpolicy.Capability]bool{}}
		disabled := []agentpolicy.Capability{}
		if c.Capabilities != nil {
			if len(c.Capabilities.Enabled) != 0 {
				return current, "", management.ErrInvalid
			}
			disabled = c.Capabilities.Disabled
		}
		seen := map[agentpolicy.Capability]bool{}
		for _, key := range disabled {
			if seen[key] || !slices.Contains(ModulesV1(), key) {
				return current, "", management.ErrInvalid
			}
			seen[key] = true
		}
		for _, key := range ModulesV1() {
			declaration.Modules[key] = !seen[key]
		}
		if c.ModelID != "" {
			declaration.Models = []string{c.ModelID}
		}
		current.Agents = append(current.Agents, management.ImportedAgent{SourceAgentID: item.SourceAgentID, Config: management.AgentConfig{DynamicInstructions: c.DynamicInstructions, Hooks: c.Hooks, WorkerDepth: c.WorkerDepth, Name: c.Name, Instructions: c.Instructions, Configuration: &declaration}})
	}
	if err := current.Validate(); err != nil {
		return current, "", err
	}
	v.Agents = slices.Clone(v.Agents)
	slices.SortFunc(v.Agents, func(a, b AgentV1) int { return strings.Compare(a.SourceAgentID, b.SourceAgentID) })
	hash, err := digest(v)
	return current, hash, err
}

type ModelOptionsV1 struct {
	Authentication string                                                                                      `json:"authentication"`
	ThinkingEffort string                                                                                      `json:"thinking_effort,omitempty"`
	Headers        map[string]string                                                                           `json:"headers,omitempty"`
	Query          map[string]string                                                                           `json:"query,omitempty"`
	Capabilities   struct{ Tools, Vision, Streaming, ReasoningEffort, ReasoningReplay, MaxOutputTokens *bool } `json:"capabilities"`
	Compat         struct {
		ReasoningReplayFields          []string
		CodexTransport, MaxTokensField string
	} `json:"compat"`
}

type ModelConfigurationV1 struct {
	Provider, Name, Endpoint, APIKey        string
	Protocol                                llm.Protocol
	ContextWindow, MaxOutput, OutputReserve int
	Enabled                                 bool
	Options                                 ModelOptionsV1
}

type ModelV1 struct {
	Configuration ModelConfigurationV1  `json:"configuration"`
	Fallbacks     []management.ModelKey `json:"fallbacks"`
}

type ModelsV1 struct {
	TenantID     string    `json:"tenant_id"`
	Source       string    `json:"source"`
	SourceSHA256 string    `json:"source_sha256"`
	Models       []ModelV1 `json:"models"`
}

// FreezeModel copies every current private field through the fixed wire format.
// A round-trip check prevents silently discarding future fields during replay.
func FreezeModel(c management.ModelConfiguration) (ModelConfigurationV1, error) {
	var frozen ModelConfigurationV1
	text := []string{c.Provider, c.Name, c.Endpoint, c.APIKey, string(c.Protocol), c.Options.Authentication, c.Options.ThinkingEffort, c.Options.Compat.CodexTransport, c.Options.Compat.MaxTokensField}
	text = append(text, c.Options.Compat.ReasoningReplayFields...)
	for _, values := range []map[string]string{c.Options.Headers, c.Options.Query} {
		for key, value := range values {
			text = append(text, key, value)
		}
	}
	for _, value := range text {
		if !utf8.ValidString(value) {
			return frozen, management.ErrInvalid
		}
	}
	data, err := json.Marshal(c)
	if err != nil {
		return frozen, management.ErrInvalid
	}
	if err = json.Unmarshal(data, &frozen); err != nil {
		return frozen, management.ErrInvalid
	}
	round, err := json.Marshal(frozen)
	if err != nil || string(data) != string(round) {
		return frozen, management.ErrConflict
	}
	return frozen, nil
}

func (v ModelsV1) Prepare() (management.ModelsImport, string, error) {
	current := management.ModelsImport{TenantID: v.TenantID, Source: v.Source, SourceSHA256: v.SourceSHA256}
	tenant, err := uuid.Parse(v.TenantID)
	sourceHash, hashErr := hex.DecodeString(v.SourceSHA256)
	if err != nil || tenant == uuid.Nil || tenant.String() != v.TenantID || hashErr != nil || len(sourceHash) != 32 || strings.ToLower(v.SourceSHA256) != v.SourceSHA256 || strings.TrimSpace(v.Source) == "" || len(v.Source) > 512 || !utf8.ValidString(v.Source) || strings.ContainsRune(v.Source, 0) || len(v.Models) == 0 || len(v.Models) > 1024 {
		return current, "", management.ErrInvalid
	}
	v.Models = slices.Clone(v.Models)
	keys := map[management.ModelKey]bool{}
	for i, item := range v.Models {
		c := item.Configuration
		text := []string{c.Provider, c.Name, c.Endpoint, c.APIKey, string(c.Protocol), c.Options.Authentication, c.Options.ThinkingEffort, c.Options.Compat.CodexTransport, c.Options.Compat.MaxTokensField}
		text = append(text, c.Options.Compat.ReasoningReplayFields...)
		for _, values := range []map[string]string{c.Options.Headers, c.Options.Query} {
			for k, s := range values {
				text = append(text, k, s)
			}
		}
		for _, s := range text {
			if !utf8.ValidString(s) {
				return current, "", management.ErrInvalid
			}
		}
		key := management.ModelKey{Provider: c.Provider, Name: c.Name}
		if keys[key] || strings.TrimSpace(c.Provider) == "" || strings.TrimSpace(c.Name) == "" || strings.ContainsRune(c.Provider, 0) || strings.ContainsRune(c.Name, 0) || len(item.Fallbacks) > 4 {
			return current, "", management.ErrInvalid
		}
		keys[key] = true
		if c.Options.Authentication == "" {
			c.Options.Authentication = "api_key"
		}
		if len(c.Options.Compat.ReasoningReplayFields) == 0 {
			c.Options.Compat.ReasoningReplayFields = nil
		}
		v.Models[i].Configuration = c
		v.Models[i].Fallbacks = append([]management.ModelKey{}, item.Fallbacks...)
	}
	for _, item := range v.Models {
		seen := map[management.ModelKey]bool{{Provider: item.Configuration.Provider, Name: item.Configuration.Name}: true}
		for _, key := range item.Fallbacks {
			if !keys[key] || seen[key] {
				return current, "", management.ErrInvalid
			}
			seen[key] = true
		}
	}
	slices.SortFunc(v.Models, func(a, b ModelV1) int {
		if order := strings.Compare(a.Configuration.Provider, b.Configuration.Provider); order != 0 {
			return order
		}
		return strings.Compare(a.Configuration.Name, b.Configuration.Name)
	})
	for _, item := range v.Models {
		data, err := json.Marshal(item.Configuration)
		if err != nil {
			return current, "", management.ErrInvalid
		}
		var c management.ModelConfiguration
		if err := json.Unmarshal(data, &c); err != nil {
			return current, "", management.ErrInvalid
		}
		current.Models = append(current.Models, management.ImportedModel{Configuration: c})
	}
	hash, err := digest(v)
	return current, hash, err
}

func digest(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > 64<<20 {
		return "", management.ErrInvalid
	}
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:]), nil
}
