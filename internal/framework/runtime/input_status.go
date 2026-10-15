package runtime

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/juex-ai/juex/internal/framework/thread"
)

// InputStatus is evidence about one delivered input, never an assessment of
// whether the user's request succeeded.
type InputStatus struct {
	InputTrackedPayload
	CheckedAt      *time.Time `json:"checked_at,omitempty"`
	CheckMessageID string     `json:"check_message_id,omitempty"`
	ToolUseID      string     `json:"tool_use_id,omitempty"`
}

type InputStatusPage struct {
	ScopeID  string                 `json:"scope_id"`
	Messages map[string]InputStatus `json:"messages"`
	Error    string                 `json:"error,omitempty"`
}

type inputStatusIndex struct {
	cursor   thread.EventCursor
	scopeID  string
	messages map[string]InputStatus
	inputs   map[string]string
}

// InputStatusReader caches only inspected Threads. Its disposable projection
// consumes new commits; inputs.json remains the bounded active checklist.
type InputStatusReader struct {
	mu      sync.Mutex
	indexes map[string]*inputStatusIndex
	order   []string
}

func (r *InputStatusReader) Read(dir string, messageIDs []string) (InputStatusPage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	snapshot, err := thread.CaptureEventStoreSnapshot(dir)
	if err != nil {
		return InputStatusPage{}, err
	}
	defer func() { _ = snapshot.Close() }()
	if r.indexes == nil {
		r.indexes = map[string]*inputStatusIndex{}
	}
	index := r.indexes[dir]
	if index == nil {
		index = &inputStatusIndex{scopeID: thread.InitialGeneration, messages: map[string]InputStatus{}, inputs: map[string]string{}}
		r.indexes[dir] = index
	}
	r.order = removePendingInputID(r.order, dir)
	r.order = append(r.order, dir)
	if len(r.order) > 16 {
		delete(r.indexes, r.order[0])
		r.order = r.order[1:]
	}
	cursor, err := snapshot.VisitAfter(index.cursor, func(commit thread.Commit) error {
		for _, fact := range commit.Facts {
			if fact.Seed != nil {
				index.scopeID = fact.Seed.ContextScopeID
			}
			if fact.Type != thread.FactEventRecorded || fact.Event == nil {
				continue
			}
			switch fact.Event.Type {
			case InputTrackedType:
				var payload InputTrackedPayload
				data, err := json.Marshal(fact.Event.Payload)
				if err != nil {
					return err
				}
				if err := json.Unmarshal(data, &payload); err != nil {
					return err
				}
				if payload.InputID == "" || payload.MessageID == "" || payload.ScopeID == "" {
					continue
				}
				if _, exists := index.messages[payload.MessageID]; !exists {
					index.messages[payload.MessageID] = InputStatus{InputTrackedPayload: payload}
					index.inputs[payload.ScopeID+"\x00"+payload.InputID] = payload.MessageID
				}
			case InputCheckedType:
				var payload InputCheckedPayload
				data, err := json.Marshal(fact.Event.Payload)
				if err != nil {
					return err
				}
				if err := json.Unmarshal(data, &payload); err != nil {
					return err
				}
				for _, id := range payload.InputIDs {
					messageID, exists := index.inputs[payload.ScopeID+"\x00"+id]
					if !exists {
						continue
					}
					status := index.messages[messageID]
					if status.CheckedAt == nil {
						status.CheckedAt = &payload.CheckedAt
						status.CheckMessageID = payload.MessageID
						status.ToolUseID = payload.ToolUseID
						index.messages[messageID] = status
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		delete(r.indexes, dir)
		return InputStatusPage{}, err
	}
	index.cursor = cursor
	page := InputStatusPage{ScopeID: index.scopeID, Messages: map[string]InputStatus{}}
	for _, id := range messageIDs {
		if status, ok := index.messages[id]; ok {
			page.Messages[id] = status
		}
	}
	return page, nil
}
