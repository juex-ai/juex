package memory

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
)

// Memory-only Workers cannot read the runtime's filesystem spill artifacts.
// Keep large Memory results as short-lived, immutable pages behind the existing
// read tool. Pages stay below the smallest runtime tool-output token budget.
const resultPageBytes = 1200

type pagedResult struct {
	parts  []string
	fence  *uint64
	source *mc.Source
}

type resultPart struct {
	ResultID string `json:"result_id"`
	Part     int    `json:"part"`
	Parts    int    `json:"parts"`
	Text     string `json:"text"`
}

func (m *Module) boundedResult(text string, err error, fence *uint64, source *mc.Source, preparation string) (string, error) {
	if err != nil || len(text) <= resultPageBytes {
		return text, err
	}
	if len(text) > 256*1024 {
		return "", errors.New("memory tool result exceeds 256 KiB; narrow the query")
	}
	id := rand.Text()
	var parts []string
	for len(text) > 0 {
		n := min(len(text), 768)
		for n > 0 && !utf8.ValidString(text[:n]) {
			n--
		}
		for {
			encoded, _ := json.Marshal(resultPart{ResultID: id, Part: 999, Parts: 999, Text: text[:n]})
			if len(encoded) <= resultPageBytes {
				break
			}
			n /= 2
			for n > 0 && !utf8.ValidString(text[:n]) {
				n--
			}
		}
		parts = append(parts, text[:n])
		text = text[n:]
	}
	m.mu.Lock()
	if preparation != m.preparation {
		m.mu.Unlock()
		return "", errors.New("memory input changed during retrieval; repeat the query")
	}
	if m.results == nil {
		m.results = map[string]pagedResult{}
	}
	if len(m.resultOrder) >= 16 {
		delete(m.results, m.resultOrder[0])
		m.resultOrder = m.resultOrder[1:]
	}
	m.results[id] = pagedResult{parts: parts, fence: fence, source: source}
	m.resultOrder = append(m.resultOrder, id)
	m.mu.Unlock()
	encoded, _ := json.Marshal(map[string]any{"result_id": id, "parts": len(parts), "instruction": "Read every part with memory_read {result_id,part} (0-based; independent calls may share a request), then concatenate text to recover the exact JSON. This handle is limited to this input; narrow large queries to fit your context budget."})
	return string(encoded), nil
}
func (m *Module) resultPart(ctx context.Context, id string, part int) (string, error) {
	m.mu.Lock()
	saved, ok := m.results[id]
	m.mu.Unlock()
	if !ok {
		return "", errors.New("memory result unavailable for this input; repeat the original query")
	}
	if saved.fence != nil {
		status, err := m.options.API.Status(ctx, m.options.Caller)
		if err != nil {
			m.discardResult(id)
			return "", err
		}
		if status.Fence != *saved.fence {
			m.discardResult(id)
			return "", errors.New("memory result invalidated by user administration; repeat the original query")
		}
	}
	if saved.source != nil {
		if _, err := m.options.API.History(ctx, m.options.Caller, *saved.source); err != nil {
			m.discardResult(id)
			return "", err
		}
	}
	parts := saved.parts
	if part < 0 || part >= len(parts) {
		return "", fmt.Errorf("memory result part must be 0-%d", len(parts)-1)
	}
	encoded, _ := json.Marshal(resultPart{ResultID: id, Part: part, Parts: len(parts), Text: parts[part]})
	return string(encoded), nil
}

func (m *Module) discardResult(id string) { m.mu.Lock(); delete(m.results, id); m.mu.Unlock() }

// A successful write must expose its outcome immediately, even when its reason
// or list of entries is too large for one model-visible result.
func receiptResult(receipt mc.Receipt, err error) (string, error) {
	if err != nil {
		return "", err
	}
	count := len(receipt.EntryIDs)
	if count > 5 {
		receipt.EntryIDs = nil
	}
	if len(receipt.Reason) > 256 {
		n := 256
		for n > 0 && !utf8.ValidString(receipt.Reason[:n]) {
			n--
		}
		receipt.Reason = receipt.Reason[:n] + "…"
	}
	return result(struct {
		mc.Receipt
		EntryCount int `json:"entry_count"`
	}{receipt, count}, nil)
}
