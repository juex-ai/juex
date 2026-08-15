package runtime

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/provenance"
)

func (e *Engine) checkpointProviderRequestLocked(
	turnID string,
	prepared preparedTurnContext,
	request providerTurnRequest,
	candidate ModelCandidate,
	attempt int,
) (provenance.RequestEpoch, error) {
	hookIDs := make([]string, 0, len(request.hookContext))
	for _, message := range request.hookContext {
		hookIDs = append(hookIDs, message.ID)
	}
	epoch, err := provenance.BuildRequestEpoch(provenance.RequestInput{
		Purpose:               "turn",
		Provider:              candidateProvenance(candidate),
		ContextWindow:         candidateContextWindow(candidate, e.ContextWindow),
		MaxOutputTokens:       candidateMaxOutputTokens(candidate, e.MaxOutputTokens),
		SystemPrompt:          prepared.systemPrompt,
		Tools:                 prepared.tools,
		History:               request.history,
		Compaction:            requestCompactionSelection(request.history),
		HookContextMessageIDs: hookIDs,
	})
	if err != nil {
		return provenance.RequestEpoch{}, err
	}
	epoch.EpochID = randomProvenanceID("epoch-")
	epoch.Iter = request.iter
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
