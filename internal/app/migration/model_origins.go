package migration

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/management"
	"github.com/juex-ai/juex/internal/migration/legacy"
)

// These wire shapes and digest ordering belong to fixed source 281889e5's
// framework/provenance, not the target provider's current normalization rules.
type sourceSafeProvider struct {
	ID                    string                   `json:"id,omitempty"`
	Protocol              llm.Protocol             `json:"protocol,omitempty"`
	Model                 string                   `json:"model,omitempty"`
	EndpointDigest        string                   `json:"endpoint_digest,omitempty"`
	HeaderDigest          string                   `json:"header_digest,omitempty"`
	QueryDigest           string                   `json:"query_digest,omitempty"`
	ThinkingEffort        string                   `json:"thinking_effort,omitempty"`
	Capabilities          llm.ProviderCapabilities `json:"capabilities"`
	ReasoningReplayFields []string                 `json:"reasoning_replay_fields,omitempty"`
	CodexTransport        string                   `json:"codex_transport,omitempty"`
	MaxTokensField        string                   `json:"max_tokens_field,omitempty"`
}

func sourceProvider(p llm.ProviderProfile) sourceSafeProvider {
	endpoint := strings.TrimSpace(p.BaseURL)
	if endpoint != "" {
		if u, err := url.Parse(endpoint); err == nil {
			u.User, u.RawQuery, u.Fragment, u.ForceQuery = nil, "", "", false
			u.Scheme, u.Host = strings.ToLower(u.Scheme), strings.ToLower(u.Host)
			endpoint = u.String()
		}
		endpoint = bundleDigest([]byte(endpoint))
	}
	mapDigest := func(value map[string]string) string {
		if len(value) == 0 {
			return ""
		}
		raw, _ := json.Marshal(value)
		return bundleDigest(raw)
	}
	return sourceSafeProvider{p.ID, p.Protocol, p.Model, endpoint, mapDigest(p.Headers), mapDigest(p.Query), p.ThinkingEffort, p.Capabilities, slices.Clone(p.Compat.ReasoningReplayFields), p.Compat.CodexTransport, p.Compat.MaxTokensField}
}

type sourceMessageRef struct {
	ID            string `json:"id"`
	Source        string `json:"source"`
	ContentDigest string `json:"content_digest"`
}

type sourceCompaction struct {
	MarkerMessageID    string   `json:"marker_message_id,omitempty"`
	PreviousSummaryID  string   `json:"previous_summary_id,omitempty"`
	TailStartMessageID string   `json:"tail_start_message_id,omitempty"`
	RetainedMessageIDs []string `json:"retained_message_ids,omitempty"`
}

type sourceCachePolicy struct {
	StablePrefixKeyDigest string `json:"stable_prefix_key_digest,omitempty"`
	RetentionDigest       string `json:"retention_digest,omitempty"`
}

type sourceRequestEpoch struct {
	EpochID         string             `json:"epoch_id"`
	Purpose         string             `json:"purpose"`
	Iter            int                `json:"iter"`
	Attempt         int                `json:"attempt"`
	Provider        sourceSafeProvider `json:"provider"`
	ContextWindow   int                `json:"context_window,omitempty"`
	MaxOutputTokens int                `json:"max_output_tokens,omitempty"`
	CachePolicy     sourceCachePolicy  `json:"cache_policy,omitempty"`
	SystemPrompt    struct {
		Digest string `json:"digest"`
	} `json:"system_prompt"`
	ToolCatalog struct {
		Digest string `json:"digest"`
	} `json:"tool_catalog"`
	HistoryDigest           string             `json:"history_digest"`
	HistoryMessageIDs       []string           `json:"history_message_ids"`
	Messages                []sourceMessageRef `json:"messages"`
	Compaction              sourceCompaction   `json:"compaction,omitempty"`
	PolicyContextMessageIDs []string           `json:"policy_context_message_ids,omitempty"`
	RequestDigest           string             `json:"request_digest"`
}

