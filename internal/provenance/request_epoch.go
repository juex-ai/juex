// Package provenance defines the durable, secret-safe identity of logical
// provider requests. It records request selection without replacing the
// transcript or mirroring provider wire payloads.
package provenance

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
)

const (
	RequestEpochType         = "provider.request_epoch"
	HookContextQueuedType    = "provider.hook_context.queued"
	SchemaVersion            = 1
	MaxInlineSnapshotBytes   = 256 * 1024
	MaxHookContextBatchBytes = 1 * 1024 * 1024
	MaxRequestEpochBytes     = 2 * 1024 * 1024
)

type SafeProvider struct {
	ID                    string                   `json:"id,omitempty"`
	Protocol              llm.Protocol             `json:"protocol,omitempty"`
	Model                 string                   `json:"model,omitempty"`
	ThinkingEffort        string                   `json:"thinking_effort,omitempty"`
	Capabilities          llm.ProviderCapabilities `json:"capabilities"`
	ReasoningReplayFields []string                 `json:"reasoning_replay_fields,omitempty"`
	CodexTransport        string                   `json:"codex_transport,omitempty"`
}

func SafeProviderFromProfile(profile llm.ProviderProfile) SafeProvider {
	return SafeProvider{
		ID:                    profile.ID,
		Protocol:              profile.Protocol,
		Model:                 profile.Model,
		ThinkingEffort:        profile.ThinkingEffort,
		Capabilities:          profile.Capabilities,
		ReasoningReplayFields: append([]string(nil), profile.Compat.ReasoningReplayFields...),
		CodexTransport:        profile.Compat.CodexTransport,
	}
}

type Snapshot struct {
	Digest  string          `json:"digest"`
	Bytes   int             `json:"bytes"`
	Content json.RawMessage `json:"content,omitempty"`
	Omitted string          `json:"omitted,omitempty"`
	Reused  bool            `json:"reused,omitempty"`
}

type CompactionSelection struct {
	MarkerMessageID    string   `json:"marker_message_id,omitempty"`
	PreviousSummaryID  string   `json:"previous_summary_id,omitempty"`
	TailStartMessageID string   `json:"tail_start_message_id,omitempty"`
	RetainedMessageIDs []string `json:"retained_message_ids,omitempty"`
}

type MessageRef struct {
	ID            string `json:"id"`
	Source        string `json:"source"`
	ContentDigest string `json:"content_digest"`
}

type RequestInput struct {
	Purpose               string              `json:"purpose,omitempty"`
	Provider              SafeProvider        `json:"provider"`
	ContextWindow         int                 `json:"context_window,omitempty"`
	MaxOutputTokens       int                 `json:"max_output_tokens,omitempty"`
	SystemPrompt          string              `json:"system_prompt,omitempty"`
	Tools                 []llm.ToolSpec      `json:"tools,omitempty"`
	History               []llm.Message       `json:"history"`
	Compaction            CompactionSelection `json:"compaction,omitempty"`
	HookContextMessageIDs []string            `json:"hook_context_message_ids,omitempty"`
}

type RequestEpoch struct {
	EpochID               string              `json:"epoch_id"`
	Purpose               string              `json:"purpose"`
	Iter                  int                 `json:"iter"`
	Attempt               int                 `json:"attempt"`
	Provider              SafeProvider        `json:"provider"`
	ContextWindow         int                 `json:"context_window,omitempty"`
	MaxOutputTokens       int                 `json:"max_output_tokens,omitempty"`
	SystemPromptSnapshot  Snapshot            `json:"system_prompt"`
	ToolCatalogSnapshot   Snapshot            `json:"tool_catalog"`
	HistoryDigest         string              `json:"history_digest"`
	HistoryMessageIDs     []string            `json:"history_message_ids"`
	Messages              []MessageRef        `json:"messages"`
	Compaction            CompactionSelection `json:"compaction,omitempty"`
	HookContextMessageIDs []string            `json:"hook_context_message_ids,omitempty"`
	RequestDigest         string              `json:"request_digest"`
}

type RequestEpochPayload struct {
	Epoch RequestEpoch `json:"epoch"`
}

type HookContextQueuedPayload struct {
	Messages []llm.Message `json:"messages"`
}

