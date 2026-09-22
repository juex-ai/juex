package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/features/memory"
	"github.com/juex-ai/juex/internal/foundation/llm"
	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
)

func readMemoryToolResult(t *testing.T, ctx context.Context, read func(context.Context, map[string]any) (string, error), body string) string {
	t.Helper()
	var ref struct {
		ResultID string `json:"result_id"`
		Parts    int    `json:"parts"`
	}
	if err := json.Unmarshal([]byte(body), &ref); err != nil {
		t.Fatal(err)
	}
	if ref.ResultID == "" {
		return body
	}
	if ref.Parts <= 0 || ref.Parts > 1024 {
		t.Fatalf("invalid Memory result handle: %s", body)
	}
	var full strings.Builder
	for i := 0; i < ref.Parts; i++ {
		part, err := read(ctx, map[string]any{"result_id": ref.ResultID, "part": i})
		if err != nil {
			t.Fatal(err)
		}
		var value struct {
			ResultID string `json:"result_id"`
			Part     int    `json:"part"`
			Parts    int    `json:"parts"`
			Text     string `json:"text"`
		}
		if err := json.Unmarshal([]byte(part), &value); err != nil || value.ResultID != ref.ResultID || value.Part != i || value.Parts != ref.Parts {
			t.Fatalf("invalid Memory result part: %s %v", part, err)
		}
		full.WriteString(value.Text)
	}
	return full.String()
}

func TestEndToEnd_MemoryCrossAgentReadResultPages(t *testing.T) {
	isolateModuleConfig(t)
	home := t.TempDir()
	api, user := startMemoryFixture(t, home, mc.Basic)
	reader := memoryApp(t, memoryAgentConfig(t, home, "reader"), &bareScriptProvider{})
	read, ok := reader.Engine.Tools.Get(memory.ToolRead)
	if !ok {
		t.Fatal("Memory read unavailable")
	}
	for _, size := range []int{1, 200} {
		entry := mc.Entry{ID: fmt.Sprintf("shared-%d", size), Name: "Shared preference", Summary: "Release language", Type: "user", Body: strings.Repeat("Use Simplified Chinese（简体中文）.\n", size)}
		if _, err := api.Admin(t.Context(), user, mc.AdminRequest{Key: entry.ID, Action: "correct", Changes: []mc.Change{{Entry: entry}}}); err != nil {
			t.Fatal(err)
		}
		body, err := read.Handler(t.Context(), map[string]any{"id": entry.ID})
		if err != nil {
			t.Fatal(err)
		}
		if paged := strings.Contains(body, `"result_id"`); paged != (size == 200) {
			t.Fatalf("unexpected paging for %d repetitions: %s", size, body)
		}
		var got mc.Entry
		full := readMemoryToolResult(t, t.Context(), read.Handler, body)
		if err := json.Unmarshal([]byte(full), &got); err != nil || got.ID != entry.ID || got.Body != entry.Body {
			t.Fatalf("cross-Agent read changed committed content: %+v %v", got, err)
		}
	}
}

// This provider exercises the real Worker tool loop. Semantic extraction is
// assessed separately by the build-tagged live multi-turn evaluation.
type domainReviewProvider struct {
	templates atomic.Int64
}

