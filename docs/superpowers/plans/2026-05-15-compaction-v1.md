# Compaction V1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Upgrade Juex compaction so active context is summary + retained tail + new messages, failed compaction never pollutes sessions, Anthropic-compatible high-thinking configs work, and both configured smoke providers pass.

**Architecture:** Keep compaction inside the existing runtime loop, but split it into policy, selection, summary assembly, active-context assembly, and orchestration helpers. Keep provider SDK changes inside `internal/llm`; CLI and Web call runtime APIs rather than duplicating context logic.

**Tech Stack:** Go 1.22+, stdlib tests, Cobra CLI, React/Vite frontend already in `frontend/`, Anthropic SDK streaming accumulator, OpenAI chat SDK. Validation uses `mise exec -- make test`, `mise exec -- make build`, browser checks, and real provider configs `.juex/doubao.juex.yaml` / `.juex/minimax.juex.yaml`.

**Spec:** `docs/superpowers/specs/2026-05-15-compaction-v1-design.md`

---

## File Map

| File | Change | Responsibility |
|---|---|---|
| `internal/llm/provider.go` | modify | `CompleteOptions`, provider option helper, overflow classifier |
| `internal/llm/anthropic.go` | modify | Anthropic streaming completion and compaction output-budget options |
| `internal/llm/openai.go` | modify | compaction `MaxOutputTokens` support |
| `internal/llm/llm_test.go` | modify | streaming, options, overflow classifier tests |
| `internal/llm/types.go` | modify | message ID and `CompactionMetadata` |
| `internal/config/config.go` | modify | `compaction` config section and defaults |
| `internal/config/config_test.go` | modify | config parsing/default tests |
| `internal/session/session.go` | modify | assign message IDs on append, normalize loaded legacy IDs |
| `internal/session/info.go` | modify | normalize loaded legacy IDs for read-only loading |
| `internal/session/session_test.go` | modify | ID persistence and legacy normalization tests |
| `internal/session/info_test.go` | modify | compact metadata readback tests |
| `internal/runtime/compaction_policy.go` | create | policy defaults, clamping, trigger threshold |
| `internal/runtime/compaction_select.go` | create | select summary head and retained tail safely |
| `internal/runtime/compaction_summary.go` | create | structured summary prompt and transcript serialization |
| `internal/runtime/active_context.go` | create | provider active-context assembler and debug snapshot |
| `internal/runtime/compact.go` | replace | compact orchestration and events |
| `internal/runtime/loop.go` | modify | use active context and overflow compact-and-retry |
| `internal/runtime/*_test.go` | modify/create | policy, selector, summary, active context, retry tests |
| `internal/app/app.go` | modify | pass compaction config into runtime |
| `internal/cli/sessions.go` | modify | `sessions compact` and `sessions context` commands |
| `internal/cli/sessions_test.go` | modify | CLI compact/context tests |
| `internal/web/server.go` | modify | route compact/context endpoints |
| `internal/web/handlers.go` | modify | Web compact/context handlers |
| `internal/web/handlers_test.go` | modify | Web endpoint tests |
| `frontend/src/api.ts` | modify | compact/context API functions |
| `frontend/src/types.ts` | modify | compaction metadata and active context types |
| `frontend/src/pages/Session.tsx` | modify | compact action and context debug view |
| `ARCHITECTURE.md` | modify | document compaction v1 and config |
| `juex.yaml.example` | modify | show `compaction` defaults |

---

## Task 1: Provider Options And Anthropic Streaming

**Files:**
- Modify: `internal/llm/provider.go`
- Modify: `internal/llm/anthropic.go`
- Modify: `internal/llm/openai.go`
- Modify: `internal/llm/llm_test.go`

- [ ] **Step 1: Write failing provider tests**

Add these tests to `internal/llm/llm_test.go`:

```go
func TestAnthropic_AlwaysUsesStreaming(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("accept") != "text/event-stream" {
			t.Fatalf("accept = %q, want text/event-stream", r.Header.Get("accept"))
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\n")
		fmt.Fprint(w, `data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[],"stop_reason":null,"usage":{"input_tokens":11,"output_tokens":1}}}`+"\n\n")
		fmt.Fprint(w, "event: content_block_start\n")
		fmt.Fprint(w, `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`+"\n\n")
		fmt.Fprint(w, "event: content_block_delta\n")
		fmt.Fprint(w, `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"streamed ok"}}`+"\n\n")
		fmt.Fprint(w, "event: content_block_stop\n")
		fmt.Fprint(w, `data: {"type":"content_block_stop","index":0}`+"\n\n")
		fmt.Fprint(w, "event: message_delta\n")
		fmt.Fprint(w, `data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":3}}`+"\n\n")
		fmt.Fprint(w, "event: message_stop\n")
		fmt.Fprint(w, `data: {"type":"message_stop"}`+"\n\n")
	}))
	defer srv.Close()

	p := NewAnthropic(Config{Type: "anthropic", BaseURL: srv.URL, APIKey: "test-key", Model: "minimax-m2.7", ThinkingEffort: "high"}, nil)
	resp, err := p.Complete(context.Background(), "", []Message{TextMessage(RoleUser, "hi")}, nil)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Message.FirstText() != "streamed ok" {
		t.Fatalf("text = %q", resp.Message.FirstText())
	}
	if resp.Usage.InputTokens != 11 || resp.Usage.OutputTokens != 3 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
	if captured["stream"] != true {
		t.Fatalf("stream flag = %v, want true", captured["stream"])
	}
}

