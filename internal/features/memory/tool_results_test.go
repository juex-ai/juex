package memory

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
	"github.com/juex-ai/juex/internal/framework/module"
	"github.com/juex-ai/juex/internal/framework/runtime/contextbudget"
)

type pagingAPI struct {
	mc.API
	fence          uint64
	historyAllowed bool
}

func (a *pagingAPI) Status(context.Context, mc.Caller) (mc.Status, error) {
	return mc.Status{Fence: a.fence}, nil
}
func (a *pagingAPI) History(context.Context, mc.Caller, mc.Source) ([]mc.Evidence, error) {
	if !a.historyAllowed {
		return nil, errors.New("assignment unavailable")
	}
	return nil, nil
}
func TestMemoryResultPagesAreExactBoundedAndInputScoped(t *testing.T) {
	for _, content := range []string{strings.Repeat("中文记忆", 700), strings.Repeat("\"\n\t\\", 700), strings.Repeat("ordinary content ", 700)} {
		t.Run(content[:6], func(t *testing.T) {
			m := New(Options{})
			encoded, _ := json.Marshal(content)
			raw := string(encoded)
			descriptor, err := m.boundedResult(raw, nil, nil, nil, "")
			if err != nil {
				t.Fatal(err)
			}
			var ref resultPart
			if err := json.Unmarshal([]byte(descriptor), &ref); err != nil || ref.Parts < 2 {
				t.Fatalf("descriptor %s %v", descriptor, err)
			}
			var reconstructed strings.Builder
			for i := 0; i < ref.Parts; i++ {
				part, err := m.resultPart(t.Context(), ref.ResultID, i)
				if err != nil {
					t.Fatal(err)
				}
				if len(part) > resultPageBytes || contextbudget.EstimateTextTokens(part) > 500 {
					t.Fatalf("part exceeds runtime budget: %d bytes", len(part))
				}
				var page resultPart
				if err := json.Unmarshal([]byte(part), &page); err != nil {
					t.Fatal(err)
				}
				reconstructed.WriteString(page.Text)
			}
			if reconstructed.String() != raw {
				t.Fatal("paging changed content")
			}
			m.options.Caller.Purpose = "maintenance"
			if err := m.PrepareInput(t.Context(), module.InputPreparationRequest{PreparationID: "new-input"}); err != nil {
				t.Fatal(err)
			}
			if _, err := m.resultPart(t.Context(), ref.ResultID, 0); err == nil {
				t.Fatal("handle survived new input")
			}
			if _, err := m.boundedResult(raw, nil, nil, nil, ""); err == nil {
				t.Fatal("late old-input result repopulated cache")
			}
			reopened := New(Options{})
			if _, err := reopened.resultPart(t.Context(), ref.ResultID, 0); err == nil {
				t.Fatal("handle survived Module reopening")
			}
		})
	}
}
func TestMemoryPagesRespectForgetFenceAndHistoryCapability(t *testing.T) {
	api := &pagingAPI{fence: 1, historyAllowed: true}
	m := New(Options{API: api})
	fence := uint64(1)
	descriptor, err := m.boundedResult(strings.Repeat("private", 700), nil, &fence, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	var ref resultPart
	_ = json.Unmarshal([]byte(descriptor), &ref)
	api.fence++
	if _, err := m.resultPart(t.Context(), ref.ResultID, 0); err == nil || !strings.Contains(err.Error(), "invalidated") {
		t.Fatalf("forgotten page: %v", err)
	}
	if _, ok := m.results[ref.ResultID]; ok {
		t.Fatal("invalidated contents retained")
	}
	fence = api.fence
	source := mc.Source{FleetID: "fleet", AgentID: "agent", ThreadID: "0", GenerationID: "g000001", From: 1, Through: 1}
	descriptor, err = m.boundedResult(strings.Repeat("history", 700), nil, &fence, &source, "")
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal([]byte(descriptor), &ref)
	api.historyAllowed = false
	if _, err := m.resultPart(t.Context(), ref.ResultID, 0); err == nil || !strings.Contains(err.Error(), "assignment unavailable") {
		t.Fatalf("revoked history page: %v", err)
	}
}
func TestMemoryPageArgumentsAndImmediateReceipt(t *testing.T) {
	m := New(Options{})
	for _, args := range []map[string]any{{"result_id": "x"}, {"result_id": "x", "part": 0, "view": "history"}, {"part": 0}, {"result_id": "x", "part": 0.5}, {"result_id": "x", "part": "0"}} {
		if _, err := m.read(t.Context(), args); err == nil {
			t.Fatalf("invalid page arguments accepted: %+v", args)
		}
	}
	receipt := mc.Receipt{ID: "commit", State: "applied", Committed: true, IndexReady: true, Reason: strings.Repeat("长期记忆", 200)}
	for i := 0; i < 20; i++ {
		receipt.EntryIDs = append(receipt.EntryIDs, strings.Repeat("a", 64))
	}
	text, err := receiptResult(receipt, nil)
	if err != nil || contextbudget.EstimateTextTokens(text) > 500 {
		t.Fatalf("receipt budget: %s %v", text, err)
	}
	var got mc.Receipt
	if err := json.Unmarshal([]byte(text), &got); err != nil || !got.Committed || got.State != "applied" || got.ID != "commit" {
		t.Fatalf("commit not immediately visible: %s %v", text, err)
	}
}