func (*domainReviewProvider) Name() string { return "domain-review-fixture" }
func (p *domainReviewProvider) Complete(_ context.Context, _ string, history []llm.Message, _ []llm.ToolSpec) (llm.Response, error) {
	var proposal mc.Proposal
	results := map[string]llm.Block{}
	for _, m := range history {
		if _, raw, ok := strings.Cut(m.FirstText(), "Proposal JSON:\n"); ok {
			if err := json.Unmarshal([]byte(raw), &proposal); err != nil {
				return llm.Response{}, err
			}
		}
		for _, b := range m.Blocks {
			if b.Type == llm.BlockToolResult {
				results[b.ToolUseID] = b
			}
		}
	}
	response := func(blocks ...llm.Block) llm.Response {
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: blocks}, StopReason: llm.StopToolUse}
	}
	if _, ok := results["templates"]; !ok {
		return response(memoryCall("templates", memory.ToolDomains, map[string]any{"id": "preferences"}), memoryCall("facts", memory.ToolFacts, map[string]any{"view": "history", "entity": "self"}), memoryCall("prose", memory.ToolSearch, map[string]any{"text": ""})), nil
	}
	var pendingParts []llm.Block
	for id, b := range results {
		var ref struct {
			ResultID    string `json:"result_id"`
			Parts       int    `json:"parts"`
			Instruction string `json:"instruction"`
		}
		if json.Unmarshal([]byte(b.Content), &ref) != nil || ref.Instruction == "" {
			continue
		}
		var calls []llm.Block
		var full strings.Builder
		for i := 0; i < ref.Parts; i++ {
			partID := fmt.Sprintf("%s-part-%d", id, i)
			part, ok := results[partID]
			if !ok {
				calls = append(calls, memoryCall(partID, memory.ToolRead, map[string]any{"result_id": ref.ResultID, "part": i}))
				continue
			}
			var value struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal([]byte(part.Content), &value); err != nil {
				return llm.Response{}, err
			}
			full.WriteString(value.Text)
		}
		if len(calls) > 0 {
			pendingParts = append(pendingParts, calls...)
			continue
		}
		b.Content = full.String()
		results[id] = b
	}
	if len(pendingParts) > 0 {
		return response(pendingParts...), nil
	}
	for id, b := range results {
		if b.IsError && id != "invalid-decision" {
			return llm.Response{}, fmt.Errorf("%s: %s", id, b.Content)
		}
	}
	var domains []mc.Domain
	if err := json.Unmarshal([]byte(results["templates"].Content), &domains); err != nil || len(domains) != 1 || domains[0].ID != "preferences" || len(domains[0].Relations) < 2 {
		return llm.Response{}, fmt.Errorf("template not returned: %v", err)
	}
	if _, ok := results["decision"]; ok {
		p.templates.Add(1)
		return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "Committed supported knowledge"), StopReason: llm.StopEndTurn}, nil
	}
	var page mc.FactPage
	if err := json.Unmarshal([]byte(results["facts"].Content), &page); err != nil {
		return llm.Response{}, err
	}
	if len(page.Facts) > 0 && results["read"].Content == "" {
		return response(memoryCall("read", memory.ToolRead, map[string]any{"id": page.Facts[0].EntryID})), nil
	}
	e := mc.Entry{ID: "preferences", Name: "User preferences", Summary: "Supported preferences", Type: "user", Body: proposal.Text, Entities: []mc.Entity{{ID: "self", Name: "User", Kind: "person"}}}
	if results["read"].Content != "" {
		if err := json.Unmarshal([]byte(results["read"].Content), &e); err != nil {
			return llm.Response{}, err
		}
	}
	e.Sources = append(e.Sources, proposal.Sources...)
	e.Body = proposal.Text
	if proposal.Key == "start" || proposal.Key == "add" {
		value := "climbing"
		if proposal.Key == "add" {
			value = "photography"
		}
		e.Facts = append(e.Facts, mc.Fact{ID: value, Domain: "preferences", Subject: "self", Predicate: "prefers", Value: value, Status: "valid", SourceType: "user_statement", Sources: proposal.Sources, RecordedAt: proposal.Evidence[0].RecordedAt, Reason: proposal.Text})
	} else {
		e.Facts[0].Sources = append(e.Facts[0].Sources, proposal.Sources...)
		if proposal.Key == "retract" {
			e.Facts[0].Status = "retracted"
			e.Facts[0].Reason = proposal.Text
		}
	}
	decision := mc.Decision{Outcome: "applied", Reason: proposal.Text, Changes: []mc.Change{{Entry: e, ExpectedRevision: e.Revision}}}
	id := "decision"
	if proposal.Key == "add" && results["invalid-decision"].Content == "" {
		id = "invalid-decision"
		decision.Changes[0].Entry.Facts[len(e.Facts)-1].Predicate = "invented_preference"
	}
	var args map[string]any
	raw, _ := json.Marshal(decision)
	_ = json.Unmarshal(raw, &args)
	return response(memoryCall(id, memory.ToolDecide, args)), nil
}
func TestEndToEnd_MemoryDomainsWorkerMaintenance(t *testing.T) {
	isolateModuleConfig(t)
	home := t.TempDir()
	api, user := startMemoryFixture(t, home, mc.Advanced)
	cfg := memoryAgentConfig(t, home, "supervisor")
	cfg.MemoryProfile = mc.ProfileSupervisor
	cfg.Modules["worker-threads"] = config.ModuleSettings{Enabled: true}
	provider := &domainReviewProvider{}
	_ = memoryApp(t, cfg, provider)
	initialTime := time.Now().UTC().Truncate(time.Millisecond)
	for i, key := range []string{"start", "confirm", "add", "retract"} {
		text := map[string]string{"start": "Explicitly remember that I like climbing.", "confirm": "I still like climbing; remember this confirmation.", "add": "I also like photography. Keep climbing as well.", "retract": "Retract my earlier climbing preference; keep photography."}[key]
		source := mc.Source{FleetID: user.FleetID, AgentID: user.AgentID, ThreadID: user.ThreadID, GenerationID: "g000001", From: uint64(i + 1), Through: uint64(i + 1)}
		receipt, err := api.Propose(t.Context(), user, mc.Proposal{Key: key, Text: text, Reason: "Explicit maintenance", Sources: []mc.Source{source}, Evidence: []mc.Evidence{{Source: source, Kind: "user", Text: text, RecordedAt: initialTime.Add(time.Duration(i) * time.Minute)}}})
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(20 * time.Second)
		for !receipt.Committed {
			receipt, err = api.Result(t.Context(), user, receipt.ID)
			if err != nil {
				t.Fatal(err)
			}
			if receipt.State == "failed" || receipt.State == "rejected" || time.Now().After(deadline) {
				t.Fatalf("Worker result: %+v", receipt)
			}
			time.Sleep(20 * time.Millisecond)
		}
		if receipt.Attempts != 1 {
			t.Fatalf("validation needed a new Worker: %+v", receipt)
		}
		page, err := api.Facts(t.Context(), user, mc.Query{Domain: "preferences"})
		if err != nil {
			t.Fatal(err)
		}
		expected := 1
		if key == "add" {
			expected = 2
		}
		if len(page.Facts) != expected {
			t.Fatalf("%s facts %+v", key, page)
		}
		if key == "confirm" && (len(page.Facts[0].Fact.Sources) != 2 || !page.Facts[0].Fact.RecordedAt.Equal(initialTime)) {
			t.Fatal("confirmation duplicated or refreshed fact")
		}
		if key == "retract" && page.Facts[0].Fact.Value != "photography" {
			t.Fatal("retraction damaged independent fact")
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for provider.templates.Load() != 4 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if provider.templates.Load() != 4 {
		t.Fatal("Worker did not use templates for each assignment")
	}
	history, err := api.Facts(t.Context(), user, mc.Query{View: "history", Status: "retracted"})
	if err != nil || len(history.Facts) != 1 || history.Facts[0].Fact.ID != "climbing" {
		t.Fatalf("history %+v %v", history, err)
	}
}