func TestProviderCompleteOptions_PreservesThinkingForCompaction(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\n")
		fmt.Fprint(w, `data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[],"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":1}}}`+"\n\n")
		fmt.Fprint(w, "event: content_block_start\n")
		fmt.Fprint(w, `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`+"\n\n")
		fmt.Fprint(w, "event: content_block_delta\n")
		fmt.Fprint(w, `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"summary"}}`+"\n\n")
		fmt.Fprint(w, "event: content_block_stop\n")
		fmt.Fprint(w, `data: {"type":"content_block_stop","index":0}`+"\n\n")
		fmt.Fprint(w, "event: message_delta\n")
		fmt.Fprint(w, `data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":2}}`+"\n\n")
		fmt.Fprint(w, "event: message_stop\n")
		fmt.Fprint(w, `data: {"type":"message_stop"}`+"\n\n")
	}))
	defer srv.Close()

	p := NewAnthropic(Config{Type: "anthropic", BaseURL: srv.URL, APIKey: "test-key", Model: "minimax-m2.7", ThinkingEffort: "high"}, nil)
	withOpts, ok := p.(ProviderWithOptions)
	if !ok {
		t.Fatal("anthropic provider does not implement ProviderWithOptions")
	}
	_, err := withOpts.CompleteWithOptions(context.Background(), "", []Message{TextMessage(RoleUser, "hi")}, nil, CompleteOptions{
		Purpose:         "compaction",
		MaxOutputTokens: 1234,
	})
	if err != nil {
		t.Fatalf("CompleteWithOptions: %v", err)
	}
	if _, ok := captured["thinking"]; !ok {
		t.Fatalf("thinking should follow provider config for compaction")
	}
	if captured["max_tokens"] != float64(32768+1234) {
		t.Fatalf("max_tokens = %v, want thinking budget plus visible output", captured["max_tokens"])
	}
}

func TestIsContextOverflowError(t *testing.T) {
	for _, msg := range []string{
		"openai: context_length_exceeded",
		"maximum context length is 6400 tokens",
		"prompt is too long",
		"input length exceeds context window",
	} {
		if !IsContextOverflowError(fmt.Errorf("wrapped: %s", msg)) {
			t.Fatalf("expected overflow for %q", msg)
		}
	}
	if IsContextOverflowError(fmt.Errorf("rate limit exceeded")) {
		t.Fatal("rate limit should not be classified as context overflow")
	}
}
```

- [ ] **Step 2: Run tests and verify red**

Run:

```bash
mise exec -- go test ./internal/llm -run 'TestAnthropic_AlwaysUsesStreaming|TestProviderCompleteOptions_PreservesThinkingForCompaction|TestIsContextOverflowError' -count=1
```

Expected: FAIL because `ProviderWithOptions` and `IsContextOverflowError` do not exist, and Anthropic still uses non-streaming `Messages.New`.

- [ ] **Step 3: Implement provider options and streaming**

Add to `internal/llm/provider.go`:

```go
type CompleteOptions struct {
	Purpose         string
	MaxOutputTokens int
}

type ProviderWithOptions interface {
	CompleteWithOptions(ctx context.Context, sys string, history []Message, tools []ToolSpec, opts CompleteOptions) (Response, error)
}

func CompleteWithOptions(ctx context.Context, p Provider, sys string, history []Message, tools []ToolSpec, opts CompleteOptions) (Response, error) {
	if withOpts, ok := p.(ProviderWithOptions); ok {
		return withOpts.CompleteWithOptions(ctx, sys, history, tools, opts)
	}
	return p.Complete(ctx, sys, history, tools)
}

func IsContextOverflowError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{"context_length_exceeded", "context window", "maximum context length", "prompt is too long", "input length"} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}
```

Refactor `internal/llm/anthropic.go` so `Complete` delegates:

```go
func (p *anthropicProvider) Complete(ctx context.Context, sys string, history []Message, tools []ToolSpec) (Response, error) {
	return p.CompleteWithOptions(ctx, sys, history, tools, CompleteOptions{})
}
```

Build params with `maxTokens` and `MaxOutputTokens`. If Anthropic thinking is
enabled, preserve it and add the thinking budget to the visible output budget.
Then call:

```go
var acc anthropic.Message
stream := p.client.Messages.NewStreaming(ctx, params)
for stream.Next() {
	if err := acc.Accumulate(stream.Current()); err != nil {
		return Response{}, fmt.Errorf("anthropic stream: %w", err)
	}
}
if err := stream.Err(); err != nil {
	return Response{}, fmt.Errorf("anthropic: %w", err)
}
return anthropicMessageToResponse(p.Name(), &acc), nil
```

Extract existing response mapping into `anthropicMessageToResponse(model string, msg *anthropic.Message) Response`.

In `internal/llm/openai.go`, add `CompleteWithOptions` and set only
`max_completion_tokens` for compaction. Do not also send `max_tokens`; Ark-style
OpenAI-compatible endpoints reject requests that contain both fields.

```go
if opts.MaxOutputTokens > 0 {
	params.MaxCompletionTokens = openai.Int(int64(opts.MaxOutputTokens))
}
```

- [ ] **Step 4: Run provider tests and verify green**

Run:

```bash
mise exec -- go test ./internal/llm -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

