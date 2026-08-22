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
	"net/url"
	"strings"
	"sync"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
)

const (
	RequestEpochType           = "provider.request_epoch"
	PolicyContextQueuedType    = "provider.policy_context.queued"
	SchemaVersion              = 1
	MaxInlineSnapshotBytes     = 256 * 1024
	MaxPolicyContextBatchBytes = 1 * 1024 * 1024
	MaxRequestEpochBytes       = 2 * 1024 * 1024
)

type SafeProvider struct {
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
}

func SafeProviderFromProfile(profile llm.ProviderProfile) SafeProvider {
	return SafeProvider{
		ID:                    profile.ID,
		Protocol:              profile.Protocol,
		Model:                 profile.Model,
		EndpointDigest:        safeEndpointDigest(profile.BaseURL),
		HeaderDigest:          safeStringMapDigest(profile.Headers),
		QueryDigest:           safeStringMapDigest(profile.Query),
		ThinkingEffort:        profile.ThinkingEffort,
		Capabilities:          profile.Capabilities,
		ReasoningReplayFields: append([]string(nil), profile.Compat.ReasoningReplayFields...),
		CodexTransport:        profile.Compat.CodexTransport,
	}
}

func safeStringMapDigest(values map[string]string) string {
	if len(values) == 0 {
		return ""
	}
	raw, _ := json.Marshal(values)
	return digest(raw)
}

func safeEndpointDigest(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if parsed, err := url.Parse(raw); err == nil {
		parsed.User = nil
		parsed.RawQuery = ""
		parsed.ForceQuery = false
		parsed.Fragment = ""
		parsed.Scheme = strings.ToLower(parsed.Scheme)
		parsed.Host = strings.ToLower(parsed.Host)
		raw = parsed.String()
	}
	return digest([]byte(raw))
}

type Snapshot struct {
	Digest  string          `json:"digest"`
	Bytes   int             `json:"bytes"`
	Content json.RawMessage `json:"content,omitempty"`
	Omitted string          `json:"omitted,omitempty"`
	Reused  bool            `json:"reused,omitempty"`
	Parts   []Snapshot      `json:"parts,omitempty"`
	Joiner  string          `json:"joiner,omitempty"`
}

type CompactionSelection struct {
	MarkerMessageID    string   `json:"marker_message_id,omitempty"`
	PreviousSummaryID  string   `json:"previous_summary_id,omitempty"`
	TailStartMessageID string   `json:"tail_start_message_id,omitempty"`
	RetainedMessageIDs []string `json:"retained_message_ids,omitempty"`
}

type SafeCachePolicy struct {
	StablePrefixKeyDigest string `json:"stable_prefix_key_digest,omitempty"`
	RetentionDigest       string `json:"retention_digest,omitempty"`
}

func SafeCachePolicyFrom(policy llm.CachePolicy) SafeCachePolicy {
	return SafeCachePolicy{
		StablePrefixKeyDigest: optionalDigest(policy.StablePrefixKey),
		RetentionDigest:       optionalDigest(policy.Retention),
	}
}

func optionalDigest(value string) string {
	if value == "" {
		return ""
	}
	return digest([]byte(value))
}

type MessageRef struct {
	ID            string    `json:"id"`
	Source        string    `json:"source"`
	ContentDigest string    `json:"content_digest"`
	Snapshot      *Snapshot `json:"snapshot,omitempty"`
}

type RequestInput struct {
	Purpose                 string              `json:"purpose,omitempty"`
	Provider                SafeProvider        `json:"provider"`
	ContextWindow           int                 `json:"context_window,omitempty"`
	MaxOutputTokens         int                 `json:"max_output_tokens,omitempty"`
	CachePolicy             SafeCachePolicy     `json:"cache_policy,omitempty"`
	SystemPrompt            string              `json:"system_prompt,omitempty"`
	SystemPromptParts       []string            `json:"system_prompt_parts,omitempty"`
	SystemPromptJoiner      string              `json:"system_prompt_joiner,omitempty"`
	Tools                   []llm.ToolSpec      `json:"tools,omitempty"`
	History                 []llm.Message       `json:"history"`
	Compaction              CompactionSelection `json:"compaction,omitempty"`
	PolicyContextMessageIDs []string            `json:"policy_context_message_ids,omitempty"`
}