type digestEnvelope struct {
	SchemaVersion         int                 `json:"schema_version"`
	Purpose               string              `json:"purpose"`
	Provider              SafeProvider        `json:"provider"`
	ContextWindow         int                 `json:"context_window,omitempty"`
	MaxOutputTokens       int                 `json:"max_output_tokens,omitempty"`
	SystemPromptDigest    string              `json:"system_prompt_digest"`
	ToolCatalogDigest     string              `json:"tool_catalog_digest"`
	HistoryDigest         string              `json:"history_digest"`
	HistoryMessageIDs     []string            `json:"history_message_ids"`
	Messages              []MessageRef        `json:"messages"`
	Compaction            CompactionSelection `json:"compaction,omitempty"`
	HookContextMessageIDs []string            `json:"hook_context_message_ids,omitempty"`
}

func BuildRequestEpoch(input RequestInput) (RequestEpoch, error) {
	purpose := input.Purpose
	if purpose == "" {
		purpose = "turn"
	}
	historyIDs := make([]string, len(input.History))
	messageRefs := make([]MessageRef, len(input.History))
	hookIDs := make(map[string]struct{}, len(input.HookContextMessageIDs))
	for _, id := range input.HookContextMessageIDs {
		hookIDs[id] = struct{}{}
	}
	for index, message := range input.History {
		if message.ID == "" {
			return RequestEpoch{}, fmt.Errorf("provenance: history message id is required at index %d", index)
		}
		historyIDs[index] = message.ID
		messageRaw, err := json.Marshal(message)
		if err != nil {
			return RequestEpoch{}, fmt.Errorf("provenance: history message digest at index %d: %w", index, err)
		}
		messageRefs[index] = MessageRef{ID: message.ID, Source: messageSource(message, hookIDs), ContentDigest: digest(messageRaw)}
	}
	systemSnapshot, err := newSnapshot(input.SystemPrompt)
	if err != nil {
		return RequestEpoch{}, fmt.Errorf("provenance: system prompt snapshot: %w", err)
	}
	toolSnapshot, err := newSnapshot(input.Tools)
	if err != nil {
		return RequestEpoch{}, fmt.Errorf("provenance: tool catalog snapshot: %w", err)
	}
	historyRaw, err := json.Marshal(input.History)
	if err != nil {
		return RequestEpoch{}, fmt.Errorf("provenance: history digest: %w", err)
	}
	historyDigest := digest(historyRaw)
	envelope := digestEnvelope{
		SchemaVersion:         SchemaVersion,
		Purpose:               purpose,
		Provider:              cloneSafeProvider(input.Provider),
		ContextWindow:         input.ContextWindow,
		MaxOutputTokens:       input.MaxOutputTokens,
		SystemPromptDigest:    systemSnapshot.Digest,
		ToolCatalogDigest:     toolSnapshot.Digest,
		HistoryDigest:         historyDigest,
		HistoryMessageIDs:     append([]string(nil), historyIDs...),
		Messages:              append([]MessageRef(nil), messageRefs...),
		Compaction:            cloneCompaction(input.Compaction),
		HookContextMessageIDs: append([]string(nil), input.HookContextMessageIDs...),
	}
	epoch := RequestEpoch{
		Purpose:               purpose,
		Provider:              envelope.Provider,
		ContextWindow:         input.ContextWindow,
		MaxOutputTokens:       input.MaxOutputTokens,
		SystemPromptSnapshot:  systemSnapshot,
		ToolCatalogSnapshot:   toolSnapshot,
		HistoryDigest:         historyDigest,
		HistoryMessageIDs:     historyIDs,
		Messages:              messageRefs,
		Compaction:            envelope.Compaction,
		HookContextMessageIDs: envelope.HookContextMessageIDs,
	}
	epoch.RequestDigest, err = requestDigest(epoch)
	if err != nil {
		return RequestEpoch{}, err
	}
	return epoch, nil
}

func requestDigest(epoch RequestEpoch) (string, error) {
	envelope := digestEnvelope{
		SchemaVersion:         SchemaVersion,
		Purpose:               epoch.Purpose,
		Provider:              cloneSafeProvider(epoch.Provider),
		ContextWindow:         epoch.ContextWindow,
		MaxOutputTokens:       epoch.MaxOutputTokens,
		SystemPromptDigest:    epoch.SystemPromptSnapshot.Digest,
		ToolCatalogDigest:     epoch.ToolCatalogSnapshot.Digest,
		HistoryDigest:         epoch.HistoryDigest,
		HistoryMessageIDs:     append([]string(nil), epoch.HistoryMessageIDs...),
		Messages:              append([]MessageRef(nil), epoch.Messages...),
		Compaction:            cloneCompaction(epoch.Compaction),
		HookContextMessageIDs: append([]string(nil), epoch.HookContextMessageIDs...),
	}
	canonical, err := json.Marshal(envelope)
	if err != nil {
		return "", fmt.Errorf("provenance: request digest: %w", err)
	}
	return digest(canonical), nil
}