Run:

```bash
git add internal/llm/provider.go internal/llm/anthropic.go internal/llm/openai.go internal/llm/llm_test.go
git commit -m "fix: stream anthropic completions"
```

---

## Task 2: Message Metadata And Compaction Config

**Files:**
- Modify: `internal/llm/types.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `internal/session/session.go`
- Modify: `internal/session/info.go`
- Modify: `internal/session/session_test.go`
- Modify: `internal/session/info_test.go`
- Modify: `juex.yaml.example`

- [ ] **Step 1: Write failing tests**

Add to `internal/config/config_test.go`:

```go
func TestLoadFromFile_CompactionConfig(t *testing.T) {
	body := "provider:\n  type: openai\n  api_key: sk-x\n  model: gpt-4\ncompaction:\n  enabled: false\n  reserve_tokens: 1000\n  keep_recent_tokens: 2000\n  tail_turns: 3\n  summary_max_tokens: 777\n  tool_result_max_chars: 888\n"
	path := writeConfig(t, body)
	for _, key := range providerEnvKeys {
		t.Setenv(key, "")
	}
	cfg, err := LoadFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Compaction.Enabled || cfg.Compaction.ReserveTokens != 1000 || cfg.Compaction.KeepRecentTokens != 2000 || cfg.Compaction.TailTurns != 3 || cfg.Compaction.SummaryMaxTokens != 777 || cfg.Compaction.ToolResultMaxChars != 888 {
		t.Fatalf("Compaction = %+v", cfg.Compaction)
	}
}
```

Add to `internal/session/session_test.go`:

```go
func TestAppend_AssignsMessageID(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Append(llm.TextMessage(llm.RoleUser, "hello")); err != nil {
		t.Fatal(err)
	}
	if s.History[0].ID == "" {
		t.Fatal("message ID was not assigned")
	}
	s2, err := Load(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if s2.History[0].ID != s.History[0].ID {
		t.Fatalf("loaded ID = %q, want %q", s2.History[0].ID, s.History[0].ID)
	}
}

func TestLoad_AssignsDeterministicLegacyIDs(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "20260515T010203-legacy")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"role":"user","blocks":[{"type":"text","text":"old"}]}` + "\n"
	if err := os.WriteFile(filepath.Join(sessionDir, "conversation.jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.History[0].ID != "legacy-000001" {
		t.Fatalf("legacy ID = %q", s.History[0].ID)
	}
}
```

- [ ] **Step 2: Run tests and verify red**

Run:

```bash
mise exec -- go test ./internal/config ./internal/session -run 'TestLoadFromFile_CompactionConfig|TestAppend_AssignsMessageID|TestLoad_AssignsDeterministicLegacyIDs' -count=1
```

Expected: FAIL because config and message ID fields do not exist.

- [ ] **Step 3: Implement metadata and config**

Add to `internal/llm/types.go`:

```go
type CompactionMetadata struct {
	Auto               bool   `json:"auto"`
	Reason             string `json:"reason"`
	PreviousSummaryID  string `json:"previous_summary_id,omitempty"`
	FirstKeptMessageID string `json:"first_kept_message_id,omitempty"`
	TailStartMessageID string `json:"tail_start_message_id,omitempty"`
	TokensBefore       int    `json:"tokens_before"`
	TokensAfter        int    `json:"tokens_after"`
	SummaryChars       int    `json:"summary_chars"`
	SummaryModel       string `json:"summary_model,omitempty"`
}
```

Add fields to `Message`:

```go
ID         string              `json:"id,omitempty"`
Compaction *CompactionMetadata `json:"compaction,omitempty"`
```

Add to `internal/config/config.go`:

```go
type CompactionConfig struct {
	Enabled            bool
	ReserveTokens      int
	KeepRecentTokens   int
	TailTurns          int
	SummaryMaxTokens   int
	ToolResultMaxChars int
}
```

Initialize defaults:

```go
cfg := Config{ContextWindow: DefaultContextWindow, Compaction: DefaultCompactionConfig()}
```

Add YAML fields under `fileConfig` and apply them only when positive, while allowing `enabled: false` to disable compaction by using a pointer-backed file config:

```go
type compactionConfig struct {
	Enabled            *bool `yaml:"enabled"`
	ReserveTokens      int   `yaml:"reserve_tokens"`
	KeepRecentTokens   int   `yaml:"keep_recent_tokens"`
	TailTurns          int   `yaml:"tail_turns"`
	SummaryMaxTokens   int   `yaml:"summary_max_tokens"`
	ToolResultMaxChars int   `yaml:"tool_result_max_chars"`
}
```

In `session.normalizeMessage`, assign legacy IDs when called from loaders by introducing `normalizeLoadedMessage(m llm.Message, index int) llm.Message`; in `Append`, call `ensureMessageID` before writing:

```go
func ensureMessageID(m llm.Message) llm.Message {
	if m.ID == "" {
		m.ID = newMessageID()
	}
	if m.Blocks == nil {
		m.Blocks = []llm.Block{}
	}
	return m
}
```