func (e sourceRequestEpoch) digest() string {
	envelope := struct {
		SchemaVersion           int                `json:"schema_version"`
		Purpose                 string             `json:"purpose"`
		Provider                sourceSafeProvider `json:"provider"`
		ContextWindow           int                `json:"context_window,omitempty"`
		MaxOutputTokens         int                `json:"max_output_tokens,omitempty"`
		CachePolicy             sourceCachePolicy  `json:"cache_policy,omitempty"`
		SystemPromptDigest      string             `json:"system_prompt_digest"`
		ToolCatalogDigest       string             `json:"tool_catalog_digest"`
		HistoryDigest           string             `json:"history_digest"`
		HistoryMessageIDs       []string           `json:"history_message_ids"`
		Messages                []sourceMessageRef `json:"messages"`
		Compaction              sourceCompaction   `json:"compaction,omitempty"`
		PolicyContextMessageIDs []string           `json:"policy_context_message_ids,omitempty"`
	}{1, e.Purpose, e.Provider, e.ContextWindow, e.MaxOutputTokens, e.CachePolicy, e.SystemPrompt.Digest, e.ToolCatalog.Digest, e.HistoryDigest, append([]string(nil), e.HistoryMessageIDs...), append([]sourceMessageRef{}, e.Messages...), e.Compaction, e.PolicyContextMessageIDs}
	raw, _ := json.Marshal(envelope)
	return bundleDigest(raw)
}

type sourceOriginEvent struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	TurnID  string `json:"turn_id"`
	Payload struct {
		Epoch         sourceRequestEpoch `json:"epoch"`
		EpochID       string             `json:"epoch_id"`
		Purpose       string             `json:"purpose"`
		RequestDigest string             `json:"request_digest"`
		Iter          int                `json:"iter"`
		MessageID     string             `json:"message_id"`
		Blocks        []llm.Block        `json:"blocks"`
	} `json:"payload"`
	position int
}

type sourceOrigins map[string]map[string]ModelKey

func reasoningBlocks(blocks []llm.Block) []llm.Block {
	return slices.DeleteFunc(slices.Clone(blocks), func(b llm.Block) bool { return b.Type != llm.BlockReasoning })
}

// sourceModelOrigins never infers identity from Message.Model. It proves only
// retained context; original history remains intact regardless of projection.
func sourceModelOrigins(agent legacy.Agent, profiles []ResolvedModels) (sourceOrigins, error) {
	result := sourceOrigins{}
	for _, thread := range agent.Threads {
		epochs, requests, responses := map[string][]sourceOriginEvent{}, map[string][]sourceOriginEvent{}, map[string][]sourceOriginEvent{}
		messages := map[string][]llm.Message{}
		position := 0
		for _, commit := range thread.Commits {
			for _, fact := range commit.Facts {
				position++
				if fact.Type == "message.appended" && fact.Message != nil {
					messages[fact.Message.ID] = append(messages[fact.Message.ID], *fact.Message)
				}
				if len(fact.Event) == 0 {
					continue
				}
				var event sourceOriginEvent
				// Unrelated event payloads have different field types; decode only
				// their envelope before selecting these three fixed source records.
				var envelope struct {
					Type string `json:"type"`
				}
				if err := json.Unmarshal(fact.Event, &envelope); err != nil {
					return nil, errors.New("invalid source event envelope")
				}
				if envelope.Type != "provider.request_epoch" && envelope.Type != "llm.requested" && envelope.Type != "llm.responded" {
					continue
				}
				if err := json.Unmarshal(fact.Event, &event); err != nil {
					return nil, errors.New("invalid source model provenance")
				}
				event.position = position
				switch event.Type {
				case "provider.request_epoch":
					epochs[event.Payload.Epoch.EpochID] = append(epochs[event.Payload.Epoch.EpochID], event)
				case "llm.requested":
					requests[event.Payload.EpochID] = append(requests[event.Payload.EpochID], event)
				case "llm.responded":
					responses[event.Payload.MessageID] = append(responses[event.Payload.MessageID], event)
				}
			}
		}
		for _, message := range thread.Context {
			blocks := reasoningBlocks(message.Blocks)
			if len(blocks) == 0 {
				continue
			}
			failure := func(reason string) (sourceOrigins, error) {
				return nil, fmt.Errorf("source Thread %s message %s: %s", thread.Metadata.ThreadID, message.ID, reason)
			}
			if message.Role != llm.RoleAssistant || len(responses[message.ID]) != 1 || len(messages[message.ID]) == 0 {
				return failure("reasoning requires a unique response and original message")
			}
			response := responses[message.ID][0]
			if response.TurnID == "" || !reflect.DeepEqual(reasoningBlocks(response.Payload.Blocks), blocks) {
				return failure("response reasoning or Turn differs")
			}
			for _, original := range messages[message.ID] {
				if original.Role != llm.RoleAssistant || !reflect.DeepEqual(reasoningBlocks(original.Blocks), blocks) {
					return failure("original reasoning differs")
				}
			}
			id := response.Payload.EpochID
			if id == "" || len(epochs[id]) != 1 || len(requests[id]) != 1 {
				return failure("reasoning requires one request epoch and request")
			}
			event, request := epochs[id][0], requests[id][0]
			epoch := event.Payload.Epoch
			if event.ID != id || event.TurnID != response.TurnID || request.TurnID != response.TurnID || event.position >= request.position || request.position >= response.position || epoch.Purpose != "turn" || request.Payload.Purpose != "turn" || epoch.Iter < 0 || epoch.Attempt < 1 || epoch.Iter != request.Payload.Iter || epoch.Iter != response.Payload.Iter || epoch.RequestDigest != request.Payload.RequestDigest || epoch.RequestDigest != response.Payload.RequestDigest || epoch.digest() != epoch.RequestDigest {
				return failure("request provenance is inconsistent")
			}
			var match *ModelKey
			for _, resolved := range profiles {
				for _, model := range resolved.Models {
					p := model.Profile
					if !p.Capabilities.ReasoningReplay || !reflect.DeepEqual(sourceProvider(p), epoch.Provider) {
						continue
					}
					opaque := slices.ContainsFunc(blocks, func(b llm.Block) bool { return b.Signature != "" || b.Redacted })
					if opaque && !sourceOpaqueAccount(p) {
						return failure("opaque reasoning account cannot be proven")
					}
					key := ModelKey{Provider: p.ID, Name: p.Model}
					if match != nil && *match != key {
						return failure("ambiguous source model")
					}
					match = &key
				}
			}
			if match == nil {
				return failure("captured catalog does not match request provenance")
			}
			if result[thread.Metadata.ThreadID] == nil {
				result[thread.Metadata.ThreadID] = map[string]ModelKey{}
			}
			result[thread.Metadata.ThreadID][message.ID] = *match
		}
	}
	return result, nil
}