func messageSource(message llm.Message, hookIDs map[string]struct{}) string {
	if _, ok := hookIDs[message.ID]; ok {
		return "hook_context"
	}
	switch message.Kind {
	case llm.MessageKindRuntimeContext:
		return "runtime_context"
	case llm.MessageKindModelChange:
		return "model_change"
	case llm.MessageKindCompact:
		return "compaction"
	default:
		return "transcript"
	}
}

func newSnapshot(value any) (Snapshot, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return Snapshot{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		return Snapshot{}, err
	}
	raw, err = json.Marshal(normalized)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot := Snapshot{Digest: digest(raw), Bytes: len(raw)}
	if len(raw) > MaxInlineSnapshotBytes {
		snapshot.Omitted = "size_limit"
		return snapshot, nil
	}
	snapshot.Content = append(json.RawMessage(nil), raw...)
	return snapshot, nil
}

func digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func cloneSafeProvider(provider SafeProvider) SafeProvider {
	provider.ReasoningReplayFields = append([]string(nil), provider.ReasoningReplayFields...)
	return provider
}

func cloneCompaction(selection CompactionSelection) CompactionSelection {
	selection.RetainedMessageIDs = append([]string(nil), selection.RetainedMessageIDs...)
	return selection
}

type Tracker struct {
	mu        sync.Mutex
	queued    []llm.Message
	consumed  map[string]struct{}
	known     map[string]struct{}
	epochs    map[string]string
	requested map[string]struct{}
	responded map[string]struct{}
}

func NewTracker() *Tracker {
	return &Tracker{
		consumed:  make(map[string]struct{}),
		known:     make(map[string]struct{}),
		epochs:    make(map[string]string),
		requested: make(map[string]struct{}),
		responded: make(map[string]struct{}),
	}
}

func Recover(journal []events.Event) (*Tracker, error) {
	tracker := NewTracker()
	for _, event := range journal {
		switch event.Type {
		case HookContextQueuedType:
			var payload HookContextQueuedPayload
			if err := decodePayload(event.Payload, &payload); err != nil {
				return nil, fmt.Errorf("provenance: recover queued hook context: %w", err)
			}
			if err := ValidateHookContextQueued(payload); err != nil {
				return nil, err
			}
			for _, message := range payload.Messages {
				if tracker.hasQueuedIDLocked(message.ID) {
					return nil, fmt.Errorf("provenance: duplicate queued hook context id %q", message.ID)
				}
				tracker.queued = append(tracker.queued, message)
			}
		case RequestEpochType:
			var payload RequestEpochPayload
			if err := decodePayload(event.Payload, &payload); err != nil {
				return nil, fmt.Errorf("provenance: recover request epoch: %w", err)
			}
			if err := ValidateRequestEpoch(payload); err != nil {
				return nil, err
			}
			if err := tracker.validateSnapshotReferencesLocked(payload.Epoch); err != nil {
				return nil, err
			}
			if _, duplicate := tracker.epochs[payload.Epoch.EpochID]; duplicate {
				return nil, fmt.Errorf("provenance: duplicate request epoch id %q", payload.Epoch.EpochID)
			}
			tracker.recordEpochLocked(payload.Epoch)
		case "llm.requested":
			if err := tracker.recordRequestLinkLocked(event.Payload); err != nil {
				return nil, err
			}
		case "llm.responded":
			if err := tracker.recordResponseLinkLocked(event.Payload); err != nil {
				return nil, err
			}
		case "llm.retry":
			if err := tracker.validateRetryLinkLocked(event.Payload); err != nil {
				return nil, err
			}
		}
	}
	return tracker, nil
}

func decodePayload(input any, output any) error {
	raw, err := json.Marshal(input)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, output)
}

func (t *Tracker) AddQueued(message llm.Message) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.queued = append(t.queued, message)
	t.mu.Unlock()
}

func (t *Tracker) hasQueuedIDLocked(id string) bool {
	for _, message := range t.queued {
		if message.ID == id {
			return true
		}
	}
	return false
}

func (t *Tracker) PendingHookContext() []llm.Message {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.pendingHookContextLocked()
}