- [ ] **Step 4: Run tests and verify green**

Run:

```bash
mise exec -- go test ./internal/config ./internal/session -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

Run:

```bash
git add internal/llm/types.go internal/config/config.go internal/config/config_test.go internal/session/session.go internal/session/info.go internal/session/session_test.go internal/session/info_test.go juex.yaml.example
git commit -m "feat: add compaction metadata config"
```

---

## Task 3: Policy, Selection, Summary Serialization, And Active Context

**Files:**
- Create: `internal/runtime/compaction_policy.go`
- Create: `internal/runtime/compaction_select.go`
- Create: `internal/runtime/compaction_summary.go`
- Create: `internal/runtime/active_context.go`
- Create: `internal/runtime/compaction_policy_test.go`
- Create: `internal/runtime/compaction_select_test.go`
- Create: `internal/runtime/compaction_summary_test.go`
- Create: `internal/runtime/active_context_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/runtime/compaction_policy_test.go`:

```go
package runtime

import (
	"testing"

	"github.com/juex-ai/juex/internal/config"
)

func TestEffectiveCompactionPolicy_ClampsSmallContextWindow(t *testing.T) {
	p := effectiveCompactionPolicy(config.DefaultCompactionConfig(), 6400)
	if p.ReserveTokens <= 0 || p.ReserveTokens >= 6400 {
		t.Fatalf("reserve = %d", p.ReserveTokens)
	}
	if p.KeepRecentTokens >= 6400 {
		t.Fatalf("keep recent = %d", p.KeepRecentTokens)
	}
	if p.TriggerTokens >= 6400 {
		t.Fatalf("trigger = %d", p.TriggerTokens)
	}
}
```

Create `internal/runtime/compaction_select_test.go`:

```go
package runtime

import (
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/llm"
)

func TestSelectCompactionInput_KeepsRecentTailOutOfSummary(t *testing.T) {
	h := []llm.Message{
		testMsg("m1", llm.RoleUser, "old question"),
		testMsg("m2", llm.RoleAssistant, "old answer"),
		testMsg("m3", llm.RoleUser, "recent question"),
		testMsg("m4", llm.RoleAssistant, "recent answer"),
	}
	sel := selectCompactionInput(h, compactionPolicy{KeepRecentTokens: 1000, TailTurns: 1})
	if len(sel.SummaryInput) != 2 {
		t.Fatalf("summary len = %d, want 2", len(sel.SummaryInput))
	}
	if len(sel.RetainedTail) != 2 || sel.RetainedTail[0].ID != "m3" {
		t.Fatalf("tail = %+v", sel.RetainedTail)
	}
}

func TestSelectCompactionInput_DoesNotOrphanToolResult(t *testing.T) {
	h := []llm.Message{
		testMsg("m1", llm.RoleUser, "old"),
		{ID: "m2", Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ToolUseID: "tu1", ToolName: "read", Input: map[string]any{"path": "x"}}}},
		{ID: "m3", Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "tu1", Content: strings.Repeat("result ", 200)}}},
		testMsg("m4", llm.RoleAssistant, "done"),
	}
	sel := selectCompactionInput(h, compactionPolicy{KeepRecentTokens: 40, TailTurns: 1})
	if sel.RetainedTail[0].ID == "m3" {
		t.Fatalf("tail starts with orphan tool result: %+v", sel.RetainedTail)
	}
}
```

Create `internal/runtime/compaction_summary_test.go`:

```go
func TestBuildCompactionSummaryRequest_UsesPreviousSummaryAndTruncatesToolResult(t *testing.T) {
	prev := testMsg("compact-1", llm.RoleUser, "Summary of earlier conversation:\nGoal\nold")
	prev.Kind = llm.MessageKindCompact
	input := []llm.Message{
		{ID: "tool-result", Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "tu1", Content: strings.Repeat("x", 50)}}},
	}
	sys, hist := buildCompactionSummaryRequest("base", prev, input, compactionPolicy{ToolResultMaxChars: 10})
	if !strings.Contains(sys, "Goal") || !strings.Contains(sys, "Tool Failures") {
		t.Fatalf("system prompt missing required headings: %s", sys)
	}
	body := hist[0].FirstText()
	if !strings.Contains(body, "<previous-summary>") || !strings.Contains(body, "truncated") {
		t.Fatalf("summary request body = %s", body)
	}
}
```

Create `internal/runtime/active_context_test.go`:

```go
func TestActiveContext_AssemblesSummaryBeforeRetainedTail(t *testing.T) {
	h := []llm.Message{
		testMsg("old-1", llm.RoleUser, "old"),
		testMsg("tail-1", llm.RoleUser, "tail"),
	}
	c := testMsg("compact-1", llm.RoleUser, "Summary of earlier conversation:\nold summary")
	c.Kind = llm.MessageKindCompact
	c.Compaction = &llm.CompactionMetadata{TailStartMessageID: "tail-1"}
	h = append(h, c, testMsg("new-1", llm.RoleUser, "new"))
	got := assembleActiveContext(h, nil)
	if len(got.Messages) != 3 {
		t.Fatalf("active len = %d", len(got.Messages))
	}
	if got.Messages[0].ID != "compact-1" || got.Messages[1].ID != "tail-1" || got.Messages[2].ID != "new-1" {
		t.Fatalf("active order = %+v", got.Messages)
	}
}
```

Also add helper in tests:

```go
func testMsg(id string, role llm.Role, text string) llm.Message {
	m := llm.TextMessage(role, text)
	m.ID = id
	return m
}
```

- [ ] **Step 2: Run tests and verify red**

Run:

```bash
mise exec -- go test ./internal/runtime -run 'TestEffectiveCompactionPolicy|TestSelectCompactionInput|TestBuildCompactionSummaryRequest|TestActiveContext' -count=1
```

Expected: FAIL because these helpers and files do not exist.

- [ ] **Step 3: Implement policy, selector, summary, and active context**

Implement `compactionPolicy`:

```go
type compactionPolicy struct {
	Enabled            bool
	ReserveTokens      int
	KeepRecentTokens   int
	TailTurns          int
	SummaryMaxTokens   int
	ToolResultMaxChars int
	TriggerTokens      int
}
```

Use clamp rules:

```go
reserve := minPositive(cfg.ReserveTokens, max(1024, contextWindow/4))
keep := minPositive(cfg.KeepRecentTokens, max(512, contextWindow/3))
trigger := max(1, contextWindow-reserve)
```

Implement `compactionSelection`:

```go
type compactionSelection struct {
	PreviousSummary llm.Message
	HasPreviousSummary bool
	SummaryInput []llm.Message
	RetainedTail []llm.Message
	FirstKeptMessageID string
	TailStartMessageID string
}
```

Implement `assembleActiveContext(history []llm.Message, incoming []llm.Message) ActiveContextSnapshot` with stable output ordering and estimated tokens.

Implement `buildCompactionSummaryRequest(base string, previous llm.Message, input []llm.Message, policy compactionPolicy) (string, []llm.Message)`.

- [ ] **Step 4: Run tests and verify green**

Run:

```bash
mise exec -- go test ./internal/runtime -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