func sourceOpaqueAccount(p llm.ProviderProfile) bool {
	if p.Protocol != llm.ProtocolOpenAICodexResponses {
		return false
	}
	if p.BaseURL != "" {
		u, err := url.Parse(p.BaseURL)
		if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return false
		}
	}
	count := 0
	for name, value := range p.Headers {
		if !strings.EqualFold(name, "ChatGPT-Account-ID") {
			continue
		}
		if (name != "ChatGPT-Account-ID" && name != "chatgpt-account-id") || strings.TrimSpace(value) == "" || strings.Contains(value, "${") || strings.ContainsAny(value, "\r\n") {
			return false
		}
		count++
	}
	return count == 1
}

// Only the fixed Codex adapter has a default route proved here. Other SDK
// defaults may depend on uncaptured environment, so their opaque route is not
// inferred from a requested target endpoint.
func sourceReplayRoute(p llm.ProviderProfile, endpoint string) bool {
	if p.BaseURL != "" {
		return endpoint == "" || endpoint == p.BaseURL
	}
	if p.Protocol != llm.ProtocolOpenAICodexResponses {
		return false
	}
	normalized := strings.TrimRight(strings.TrimSpace(endpoint), "/")
	normalized = strings.TrimRight(strings.TrimSuffix(normalized, "/responses"), "/")
	if !strings.HasSuffix(normalized, "/codex") {
		normalized += "/codex"
	}
	return normalized == "https://chatgpt.com/backend-api/codex"
}

func bindModelOrigins(origins sourceOrigins, plan ModelPublicationPlan, receipt map[ModelKey]management.ModelImportIdentity) (map[string]map[string]managedruntime.ModelConfig, error) {
	catalog := map[ModelKey]management.ModelConfiguration{}
	for _, model := range plan.Catalog {
		catalog[model.Key] = model.Configuration
	}
	result := map[string]map[string]managedruntime.ModelConfig{}
	for thread, messages := range origins {
		for message, key := range messages {
			model, ok := catalog[key]
			identity, exists := receipt[key]
			id, err := uuid.Parse(identity.ID)
			if !ok || !exists || err != nil || id == uuid.Nil || id.String() != identity.ID || identity.ModelAuthorizationEpoch < 1 {
				return nil, errors.New("model origin requires the original owner publication receipt")
			}
			if result[thread] == nil {
				result[thread] = map[string]managedruntime.ModelConfig{}
			}
			result[thread][message] = managedruntime.ModelConfig{ModelID: identity.ID, Provider: model.Provider, Model: model.Name, Protocol: model.Protocol, Endpoint: model.Endpoint, ModelAuthorizationEpoch: identity.ModelAuthorizationEpoch}
		}
	}
	return result, nil
}