func (t *Tracker) pendingHookContextLocked() []llm.Message {
	pending := make([]llm.Message, 0, len(t.queued))
	for _, message := range t.queued {
		if _, ok := t.consumed[message.ID]; ok {
			continue
		}
		pending = append(pending, message)
	}
	return pending
}

// PrepareEpoch removes snapshot bodies that have already been committed.
// Call CommitEpoch only after the epoch event is durably appended.
func (t *Tracker) PrepareEpoch(epoch *RequestEpoch) {
	if t == nil || epoch == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	markSnapshotReused(&epoch.SystemPromptSnapshot, t.known)
	markSnapshotReused(&epoch.ToolCatalogSnapshot, t.known)
}

func markSnapshotReused(snapshot *Snapshot, known map[string]struct{}) {
	if snapshot == nil || snapshot.Digest == "" {
		return
	}
	if _, ok := known[snapshot.Digest]; !ok {
		return
	}
	snapshot.Content = nil
	snapshot.Reused = true
}

func (t *Tracker) CommitEpoch(epoch RequestEpoch) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.recordEpochLocked(epoch)
	t.mu.Unlock()
}

func (t *Tracker) recordEpochLocked(epoch RequestEpoch) {
	t.epochs[epoch.EpochID] = epoch.RequestDigest
	if epoch.SystemPromptSnapshot.Digest != "" {
		t.known[epoch.SystemPromptSnapshot.Digest] = struct{}{}
	}
	if epoch.ToolCatalogSnapshot.Digest != "" {
		t.known[epoch.ToolCatalogSnapshot.Digest] = struct{}{}
	}
	for _, id := range epoch.HookContextMessageIDs {
		t.consumed[id] = struct{}{}
	}
}

type epochLink struct {
	EpochID       string `json:"epoch_id"`
	RequestDigest string `json:"request_digest"`
	Purpose       string `json:"purpose,omitempty"`
}

func (t *Tracker) recordRequestLinkLocked(payload any) error {
	link, err := decodeEpochLink(payload)
	if err != nil {
		return fmt.Errorf("provenance: decode llm.requested link: %w", err)
	}
	if err := t.validateKnownEpochLinkLocked("llm.requested", link); err != nil {
		return err
	}
	if _, duplicate := t.requested[link.EpochID]; duplicate {
		return fmt.Errorf("provenance: duplicate llm.requested for epoch %q", link.EpochID)
	}
	t.requested[link.EpochID] = struct{}{}
	return nil
}

func (t *Tracker) recordResponseLinkLocked(payload any) error {
	link, err := decodeEpochLink(payload)
	if err != nil {
		return fmt.Errorf("provenance: decode llm.responded link: %w", err)
	}
	if err := t.validateKnownEpochLinkLocked("llm.responded", link); err != nil {
		return err
	}
	if _, requested := t.requested[link.EpochID]; !requested {
		return fmt.Errorf("provenance: llm.responded references epoch %q before llm.requested", link.EpochID)
	}
	if _, duplicate := t.responded[link.EpochID]; duplicate {
		return fmt.Errorf("provenance: duplicate llm.responded for epoch %q", link.EpochID)
	}
	t.responded[link.EpochID] = struct{}{}
	return nil
}

func (t *Tracker) validateRetryLinkLocked(payload any) error {
	var link epochLink
	if err := decodePayload(payload, &link); err != nil {
		return fmt.Errorf("provenance: decode llm.retry link: %w", err)
	}
	if link.EpochID == "" && link.RequestDigest == "" {
		if link.Purpose == "turn" {
			return fmt.Errorf("provenance: turn llm.retry requires epoch_id and request_digest")
		}
		return nil
	}
	if link.EpochID == "" || link.RequestDigest == "" {
		return fmt.Errorf("provenance: llm.retry epoch_id and request_digest must be set together")
	}
	if err := t.validateKnownEpochLinkLocked("llm.retry", link); err != nil {
		return err
	}
	if _, requested := t.requested[link.EpochID]; !requested {
		return fmt.Errorf("provenance: llm.retry references epoch %q before llm.requested", link.EpochID)
	}
	return nil
}

func decodeEpochLink(payload any) (epochLink, error) {
	var link epochLink
	if err := decodePayload(payload, &link); err != nil {
		return epochLink{}, err
	}
	if link.EpochID == "" || link.RequestDigest == "" {
		return epochLink{}, fmt.Errorf("epoch_id and request_digest are required")
	}
	return link, nil
}