Run:

```bash
git add internal/runtime/compaction_policy.go internal/runtime/compaction_select.go internal/runtime/compaction_summary.go internal/runtime/active_context.go internal/runtime/compaction_policy_test.go internal/runtime/compaction_select_test.go internal/runtime/compaction_summary_test.go internal/runtime/active_context_test.go
git commit -m "feat: add active context pipeline"
```

---

## Task 4: Compact Orchestration And Turn Integration

**Files:**
- Modify: `internal/runtime/compact.go`
- Modify: `internal/runtime/loop.go`
- Modify: `internal/runtime/loop_test.go`
- Modify: `internal/app/app.go`

- [ ] **Step 1: Write failing runtime tests**

Add tests to `internal/runtime/loop_test.go`:

```go
func TestTurn_CompactionKeepsRecentTailInProviderContext(t *testing.T) {
	prov := &mockProvider{script: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "summary"), StopReason: llm.StopEndTurn, Usage: llm.Usage{InputTokens: 10, OutputTokens: 2}},
		{Message: llm.TextMessage(llm.RoleAssistant, "answer"), StopReason: llm.StopEndTurn, Usage: llm.Usage{InputTokens: 20, OutputTokens: 3}},
	}}
	eng, _ := newEngine(t, prov, false)
	eng.ContextWindow = 200
	eng.Compaction = config.DefaultCompactionConfig()
	eng.Compaction.KeepRecentTokens = 80
	eng.Compaction.TailTurns = 1
	eng.Compaction.ReserveTokens = 50
	for _, text := range []string{strings.Repeat("old ", 80), "old answer", "recent question", "recent answer"} {
		role := llm.RoleUser
		if strings.Contains(text, "answer") {
			role = llm.RoleAssistant
		}
		if err := eng.Session.Append(llm.TextMessage(role, text)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := eng.Turn(context.Background(), "latest"); err != nil {
		t.Fatal(err)
	}
	second := prov.histories[1]
	if len(second) < 4 {
		t.Fatalf("second provider history too short: %+v", second)
	}
	if second[0].Kind != llm.MessageKindCompact {
		t.Fatalf("first active message kind = %q", second[0].Kind)
	}
	if !strings.Contains(messagesText(second), "recent question") || !strings.Contains(messagesText(second), "latest") {
		t.Fatalf("active context missing retained tail or latest: %+v", second)
	}
}

func TestTurn_CompactionFailureDoesNotAppendMarker(t *testing.T) {
	prov := &mockProviderWithErrors{errs: []error{fmt.Errorf("summary failed")}}
	eng, _ := newEngine(t, prov, false)
	eng.ContextWindow = 100
	if err := eng.Session.Append(llm.TextMessage(llm.RoleUser, strings.Repeat("old ", 80))); err != nil {
		t.Fatal(err)
	}
	_, err := eng.Turn(context.Background(), "latest")
	if err == nil {
		t.Fatal("expected compaction error")
	}
	for _, msg := range eng.Session.History {
		if msg.Kind == llm.MessageKindCompact {
			t.Fatalf("unexpected compact marker after failure: %+v", msg)
		}
	}
}

func TestTurn_OverflowCompactsAndRetriesOnce(t *testing.T) {
	prov := &mockProviderWithErrors{
		errs: []error{fmt.Errorf("context_length_exceeded")},
		responses: []llm.Response{
			{Message: llm.TextMessage(llm.RoleAssistant, "summary"), StopReason: llm.StopEndTurn, Usage: llm.Usage{InputTokens: 1, OutputTokens: 1}},
			{Message: llm.TextMessage(llm.RoleAssistant, "after retry"), StopReason: llm.StopEndTurn, Usage: llm.Usage{InputTokens: 2, OutputTokens: 1}},
		},
	}
	eng, _ := newEngine(t, prov, false)
	eng.ContextWindow = 10000
	if err := eng.Session.Append(llm.TextMessage(llm.RoleUser, strings.Repeat("old ", 400))); err != nil {
		t.Fatal(err)
	}
	out, err := eng.Turn(context.Background(), "latest")
	if err != nil {
		t.Fatal(err)
	}
	if out != "after retry" {
		t.Fatalf("out = %q", out)
	}
	if prov.called != 3 {
		t.Fatalf("provider calls = %d, want normal fail + compact + retry", prov.called)
	}
}
```