type RequestEpoch struct {
	EpochID                 string              `json:"epoch_id"`
	Purpose                 string              `json:"purpose"`
	Iter                    int                 `json:"iter"`
	Attempt                 int                 `json:"attempt"`
	Provider                SafeProvider        `json:"provider"`
	ContextWindow           int                 `json:"context_window,omitempty"`
	MaxOutputTokens         int                 `json:"max_output_tokens,omitempty"`
	CachePolicy             SafeCachePolicy     `json:"cache_policy,omitempty"`
	SystemPromptSnapshot    Snapshot            `json:"system_prompt"`
	ToolCatalogSnapshot     Snapshot            `json:"tool_catalog"`
	HistoryDigest           string              `json:"history_digest"`
	HistoryMessageIDs       []string            `json:"history_message_ids"`
	Messages                []MessageRef        `json:"messages"`
	Compaction              CompactionSelection `json:"compaction,omitempty"`
	PolicyContextMessageIDs []string            `json:"policy_context_message_ids,omitempty"`
	RequestDigest           string              `json:"request_digest"`
}

type RequestEpochPayload struct {
	Epoch RequestEpoch `json:"epoch"`
}

type PolicyContextQueuedPayload struct {
	Messages []llm.Message `json:"messages"`
}

type digestEnvelope struct {
	SchemaVersion           int                 `json:"schema_version"`
	Purpose                 string              `json:"purpose"`
	Provider                SafeProvider        `json:"provider"`
	ContextWindow           int                 `json:"context_window,omitempty"`
	MaxOutputTokens         int                 `json:"max_output_tokens,omitempty"`
	CachePolicy             SafeCachePolicy     `json:"cache_policy,omitempty"`
	SystemPromptDigest      string              `json:"system_prompt_digest"`
	ToolCatalogDigest       string              `json:"tool_catalog_digest"`
	HistoryDigest           string              `json:"history_digest"`
	HistoryMessageIDs       []string            `json:"history_message_ids"`
	Messages                []messageDigestRef  `json:"messages"`
	Compaction              CompactionSelection `json:"compaction,omitempty"`
	PolicyContextMessageIDs []string            `json:"policy_context_message_ids,omitempty"`
}

type messageDigestRef struct {
	ID            string `json:"id"`
	Source        string `json:"source"`
	ContentDigest string `json:"content_digest"`
}