func (t *Tracker) validateKnownEpochLinkLocked(eventType string, link epochLink) error {
	digest, exists := t.epochs[link.EpochID]
	if !exists {
		return fmt.Errorf("provenance: %s references unknown epoch %q", eventType, link.EpochID)
	}
	if digest != link.RequestDigest {
		return fmt.Errorf("provenance: %s digest does not match epoch %q", eventType, link.EpochID)
	}
	return nil
}

func (t *Tracker) validateSnapshotReferencesLocked(epoch RequestEpoch) error {
	for name, snapshot := range map[string]Snapshot{
		"system_prompt": epoch.SystemPromptSnapshot,
		"tool_catalog":  epoch.ToolCatalogSnapshot,
	} {
		if len(snapshot.Content) > 0 {
			if actual := digest(snapshot.Content); actual != snapshot.Digest {
				return fmt.Errorf("provenance: %s snapshot digest mismatch: got %s want %s", name, actual, snapshot.Digest)
			}
			continue
		}
		if snapshot.Omitted != "" {
			continue
		}
		if snapshot.Reused {
			if _, ok := t.known[snapshot.Digest]; ok {
				continue
			}
			return fmt.Errorf("provenance: %s snapshot references unknown digest %q", name, snapshot.Digest)
		}
		return fmt.Errorf("provenance: %s snapshot content is missing", name)
	}
	return nil
}

func ValidateHookContextQueued(payload HookContextQueuedPayload) error {
	if len(payload.Messages) == 0 {
		return fmt.Errorf("provenance: queued hook context requires messages")
	}
	seen := make(map[string]struct{}, len(payload.Messages))
	for _, message := range payload.Messages {
		if message.ID == "" || message.Kind != llm.MessageKindRuntimeContext || message.Role != llm.RoleUser {
			return fmt.Errorf("provenance: queued hook context requires stable user runtime-context messages")
		}
		if _, duplicate := seen[message.ID]; duplicate {
			return fmt.Errorf("provenance: queued hook context id %q is duplicated", message.ID)
		}
		seen[message.ID] = struct{}{}
	}
	if raw, err := json.Marshal(payload); err != nil {
		return fmt.Errorf("provenance: encode queued hook context: %w", err)
	} else if len(raw) > MaxHookContextBatchBytes {
		return fmt.Errorf("provenance: queued hook context exceeds %d bytes", MaxHookContextBatchBytes)
	}
	return nil
}

func ValidateRequestEpoch(payload RequestEpochPayload) error {
	epoch := payload.Epoch
	if epoch.EpochID == "" || epoch.Iter < 0 || epoch.RequestDigest == "" || epoch.HistoryDigest == "" {
		return fmt.Errorf("provenance: request epoch requires epoch_id, non-negative iter, request_digest, and history_digest")
	}
	if len(epoch.HistoryMessageIDs) == 0 || len(epoch.Messages) != len(epoch.HistoryMessageIDs) {
		return fmt.Errorf("provenance: request epoch requires matching history message ids and message refs")
	}
	for index, id := range epoch.HistoryMessageIDs {
		if id == "" || epoch.Messages[index].ID != id || epoch.Messages[index].ContentDigest == "" {
			return fmt.Errorf("provenance: request epoch history message ref is invalid")
		}
	}
	if raw, err := json.Marshal(payload); err != nil {
		return fmt.Errorf("provenance: encode request epoch: %w", err)
	} else if len(raw) > MaxRequestEpochBytes {
		return fmt.Errorf("provenance: request epoch exceeds %d bytes", MaxRequestEpochBytes)
	}
	return VerifyRequestEpoch(epoch)
}

// VerifyRequestEpoch reconstructs the logical request digest from the durable
// selection record. Inline snapshot bodies are checked when present; reused or
// size-omitted bodies are resolved by journal replay rather than this method.
func VerifyRequestEpoch(epoch RequestEpoch) error {
	for name, snapshot := range map[string]Snapshot{
		"system_prompt": epoch.SystemPromptSnapshot,
		"tool_catalog":  epoch.ToolCatalogSnapshot,
	} {
		if len(snapshot.Content) > 0 {
			if actual := digest(snapshot.Content); actual != snapshot.Digest {
				return fmt.Errorf("provenance: %s snapshot digest mismatch: got %s want %s", name, actual, snapshot.Digest)
			}
		}
	}
	reconstructed, err := requestDigest(epoch)
	if err != nil {
		return err
	}
	if reconstructed != epoch.RequestDigest {
		return fmt.Errorf("provenance: request digest mismatch: got %s want %s", reconstructed, epoch.RequestDigest)
	}
	return nil
}