Add helpers:

```go
type mockProviderWithErrors struct {
	errs []error
	responses []llm.Response
	called int
	histories [][]llm.Message
}

func (m *mockProviderWithErrors) Name() string { return "mock" }
func (m *mockProviderWithErrors) Complete(ctx context.Context, sys string, h []llm.Message, t []llm.ToolSpec) (llm.Response, error) {
	m.histories = append(m.histories, append([]llm.Message(nil), h...))
	if m.called < len(m.errs) && m.errs[m.called] != nil {
		err := m.errs[m.called]
		m.called++
		return llm.Response{}, err
	}
	idx := m.called - len(m.errs)
	m.called++
	if idx < 0 || idx >= len(m.responses) {
		return llm.Response{}, fmt.Errorf("out of script")
	}
	return m.responses[idx], nil
}

func messagesText(msgs []llm.Message) string {
	var sb strings.Builder
	for _, msg := range msgs {
		for _, block := range msg.Blocks {
			sb.WriteString(block.Text)
			sb.WriteString(block.Content)
		}
	}
	return sb.String()
}
```

- [ ] **Step 2: Run tests and verify red**

Run:

```bash
mise exec -- go test ./internal/runtime -run 'TestTurn_CompactionKeepsRecentTailInProviderContext|TestTurn_CompactionFailureDoesNotAppendMarker|TestTurn_OverflowCompactsAndRetriesOnce' -count=1
```

Expected: FAIL because `Engine.Compaction`, active context integration, and overflow retry are not implemented.

- [ ] **Step 3: Implement compact orchestration**

Add `Compaction config.CompactionConfig` to `runtime.Engine`.

Replace `maybeCompact` flow with:

```go
func (e *Engine) Compact(ctx context.Context, turnID, systemPrompt, reason string, auto bool) (CompactionResult, error)
func (e *Engine) maybeCompact(ctx context.Context, turnID, systemPrompt string, tools []llm.ToolSpec, incoming llm.Message) error
```

Use `llm.CompleteWithOptions` for compaction:

```go
resp, err := llm.CompleteWithOptions(ctx, e.Provider, summarySystem, summaryHistory, nil, llm.CompleteOptions{
	Purpose: "compaction",
	MaxOutputTokens: policy.SummaryMaxTokens,
})
```

On empty summary:

```go
return CompactionResult{}, fmt.Errorf("compact context: empty summary")
```

Append compact marker only after valid summary and metadata are ready.

In `TurnMessage`, use active context before provider calls:

```go
requestHistory := e.ActiveContext().Messages
resp, err := e.Provider.Complete(turnCtx, systemPrompt, requestHistory, tools)
if llm.IsContextOverflowError(err) && !retriedOverflow {
	_, compactErr := e.Compact(turnCtx, turnID, systemPrompt, "overflow_retry", true)
	if compactErr != nil {
		return "", e.failTurn(turnID, fmt.Errorf("llm context overflow; compaction failed: %w", compactErr))
	}
	retriedOverflow = true
	continue
}
```

In `internal/app/app.go`, set `Engine.Compaction = cfg.Compaction`.

- [ ] **Step 4: Run runtime tests and verify green**

Run:

