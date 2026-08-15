package runtime

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/prompt"
	"github.com/juex-ai/juex/internal/provenance"
)

func (e *Engine) checkpointProviderRequestLocked(
	turnID string,
	prepared preparedTurnContext,
	request providerTurnRequest,
	candidate ModelCandidate,
	cachePolicy llm.CachePolicy,
	attempt int,
) (provenance.RequestEpoch, error) {
	hookIDs := make([]string, 0, len(request.hookContext))
	for _, message := range request.hookContext {
		hookIDs = append(hookIDs, message.ID)
	}
	return e.checkpointProviderRequestEpochLocked(turnID, request.iter, attempt, provenance.RequestInput{
		Purpose:               "turn",
		Provider:              candidateProvenance(candidate),
		ContextWindow:         candidateContextWindow(candidate, e.ContextWindow),
		MaxOutputTokens:       candidateMaxOutputTokens(candidate, e.MaxOutputTokens),
		CachePolicy:           provenance.SafeCachePolicyFrom(cachePolicy),
		SystemPrompt:          prepared.systemPrompt,
		SystemPromptParts:     promptSectionTexts(prepared.promptSections),
		SystemPromptJoiner:    prompt.SectionSeparator,
		Tools:                 prepared.tools,
		History:               request.history,
		Compaction:            requestCompactionSelection(request.history),
		HookContextMessageIDs: hookIDs,
	})
}

func promptSectionTexts(sections []prompt.Section) []string {
	texts := make([]string, 0, len(sections))
	for _, section := range sections {
		if section.Text != "" {
			texts = append(texts, section.Text)
		}
	}
	return texts
}

func (e *Engine) checkpointProviderRequestEpochLocked(
	turnID string,
	iter int,
	attempt int,
	input provenance.RequestInput,
) (provenance.RequestEpoch, error) {
	epoch, err := provenance.BuildRequestEpoch(input)
	if err != nil {
		return provenance.RequestEpoch{}, err
	}
	epoch.EpochID = randomProvenanceID("epoch-")
	epoch.Iter = iter
	epoch.Attempt = attempt
	tracker := e.requestProvenanceTracker()
	tracker.PrepareEpoch(&epoch)
	payload := provenance.RequestEpochPayload{Epoch: epoch}
	if err := provenance.ValidateRequestEpoch(payload); err != nil {
		return provenance.RequestEpoch{}, err
	}
	if err := e.emit(events.Event{
		ID:      epoch.EpochID,
		Type:    provenance.RequestEpochType,
		TurnID:  turnID,
		Payload: payload,
	}); err != nil {
		return provenance.RequestEpoch{}, fmt.Errorf("commit provider request epoch: %w", err)
	}
	tracker.CommitEpoch(epoch)
	e.syncPendingHookContextFromTracker(tracker)
	return epoch, nil
}

func (e *Engine) providerProvenanceLocked(provider llm.Provider) provenance.SafeProvider {
	for _, candidate := range e.ModelCandidates {
		if candidate.Provider == provider {
			return candidateProvenance(candidate)
		}
	}
	if provider == e.SummaryProvider {
		descriptor := e.SummaryProvenance
		if descriptor.ID != "" || descriptor.Model != "" || descriptor.EndpointDigest != "" {
			return descriptor
		}
	}
	if provider == nil {
		return provenance.SafeProvider{}
	}
	name := provider.Name()
	return provenance.SafeProvider{ID: name, Model: name}
}

func randomProvenanceID(prefix string) string {
	var value [12]byte
	_, _ = rand.Read(value[:])
	return prefix + hex.EncodeToString(value[:])
}

func candidateProvenance(candidate ModelCandidate) provenance.SafeProvider {
	descriptor := candidate.Provenance
	if descriptor.ID == "" && candidate.Provider != nil {
		descriptor.ID = candidate.Provider.Name()
	}
	if descriptor.Model == "" {
		descriptor.Model = candidate.Ref
	}
	return descriptor
}

func requestCompactionSelection(history []llm.Message) provenance.CompactionSelection {
	for index := len(history) - 1; index >= 0; index-- {
		message := history[index]
		if message.Kind != llm.MessageKindCompact || message.Compaction == nil {
			continue
		}
		return provenance.CompactionSelection{
			MarkerMessageID:    message.ID,
			PreviousSummaryID:  message.Compaction.PreviousSummaryID,
			TailStartMessageID: message.Compaction.TailStartMessageID,
			RetainedMessageIDs: append([]string(nil), message.Compaction.RetainedMessageIDs...),
		}
	}
	return provenance.CompactionSelection{}
}

func providerNoticeMessageID(turnID string, iter int, model string, message llm.Message) string {
	raw, _ := json.Marshal(struct {
		TurnID  string      `json:"turn_id"`
		Iter    int         `json:"iter"`
		Model   string      `json:"model"`
		Message llm.Message `json:"message"`
	}{TurnID: turnID, Iter: iter, Model: model, Message: message})
	sum := sha256.Sum256(raw)
	return "runtime-model-change-" + hex.EncodeToString(sum[:12])
}

func stableProvenanceMessageID(prefix string, index int, message llm.Message) string {
	message.ID = ""
	raw, _ := json.Marshal(struct {
		Index   int         `json:"index"`
		Message llm.Message `json:"message"`
	}{Index: index, Message: message})
	sum := sha256.Sum256(raw)
	return prefix + hex.EncodeToString(sum[:12])
}

func (e *Engine) requestProvenanceTracker() *provenance.Tracker {
	e.hookRuntimeContextMu.Lock()
	defer e.hookRuntimeContextMu.Unlock()
	if e.provenanceTracker == nil {
		e.provenanceTracker = provenance.NewTracker()
	}
	return e.provenanceTracker
}

func (e *Engine) syncPendingHookContextFromTracker(tracker *provenance.Tracker) {
	pending := tracker.PendingHookContext()
	e.hookRuntimeContextMu.Lock()
	e.pendingHookRuntimeContext = pending
	e.hookRuntimeContextMu.Unlock()
}