func BuildRequestEpoch(input RequestInput) (RequestEpoch, error) {
	purpose := input.Purpose
	if purpose == "" {
		purpose = "turn"
	}
	if purpose != "turn" && purpose != "compaction" {
		return RequestEpoch{}, fmt.Errorf("provenance: request purpose must be turn or compaction")
	}
	historyIDs := make([]string, len(input.History))
	messageRefs := make([]MessageRef, len(input.History))
	policyIDs := make(map[string]struct{}, len(input.PolicyContextMessageIDs))
	for _, id := range input.PolicyContextMessageIDs {
		policyIDs[id] = struct{}{}
	}
	for index, message := range input.History {
		if message.ID == "" {
			return RequestEpoch{}, fmt.Errorf("provenance: history message id is required at index %d", index)
		}
		historyIDs[index] = message.ID
		messageSnapshot, err := newSnapshot(message)
		if err != nil {
			return RequestEpoch{}, fmt.Errorf("provenance: history message digest at index %d: %w", index, err)
		}
		source := messageSource(message, policyIDs, purpose)
		messageRefs[index] = MessageRef{ID: message.ID, Source: source, ContentDigest: messageSnapshot.Digest}
		if messageSnapshotRequired(source) {
			if messageSnapshot.Omitted != "" {
				return RequestEpoch{}, fmt.Errorf("provenance: derived message snapshot at index %d exceeds %d bytes", index, MaxInlineSnapshotBytes)
			}
			messageRefs[index].Snapshot = &messageSnapshot
		}
	}
	systemSnapshot, err := newSystemPromptSnapshot(input.SystemPrompt, input.SystemPromptParts, input.SystemPromptJoiner)
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
		SchemaVersion:           SchemaVersion,
		Purpose:                 purpose,
		Provider:                cloneSafeProvider(input.Provider),
		ContextWindow:           input.ContextWindow,
		MaxOutputTokens:         input.MaxOutputTokens,
		CachePolicy:             input.CachePolicy,
		SystemPromptDigest:      systemSnapshot.Digest,
		ToolCatalogDigest:       toolSnapshot.Digest,
		HistoryDigest:           historyDigest,
		HistoryMessageIDs:       append([]string(nil), historyIDs...),
		Messages:                messageDigestRefs(messageRefs),
		Compaction:              cloneCompaction(input.Compaction),
		PolicyContextMessageIDs: append([]string(nil), input.PolicyContextMessageIDs...),
	}
	epoch := RequestEpoch{
		Purpose:                 purpose,
		Provider:                envelope.Provider,
		ContextWindow:           input.ContextWindow,
		MaxOutputTokens:         input.MaxOutputTokens,
		CachePolicy:             input.CachePolicy,
		SystemPromptSnapshot:    systemSnapshot,
		ToolCatalogSnapshot:     toolSnapshot,
		HistoryDigest:           historyDigest,
		HistoryMessageIDs:       historyIDs,
		Messages:                messageRefs,
		Compaction:              envelope.Compaction,
		PolicyContextMessageIDs: envelope.PolicyContextMessageIDs,
	}
	epoch.RequestDigest, err = requestDigest(epoch)
	if err != nil {
		return RequestEpoch{}, err
	}
	return epoch, nil
}

func requestDigest(epoch RequestEpoch) (string, error) {
	envelope := digestEnvelope{
		SchemaVersion:           SchemaVersion,
		Purpose:                 epoch.Purpose,
		Provider:                cloneSafeProvider(epoch.Provider),
		ContextWindow:           epoch.ContextWindow,
		MaxOutputTokens:         epoch.MaxOutputTokens,
		CachePolicy:             epoch.CachePolicy,
		SystemPromptDigest:      epoch.SystemPromptSnapshot.Digest,
		ToolCatalogDigest:       epoch.ToolCatalogSnapshot.Digest,
		HistoryDigest:           epoch.HistoryDigest,
		HistoryMessageIDs:       append([]string(nil), epoch.HistoryMessageIDs...),
		Messages:                messageDigestRefs(epoch.Messages),
		Compaction:              cloneCompaction(epoch.Compaction),
		PolicyContextMessageIDs: append([]string(nil), epoch.PolicyContextMessageIDs...),
	}
	canonical, err := json.Marshal(envelope)
	if err != nil {
		return "", fmt.Errorf("provenance: request digest: %w", err)
	}
	return digest(canonical), nil
}

func messageDigestRefs(messages []MessageRef) []messageDigestRef {
	refs := make([]messageDigestRef, len(messages))
	for index, message := range messages {
		refs[index] = messageDigestRef{
			ID:            message.ID,
			Source:        message.Source,
			ContentDigest: message.ContentDigest,
		}
	}
	return refs
}