```bash
mise exec -- go test ./internal/runtime ./internal/app -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

Run:

```bash
git add internal/runtime/compact.go internal/runtime/loop.go internal/runtime/loop_test.go internal/app/app.go
git commit -m "feat: compact with retained tail"
```

---

## Task 5: CLI Manual Compact And Active Context

**Files:**
- Modify: `internal/cli/sessions.go`
- Modify: `internal/cli/sessions_test.go`
- Modify: `internal/app/app.go`

- [ ] **Step 1: Write failing CLI tests**

Add to `internal/cli/sessions_test.go`:

```go
func TestSessionsContextJSON(t *testing.T) {
	work := t.TempDir()
	id := "20260515T010203-context"
	dir := filepath.Join(work, ".juex", "sessions", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"id":"m1","role":"user","blocks":[{"type":"text","text":"hi"}]}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "conversation.jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := executeCommand(t, "sessions", "context", id, "--cwd", work, "--format", "json")
	if code != 0 {
		t.Fatalf("code = %d output = %s", code, out)
	}
	if !strings.Contains(out, `"messages"`) || !strings.Contains(out, `"hi"`) {
		t.Fatalf("output = %s", out)
	}
}
```

Add a compact command test using the CLI provider injection already used in tests. If no injection exists for this command path, test the handler-level helper `compactSession` directly with a mock provider.

- [ ] **Step 2: Run tests and verify red**

Run:

```bash
mise exec -- go test ./internal/cli -run 'TestSessionsContextJSON|TestSessionsCompact' -count=1
```

Expected: FAIL because commands do not exist.

- [ ] **Step 3: Implement CLI commands**

Register:

```go
cmd.AddCommand(newSessionsCompactCmd(flags))
cmd.AddCommand(newSessionsContextCmd(flags))
```

`sessions context` loads the session and prints `runtime.ActiveContextSnapshot`:

```go
snap := runtime.ActiveContextFromHistory(msgs, cfg.ContextWindow)
cmdPrintln(cmd, mustJSON(snap))
```

`sessions compact` loads an `app.App` with `ResumeDir`, calls `a.Engine.Compact(ctx, "manual", false)`, prints JSON result, and closes the app.

- [ ] **Step 4: Run CLI tests and verify green**

Run:

```bash
mise exec -- go test ./internal/cli -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

Run:

```bash
git add internal/cli/sessions.go internal/cli/sessions_test.go internal/app/app.go
git commit -m "feat: add session compact commands"
```

---

## Task 6: Web API And UI Controls

**Files:**
- Modify: `internal/web/server.go`
- Modify: `internal/web/handlers.go`
- Modify: `internal/web/handlers_test.go`
- Modify: `frontend/src/api.ts`
- Modify: `frontend/src/types.ts`
- Modify: `frontend/src/pages/Session.tsx`

- [ ] **Step 1: Write failing Web tests**

Add to `internal/web/handlers_test.go`:

```go
func TestPostSessionCompact(t *testing.T) {
	srv := newTestServer(t)
	c := createTestSession(t, srv)
	seedSession(t, srv.opts.Cfg.WorkDir, c.ID,
		`{"id":"m1","role":"user","blocks":[{"type":"text","text":"`+strings.Repeat("old ", 200)+`"}]}`+"\n")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	resp, err := http.Post(ts.URL+"/api/sessions/"+c.ID+"/compact", "application/json", strings.NewReader(`{"reason":"manual"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body = %s", resp.StatusCode, body)
	}
}

func TestGetSessionContext(t *testing.T) {
	srv := newTestServer(t)
	id := "20260515T010203-webctx"
	seedSession(t, srv.opts.Cfg.WorkDir, id, `{"id":"m1","role":"user","blocks":[{"type":"text","text":"hi"}]}`+"\n")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/api/sessions/" + id + "/context")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"messages"`) {
		t.Fatalf("body = %s", body)
	}
}
```

- [ ] **Step 2: Run Web tests and verify red**

Run:

```bash
mise exec -- go test ./internal/web -run 'TestPostSessionCompact|TestGetSessionContext' -count=1
```

Expected: FAIL because routes do not exist.

- [ ] **Step 3: Implement Web routes**

In `dispatchSession` add:

```go
case rest == "compact" && r.Method == http.MethodPost:
	s.handleCompactSession(w, r, id)
case rest == "context" && r.Method == http.MethodGet:
	s.handleSessionContext(w, r, id)
```

Implement handlers using active session app for compact and loaded info for context. Keep JSON response shapes stable:

```go
type compactRequest struct {
	Reason string `json:"reason"`
}
```

Frontend:

- Add `compactSession(id, reason)` and `getSessionContext(id)` to `api.ts`.
- Add `compaction?: CompactionMetadata` to `Message`.
- Add a compact icon button near interrupt/send controls.
- Add a compact context details disclosure that shows message count and estimated tokens from `getSessionContext`.

- [ ] **Step 4: Run Web and frontend tests/build**

Run:

```bash
mise exec -- go test ./internal/web -count=1
cd frontend && pnpm build
```

Expected: PASS and frontend build succeeds.

- [ ] **Step 5: Commit**

Run:

```bash
git add internal/web/server.go internal/web/handlers.go internal/web/handlers_test.go frontend/src/api.ts frontend/src/types.ts frontend/src/pages/Session.tsx
git commit -m "feat: add web compaction controls"
```

---

## Task 7: Documentation And Examples

**Files:**
- Modify: `ARCHITECTURE.md`
- Modify: `juex.yaml.example`

- [ ] **Step 1: Update docs**

In `ARCHITECTURE.md`, replace the old 80% compaction paragraph with:

```markdown
Compaction is controlled by the `compaction` config section. The runtime keeps
the full `conversation.jsonl` transcript, appends a compact boundary message
with metadata, and assembles provider context as latest compact summary,
retained recent tail, and messages after the compact marker. Manual compact and
active-context inspection are available through `juex sessions compact`,
`juex sessions context`, and matching Web API routes.
```

In `juex.yaml.example`, add:

```yaml
compaction:
  enabled: true
  reserve_tokens: 16384
  keep_recent_tokens: 20000
  tail_turns: 2
  summary_max_tokens: 2048
  tool_result_max_chars: 2000
```

- [ ] **Step 2: Check docs**

Run:

```bash
rg -n "80%|context.compact|sessions compact|compaction:" ARCHITECTURE.md README.md juex.yaml.example
```

Expected: no stale "80%" description remains; new commands and config are discoverable.

- [ ] **Step 3: Commit**

Run:

```bash
git add ARCHITECTURE.md juex.yaml.example
git commit -m "docs: document compaction v1"
```

---

## Task 8: Full Local Verification

**Files:**
- No planned source edits unless verification finds a defect.

- [ ] **Step 1: Run project tests**

Run:

```bash
mise exec -- make test
```

Expected: PASS.

- [ ] **Step 2: Run build**

Run:

```bash
mise exec -- make build
```

Expected: PASS and `dist/juex` exists.

- [ ] **Step 3: Run race tests**

Run:

```bash
mise exec -- go test ./... -race -count=1
```

Expected: PASS.

- [ ] **Step 4: Browser verify Web UI**

Start:

```bash
./dist/juex --config .juex/doubao.juex.yaml serve --addr 127.0.0.1:8091
```

Open `http://127.0.0.1:8091`, create a session, confirm the compact button and context debug view render without overlap, then stop the server.

- [ ] **Step 5: Commit any verification fixes**

If fixes were required:

```bash
git add internal frontend ARCHITECTURE.md juex.yaml.example
git commit -m "fix: polish compaction verification"
```

If no fixes were required, skip this commit.

---

## Task 9: Real Provider Smoke Tests

**Files:**
- No source edits unless smoke testing finds a defect.

- [ ] **Step 1: Prepare temporary workdirs**

Run:

```bash
tmp_doubao="$(mktemp -d /tmp/juex-doubao-XXXXXX)"
tmp_minimax="$(mktemp -d /tmp/juex-minimax-XXXXXX)"
mkdir -p "$tmp_doubao/.juex" "$tmp_minimax/.juex"
cp .juex/doubao.juex.yaml "$tmp_doubao/.juex/juex.yaml"
cp .juex/minimax.juex.yaml "$tmp_minimax/.juex/juex.yaml"
```

Expected: two temp workdirs with copied configs.

- [ ] **Step 2: Run Doubao automatic compaction smoke**

Run:

```bash
./dist/juex -C "$tmp_doubao" run "Create a compact test. First restate this exact marker DOUBAO-COMPACTION-SMOKE. Then summarize the following repeated context briefly: $(printf 'alpha beta gamma %.0s' {1..1500})"
./dist/juex -C "$tmp_doubao" run --resume=last "Continue from the previous session and say whether you still see DOUBAO-COMPACTION-SMOKE."
find "$tmp_doubao/.juex/sessions" -name conversation.jsonl -maxdepth 2 -print -exec rg -n '"kind":"compact"|tail_start_message_id|tokens_before' {} \;
```

Expected: commands finish successfully; at least one compact marker or compact metadata appears when the configured 6400-token context is exceeded.

- [ ] **Step 3: Run MiniMax Anthropic-compatible smoke**

Run:

```bash
./dist/juex -C "$tmp_minimax" run "Create a compact test. First restate this exact marker MINIMAX-COMPACTION-SMOKE. Then summarize the following repeated context briefly: $(printf 'delta epsilon zeta %.0s' {1..1500})"
./dist/juex -C "$tmp_minimax" run --resume=last "Continue from the previous session and say whether you still see MINIMAX-COMPACTION-SMOKE."
find "$tmp_minimax/.juex/sessions" -name events.jsonl -maxdepth 2 -print -exec rg -n 'context.compact.completed|streaming is required|context.compact.errored' {} \;
```

Expected: no `streaming is required for operations that may take longer than 10 minutes` error; compaction completion is recorded when triggered.

- [ ] **Step 4: Record smoke outcome**

If smoke testing reveals a defect, fix with TDD, rerun unit tests, rebuild, and rerun both smoke tests before proceeding.

---

## Task 10: Taskline Review Handoff

**Files:**
- No source edits.

- [ ] **Step 1: Run final status checks**

Run:

```bash
git status --short --branch
git log --oneline --decorate -8
```

Expected: branch contains focused commits; no unrelated unstaged changes.

- [ ] **Step 2: Advance taskline to review**

Run:

```bash
taskline task update c9b09d69-e313-4378-b419-a80a0558c0dd --state review --format json
```

Expected: task state is `review`.

- [ ] **Step 3: Prepare PR**

Run:

```bash
git push -u origin high/optimize-compaction
pr_body="$(mktemp)"
cat > "$pr_body" <<'BODY'
## Summary
- add compaction v1 active-context assembly with retained tail and metadata
- stream Anthropic-compatible completions to support high-thinking configs
- add manual compact/context surfaces for CLI and Web

## Test plan
- mise exec -- make test
- mise exec -- make build
- mise exec -- go test ./... -race -count=1
- real smoke: .juex/doubao.juex.yaml
- real smoke: .juex/minimax.juex.yaml
BODY
pr_url="$(gh pr create --base main --head high/optimize-compaction --title "Optimize context compaction" --body-file "$pr_body")"
printf '%s\n' "$pr_url"
```

Expected: PR URL is printed. Attach it to taskline with the same captured value:

```bash
taskline task link c9b09d69-e313-4378-b419-a80a0558c0dd --url "$pr_url" --label "PR" --format json
```