func messageSource(message llm.Message, policyIDs map[string]struct{}, purpose string) string {
	if _, ok := policyIDs[message.ID]; ok {
		return "policy_context"
	}
	if purpose == "compaction" {
		return "compaction_input"
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

func messageSnapshotRequired(source string) bool {
	return source == "runtime_context" || source == "model_change" || source == "compaction_input"
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

func newSystemPromptSnapshot(prompt string, parts []string, joiner string) (Snapshot, error) {
	if len(parts) == 0 {
		return newSnapshot(prompt)
	}
	if strings.Join(parts, joiner) != prompt {
		return Snapshot{}, fmt.Errorf("system prompt parts do not reconstruct the provider prompt")
	}
	plain, err := newSnapshot(prompt)
	if err != nil {
		return Snapshot{}, err
	}
	if plain.Omitted != "" {
		return plain, nil
	}
	partSnapshots := make([]Snapshot, len(parts))
	partDigests := make([]string, len(parts))
	for index, part := range parts {
		snapshot, err := newSnapshot(part)
		if err != nil {
			return Snapshot{}, err
		}
		if snapshot.Omitted != "" {
			return Snapshot{}, fmt.Errorf("system prompt part at index %d exceeds %d bytes", index, MaxInlineSnapshotBytes)
		}
		partSnapshots[index] = snapshot
		partDigests[index] = snapshot.Digest
	}
	composition, err := json.Marshal(struct {
		Parts  []string `json:"parts"`
		Joiner string   `json:"joiner"`
	}{Parts: partDigests, Joiner: joiner})
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{
		Digest: digest(composition),
		Bytes:  plain.Bytes,
		Parts:  partSnapshots,
		Joiner: joiner,
	}, nil
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
	policyIDs map[string]struct{}
	known     map[string]struct{}
	epochs    map[string]string
	purposes  map[string]string
	requested map[string]struct{}
	terminal  map[string]string
}

func NewTracker() *Tracker {
	return &Tracker{
		policyIDs: make(map[string]struct{}),
		known:     make(map[string]struct{}),
		epochs:    make(map[string]string),
		purposes:  make(map[string]string),
		requested: make(map[string]struct{}),
		terminal:  make(map[string]string),
	}
}

func Recover(journal []events.Event) (*Tracker, error) {
	tracker := NewTracker()
	for _, event := range journal {
		if err := tracker.ReplayEvent(event); err != nil {
			return nil, err
		}
	}
	return tracker, nil
}

func (t *Tracker) ReplayEvent(event events.Event) error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	switch event.Type {
	case PolicyContextQueuedType:
		var payload PolicyContextQueuedPayload
		if err := decodePayload(event.Payload, &payload); err != nil {
			return fmt.Errorf("provenance: recover queued policy context: %w", err)
		}
		if err := ValidatePolicyContextQueued(payload); err != nil {
			return err
		}
		for _, message := range payload.Messages {
			if _, duplicate := t.policyIDs[message.ID]; duplicate {
				return fmt.Errorf("provenance: duplicate queued policy context id %q", message.ID)
			}
			t.policyIDs[message.ID] = struct{}{}
			t.queued = append(t.queued, message)
		}
	case RequestEpochType:
		var payload RequestEpochPayload
		if err := decodePayload(event.Payload, &payload); err != nil {
			return fmt.Errorf("provenance: recover request epoch: %w", err)
		}
		if err := ValidateRequestEpoch(payload); err != nil {
			return err
		}
		if err := t.validateSnapshotReferencesLocked(payload.Epoch); err != nil {
			return err
		}
		if _, duplicate := t.epochs[payload.Epoch.EpochID]; duplicate {
			return fmt.Errorf("provenance: duplicate request epoch id %q", payload.Epoch.EpochID)
		}
		t.recordEpochLocked(payload.Epoch)
	case "llm.requested":
		if err := t.recordRequestLinkLocked(event.Payload); err != nil {
			return err
		}
	case "llm.responded":
		if err := t.recordResponseLinkLocked(event.Payload); err != nil {
			return err
		}
	case "llm.errored":
		if err := t.recordTerminalLinkLocked("llm.errored", event.Payload, "turn"); err != nil {
			return err
		}
	case "llm.retry":
		if err := t.validateRetryLinkLocked(event.Payload); err != nil {
			return err
		}
	case "context.compact.summary_responded", "context.compact.summary_errored":
		if err := t.recordTerminalLinkLocked(event.Type, event.Payload, "compaction"); err != nil {
			return err
		}
	}
	return nil
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
	t.policyIDs[message.ID] = struct{}{}
	t.queued = append(t.queued, message)
	t.mu.Unlock()
}

func (t *Tracker) PendingPolicyContext() []llm.Message {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.pendingPolicyContextLocked()
}

func (t *Tracker) pendingPolicyContextLocked() []llm.Message {
	pending := make([]llm.Message, 0, len(t.queued))
	pending = append(pending, t.queued...)
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
	for index := range epoch.Messages {
		if epoch.Messages[index].Snapshot != nil {
			markSnapshotReused(epoch.Messages[index].Snapshot, t.known)
		}
	}
}

func markSnapshotReused(snapshot *Snapshot, known map[string]struct{}) {
	if snapshot == nil || snapshot.Digest == "" {
		return
	}
	if snapshot.Omitted != "" {
		return
	}
	if _, ok := known[snapshot.Digest]; !ok {
		for index := range snapshot.Parts {
			markSnapshotReused(&snapshot.Parts[index], known)
		}
		return
	}
	snapshot.Content = nil
	snapshot.Parts = nil
	snapshot.Joiner = ""
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
	t.purposes[epoch.EpochID] = epoch.Purpose
	recordKnownSnapshot(epoch.SystemPromptSnapshot, t.known)
	recordKnownSnapshot(epoch.ToolCatalogSnapshot, t.known)
	for _, message := range epoch.Messages {
		if message.Snapshot != nil {
			recordKnownSnapshot(*message.Snapshot, t.known)
		}
	}
	t.releaseConsumedPolicyContextLocked(epoch.PolicyContextMessageIDs)
}

func (t *Tracker) releaseConsumedPolicyContextLocked(ids []string) {
	if len(ids) == 0 || len(t.queued) == 0 {
		return
	}
	consumed := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		consumed[id] = struct{}{}
	}
	write := 0
	for _, message := range t.queued {
		if _, ok := consumed[message.ID]; ok {
			continue
		}
		t.queued[write] = message
		write++
	}
	clear(t.queued[write:])
	t.queued = t.queued[:write]
}

func recordKnownSnapshot(snapshot Snapshot, known map[string]struct{}) {
	if snapshot.Digest != "" {
		known[snapshot.Digest] = struct{}{}
	}
	for _, part := range snapshot.Parts {
		recordKnownSnapshot(part, known)
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
	if err := t.validatePurposeLocked("llm.requested", link.EpochID, link.Purpose); err != nil {
		return err
	}
	if _, duplicate := t.requested[link.EpochID]; duplicate {
		return fmt.Errorf("provenance: duplicate llm.requested for epoch %q", link.EpochID)
	}
	t.requested[link.EpochID] = struct{}{}
	return nil
}

func (t *Tracker) recordResponseLinkLocked(payload any) error {
	return t.recordTerminalLinkLocked("llm.responded", payload, "turn")
}

func (t *Tracker) validateRetryLinkLocked(payload any) error {
	link, err := decodeEpochLink(payload)
	if err != nil {
		return fmt.Errorf("provenance: decode llm.retry link: %w", err)
	}
	if err := t.validateKnownEpochLinkLocked("llm.retry", link); err != nil {
		return err
	}
	if err := t.validatePurposeLocked("llm.retry", link.EpochID, link.Purpose); err != nil {
		return err
	}
	if _, requested := t.requested[link.EpochID]; !requested {
		return fmt.Errorf("provenance: llm.retry references epoch %q before llm.requested", link.EpochID)
	}
	return nil
}

func (t *Tracker) recordTerminalLinkLocked(eventType string, payload any, expectedPurpose string) error {
	link, err := decodeEpochLink(payload)
	if err != nil {
		return fmt.Errorf("provenance: decode %s link: %w", eventType, err)
	}
	if err := t.validateKnownEpochLinkLocked(eventType, link); err != nil {
		return err
	}
	if err := t.validateExpectedPurposeLocked(eventType, link.EpochID, expectedPurpose); err != nil {
		return err
	}
	if _, requested := t.requested[link.EpochID]; !requested {
		return fmt.Errorf("provenance: %s references epoch %q before llm.requested", eventType, link.EpochID)
	}
	if previous, duplicate := t.terminal[link.EpochID]; duplicate {
		return fmt.Errorf("provenance: %s duplicates terminal event %s for epoch %q", eventType, previous, link.EpochID)
	}
	t.terminal[link.EpochID] = eventType
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

func (t *Tracker) validatePurposeLocked(eventType, epochID, actual string) error {
	expected := t.purposes[epochID]
	if actual == "" || actual != expected {
		return fmt.Errorf("provenance: %s purpose %q does not match epoch %q purpose %q", eventType, actual, epochID, expected)
	}
	return nil
}

func (t *Tracker) validateExpectedPurposeLocked(eventType, epochID, expected string) error {
	actual := t.purposes[epochID]
	if actual != expected {
		return fmt.Errorf("provenance: %s requires %s epoch, got %q for epoch %q", eventType, expected, actual, epochID)
	}
	return nil
}

func (t *Tracker) validateSnapshotReferencesLocked(epoch RequestEpoch) error {
	for name, snapshot := range map[string]Snapshot{
		"system_prompt": epoch.SystemPromptSnapshot,
		"tool_catalog":  epoch.ToolCatalogSnapshot,
	} {
		if err := validateSnapshotReference(snapshot, t.known); err != nil {
			return fmt.Errorf("provenance: %s snapshot: %w", name, err)
		}
	}
	for index, message := range epoch.Messages {
		if !messageSnapshotRequired(message.Source) {
			if message.Snapshot != nil {
				return fmt.Errorf("provenance: message snapshot at index %d is only valid for derived messages", index)
			}
			continue
		}
		if message.Snapshot == nil {
			return fmt.Errorf("provenance: derived message snapshot at index %d is missing", index)
		}
		if err := validateSnapshotReference(*message.Snapshot, t.known); err != nil {
			return fmt.Errorf("provenance: derived message snapshot at index %d: %w", index, err)
		}
	}
	return nil
}

func validateSnapshotReference(snapshot Snapshot, known map[string]struct{}) error {
	if err := validateSnapshotShape(snapshot); err != nil {
		return err
	}
	if len(snapshot.Content) > 0 {
		if actual := digest(snapshot.Content); actual != snapshot.Digest {
			return fmt.Errorf("digest mismatch: got %s want %s", actual, snapshot.Digest)
		}
		return nil
	}
	if snapshot.Omitted != "" {
		return nil
	}
	if snapshot.Reused {
		if _, ok := known[snapshot.Digest]; ok {
			return nil
		}
		return fmt.Errorf("references unknown digest %q", snapshot.Digest)
	}
	if len(snapshot.Parts) > 0 {
		for index, part := range snapshot.Parts {
			if err := validateSnapshotReference(part, known); err != nil {
				return fmt.Errorf("part %d: %w", index, err)
			}
		}
		if actual, err := compositeSnapshotDigest(snapshot.Parts, snapshot.Joiner); err != nil {
			return err
		} else if actual != snapshot.Digest {
			return fmt.Errorf("composition digest mismatch: got %s want %s", actual, snapshot.Digest)
		}
		return nil
	}
	return fmt.Errorf("content is missing")
}

func compositeSnapshotDigest(parts []Snapshot, joiner string) (string, error) {
	partDigests := make([]string, len(parts))
	for index, part := range parts {
		if part.Digest == "" {
			return "", fmt.Errorf("part %d digest is missing", index)
		}
		partDigests[index] = part.Digest
	}
	composition, err := json.Marshal(struct {
		Parts  []string `json:"parts"`
		Joiner string   `json:"joiner"`
	}{Parts: partDigests, Joiner: joiner})
	if err != nil {
		return "", err
	}
	return digest(composition), nil
}

func ValidatePolicyContextQueued(payload PolicyContextQueuedPayload) error {
	if len(payload.Messages) == 0 {
		return fmt.Errorf("provenance: queued policy context requires messages")
	}
	seen := make(map[string]struct{}, len(payload.Messages))
	for _, message := range payload.Messages {
		if message.ID == "" || message.Kind != llm.MessageKindRuntimeContext || message.Role != llm.RoleUser {
			return fmt.Errorf("provenance: queued policy context requires stable user runtime-context messages")
		}
		if _, duplicate := seen[message.ID]; duplicate {
			return fmt.Errorf("provenance: queued policy context id %q is duplicated", message.ID)
		}
		seen[message.ID] = struct{}{}
	}
	if raw, err := json.Marshal(payload); err != nil {
		return fmt.Errorf("provenance: encode queued policy context: %w", err)
	} else if len(raw) > MaxPolicyContextBatchBytes {
		return fmt.Errorf("provenance: queued policy context exceeds %d bytes", MaxPolicyContextBatchBytes)
	}
	return nil
}

func ValidateRequestEpoch(payload RequestEpochPayload) error {
	epoch := payload.Epoch
	if epoch.EpochID == "" || epoch.Iter < 0 || epoch.RequestDigest == "" || epoch.HistoryDigest == "" {
		return fmt.Errorf("provenance: request epoch requires epoch_id, non-negative iter, request_digest, and history_digest")
	}
	if epoch.Purpose != "turn" && epoch.Purpose != "compaction" {
		return fmt.Errorf("provenance: request epoch purpose must be turn or compaction")
	}
	if len(epoch.HistoryMessageIDs) == 0 || len(epoch.Messages) != len(epoch.HistoryMessageIDs) {
		return fmt.Errorf("provenance: request epoch requires matching history message ids and message refs")
	}
	for index, id := range epoch.HistoryMessageIDs {
		message := epoch.Messages[index]
		if id == "" || message.ID != id || message.ContentDigest == "" {
			return fmt.Errorf("provenance: request epoch history message ref is invalid")
		}
		if messageSnapshotRequired(message.Source) {
			if message.Snapshot == nil || message.Snapshot.Digest != message.ContentDigest {
				return fmt.Errorf("provenance: request epoch derived message snapshot is required and must match message content")
			}
		} else if message.Snapshot != nil {
			return fmt.Errorf("provenance: request epoch message snapshots are only valid for derived messages")
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
		if err := verifySnapshot(snapshot); err != nil {
			return fmt.Errorf("provenance: %s snapshot: %w", name, err)
		}
	}
	for index, message := range epoch.Messages {
		if message.Snapshot == nil {
			continue
		}
		if err := verifySnapshot(*message.Snapshot); err != nil {
			return fmt.Errorf("provenance: message snapshot at index %d: %w", index, err)
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

func verifySnapshot(snapshot Snapshot) error {
	if err := validateSnapshotShape(snapshot); err != nil {
		return err
	}
	if len(snapshot.Content) > 0 {
		if actual := digest(snapshot.Content); actual != snapshot.Digest {
			return fmt.Errorf("digest mismatch: got %s want %s", actual, snapshot.Digest)
		}
	}
	if len(snapshot.Parts) > 0 {
		for index, part := range snapshot.Parts {
			if err := verifySnapshot(part); err != nil {
				return fmt.Errorf("part %d: %w", index, err)
			}
		}
		actual, err := compositeSnapshotDigest(snapshot.Parts, snapshot.Joiner)
		if err != nil {
			return err
		}
		if actual != snapshot.Digest {
			return fmt.Errorf("composition digest mismatch: got %s want %s", actual, snapshot.Digest)
		}
	}
	return nil
}

func validateSnapshotShape(snapshot Snapshot) error {
	representations := 0
	if len(snapshot.Content) > 0 {
		representations++
	}
	if snapshot.Omitted != "" {
		representations++
	}
	if snapshot.Reused {
		representations++
	}
	if len(snapshot.Parts) > 0 {
		representations++
	}
	if representations != 1 {
		return fmt.Errorf("must have exactly one content, omitted, reused, or parts representation")
	}
	if len(snapshot.Parts) == 0 && snapshot.Joiner != "" {
		return fmt.Errorf("joiner requires snapshot parts")
	}
	return nil
}
